package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"baize/internal/accounts"
	"baize/internal/conflicts"
)

// errConflictNotInGroup 会话不属于这个用户组（防止拿别组的 sid 来撞库）
var errConflictNotInGroup = errors.New("这个协商会话不属于该用户组")

// atoi64 解析查询串里的 64 位整数（错值一律当 0）
func atoi64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// 冲突协商：同一份文档被两个人各改了一版之后怎么收场。
//
// 用户原话的落地：两版**左右对照**（前端拿 versions 里的 docId 各取一次内容即可），
// 各自只能**看**对方那版（这里下载接口对双方一视同仁、都是只读——"不能编辑"由前端渲染决定，
// 后端没有"改别人那版"的接口，所以从接口层面也改不了）；聊天用会话内的 messages；
// 设备有麦克风/摄像头就用信令中转开 WebRTC，没有就只用文字——后端两种都只是记录/中转。
//
// 路由都挂在用户组下（`/api/agent/groups/{id}/conflicts/...`），这样"这是哪个组的协商"
// 一目了然，鉴权也只需要一次成员校验。**聊天链接**就是会话 id 组成的深链。
func (s *Server) registerConflicts(mux *http.ServeMux) {
	if s.agent == nil || s.agent.Accounts() == nil {
		return
	}

	// 会话列表
	mux.HandleFunc("GET /api/agent/groups/{id}/conflicts", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, _, err := s.groupMemberCheck(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		cs, err := s.agent.ConflictsFor(accounts.GroupPrincipal(g.ID))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		open := r.URL.Query().Get("all") != "1"
		list, err := cs.Sessions(g.ID, open)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"group": g.ID, "conflicts": list})
	}))

	// 我全部组里还在协商的冲突（界面开屏就靠它提示"有两份文档等你定"）
	mux.HandleFunc("GET /api/agent/conflicts", s.api(func(w http.ResponseWriter, r *http.Request) {
		reg := s.agent.Accounts()
		_, u, loggedIn := s.principalOf(r)
		if !loggedIn {
			writeErr(w, http.StatusForbidden, "请先登录")
			return
		}
		groups, err := reg.GroupsOf(u.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := []map[string]any{}
		for _, g := range groups {
			cs, err := s.agent.ConflictsFor(accounts.GroupPrincipal(g.ID))
			if err != nil {
				continue
			}
			list, err := cs.Sessions(g.ID, true)
			if err != nil {
				continue
			}
			for _, c := range list {
				out = append(out, map[string]any{"group": g.ID, "groupName": g.Name, "conflict": c})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"conflicts": out})
	}))

	// 单个会话：会话本体 + 两版清单 + （可选）聊天与信令增量
	//
	// 参数：afterMsg / afterSignal 都是上次拿到的最大 id，用来做增量轮询。
	mux.HandleFunc("GET /api/agent/groups/{id}/conflicts/{sid}", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, err := s.groupMemberCheck(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		cs, sess, err := s.conflictSession(g.ID, r.PathValue("sid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		vers, err := cs.Versions(sess.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		msgs, err := cs.Messages(sess.ID, atoi64(r.URL.Query().Get("afterMsg")), atoiDefault(r.URL.Query().Get("msgLimit"), 200))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		signals, err := cs.Signals(sess.ID, u.ID, atoi64(r.URL.Query().Get("afterSignal")), 200)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"group": g.ID, "conflict": sess, "versions": vers,
			"messages": msgs, "signals": signals, "me": u.ID,
		})
	}))

	// 发一条聊天
	mux.HandleFunc("POST /api/agent/groups/{id}/conflicts/{sid}/messages", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, err := s.groupMemberCheck(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		cs, sess, err := s.conflictSession(g.ID, r.PathValue("sid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		m, err := cs.AddMessage(sess.ID, u.ID, u.Name, req.Text)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": m})
	}))

	// 定稿：保留某一版（docId），其余同名版本会被删掉（同一份文档只留一个"正本"）
	mux.HandleFunc("POST /api/agent/groups/{id}/conflicts/{sid}/resolve", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, err := s.groupMemberCheck(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		cs, sess, err := s.conflictSession(g.ID, r.PathValue("sid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		var req struct {
			DocID string `json:"docId"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		updated, err := cs.Resolve(sess.ID, req.DocID, u.ID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := cs.AddMessage(sess.ID, u.ID, u.Name,
			"已定稿：保留 "+req.DocID+" 这一版。"); err != nil {
			s.lg.Warn("写定稿记录消息失败", "err", err, "conflict", sess.ID)
		}
		removed := s.pruneFork(g.ID, updated.Name, req.DocID)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "conflict": updated, "removed": removed,
		})
	}))

	// 一方放弃自己那版 → 对方的版本自动成为正本
	mux.HandleFunc("POST /api/agent/groups/{id}/conflicts/{sid}/giveup", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, err := s.groupMemberCheck(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		cs, sess, err := s.conflictSession(g.ID, r.PathValue("sid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		updated, err := cs.GiveUp(sess.ID, u.ID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		removed := []string{}
		if updated.ResolvedDoc != "" {
			if _, err := cs.AddMessage(sess.ID, u.ID, u.Name,
				"我放弃了自己那版，保留 "+updated.ResolvedDoc+"。"); err != nil {
				s.lg.Warn("写放弃记录消息失败", "err", err, "conflict", sess.ID)
			}
			removed = s.pruneFork(g.ID, updated.Name, updated.ResolvedDoc)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "conflict": updated, "removed": removed,
		})
	}))

	// 音视频信令：客户端把 SDP/ICE 放这里，后端负责转给对方（不碰媒体本身）
	mux.HandleFunc("POST /api/agent/groups/{id}/conflicts/{sid}/signal", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, err := s.groupMemberCheck(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		cs, sess, err := s.conflictSession(g.ID, r.PathValue("sid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		var req struct {
			To      string `json:"to"`
			Kind    string `json:"kind"`
			Payload string `json:"payload"`
		}
		if err := decodeBodyLimit(r, &req, 1<<20); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := cs.PostSignal(sess.ID, u.ID, strings.TrimSpace(req.To), req.Kind, req.Payload); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	// 拉信令（增量轮询）。to 空 = 拉全部（自己发的会被后端过滤掉）。
	mux.HandleFunc("GET /api/agent/groups/{id}/conflicts/{sid}/signal", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, err := s.groupMemberCheck(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		cs, sess, err := s.conflictSession(g.ID, r.PathValue("sid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		list, err := cs.Signals(sess.ID, u.ID, atoi64(r.URL.Query().Get("after")), 200)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"signals": list})
	}))
}

// conflictSession 取会话（顺带确认它确实属于这个用户组，别让人拿别组的 sid 来撞库）
func (s *Server) conflictSession(groupID, sid string) (*conflicts.Store, conflicts.Session, error) {
	cs, err := s.agent.ConflictsFor(accounts.GroupPrincipal(groupID))
	if err != nil {
		return nil, conflicts.Session{}, err
	}
	sess, err := cs.Session(sid)
	if err != nil {
		return nil, conflicts.Session{}, err
	}
	if sess.GroupID != groupID {
		return nil, conflicts.Session{}, errConflictNotInGroup
	}
	return cs, sess, nil
}

// pruneFork 定稿后把同名文档的其它版本删掉，只留正本。
// 实现挪到 agentsvc（AI 判定那半边也要用同一份语义，别写两遍）。
func (s *Server) pruneFork(groupID, name, keepDocID string) []string {
	if s.agent == nil {
		return nil
	}
	return s.agent.PruneFork(groupID, name, keepDocID)
}
