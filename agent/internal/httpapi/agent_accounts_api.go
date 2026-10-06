package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"baize/internal/accounts"
	"baize/internal/kb"
	"baize/internal/memory"
)

// 多用户底座接口：登录会话 + 账号 + 用户组。
//
// 鉴权是**两层**：
//  1. 外层仍是配对令牌（X-Baize-Token）——决定"能不能连上这个后端"；
//  2. 内层才是登录会话（X-Baize-Session / ?session= / cookie）——决定"以谁的身份访问数据"。
//
// 没登录 = 默认主体（根数据目录），老的单人用法一点没变；登录后记忆库/知识库按该用户的分区走。

// sessionHeader 登录会话令牌的请求头名
const sessionHeader = "X-Baize-Session"

// sessionCookie 桌面端界面用的会话 cookie（同源，HttpOnly）
const sessionCookie = "bz_session"

// registerAccounts 注册账号与用户组接口
func (s *Server) registerAccounts(mux *http.ServeMux) {
	if s.agent == nil || s.agent.Accounts() == nil {
		return
	}
	reg := s.agent.Accounts()

	/* ---- 身份 ---- */

	// 当前身份：没登录就是默认主体（供界面显示"现在是谁"）
	mux.HandleFunc("GET /api/agent/auth/whoami", s.api(func(w http.ResponseWriter, r *http.Request) {
		p, u, ok := s.principalOf(r)
		out := map[string]any{
			"principal": p.Key(), "loggedIn": ok,
			"default": p.IsDefault(),
		}
		if ok {
			out["user"] = u
			if gs, err := reg.GroupsOf(u.ID); err == nil {
				out["groups"] = gs
			}
		}
		writeJSON(w, http.StatusOK, out)
	}))

	// 登录：回一个会话令牌，之后把它放进 X-Baize-Session 头
	mux.HandleFunc("POST /api/agent/auth/login", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		u, tok, err := reg.Login(req.Name, req.Password)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: tok, Path: "/", MaxAge: int(accounts.SessionTTL.Seconds()),
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "user": u, "token": tok, "principal": accounts.UserPrincipal(u.ID).Key(),
		})
	}))

	// 登出
	mux.HandleFunc("POST /api/agent/auth/logout", s.api(func(w http.ResponseWriter, r *http.Request) {
		tok := s.sessionToken(r)
		if tok != "" {
			_ = reg.Logout(tok)
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	/* ---- 用户 ---- */

	mux.HandleFunc("GET /api/agent/users", s.api(func(w http.ResponseWriter, r *http.Request) {
		users, err := reg.Users()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": users})
	}))

	mux.HandleFunc("POST /api/agent/users", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name     string `json:"name"`
			Password string `json:"password"`
			Admin    bool   `json:"admin"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		u, err := reg.CreateUser(req.Name, req.Password, req.Admin)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
	}))

	mux.HandleFunc("POST /api/agent/users/{id}/password", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Password string `json:"password"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := reg.SetPassword(r.PathValue("id"), req.Password); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	mux.HandleFunc("DELETE /api/agent/users/{id}", s.api(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := reg.DeleteUser(id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": id})
	}))

	/* ---- 用户组 ---- */

	mux.HandleFunc("GET /api/agent/groups", s.api(func(w http.ResponseWriter, r *http.Request) {
		groups, err := reg.Groups()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
	}))

	mux.HandleFunc("POST /api/agent/groups", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name    string `json:"name"`
			OwnerID string `json:"ownerId"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		g, err := reg.CreateGroup(req.Name, req.OwnerID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": g})
	}))

	mux.HandleFunc("GET /api/agent/groups/{id}", s.api(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		g, err := reg.Group(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		members, err := reg.Members(id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"group": g, "members": members})
	}))

	// 成员增删：action=add|remove
	mux.HandleFunc("POST /api/agent/groups/{id}/members", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string `json:"action"`
			UserID string `json:"userId"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		id := r.PathValue("id")
		switch strings.ToLower(strings.TrimSpace(req.Action)) {
		case "add", "":
			if err := reg.AddMember(id, req.UserID); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		case "remove":
			if err := reg.RemoveMember(id, req.UserID); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+req.Action+"（可用 add | remove）")
			return
		}
		members, _ := reg.Members(id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "members": members})
	}))

	mux.HandleFunc("DELETE /api/agent/groups/{id}", s.api(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := reg.DeleteGroup(id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": id})
	}))
}

/* ---------- 身份解析（供记忆库/知识库按主体路由） ---------- */

// sessionToken 从请求里取会话令牌（头 > 查询串 > cookie）
func (s *Server) sessionToken(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get(sessionHeader)); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("session")); v != "" {
		return v
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return strings.TrimSpace(c.Value)
	}
	return ""
}

// principalOf 决定这次请求以谁的身份访问数据。
//
// 优先级：
//  1. ?as=<主体>（只有**管理员**能用，普通用户传了会被忽略，防止越权读别人的库）；
//  2. 登录会话 → 该用户的私人分区；
//  3. 都没有 → 默认主体（根数据目录，老行为）。
func (s *Server) principalOf(r *http.Request) (accounts.Principal, accounts.User, bool) {
	if s.agent == nil || s.agent.Accounts() == nil {
		return accounts.PrincipalDefault, accounts.User{}, false
	}
	reg := s.agent.Accounts()
	u, loggedIn := reg.Session(s.sessionToken(r))
	if as := strings.TrimSpace(r.URL.Query().Get("as")); as != "" && loggedIn && u.Admin {
		return accounts.ParsePrincipal(as), u, true
	}
	if loggedIn {
		return accounts.UserPrincipal(u.ID), u, true
	}
	return accounts.PrincipalDefault, accounts.User{}, false
}

// principal 只取主体（多数接口用不到用户对象）
func (s *Server) principal(r *http.Request) accounts.Principal {
	p, _, _ := s.principalOf(r)
	return p
}

// memOf 取这次请求该用的记忆库（按登录身份路由到用户分区）
func (s *Server) memOf(r *http.Request) (*memory.Store, error) {
	if s.agent == nil {
		return nil, errors.New("Agent 服务未启动")
	}
	p, _, _ := s.principalOf(r)
	return s.agent.PartitionMemory(p)
}

// kbOf 取这次请求该用的知识库（按登录身份路由到用户分区）
//
// 没挂 Agent 的「精简装配」（只挂了知识库，例如手机端那套）直接用默认那本；
// 挂了 Agent 就按主体走分区（未登录 = 默认主体 = Agent 自己那本，行为不变）。
func (s *Server) kbOf(r *http.Request) (*kb.Service, error) {
	if s.agent == nil {
		if s.kb == nil {
			return nil, errors.New("知识库未启用")
		}
		return s.kb, nil
	}
	return s.agent.PartitionKB(s.principal(r))
}
