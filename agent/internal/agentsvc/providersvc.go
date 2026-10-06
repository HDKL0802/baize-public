package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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
