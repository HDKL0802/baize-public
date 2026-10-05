package httpapi_test

import (
	"strings"
	"testing"
)

/* 人设接口测试（QwenPaw 人设机制的 Go 版）。
   关注四件事：文件读写/启停/顺序真的生效；非法文件名必须挡住；
   归档不硬删；没挂 Agent 时不注册。 */

type personaFileView struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Exists  bool   `json:"exists"`
	Builtin bool   `json:"builtin"`
}

type personaResp struct {
	Enabled bool              `json:"enabled"`
	Dir     string            `json:"dir"`
	Files   []personaFileView `json:"files"`
	Builtin []string          `json:"builtin"`
	Preview string            `json:"promptPreview"`
	Tokens  int               `json:"tokens"`
}

type personaFileResp struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func TestPersonaAPI(t *testing.T) {
	e, _ := newAgentEnv(t)

	// 首次启动应自动生成默认模板（三个启用、文件都在）
	var base personaResp
	if code := e.do("GET", "/api/agent/persona", nil, true, &base); code != 200 {
		t.Fatalf("GET /api/agent/persona 期望 200，实际 %d", code)
	}
	if !base.Enabled || len(base.Files) != 3 {
		t.Fatalf("默认应启用三个人设文件：%+v", base)
	}
	for _, f := range base.Files {
		if !f.Enabled || !f.Exists {
			t.Fatalf("默认人设文件应已生成且启用：%+v", f)
		}
	}

	// 读原文
	var one personaFileResp
	if code := e.do("GET", "/api/agent/persona/file?name=SOUL.md", nil, true, &one); code != 200 {
		t.Fatalf("读文件期望 200，实际 %d", code)
	}
	if !strings.Contains(one.Content, "白泽") {
		t.Fatalf("默认 SOUL.md 内容不对：%q", one.Content)
	}
	// 读不存在的文件要明确 404
	if code := e.do("GET", "/api/agent/persona/file?name=NOPE.md", nil, true, nil); code != 404 {
		t.Fatalf("读不存在的文件期望 404，实际 %d", code)
	}
	// 路径穿越必须被挡住
	if code := e.do("GET", "/api/agent/persona/file?name=../config.json", nil, true, nil); code != 404 {
		t.Fatalf("路径穿越期望 404，实际 %d", code)
	}

	// 保存（改写已有文件）
	var saved personaResp
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "save", "name": "SOUL.md", "content": "灵魂：先给结论。"}, true, &saved); code != 200 {
		t.Fatalf("保存期望 200，实际 %d", code)
	}
	if !strings.Contains(saved.Preview, "先给结论") {
		t.Fatalf("拼装预览里应能看到刚保存的内容：%q", saved.Preview)
	}
	// 空内容要挡：停用文件靠开关，不是清空
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "save", "name": "SOUL.md", "content": "  "}, true, nil); code != 400 {
		t.Fatalf("空内容期望 400，实际 %d", code)
	}
	// 非法文件名（带路径）要挡
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "save", "name": "../evil.md", "content": "x"}, true, nil); code != 400 {
		t.Fatalf("非法文件名期望 400，实际 %d", code)
	}

	// 新建文件默认不启用；打开开关后才进清单
	var created personaResp
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "save", "name": "RULES.md", "content": "规矩：不熬夜。"}, true, &created); code != 200 {
		t.Fatalf("新建期望 200，实际 %d", code)
	}
	if found := findPersonaFile(created, "RULES.md"); found == nil || found.Enabled {
		t.Fatalf("新建文件默认应不启用：%+v", created.Files)
	}
	var on personaResp
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "enable", "name": "RULES.md"}, true, &on); code != 200 {
		t.Fatalf("启用期望 200，实际 %d", code)
	}
	if found := findPersonaFile(on, "RULES.md"); found == nil || !found.Enabled {
		t.Fatalf("启用后应在清单里：%+v", on.Files)
	}
	if !strings.Contains(on.Preview, "不熬夜") {
		t.Fatalf("启用后拼装预览里应有它的内容：%q", on.Preview)
	}

	// 调顺序：RULES.md 排到最前
	var ordered personaResp
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "order", "files": []string{"RULES.md", "AGENTS.md", "SOUL.md", "PROFILE.md"}}, true, &ordered); code != 200 {
		t.Fatalf("排序期望 200，实际 %d", code)
	}
	if idx := strings.Index(ordered.Preview, "不熬夜"); idx < 0 || idx > 60 {
		t.Fatalf("排到最前的文件应出现在预览开头附近（实际位置 %d）：%q", idx, ordered.Preview)
	}
	// 清单里放非法文件名要挡
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "order", "files": []string{"../../x.md"}}, true, nil); code != 400 {
		t.Fatalf("清单里非法文件名期望 400，实际 %d", code)
	}

	// 停用 → 不再进预览
	var off personaResp
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "disable", "name": "RULES.md"}, true, &off); code != 200 {
		t.Fatalf("停用期望 200，实际 %d", code)
	}
	if strings.Contains(off.Preview, "不熬夜") {
		t.Fatalf("停用后不该进系统提示：%q", off.Preview)
	}

	// 总开关关掉 → 没有拼装预览（什么都不注入）
	var master personaResp
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "master", "enabled": false}, true, &master); code != 200 {
		t.Fatalf("总开关期望 200，实际 %d", code)
	}
	if master.Enabled || master.Preview != "" {
		t.Fatalf("关掉总开关后不该有拼装预览：%+v", master)
	}
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "master", "enabled": true}, true, nil); code != 200 {
		t.Fatalf("恢复总开关期望 200，实际 %d", code)
	}

	// 归档（删除 = 归档，不硬删）：清单里不再出现
	var archived personaResp
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "archive", "name": "RULES.md"}, true, &archived); code != 200 {
		t.Fatalf("归档期望 200，实际 %d", code)
	}
	if findPersonaFile(archived, "RULES.md") != nil {
		t.Fatalf("归档后不该再出现在清单里：%+v", archived.Files)
	}
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "archive", "name": "RULES.md"}, true, nil); code != 404 {
		t.Fatalf("归档不存在的文件期望 404，实际 %d", code)
	}

	// 恢复默认模板；对没有模板的文件要明确报错
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "reset", "name": "SOUL.md"}, true, &personaResp{}); code != 200 {
		t.Fatalf("恢复默认期望 200，实际 %d", code)
	}
	var again personaFileResp
	_ = e.do("GET", "/api/agent/persona/file?name=SOUL.md", nil, true, &again)
	if !strings.Contains(again.Content, "山海经") {
		t.Fatalf("恢复默认后应是模板原文：%q", again.Content)
	}
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "reset", "name": "RULES.md"}, true, nil); code != 400 {
		t.Fatalf("无模板文件恢复默认期望 400，实际 %d", code)
	}

	// 未知 action
	if code := e.do("POST", "/api/agent/persona",
		map[string]any{"action": "wat"}, true, nil); code != 400 {
		t.Fatalf("未知 action 期望 400，实际 %d", code)
	}
}

func findPersonaFile(resp personaResp, name string) *personaFileView {
	for i := range resp.Files {
		if resp.Files[i].Name == name {
			return &resp.Files[i]
		}
	}
	return nil
}

// 没挂 Agent 服务时人设接口不该注册
func TestPersonaAbsentWithoutAgent(t *testing.T) {
	e := newEnv(t, 5e9)
	if code := e.do("GET", "/api/agent/persona", nil, true, nil); code == 200 {
		t.Fatalf("没挂 Agent 时 /api/agent/persona 不该可用，实际 %d", code)
	}
}