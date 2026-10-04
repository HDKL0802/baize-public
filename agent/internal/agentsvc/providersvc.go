package agentsvc

import (
	"context"
	"errors"
	"fmt"
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
