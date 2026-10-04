package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"baize/internal/config"
	"baize/internal/voice"
)

// registerVoice 注册语音相关接口（说话 / 听写 / 自检 / 配置）。
//
// 手机端怎么用：
//   - 朗读一段文字：POST /api/agent/voice/tts {"text":"..."} → 直接回 WAV 字节（audio/wav）。
//     加 ?b64=1 则回 JSON（{"audio":"<base64>","mime":"audio/wav"}），给不方便收二进制的入口用。
//   - 按住说话：把录音转成 16k 单声道 WAV 后 POST /api/agent/voice/stt（体就是原始 WAV 字节，
//     或 multipart 的 file 字段）→ {"text":"..."}。
//
// 密钥一律不回显，只回 hasApiKey。
func (s *Server) registerVoice(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	a := s.agent

	// 状态 + 配置 + 可选音色 + 协议说明，一次给全（界面不用连打三个接口）
	mux.HandleFunc("GET /api/agent/voice", s.api(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.Config()
		v := cfg.Voice
		protocol := config.NormalizeVoiceProtocol(v.Protocol)
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":   v.Enabled,
			"protocol":  protocol,
			"protocols": voiceProtocolList(),
			"baseUrl":   v.BaseURL,
			"ttsModel":  v.TTSModel,
			"sttModel":  v.STTModel,
			"voice":     v.Voice,
			"voices":    voice.Presets(protocol),
			"autoSpeak": v.AutoSpeak,
			"speed":     v.Speed,
			"hasApiKey": v.APIKey != "",
			"ready":     v.Enabled && v.APIKey != "" && v.TTSModel != "",
			"note":      voiceNote(v),
		})
	}))

	// 保存语音设置。voiceApiKey 传空 = 只改其它字段、保留原来的 key（与向量通道同规矩）
	mux.HandleFunc("POST /api/agent/voice/config", s.api(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.Config()
		var patch struct {
			Enabled    *bool    `json:"enabled"`
			Protocol   *string  `json:"protocol"`
			BaseURL    *string  `json:"baseUrl"`
			APIKey     *string  `json:"apiKey"`
			TTSModel   *string  `json:"ttsModel"`
			STTModel   *string  `json:"sttModel"`
			Voice      *string  `json:"voice"`
			AutoSpeak  *bool    `json:"autoSpeak"`
			Speed      *float64 `json:"speed"`
			TimeoutSec *int     `json:"timeoutSec"`
		}
		if err := decodeBody(r, &patch); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if patch.Enabled != nil {
			cfg.Voice.Enabled = *patch.Enabled
		}
		if patch.Protocol != nil {
			cfg.Voice.Protocol = config.NormalizeVoiceProtocol(*patch.Protocol)
		}
		if patch.BaseURL != nil {
			cfg.Voice.BaseURL = strings.TrimSpace(*patch.BaseURL)
		}
		if patch.APIKey != nil && strings.TrimSpace(*patch.APIKey) != "" {
			cfg.Voice.APIKey = strings.TrimSpace(*patch.APIKey)
		}
		if patch.TTSModel != nil {
			cfg.Voice.TTSModel = strings.TrimSpace(*patch.TTSModel)
		}
		if patch.STTModel != nil {
			cfg.Voice.STTModel = strings.TrimSpace(*patch.STTModel)
		}
		if patch.Voice != nil {
			cfg.Voice.Voice = strings.TrimSpace(*patch.Voice)
		}
		if patch.AutoSpeak != nil {
			cfg.Voice.AutoSpeak = *patch.AutoSpeak
		}
		if patch.Speed != nil && *patch.Speed > 0 && *patch.Speed <= 4 {
			cfg.Voice.Speed = *patch.Speed
		}
		if patch.TimeoutSec != nil && *patch.TimeoutSec > 0 {
			cfg.Voice.TimeoutSec = *patch.TimeoutSec
		}
		if cfg.Voice.Enabled && cfg.Voice.APIKey == "" {
			writeErr(w, http.StatusBadRequest, "要打开语音，得先填 API Key")
			return
		}
		if err := a.SaveConfig(cfg); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	// 合成：文字 → 语音。
	// 支持 GET（text/voice 走查询串）——手机端的 WebView 就是内核的源，
	// 直接 new Audio('/api/agent/voice/tts?text=…') 就能放，不用把几 MB 的音频
	// 塞进 JS 桥、也不用在手机上留文件。
	mux.HandleFunc("GET /api/agent/voice/tts", s.handleTTS)
	mux.HandleFunc("POST /api/agent/voice/tts", s.handleTTS)

	// 听写：录音 → 文字。体可以是原始 WAV 字节，也可以是 multipart 的 file 字段。
	mux.HandleFunc("POST /api/agent/voice/stt", s.api(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.Config()
		if !cfg.Voice.Enabled {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "语音通道还没打开"})
			return
		}
		audio, filename, err := readAudioBody(r)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		tr, err := voice.Transcribe(ctx, voice.FromConfig(cfg.Voice), audio, filename)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "text": tr.Text, "model": tr.Model, "millis": tr.Millis,
		})
	}))

	// 自检：真合成一句、再听写回来（不是"看配置像不像")
	mux.HandleFunc("POST /api/agent/voice/test", s.api(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.Config()
		var req struct {
			Voice string `json:"voice"`
		}
		_ = decodeBody(r, &req)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		writeJSON(w, http.StatusOK, voice.SelfTest(ctx, voice.FromConfig(cfg.Voice), req.Voice))
	}))
}

