package agentsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"baize/internal/config"
	"baize/internal/llm"
	"baize/internal/tools"
)

/* ---------- 外部 Agent 委托（优先任务二） ---------- */

// 委托一次最多带回多少字符（"原始输出上限"）。外部 Agent 可能吐出一大坨，
// 全塞回上下文既贵又可能把窗口撑爆 —— 超了就截断并如实标 truncated。
const externalMaxOut = 8000

// 记进"最近委托"时，目标/结论各留多长预览（只是给人扫一眼，不是全文）
const externalPreviewLen = 240

// ExternalAgentInfo 外部 Agent 状态（给控制台 / 桌面端看；令牌只回"是否已配"）
type ExternalAgentInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	URL        string `json:"url"`
	Note       string `json:"note,omitempty"`
	Enabled    bool   `json:"enabled"`
	TimeoutSec int    `json:"timeoutSec"`
	HasToken   bool   `json:"hasToken"`
	Local      bool   `json:"local"`
}

// DelegationRecord 一次委托的记录（进程内环形，最近 N 条；重启即清零）。
//
// 为什么不落盘：与活动追踪/观测计数同一口径——"最近派过什么"看的是当下的状况，
// 单次运行的完整轨迹在 runs.db 里（agent_call 也是一次工具调用）。界面上要标明时效。
type DelegationRecord struct {
	Target    string `json:"target"`
	Name      string `json:"name"`
	Goal      string `json:"goal"`
	Status    string `json:"status"` // done | failed
	LatencyMs int64  `json:"latencyMs"`
	Preview   string `json:"preview,omitempty"`
	Error     string `json:"error,omitempty"`
	At        int64  `json:"at"`
}

// ExternalAgents 外部 Agent 清单（配置视图，全部）
func (s *Service) ExternalAgents() []ExternalAgentInfo {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	out := make([]ExternalAgentInfo, 0, len(cfg.ExternalAgents))
	for _, ea := range cfg.ExternalAgents {
		out = append(out, ExternalAgentInfo{
			ID: ea.ID, Name: ea.Name, Type: ea.Type, URL: ea.URL, Note: ea.Note,
			Enabled: ea.Enabled, TimeoutSec: ea.TimeoutSec, HasToken: ea.Token != "",
			Local: isLocalBase(ea.URL),
		})
	}
	return out
}

// ExternalAgentSave 新增或更新一个外部 Agent（按 id 匹配），并写回 config.json。
//
// 约定（与模型通道对齐）：
//   - token 留空 = 只改其他字段，保留原来的令牌（不把已有的令牌清掉）；
//   - 地址不是本机时必须显式 allowRemote=true，否则明确报错。
func (s *Service) ExternalAgentSave(one config.ExternalAgent, allowRemote bool) ([]ExternalAgentInfo, error) {
	one.ID = strings.TrimSpace(one.ID)
	one.Name = strings.TrimSpace(one.Name)
	one.URL = strings.TrimSpace(one.URL)
	one.Token = strings.TrimSpace(one.Token)
	one.Note = strings.TrimSpace(one.Note)
	if one.ID == "" {
		return nil, errors.New("外部 Agent 必须有 id")
	}
	if one.URL == "" {
		return nil, errors.New("外部 Agent 需要 url（http 端点地址，或另一个白泽实例的地址）")
	}
	if one.Type == "" {
		one.Type = "http"
	}
	if !config.ValidExternalAgentType(one.Type) {
		return nil, fmt.Errorf("类型只支持 %s，收到：%s", strings.Join(config.ExternalAgentTypes(), " / "), one.Type)
	}
	if one.TimeoutSec <= 0 {
		one.TimeoutSec = 120
	}

	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if allowRemote {
		cfg.AllowRemote = true
	}
	// 本地优先"同一条规矩"：非本机地址要显式放行
	if err := llm.NewRouter(cfg.AllowRemote).CheckEndpoint(llm.Config{Name: one.Name, BaseURL: one.URL}); err != nil {
		return nil, err
	}

	replaced := false
	for i := range cfg.ExternalAgents {
		if cfg.ExternalAgents[i].ID != one.ID {
			continue
		}
		if one.Token == "" {
			one.Token = cfg.ExternalAgents[i].Token
		}
		cfg.ExternalAgents[i] = one
		replaced = true
		break
	}
	if !replaced {
		cfg.ExternalAgents = append(cfg.ExternalAgents, one)
	}
	if err := s.SaveConfig(cfg); err != nil {
		return nil, err
	}
	s.lg.Info("外部 Agent 已保存", "id", one.ID, "type", one.Type, "url", one.URL, "allowRemote", cfg.AllowRemote)
	return s.ExternalAgents(), nil
}

