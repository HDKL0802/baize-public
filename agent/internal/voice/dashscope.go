package voice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

// 阿里云百炼（DashScope）原生接口。两条路：
//
//	合成：POST {base}/api/v1/services/aigc/multimodal-generation/generation
//	      model=qwen-tts，body {model, input:{text, voice}}
//	      → output.audio.url（一段 wav 的临时地址）/ output.audio.data（base64）
//	      compatible-mode 那条路没有 /audio/speech，所以必须走这个原生口。
//
//	听写：WebSocket {ws}/api-ws/v1/inference
//	      run-task(paraformer-realtime-v2, pcm/16000) → 二进制 PCM → finish-task
//	      实时返回逐句结果。这样不用先把音频传上公网（百炼的 URL 转写要求公网可达，
//	      家里的 NAS 和手机都在内网，那条路走不通）。

const (
	dashscopeTTSPath = "/api/v1/services/aigc/multimodal-generation/generation"
	dashscopeWSPath  = "/api-ws/v1/inference"

	// chunkRunes 单次合成最多多少字。百炼对单次输入长度有上限，长回复必须切开，
	// 否则会整段报错（用户看到的就是"不念了"）。切开的位置尽量落在句号上。
	chunkRunes = 300
	// sttChunkBytes 每帧 100ms（16kHz 单声道 16 位 = 3200 字节）
	sttChunkBytes = 3200
)

func dashscopeBase(cfg Config) string {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = config.DashScopeBaseURL
	}
	return strings.TrimRight(base, "/")
}

type dsTTSRequest struct {
	Model string `json:"model"`
	Input struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
	} `json:"input"`
}

type dsTTSResponse struct {
	Output struct {
		Audio struct {
			Data string `json:"data"`
			URL  string `json:"url"`
		} `json:"audio"`
	} `json:"output"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// dashscopeTTSOnce 合成一段（百炼原生接口），返回 WAV 字节。
func dashscopeTTSOnce(ctx context.Context, cfg Config, text, voiceID string) ([]byte, error) {
	var reqBody dsTTSRequest
	reqBody.Model = cfg.TTSModel
	reqBody.Input.Text = text
	reqBody.Input.Voice = voiceID
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	endpoint := dashscopeBase(cfg) + dashscopeTTSPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{Timeout: cfg.timeout()}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败：%w", endpoint, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取合成响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("百炼合成返回 HTTP %d：%s", resp.StatusCode, snippet(data, 300))
	}
	var parsed dsTTSResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("合成响应解析失败：%w（原始内容：%s）", err, snippet(data, 200))
	}
	if parsed.Code != "" {
		return nil, fmt.Errorf("百炼合成报错 [%s]：%s", parsed.Code, parsed.Message)
	}
	// 有时直接给 base64，有时给一个临时地址；两种都接住
	if parsed.Output.Audio.Data != "" {
		raw, err := base64.StdEncoding.DecodeString(parsed.Output.Audio.Data)
		if err != nil {
			return nil, fmt.Errorf("合成音频（base64）解不开：%w", err)
		}
		return raw, nil
	}
	if parsed.Output.Audio.URL == "" {
		return nil, fmt.Errorf("百炼没返回音频（既没有 data 也没有 url）。原始响应：%s", snippet(data, 300))
	}
	raw, err := fetchBytes(ctx, client, parsed.Output.Audio.URL)
	if err != nil {
		return nil, err
	}
	if _, err := parseWAV(raw); err != nil {
		return nil, fmt.Errorf("百炼合成回来的音频不是 PCM WAV（%w）；换个合成模型试试（qwen-tts）", err)
	}
	return raw, nil
}

func fetchBytes(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("取回合成音频失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("取回合成音频失败：HTTP %d", resp.StatusCode)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes))
	if err != nil {
		return nil, fmt.Errorf("读取合成音频失败：%w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("合成音频是 0 字节")
	}
	return out, nil
}

/* ================= 听写 ================= */

type dsWSHeader struct {
	TaskID       string `json:"task_id,omitempty"`
	Action       string `json:"action,omitempty"`
	Streaming    string `json:"streaming,omitempty"`
	Event        string `json:"event,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type dsWSMessage struct {
	Header  dsWSHeader `json:"header"`
	Payload struct {
		Output struct {
			Sentence struct {
				SentenceID  int    `json:"sentence_id"`
				Text        string `json:"text"`
				SentenceEnd bool   `json:"sentence_end"`
			} `json:"sentence"`
		} `json:"output"`
	} `json:"payload"`
}

func dashscopeWSURL(cfg Config) (string, error) {
	base := dashscopeBase(cfg)
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("百炼地址解析失败（%s）：%w", base, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		u.Scheme = "wss"
	}
	u.Path = dashscopeWSPath
	u.RawQuery = ""
	return u.String(), nil
}

