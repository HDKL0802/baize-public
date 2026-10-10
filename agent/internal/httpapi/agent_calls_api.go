package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"baize/internal/accounts"
	"baize/internal/calls"
	"baize/internal/conflicts"
)

// 两人通话（组内）：文字留言 + 音视频信令中转，像"微信"那样互通，但后端
// 不碰媒体本身（口径与 conflicts 的音视频信令一致）。通话可以挂在某次文档
// 协商下面（conflictSid），协商时的留言会一并交给「回复AI」通读。
//
// 路由都挂在用户组下（`/api/agent/groups/{id}/calls/...`）：成员校验一次就够，
// "这是哪个组的通话"一目了然；通话必须双方都在组里。
func (s *Server) registerCalls(mux *http.ServeMux) {
	if s.agent == nil || s.agent.Accounts() == nil {
		return
	}

	// 取通话库（带成员校验）
	openCalls := func(r *http.Request, groupID string) (accounts.Group, accounts.User, *calls.Store, error) {
		g, u, err := s.groupMemberCheck(r, groupID)
		if err != nil {
			return g, u, nil, err
		}
		st, err := s.agent.CallsFor(accounts.GroupPrincipal(g.ID))
		if err != nil {
			return g, u, nil, err
		}
		return g, u, st, nil
	}

	// 参与者视角：取通话并确认调用者是 a 或 b（防组内第三人偷看别人通话）
	participantCall := func(st *calls.Store, g accounts.Group, u accounts.User, cid string) (calls.Call, error) {
		c, err := st.CallInGroup(g.ID, cid)
		if err != nil {
			return c, err
		}
		if c.AID != u.ID && c.BID != u.ID {
			return c, errNotInCall
		}
		return c, nil
	}

	// 我在这个组里的通话（默认只看进行中的；?all=1 连挂断的一起列）
	mux.HandleFunc("GET /api/agent/groups/{id}/calls", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, st, err := openCalls(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		onlyLive := r.URL.Query().Get("all") != "1"
		list, err := st.Calls(g.ID, u.ID, onlyLive)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"calls": list})
	}))

	// 开（或复用）一次通话：{peer, conflictSid?}
	// 同一对人在 live 期间反复开只会拿到同一条 —— 通话像聊天窗，不该越开越多。
	mux.HandleFunc("POST /api/agent/groups/{id}/calls", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, st, err := openCalls(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		var req struct {
			Peer        string `json:"peer"`
			ConflictSID string `json:"conflictSid"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		peerID := strings.TrimSpace(req.Peer)
		if peerID == "" {
			writeErr(w, http.StatusBadRequest, "peer 不能为空")
			return
		}
		// 对方得是组里的成员（或我自己）——通话只发生在组内
		reg := s.agent.Accounts()
		peer, err := reg.User(peerID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "组里没有这个用户")
			return
		}
		if peerID != u.ID && !reg.IsMember(g.ID, peerID) && !u.Admin {
			writeErr(w, http.StatusBadRequest, "对方不在这个用户组里，通话只能在组内进行")
			return
		}
		c, err := st.OpenCall(g.ID, u.ID, u.Name, peerID, peer.Name, strings.TrimSpace(req.ConflictSID))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call": c})
	}))

	// 通话详情 + 留言/信令增量（轮询姿势与冲突会话一致：afterMsg / afterSignal）
	mux.HandleFunc("GET /api/agent/groups/{id}/calls/{cid}", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, st, err := openCalls(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		c, err := participantCall(st, g, u, r.PathValue("cid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		q := r.URL.Query()
		msgs, err := st.Messages(c.ID, atoi64(q.Get("afterMsg")), atoiDefault(q.Get("msgLimit"), 200))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		sigs, err := st.Signals(c.ID, u.ID, atoi64(q.Get("afterSignal")), 200)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := map[string]any{"call": c, "messages": msgs, "signals": sigs, "me": u.ID}
		if c.ConflictSID != "" {
			// 挂在某次协商下：把协商状态带出来（通话浮窗要显示「回复AI」按钮与进度）
			if cs, csess, err := s.conflictSession(g.ID, c.ConflictSID); err == nil {
				out["conflict"] = csess
				if confirmers, err := cs.AIConfirmers(csess.ID); err == nil {
					out["aiConfirmers"] = confirmers
				}
			} else {
				out["conflictGone"] = true // 协商会话被清了：通话还在，只是没得挂
			}
		}
		writeJSON(w, http.StatusOK, out)
	}))

	// 发一条文字留言
	mux.HandleFunc("POST /api/agent/groups/{id}/calls/{cid}/messages", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, st, err := openCalls(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		c, err := participantCall(st, g, u, r.PathValue("cid"))
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
		m, err := st.AddMessage(c.ID, u.ID, u.Name, req.Text)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": m})
	}))

	// 音视频信令：SDP/ICE 放这里转给对方（不碰媒体本身）
	mux.HandleFunc("POST /api/agent/groups/{id}/calls/{cid}/signal", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, st, err := openCalls(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		c, err := participantCall(st, g, u, r.PathValue("cid"))
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
		if err := st.PostSignal(c.ID, u.ID, strings.TrimSpace(req.To), req.Kind, req.Payload); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	// 拉信令（增量轮询；自己发的会被后端过滤掉）
	mux.HandleFunc("GET /api/agent/groups/{id}/calls/{cid}/signal", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, st, err := openCalls(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		c, err := participantCall(st, g, u, r.PathValue("cid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		list, err := st.Signals(c.ID, u.ID, atoi64(r.URL.Query().Get("after")), 200)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"signals": list})
	}))

	// 挂断（状态翻成 ended；留言与信令留着可回看）
	mux.HandleFunc("POST /api/agent/groups/{id}/calls/{cid}/end", s.api(func(w http.ResponseWriter, r *http.Request) {
		g, u, st, err := openCalls(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		c, err := participantCall(st, g, u, r.PathValue("cid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		updated, err := st.End(c.ID, u.ID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call": updated})
	}))

	// 「回复AI」确认闸：挂在协商会话下。两位（每一版的作者）各点一次，
	// 都确认了后端才让模型通读上下文去判定/合并——单人点了就如实回"还差谁"。
	mux.HandleFunc("POST /api/agent/groups/{id}/conflicts/{sid}/ai-confirm", s.api(func(w http.ResponseWriter, r *http.Request) {
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
		if sess.Status != conflicts.StatusOpen {
			writeErr(w, http.StatusBadRequest, "这次协商已经收场了（"+sess.Status+"），不用再请 AI 判定")
			return
		}
		all, missing, err := cs.ConfirmAI(sess.ID, u.ID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// 都确认了 → 触发一次 AI 通读判定（running/failed 都允许重触发，done 不收）
		triggered := false
		if all && (sess.AIStatus == "" || sess.AIStatus == "failed") {
			go s.agent.ConflictAIJudge(g.ID, sess.ID)
			triggered = true
		}
		confirmers, _ := cs.AIConfirmers(sess.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "allConfirmed": all, "missing": missing,
			"aiStatus": sess.AIStatus, "triggered": triggered,
			"aiConfirmers": confirmers,
		})
	}))
}

// errNotInCall 调用者不是这条通话的参与人（组内第三人不能偷看）
var errNotInCall = errors.New("你不是这条通话的参与人")
