package agentsvc

// 实时双语字幕的第二半：把听写出来的一句中文/英文翻成另一种语言。
//
// 通道选择：翻译是纯文本活，取**第一条可用通道**即可（不像看图那样必须多模态）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"baize/internal/config"
	"baize/internal/llm"
)

// TranslateMaxChars 单次翻译的字数上限。字幕是逐句翻译，不该来长文。
const TranslateMaxChars = 2000

// TranslateResult 一次「文字翻译」的结果
type TranslateResult struct {
	Text      string `json:"text"`
	Model     string `json:"model"`
	LatencyMs int64  `json:"millis"`
}

// TranslateText 把一段文字翻译成另一种语言：to=zh|en（缺省/非法一律按 zh）。
func (s *Service) TranslateText(ctx context.Context, text, to string) (TranslateResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return TranslateResult{}, errors.New("text 不能为空")
	}
	if n := len([]rune(text)); n > TranslateMaxChars {
		return TranslateResult{}, fmt.Errorf("text 太长（%d 字，上限 %d 字）：字幕是逐句翻译，请分段送来", n, TranslateMaxChars)
	}

	target := "中文"
	if strings.EqualFold(strings.TrimSpace(to), "en") {
		target = "英文"
	}
	prompt := "把下面这段文字翻译成" + target + "。只输出译文本身，不要解释、不要加引号、不要加语言标注。\n\n" + text

	cfg, err := s.translateProvider()
	if err != nil {
		return TranslateResult{}, err
	}
	prov, err := llm.New(cfg)
	if err != nil {
		return TranslateResult{}, err
	}

	start := time.Now()
	resp, err := prov.Chat(ctx, llm.Request{
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		MaxTokens: 1024,
	})
	if err != nil {
		return TranslateResult{}, err
	}
	s.lg.Info("翻译完成", "to", target, "model", resp.Model, "chars", len([]rune(text)))
	return TranslateResult{
		Text: resp.Text, Model: resp.Model,
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

// translateProvider 取第一条可用通道；一条都没有时明确报错。
func (s *Service) translateProvider() (llm.Config, error) {
	s.mu.RLock()
	provs := append([]config.Provider{}, s.cfg.Providers...)
	infos := append([]ProviderInfo{}, s.providers...)
	s.mu.RUnlock()

	usable := map[string]bool{}
	for _, in := range infos {
		if in.Usable {
			usable[in.Name] = true
		}
	}
	for i := range provs {
		p := provs[i]
		if !usable[p.Name] {
			continue
		}
		return llm.Config{
			Name: p.Name, Protocol: p.Protocol, BaseURL: p.BaseURL,
			APIKey: p.APIKey, Model: p.Model, TimeoutSec: p.TimeoutSec,
		}, nil
	}
	return llm.Config{}, errors.New("没有可用的模型通道：先在「模型通道」里配一个")
}
