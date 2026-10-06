package agentsvc

// 截图提问的后端能力：把一张截图交给**多模态模型**，做「提取文字 / 翻译」。
//
// 通道选择：优先自报 `kinds` 里含 `vision` 的通道（显式指定的多模态模型），
// 否则用第一条可用通道 —— 不管选到哪条，模型本身不支持图片时会把接口的原话抛出来，
// 不假装识别成功。

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"baize/internal/config"
	"baize/internal/llm"
)

// VisionResult 一次「看图」的结果
type VisionResult struct {
	Action    string `json:"action"` // extract | translate
	Text      string `json:"text"`
	Model     string `json:"model,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
}

// 图片上限（base64 解出来之后）：太大就请用户先裁小
const visionMaxImageBytes = 12 << 20

// Vision 看图做事：action=extract 提取文字 / translate 翻译（lang: zh|en，缺省 zh）。
func (s *Service) Vision(ctx context.Context, action, imageB64, lang string) (VisionResult, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action != "extract" && action != "translate" {
		return VisionResult{}, fmt.Errorf("不支持的 action：%s（可用 extract=提取文字 | translate=翻译）", action)
	}
	b64 := strings.TrimSpace(imageB64)
	if strings.HasPrefix(b64, "data:") { // 允许带 data URI 前缀
		if i := strings.Index(b64, ","); i >= 0 {
			b64 = b64[i+1:]
		}
	}
	if b64 == "" {
		return VisionResult{}, errors.New("没有图片数据（先截图）")
	}
	png, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return VisionResult{}, fmt.Errorf("图片不是合法 base64：%w", err)
	}
	if len(png) == 0 {
		return VisionResult{}, errors.New("图片是空的")
	}
	if len(png) > visionMaxImageBytes {
		return VisionResult{}, fmt.Errorf("图片太大（%.1f MB，上限 %d MB），先裁小一点",
			float64(len(png))/1024/1024, visionMaxImageBytes/1024/1024)
	}

	cfg, err := s.visionProvider()
	if err != nil {
		return VisionResult{}, err
	}

	prompt := "把这张图里的所有文字原样提取出来。只输出文字本身，不要解释、不要加标题；看不清的字用 □ 代替。"
	if action == "translate" {
		target := "中文"
		if strings.EqualFold(strings.TrimSpace(lang), "en") {
			target = "英文"
		}
		prompt = "把这张图里的文字翻译成" + target + "。先给出译文；如果原文不是" + target +
			"，再在译文后面另起一行给出原文。只输出结果，不要解释、不要加标题。"
	}

	start := time.Now()
	resp, err := llm.VisionChat(ctx, cfg, llm.VisionRequest{Prompt: prompt, ImagePNG: png, MaxTokens: 2048})
	if err != nil {
		return VisionResult{}, err
	}
	s.lg.Info("看图完成", "action", action, "model", resp.Model, "bytes", len(png))
	return VisionResult{
		Action: action, Text: resp.Text, Model: resp.Model,
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

// visionProvider 选一条给视觉用的通道：优先 kinds 含 "vision" 的，否则第一条可用通道。
func (s *Service) visionProvider() (llm.Config, error) {
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
	var first *config.Provider
	for i := range provs {
		p := &provs[i]
		if !usable[p.Name] {
			continue
		}
		if first == nil {
			first = p
		}
		for _, k := range p.Kinds {
			if strings.EqualFold(strings.TrimSpace(k), "vision") {
				return visionConfig(*p), nil
			}
		}
	}
	if first == nil {
		return llm.Config{}, errors.New("没有可用的模型通道：先在「模型通道」里配一个多模态模型（如 qwen-vl-max / gpt-4o / claude-3-5-sonnet）")
	}
	return visionConfig(*first), nil
}

func visionConfig(p config.Provider) llm.Config {
	return llm.Config{
		Name: p.Name, Protocol: p.Protocol, BaseURL: p.BaseURL,
		APIKey: p.APIKey, Model: p.Model, TimeoutSec: p.TimeoutSec,
	}
}
