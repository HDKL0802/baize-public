package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"baize/internal/config"
	"baize/internal/llm"
)

/* ---------- 模型通道管理（控制台 / CLI 共用） ---------- */

// ProviderTestResult 通道探活结果
type ProviderTestResult struct {
	Name             string `json:"name"`
	Model            string `json:"model,omitempty"`
	Reply            string `json:"reply"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	TotalTokens      int    `json:"totalTokens"`
	LatencyMs        int64  `json:"latencyMs"`
}

// Providers 通道状态列表
func (s *Service) Providers() []ProviderInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ProviderInfo{}, s.providers...)
}

// ProviderSecret 取某条通道的原始取值（apiKey | baseUrl），只读、不写库。
//
// 这是全项目唯一会回显明文 Key 的取值入口：控制台本身已在令牌闸门之后，
// 用户要复制 Key 去别处粘贴。ProviderInfo / 列表响应里始终只带 hasApiKey，
// 明文绝不进那些结构体——所以这里单独开一个最小方法，避免污染展示用模型。
func (s *Service) ProviderSecret(name, field string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("要读哪条通道？请给 name")
	}
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	for i := range cfg.Providers {
		if cfg.Providers[i].Name != name {
			continue
		}
		switch field {
		case "apiKey":
			return cfg.Providers[i].APIKey, nil
		case "baseUrl":
			return cfg.Providers[i].BaseURL, nil
		}
		return "", fmt.Errorf("不支持的 field：%s", field)
	}
	return "", fmt.Errorf("没有这个模型通道：%s", name)
}

// ProviderSave 新增或更新一条模型通道（按 name 匹配），并写回 config.json
//
// 约定：
//   - apiKey 留空 = 只改其他字段，保留原来的 key（不把已有的 key 清掉）
//   - 地址不是本机时必须显式 allowRemote=true，否则明确报错（绝不"看着配好了其实调不通"）
func (s *Service) ProviderSave(one config.Provider, allowRemote bool) ([]ProviderInfo, error) {
	one.Name = strings.TrimSpace(one.Name)
	one.Protocol = strings.ToLower(strings.TrimSpace(one.Protocol))
	one.BaseURL = strings.TrimSpace(one.BaseURL)
	one.Model = strings.TrimSpace(one.Model)
	one.APIKey = strings.TrimSpace(one.APIKey)
	one.Cost = strings.TrimSpace(one.Cost)
	one.Privacy = strings.TrimSpace(one.Privacy)
	if one.Name == "" {
		return nil, errors.New("通道必须有 name")
	}
	if one.BaseURL == "" {
		return nil, errors.New("通道需要 baseUrl（本地如 http://127.0.0.1:11434/v1，云端如 https://api.deepseek.com/v1）")
	}
	if one.Model == "" {
		return nil, errors.New("通道需要 model（例如 deepseek-chat）")
	}
	if one.Protocol == "" {
		one.Protocol = "openai"
	}
	if one.Protocol != "openai" && one.Protocol != "anthropic" {
		return nil, fmt.Errorf("协议只支持 openai / anthropic，收到：%s", one.Protocol)
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
	// 本地优先策略：非本机地址要显式放行
	router := llm.NewRouter(cfg.AllowRemote)
	if err := router.CheckEndpoint(llm.Config{Name: one.Name, BaseURL: one.BaseURL}); err != nil {
		return nil, err
	}

	replaced := false
	for i := range cfg.Providers {
		if cfg.Providers[i].Name != one.Name {
			continue
		}
		if one.APIKey == "" {
			one.APIKey = cfg.Providers[i].APIKey
		}
		cfg.Providers[i] = one
		replaced = true
		break
	}
	if !replaced {
		cfg.Providers = append(cfg.Providers, one)
	}
	if err := s.SaveConfig(cfg); err != nil {
		return nil, err
	}
	s.lg.Info("模型通道已保存", "name", one.Name, "baseUrl", one.BaseURL, "model", one.Model, "allowRemote", cfg.AllowRemote)
	return s.Providers(), nil
}

// ProviderRemove 删掉一条模型通道
func (s *Service) ProviderRemove(name string) ([]ProviderInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("要删哪条通道？请给 name")
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	kept := make([]config.Provider, 0, len(cfg.Providers))
	hit := false
	for _, p := range cfg.Providers {
		if p.Name == name {
			hit = true
			continue
		}
		kept = append(kept, p)
	}
	if !hit {
		return nil, fmt.Errorf("没有这个模型通道：%s", name)
	}
	cfg.Providers = kept
	if err := s.SaveConfig(cfg); err != nil {
		return nil, err
	}
	s.lg.Info("模型通道已删除", "name", name)
	return s.Providers(), nil
}

/* ---------- 通道充值记账（只记金额/时间/备注，不做汇率换算） ---------- */

// ProviderRecharges 全部充值流水（按通道名分组的一份拷贝，调用方随便改不影响配置）。
func (s *Service) ProviderRecharges() map[string][]config.ProviderRecharge {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	out := map[string][]config.ProviderRecharge{}
	for name, list := range cfg.ProviderRecharges {
		if len(list) == 0 {
			continue
		}
		out[name] = append([]config.ProviderRecharge{}, list...)
	}
	return out
}

// AddProviderRecharge 给某条通道记一笔充值：name 必须非空且该通道存在，amount 必须 > 0。
// ID 用「rc + 毫秒的 36 进制」（与项目里 cron/token 的既有做法一致），At 记当前毫秒，落盘。
func (s *Service) AddProviderRecharge(name string, amount float64, note string) (config.ProviderRecharge, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.ProviderRecharge{}, errors.New("要记哪条通道的充值？请给 name")
	}
	if amount <= 0 {
		return config.ProviderRecharge{}, errors.New("充值金额要大于 0")
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	found := false
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == name {
			found = true
			break
		}
	}
	if !found {
		return config.ProviderRecharge{}, fmt.Errorf("没有这个模型通道：%s", name)
	}

	now := time.Now().UnixMilli()
	entry := config.ProviderRecharge{
		ID:     "rc" + strconv.FormatInt(now, 36),
		Amount: amount,
		Note:   strings.TrimSpace(note),
		At:     now,
	}
	if cfg.ProviderRecharges == nil {
		cfg.ProviderRecharges = map[string][]config.ProviderRecharge{}
	}
	cfg.ProviderRecharges[name] = append(cfg.ProviderRecharges[name], entry)
	if err := s.SaveConfig(cfg); err != nil {
		return config.ProviderRecharge{}, err
	}
	s.lg.Info("通道充值已记账", "name", name, "amount", amount, "id", entry.ID)
	return entry, nil
}

// RemoveProviderRecharge 删掉某条通道的一笔充值流水（删空了就把这条通道的键也清掉），落盘。
func (s *Service) RemoveProviderRecharge(name, id string) error {
	name = strings.TrimSpace(name)
	id = strings.TrimSpace(id)
	if name == "" || id == "" {
		return errors.New("要删哪条充值记录？请给 name 与 id")
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	list := cfg.ProviderRecharges[name]
	kept := make([]config.ProviderRecharge, 0, len(list))
	hit := false
	for _, e := range list {
		if e.ID == id {
			hit = true
			continue
		}
		kept = append(kept, e)
	}
	if !hit {
		return fmt.Errorf("没有这条充值记录：%s", id)
	}
	if cfg.ProviderRecharges == nil {
		cfg.ProviderRecharges = map[string][]config.ProviderRecharge{}
	}
	if len(kept) == 0 {
		delete(cfg.ProviderRecharges, name)
	} else {
		cfg.ProviderRecharges[name] = kept
	}
	if err := s.SaveConfig(cfg); err != nil {
		return err
	}
	s.lg.Info("通道充值记录已删除", "name", name, "id", id)
	return nil
}

// ProviderTest 对某条通道做一次极小的真实调用（会消耗一点点额度）
func (s *Service) ProviderTest(ctx context.Context, name string) (ProviderTestResult, error) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	var target *config.Provider
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == name {
			target = &cfg.Providers[i]
			break
		}
	}
	if target == nil {
		return ProviderTestResult{}, fmt.Errorf("没有这个模型通道：%s", name)
	}
	lc := llm.Config{
		Name: target.Name, Protocol: target.Protocol, BaseURL: target.BaseURL,
		APIKey: target.APIKey, Model: target.Model, TimeoutSec: target.TimeoutSec,
	}
	router := llm.NewRouter(cfg.AllowRemote)
	if err := router.CheckEndpoint(lc); err != nil {
		return ProviderTestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := llm.Ping(ctx, lc)
	if err != nil {
		return ProviderTestResult{}, fmt.Errorf("通道 %s 调用失败：%w", name, err)
	}
	reply := strings.TrimSpace(resp.Text)
	if reply == "" {
		reply = "（模型没有返回文字）"
	}
	return ProviderTestResult{
		Name: name, Model: resp.Model, Reply: reply,
		PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens: resp.Usage.TotalTokens, LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

// ProviderDiscoverResult 模型发现结果
type ProviderDiscoverResult struct {
	Source string   `json:"source"` // 从哪儿读到的（给界面如实显示）
	Models []string `json:"models"`
}

// ProviderDiscover 从一个端点「自动发现」可用模型名：
//   - OpenAI 兼容：GET {base}/models
//   - Ollama 原生：GET {base 去掉 /v1}/api/tags
//
// 只读（不改任何配置）；地址不是本机时同样要 allowRemote —— 与「本地优先」同一条规矩。
func (s *Service) ProviderDiscover(ctx context.Context, protocol, baseURL, apiKey string, allowRemote bool) (ProviderDiscoverResult, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ProviderDiscoverResult{}, errors.New("先填 Base URL，再去发现模型")
	}
	if protocol == "" {
		protocol = "openai"
	}
	s.mu.RLock()
	allow := s.cfg.AllowRemote || allowRemote
	s.mu.RUnlock()
	if err := llm.NewRouter(allow).CheckEndpoint(llm.Config{Name: "discover", BaseURL: baseURL}); err != nil {
		return ProviderDiscoverResult{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	found := map[string]bool{}
	var sources []string
	var lastErr error

	if ids, err := fetchOpenAIModels(ctx, client, baseURL, apiKey); err == nil && len(ids) > 0 {
		for _, id := range ids {
			found[id] = true
		}
		sources = append(sources, "GET "+strings.TrimRight(baseURL, "/")+"/models")
	} else if err != nil {
		lastErr = err
	}
	// Ollama 原生口径（它同时提供 OpenAI 兼容与原生 /api/tags）
	if ids, err := fetchOllamaTags(ctx, client, baseURL); err == nil && len(ids) > 0 {
		for _, id := range ids {
			found[id] = true
		}
		sources = append(sources, "GET "+ollamaRoot(baseURL)+"/api/tags")
	} else if err != nil && lastErr == nil {
		lastErr = err
	}

	if len(found) == 0 {
		if lastErr != nil {
			return ProviderDiscoverResult{}, fmt.Errorf("没发现任何模型：%w", lastErr)
		}
		return ProviderDiscoverResult{}, errors.New("端点没给出模型清单（有些服务不提供 /models，请手动填模型名）")
	}
	models := make([]string, 0, len(found))
	for m := range found {
		models = append(models, m)
	}
	sort.Strings(models)
	s.lg.Info("模型发现完成", "baseUrl", baseURL, "models", len(models))
	return ProviderDiscoverResult{Source: strings.Join(sources, " + "), Models: models}, nil
}

// fetchOpenAIModels 读 OpenAI 兼容的 /models
func fetchOpenAIModels(ctx context.Context, client *http.Client, base, apiKey string) ([]string, error) {
	u := strings.TrimRight(strings.TrimSpace(base), "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s 返回 http %d", u, resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if s := strings.TrimSpace(d.ID); s != "" {
			ids = append(ids, s)
		}
	}
	return ids, nil
}

// fetchOllamaTags 读 Ollama 原生的 /api/tags
func fetchOllamaTags(ctx context.Context, client *http.Client, base string) ([]string, error) {
	u := ollamaRoot(base) + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s 返回 http %d", u, resp.StatusCode)
	}
	var out struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		if s := firstNonEmptyStr(m.Name, m.Model); s != "" {
			ids = append(ids, s)
		}
	}
	return ids, nil
}

// ollamaRoot 把 OpenAI 兼容地址还原成 Ollama 根地址（去掉结尾的 /v1）
func ollamaRoot(base string) string {
	root := strings.TrimRight(strings.TrimSpace(base), "/")
	root = strings.TrimSuffix(root, "/v1")
	return strings.TrimRight(root, "/")
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