// handleTTS 合成一段语音：默认回 WAV 字节，?b64=1 回 JSON（给不方便收二进制的入口）。
func (s *Server) handleTTS(w http.ResponseWriter, r *http.Request) {
	a := s.agent
	cfg := a.Config()
	if !cfg.Voice.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": "语音通道还没打开（控制台 → 语音，或手机端「API 服务」里打开）"})
		return
	}
	text, voiceID := "", ""
	if r.Method == http.MethodGet {
		text = r.URL.Query().Get("text")
		voiceID = r.URL.Query().Get("voice")
	} else {
		var req struct {
			Text  string `json:"text"`
			Voice string `json:"voice"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		text, voiceID = req.Text, req.Voice
	}
	if strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "text 不能为空"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	speech, err := voice.Synthesize(ctx, voice.FromConfig(cfg.Voice), text, voiceID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if r.URL.Query().Get("b64") != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "audio": base64.StdEncoding.EncodeToString(speech.Audio),
			"mime": speech.MIME, "voice": speech.Voice, "model": speech.Model,
		})
		return
	}
	w.Header().Set("Content-Type", speech.MIME)
	w.Header().Set("X-Baize-Voice", speech.Voice)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(speech.Audio)
}

// readAudioBody 从请求里取出音频字节，三种入口都收：
//   - multipart 的 file 字段（表单上传）
//   - JSON {"audio":"<base64>","filename":"x.wav"}（走 JS 桥/文本通道的入口用）
//   - 整块请求体就是音频字节（手机端 WebView 直接 fetch 的就是这种）
func readAudioBody(r *http.Request) ([]byte, string, error) {
	ctype := r.Header.Get("Content-Type")
	if strings.HasPrefix(ctype, "application/json") {
		var req struct {
			Audio    string `json:"audio"`
			Filename string `json:"filename"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, "", err
		}
		if strings.TrimSpace(req.Audio) == "" {
			return nil, "", fmt.Errorf("JSON 里没有 audio 字段（要 base64 的 WAV）")
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.Audio))
		if err != nil {
			return nil, "", fmt.Errorf("audio 不是合法的 base64：%w", err)
		}
		name := strings.TrimSpace(req.Filename)
		if name == "" {
			name = "speech.wav"
		}
		return data, name, nil
	}
	if strings.HasPrefix(ctype, "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			return nil, "", err
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return nil, "", err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 64<<20))
		if err != nil {
			return nil, "", err
		}
		name := "speech.wav"
		if hdr != nil && strings.TrimSpace(hdr.Filename) != "" {
			name = hdr.Filename
		}
		return data, name, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return nil, "", err
	}
	return data, "speech.wav", nil
}

func voiceProtocolList() []map[string]string {
	return []map[string]string{
		{"id": config.VoiceProtocolDashScope, "name": "阿里云百炼（dashscope）",
			"desc": "qwen-tts 合成 + paraformer 实时听写；填百炼的 API Key 即可，地址留空走官方"},
		{"id": config.VoiceProtocolOpenAI, "name": "OpenAI 兼容（openai）",
			"desc": "/audio/speech + /audio/transcriptions；适合 OpenAI、硅基流动等提供了音频接口的站"},
	}
}

// voiceNote 给界面一句"现在能不能用、缺什么"的实话
func voiceNote(v config.Voice) string {
	if !v.Enabled {
		return "语音通道没打开（打开后才会有声音）"
	}
	if v.APIKey == "" {
		return "缺 API Key"
	}
	if v.TTSModel == "" || v.STTModel == "" {
		return "缺合成模型或听写模型"
	}
	return ""
}
