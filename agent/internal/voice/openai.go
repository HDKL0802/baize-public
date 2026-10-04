package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"baize/internal/config"
)

// maxAudioBytes 单次收/发的音频上限。手机录一段话通常几十 KB 到几百 KB，
// 512MB 是"明显异常就别往里塞"的一道闸（避免把内存打爆）。
const maxAudioBytes = 512 << 20

type oaTTSRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format,omitempty"`
	Speed          float64 `json:"speed,omitempty"`
}

// openaiTTSOnce 走标准 /audio/speech：请求体是 JSON，响应体直接就是音频字节。
// 固定要 wav：这样和百炼那条路产物一致，长文分段拼接只有一条代码路径。
func openaiTTSOnce(ctx context.Context, cfg Config, text, voiceID string) ([]byte, error) {
	reqBody := oaTTSRequest{
		Model: cfg.TTSModel, Input: text, Voice: voiceID, ResponseFormat: "wav",
	}
	if cfg.Speed > 0 && cfg.Speed != 1 {
		reqBody.Speed = cfg.Speed
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	url := joinURL(cfg.BaseURL, "/audio/speech")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{Timeout: cfg.timeout()}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败：%w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, hintForMissingAudioEndpoint(config.VoiceProtocolOpenAI, cfg.BaseURL, resp.StatusCode, string(data))
	}
	audio, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes))
	if err != nil {
		return nil, fmt.Errorf("读取合成音频失败：%w", err)
	}
	if len(audio) == 0 {
		return nil, errors.New("语音接口返回了 0 字节音频（这个站可能只是假装支持 /audio/speech）")
	}
	// 上游说给 wav 却给了别的东西（有些中转站会偷换成 mp3），早点挑明，
	// 免得前端当成 wav 播出一段噪音
	if _, err := parseWAV(audio); err != nil {
		return nil, fmt.Errorf("这个站返回的不是 WAV 音频（%w）；把 tts 模型或协议换个支持 wav 的试试", err)
	}
	return audio, nil
}

type oaSTTResponse struct {
	Text  string `json:"text"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// openaiSTT 走标准 /audio/transcriptions：multipart 上传音频文件。
func openaiSTT(ctx context.Context, cfg Config, audio []byte, filename string) (*Transcript, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(audio); err != nil {
		return nil, err
	}
	_ = mw.WriteField("model", cfg.STTModel)
	_ = mw.WriteField("response_format", "json")
	if err := mw.Close(); err != nil {
		return nil, err
	}

	url := joinURL(cfg.BaseURL, "/audio/transcriptions")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{Timeout: cfg.timeout()}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败：%w", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取听写结果失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, hintForMissingAudioEndpoint(config.VoiceProtocolOpenAI, cfg.BaseURL, resp.StatusCode, string(data))
	}
	var parsed oaSTTResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("听写响应解析失败：%w（原始内容：%s）", err, snippet(data, 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("听写接口报错：%s", parsed.Error.Message)
	}
	text := strings.TrimSpace(parsed.Text)
	if text == "" {
		return nil, errors.New("没听出内容（这段录音里可能没有人声）")
	}
	return &Transcript{Text: text, Protocol: config.VoiceProtocolOpenAI, Model: cfg.STTModel}, nil
}

func joinURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return path
	}
	if strings.HasSuffix(base, path) {
		return base
	}
	return base + path
}