// dashscopeSTT 走实时 WebSocket，把整段音频推上去再收逐句结果。
func dashscopeSTT(ctx context.Context, cfg Config, audio []byte) (*Transcript, error) {
	wav, err := parseWAV(audio)
	if err != nil {
		return nil, fmt.Errorf("听写需要 WAV 音频：%w", err)
	}
	pcm := toPCM16kMono(wav)
	if len(pcm) == 0 {
		return nil, errors.New("这段录音解出来是空的")
	}
	ms := pcmMillis(pcm)
	if ms < 200 {
		return nil, fmt.Errorf("录音太短（约 %d 毫秒），按住说话至少说半秒", ms)
	}
	if !hasVoice(pcm) {
		return nil, errors.New("这段录音里几乎没有人声（可能是没录上麦克风）")
	}

	wsURL, err := dashscopeWSURL(cfg)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 20 * time.Second}
	header := http.Header{}
	header.Set("Authorization", "bearer "+cfg.APIKey)
	conn, resp, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		detail := ""
		if resp != nil && resp.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
			resp.Body.Close()
			detail = "：" + snippet(body, 200)
		}
		return nil, fmt.Errorf("连不上百炼实时听写（%s）%s：%w", wsURL, detail, err)
	}
	defer conn.Close()

	taskID := fmt.Sprintf("bz-%d", time.Now().UnixNano())
	deadline := time.Now().Add(cfg.timeout())
	_ = conn.SetWriteDeadline(deadline)
	_ = conn.SetReadDeadline(deadline)

	runTask := map[string]any{
		"header": map[string]any{
			"action": "run-task", "task_id": taskID, "streaming": "duplex",
		},
		"payload": map[string]any{
			"task_group": "audio",
			"task":       "asr",
			"function":   "recognition",
			"model":      cfg.STTModel,
			"parameters": map[string]any{"format": "pcm", "sample_rate": 16000},
			"input":      map[string]any{},
		},
	}
	if err := conn.WriteJSON(runTask); err != nil {
		return nil, fmt.Errorf("发送听写任务失败：%w", err)
	}
	if err := waitTaskStarted(conn); err != nil {
		return nil, err
	}

	for off := 0; off < len(pcm); off += sttChunkBytes {
		end := off + sttChunkBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, pcm[off:end]); err != nil {
			return nil, fmt.Errorf("推送录音失败：%w", err)
		}
		time.Sleep(5 * time.Millisecond) // 稍微匀一下，别一口气灌进去
	}
	finish := map[string]any{
		"header":  map[string]any{"action": "finish-task", "task_id": taskID, "streaming": "duplex"},
		"payload": map[string]any{"input": map[string]any{}},
	}
	if err := conn.WriteJSON(finish); err != nil {
		return nil, fmt.Errorf("结束听写任务失败：%w", err)
	}

	sentences := map[int]string{}
	var order []int
	for {
		var msg dsWSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return nil, fmt.Errorf("收听写结果失败：%w", err)
		}
		switch msg.Header.Event {
		case "task-failed":
			code := msg.Header.ErrorCode
			if code == "" {
				code = "未知错误"
			}
			return nil, fmt.Errorf("百炼听写失败 [%s]：%s", code, msg.Header.ErrorMessage)
		case "result-generated":
			s := msg.Payload.Output.Sentence
			if strings.TrimSpace(s.Text) == "" {
				continue
			}
			if _, seen := sentences[s.SentenceID]; !seen {
				order = append(order, s.SentenceID)
			}
			sentences[s.SentenceID] = strings.TrimSpace(s.Text)
		case "task-finished":
			var sb strings.Builder
			for _, id := range order {
				sb.WriteString(sentences[id])
			}
			text := strings.TrimSpace(sb.String())
			if text == "" {
				return nil, errors.New("没听出内容（这段录音里可能没有人声）")
			}
			return &Transcript{
				Text: text, Protocol: config.VoiceProtocolDashScope,
				Model: cfg.STTModel, Millis: ms,
			}, nil
		}
	}
}

func waitTaskStarted(conn *websocket.Conn) error {
	for {
		var msg dsWSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return fmt.Errorf("听写任务没起来：%w", err)
		}
		switch msg.Header.Event {
		case "task-started":
			return nil
		case "task-failed":
			return fmt.Errorf("百炼拒绝了这次听写 [%s]：%s", msg.Header.ErrorCode, msg.Header.ErrorMessage)
		}
	}
}

// splitText 按句子边界把长文切成若干段（每段不超过 limit 个字）。
// 找不到句号就硬切，但硬切也只影响断句的停顿，不会丢字。
func splitText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return []string{text}
	}
	var out []string
	var cur []rune
	for _, r := range runes {
		cur = append(cur, r)
		endsSentence := strings.ContainsRune("。！？!?；;\n", r)
		if len(cur) >= limit || (endsSentence && len(cur) >= limit/2) {
			out = append(out, strings.TrimSpace(string(cur)))
			cur = cur[:0]
		}
	}
	if s := strings.TrimSpace(string(cur)); s != "" {
		out = append(out, s)
	}
	return out
}