// ExternalAgentRemove 删掉一个外部 Agent
func (s *Service) ExternalAgentRemove(id string) ([]ExternalAgentInfo, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("要删哪个外部 Agent？请给 id")
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	kept := make([]config.ExternalAgent, 0, len(cfg.ExternalAgents))
	hit := false
	for _, ea := range cfg.ExternalAgents {
		if ea.ID == id {
			hit = true
			continue
		}
		kept = append(kept, ea)
	}
	if !hit {
		return nil, fmt.Errorf("没有这个外部 Agent：%s", id)
	}
	cfg.ExternalAgents = kept
	if err := s.SaveConfig(cfg); err != nil {
		return nil, err
	}
	s.lg.Info("外部 Agent 已删除", "id", id)
	return s.ExternalAgents(), nil
}

// ExternalAgentTest 探活：用一句极小的任务真跑一次，如实回报耗时与回文
func (s *Service) ExternalAgentTest(ctx context.Context, id string) (tools.ExternalAgentResult, error) {
	return s.CallExternalAgent(ctx, id, "探活：请只回一句 pong（不要做别的事）")
}

// listExternalAgentBriefs 给运行期用的精简清单（只给启用的——禁用的一上来就派不通）
func (s *Service) listExternalAgentBriefs() []tools.ExternalAgentBrief {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	out := make([]tools.ExternalAgentBrief, 0, len(cfg.ExternalAgents))
	for _, ea := range cfg.ExternalAgents {
		if !ea.Enabled {
			continue
		}
		out = append(out, tools.ExternalAgentBrief{ID: ea.ID, Name: ea.Name, Type: ea.Type, Note: ea.Note})
	}
	return out
}

// ListExternalAgents 实现 tools.ExternalAgentCaller
func (s *Service) ListExternalAgents() ([]tools.ExternalAgentBrief, error) {
	return s.listExternalAgentBriefs(), nil
}

// CallExternalAgent 把一段活委托给指定外部 Agent，返回结论 + 耗时（实现 tools.ExternalAgentCaller）。
//
// 安全口径（必须守住）：只把 goal 文本发出去，**不带**本机文件 / 记忆 / 密钥；
// 回来的内容当"外部输入"，不在这里执行、也不提升成指令。
func (s *Service) CallExternalAgent(ctx context.Context, id, goal string) (tools.ExternalAgentResult, error) {
	id = strings.TrimSpace(id)
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return tools.ExternalAgentResult{Target: id, Status: "failed"}, errors.New("交给外部 Agent 的任务不能为空")
	}
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	var target *config.ExternalAgent
	for i := range cfg.ExternalAgents {
		if cfg.ExternalAgents[i].ID == id {
			target = &cfg.ExternalAgents[i]
			break
		}
	}
	if target == nil {
		ids := make([]string, 0, len(cfg.ExternalAgents))
		for _, ea := range cfg.ExternalAgents {
			ids = append(ids, ea.ID)
		}
		hint := "（还没有配置任何外部 Agent，去「外部 Agent」里加一个）"
		if len(ids) > 0 {
			hint = "（可用：" + strings.Join(ids, "、") + "）"
		}
		return tools.ExternalAgentResult{Target: id, Status: "failed"}, fmt.Errorf("没有这个外部 Agent：%s %s", id, hint)
	}
	if !target.Enabled {
		return tools.ExternalAgentResult{Target: id, Status: "failed"}, fmt.Errorf("外部 Agent「%s」已停用", target.Name)
	}
	if err := llm.NewRouter(cfg.AllowRemote).CheckEndpoint(llm.Config{Name: target.Name, BaseURL: target.URL}); err != nil {
		return tools.ExternalAgentResult{Target: id, Status: "failed"}, err
	}

	timeout := time.Duration(target.TimeoutSec) * time.Second
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	var (
		text string
		err  error
	)
	switch target.Type {
	case "baize":
		text, err = callBaizeAgent(cctx, target.URL, target.Token, goal)
	default:
		text, err = callHTTPAgent(cctx, target.URL, target.Token, goal)
	}
	ms := time.Since(start).Milliseconds()

	res := tools.ExternalAgentResult{Target: target.ID, LatencyMs: ms}
	if err != nil {
		res.Status = "failed"
		res.Error = err.Error()
		if s.metrics != nil {
			s.metrics.NoteTarget(target.ID, false, ms, err.Error())
		}
		s.recordDelegation(DelegationRecord{
			Target: target.ID, Name: target.Name, Goal: snippet(goal, externalPreviewLen),
			Status: "failed", LatencyMs: ms, Error: err.Error(),
		})
		return res, fmt.Errorf("外部 Agent「%s」调用失败：%w", target.Name, err)
	}
	if truncated, out := truncateRunes(text, externalMaxOut); truncated {
		res.Truncated = true
		res.Text = out
	} else {
		res.Text = text
	}
	res.Status = "done"
	if s.metrics != nil {
		s.metrics.NoteTarget(target.ID, true, ms, "")
	}
	s.recordDelegation(DelegationRecord{
		Target: target.ID, Name: target.Name, Goal: snippet(goal, externalPreviewLen),
		Status: "done", LatencyMs: ms, Preview: snippet(strings.TrimSpace(text), externalPreviewLen),
	})
	s.lg.Info("外部 Agent 委托完成", "id", target.ID, "type", target.Type, "ms", ms, "truncated", res.Truncated)
	return res, nil
}

