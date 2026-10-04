package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubKB 假的后端知识库：只认 /api/kb/state 与 /api/kb/op，用于验证手机内核的跨端路由
type stubKB struct {
	mu    sync.Mutex
	todos []Task
	calls []string
	down  bool
}

func (s *stubKB) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/kb/state", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.down {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "后端在维护"})
			return
		}
		_ = json.NewEncoder(w).Encode(Snapshot{
			Version: Version, DataDir: "/nas/kb",
			Todos: append([]Task{}, s.todos...), Vault: []Pass{},
			Settings: DefaultSettings(),
		})
	})
	mux.HandleFunc("POST /api/kb/op", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Op   string          `json:"op"`
			Args json.RawMessage `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.calls = append(s.calls, req.Op)
		if s.down {
			s.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "后端在维护"})
			return
		}
		var data any = map[string]any{"ok": true}
		switch req.Op {
		case "todo.add":
			var a struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(req.Args, &a)
			s.todos = append(s.todos, Task{
				ID: NewID(), Title: a.Title, Status: StatusTodo, Owner: OwnerUser,
			})
		case "kb.import":
			var a struct {
				Todos []Task `json:"todos"`
			}
			_ = json.Unmarshal(req.Args, &a)
			added, skipped := 0, 0
			for _, t := range a.Todos {
				dup := false
				for _, e := range s.todos {
					if e.Title == t.Title {
						dup = true
						break
					}
				}
				if dup {
					skipped++
					continue
				}
				s.todos = append(s.todos, t)
				added++
			}
			data = map[string]any{
				"todoAdded": added, "todoSkipped": skipped,
				"passAdded": 0, "passMerged": 0,
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	})
	return mux
}

func (s *stubKB) calledOps() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.calls...)
}

func newRemoteTestService(t *testing.T) (*Service, *stubKB, *httptest.Server) {
	t.Helper()
	stub := &stubKB{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	svc, err := NewService(t.TempDir(), "")
	if err != nil {
		t.Fatalf("打开内核服务失败：%v", err)
	}
	svc.SetRemote(NewRemote(srv.URL, "tok"))
	return svc, stub, srv
}

// 跨端模式下读走后端：正本在哪，快照就从哪来
func TestRemoteSnapshotReadsBackend(t *testing.T) {
	svc, stub, _ := newRemoteTestService(t)
	if _, err := svc.Do(OpRequest{Op: "todo.add", Args: json.RawMessage(`{"title":"后端待办"}`)}); err != nil {
		t.Fatalf("往远端加待办失败：%v", err)
	}
	snap, err := svc.Snapshot()
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	if !snap.Remote || snap.RemoteServer == "" {
		t.Fatal("快照应标记为「正本在后端」并带上后端地址")
	}
	if len(snap.Todos) != 1 || snap.Todos[0].Title != "后端待办" {
		t.Fatalf("快照没读到后端待办：%+v", snap.Todos)
	}
	if len(stub.calledOps()) == 0 {
		t.Fatal("操作应当转发给后端")
	}
	// 待办要在本机留一份缓存（离线时界面还能看见列表）
	local, err := svc.LocalSnapshot()
	if err != nil {
		t.Fatalf("读本机缓存失败：%v", err)
	}
	if len(local.Todos) != 1 {
		t.Fatalf("本机应缓存一份待办，实际 %d 条", len(local.Todos))
	}
}

// 设置类操作留在本机（通知开关/提醒策略是每台设备各管各的），不上后端
func TestRemoteKeepsSettingsLocal(t *testing.T) {
	svc, stub, _ := newRemoteTestService(t)
	if _, err := svc.Do(OpRequest{Op: "settings.save", Args: json.RawMessage(`{"notify":true}`)}); err != nil {
		t.Fatalf("保存设置失败：%v", err)
	}
	for _, op := range stub.calledOps() {
		if op == "settings.save" {
			t.Fatal("settings.save 不该转发到后端")
		}
	}
	if local, err := svc.LocalSnapshot(); err != nil {
		t.Fatalf("读本机状态失败：%v", err)
	} else if !local.Settings.Notify {
		t.Fatal("设置应写在本机")
	}
}

// 跨端模式下必须拒绝 todo.replaceAll：否则手机端的整表会把后端正本冲掉
func TestRemoteBlocksReplaceAll(t *testing.T) {
	svc, _, _ := newRemoteTestService(t)
	_, err := svc.Do(OpRequest{Op: "todo.replaceAll", Args: json.RawMessage(`{"todos":[]}`)})
	if err == nil {
		t.Fatal("跨端模式下 todo.replaceAll 必须报错，不能悄悄覆盖后端正本")
	}
	if !strings.Contains(err.Error(), "跨端") {
		t.Fatalf("错误信息要说清原因，实际：%v", err)
	}
}

// 后端连不上：退回本机缓存并把原因如实带出来，不伪装成最新数据
func TestRemoteOfflineFallsBackWithReason(t *testing.T) {
	svc, stub, srv := newRemoteTestService(t)
	if _, err := svc.Do(OpRequest{Op: "todo.add", Args: json.RawMessage(`{"title":"先缓存住"}`)}); err != nil {
		t.Fatalf("加待办失败：%v", err)
	}
	srv.Close() // 后端没了

	snap, err := svc.Snapshot()
	if err != nil {
		t.Fatalf("离线时也应能返回缓存，而不是直接报错：%v", err)
	}
	if !snap.Stale || snap.StaleReason == "" {
		t.Fatalf("离线快照必须标记 stale 并给出原因：%+v", snap)
	}
	if len(snap.Todos) != 1 {
		t.Fatalf("离线时应返回本机缓存的待办，实际 %d 条", len(snap.Todos))
	}
	if len(snap.Vault) != 0 || !snap.VaultLocked {
		t.Fatal("离线时密码本不做本地缓存，应呈现为锁定")
	}
	// 写操作在离线时必须明确失败（绝不假装成功）
	if _, err := svc.Do(OpRequest{Op: "todo.add", Args: json.RawMessage(`{"title":"离线写的"}`)}); err == nil {
		t.Fatal("离线时写操作必须报错")
	}
	_ = stub
}

// 首次迁移：kb.import 能把本机数据并进后端，重复调用不会重复计数
func TestRemoteImportMergesOnce(t *testing.T) {
	svc, _, _ := newRemoteTestService(t)
	todos := []Task{{ID: NewID(), Title: "迁移进来的", Status: StatusTodo, Owner: OwnerUser}}
	first, err := svc.Import(svc.Remote(), todos, nil)
	if err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	if n, _ := first["todoAdded"].(float64); n != 1 {
		t.Fatalf("首次迁移应并入 1 条，实际 %v", first["todoAdded"])
	}
	second, err := svc.Import(svc.Remote(), todos, nil)
	if err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
	if n, _ := second["todoSkipped"].(float64); n != 1 {
		t.Fatalf("重复迁移应跳过 1 条，实际 %+v", second)
	}
}