// Delegations 最近的委托记录（新→旧），进程内、重启即清零
func (s *Service) Delegations() []DelegationRecord {
	s.delMu.Lock()
	defer s.delMu.Unlock()
	return append([]DelegationRecord{}, s.delegations...)
}

func (s *Service) recordDelegation(rec DelegationRecord) {
	rec.At = time.Now().UnixMilli()
	s.delMu.Lock()
	defer s.delMu.Unlock()
	// 新记录放最前，最多留 50 条（超出的从尾巴丢）
	s.delegations = append([]DelegationRecord{rec}, s.delegations...)
	if len(s.delegations) > 50 {
		s.delegations = s.delegations[:50]
	}
}

/* ---------- 两种类型的调用 ---------- */

// callHTTPAgent 任意 HTTP 端点：POST {"goal":...,"task":...}，把回复文本当结论。
//
// 请求体同时给 goal 与 task 两个键（不同端点可能认不同的名字，多给一个无副作用）。
func callHTTPAgent(ctx context.Context, url, token, goal string) (string, error) {
	body, _ := json.Marshal(map[string]any{"goal": goal, "task": goal})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	raw, code, err := doRequest(req)
	if err != nil {
		return "", err
	}
	if code < 200 || code >= 300 {
		return "", fmt.Errorf("端点返回 HTTP %d：%s", code, snippet(string(raw), 300))
	}
	return extractAgentText(raw), nil
}

// callBaizeAgent 另一个白泽实例：喂它的 /api/agent/run（wait=true）+ 配对令牌，复用同一套协议。
func callBaizeAgent(ctx context.Context, baseURL, token, goal string) (string, error) {
	url := strings.TrimRight(baseURL, "/") + "/api/agent/run"
	body, _ := json.Marshal(map[string]any{"goal": goal, "wait": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Baize-Token", token)
	}
	raw, code, err := doRequest(req)
	if err != nil {
		return "", err
	}
	if code < 200 || code >= 300 {
		return "", fmt.Errorf("对端白泽返回 HTTP %d：%s", code, snippet(string(raw), 300))
	}
	// 形状：{"result":{...,"text":"..."}} 或 {"result":{...},"error":"..."}
	var parsed struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("对端白泽返回的不是 JSON：%s", snippet(string(raw), 300))
	}
	if strings.TrimSpace(parsed.Error) != "" {
		return "", errors.New(strings.TrimSpace(parsed.Error))
	}
	if txt := pickText(parsed.Result); txt != "" {
		return txt, nil
	}
	return snippet(string(raw), 300), nil
}

// doRequest 发一次请求并读回（读取上限 1MiB，防对端吐超大响应把内存打爆）
func doRequest(req *http.Request) ([]byte, int, error) {
	client := &http.Client{Timeout: 0} // 超时由 ctx 控制（每次委托各有自己的 timeout）
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// extractAgentText 从响应体里取"结论文本"：是 JSON 就按常见字段找，不是就原样当文本。
func extractAgentText(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return trimmed // 不是 JSON：原样当文本（可能是纯文本端点）
	}
	if s := pickText(parsed); s != "" {
		return s
	}
	return trimmed
}

// pickText 在 JSON 结构里按常见字段名找第一段文本（递归，兼容嵌套的 result/output/message）
func pickText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.RawMessage:
		var inner any
		if err := json.Unmarshal(t, &inner); err != nil {
			return ""
		}
		return pickText(inner)
	case map[string]any:
		for _, k := range []string{"text", "reply", "output", "result", "content", "message", "answer", "data"} {
			if raw, ok := t[k]; ok {
				if s := pickText(raw); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// snippet 取一段预览（按字符截断，末尾加省略号）
func snippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// truncateRunes 按字符截断，返回 (是否截断, 结果)
func truncateRunes(s string, max int) (bool, string) {
	if max <= 0 {
		return false, s
	}
	r := []rune(s)
	if len(r) <= max {
		return false, s
	}
	return true, string(r[:max])
}
