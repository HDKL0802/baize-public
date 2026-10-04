// Package voice 是后端的语音通道：把文字说成话（TTS），把录音听成字（STT）。
//
// 为什么单独一条通道：语音模型和对话模型往往不是一家。DeepSeek 没有语音接口，
// 阿里百炼有 qwen-tts（说话）和 paraformer-realtime（听写）。所以这里按
// protocol 路由，而不是复用对话通道：
//
//	openai    —— 标准 OpenAI 音频协议：POST /audio/speech、POST /audio/transcriptions
//	dashscope —— 阿里云百炼原生：qwen-tts 合成（返回音频地址，后端取回字节）
//	             + paraformer-realtime-v2 实时听写（后端走 WebSocket 整段转写）
//
// 规矩：调用方传进来的东西缺什么就报什么（缺 key / 缺模型 / 音频格式不对），
// 一律不带兜底伪装 —— 说不出来就说"说不出来，原因是 X"，绝不返回静音音频。
package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"baize/internal/config"
)

// Config 语音通道的运行参数（由 config.Voice 映射而来，密钥不回显给界面）
type Config struct {
	Name       string
	Protocol   string
	BaseURL    string
	APIKey     string
	TTSModel   string
	STTModel   string
	Voice      string
	Speed      float64
	TimeoutSec int
}

// FromConfig 把后端配置里的语音通道映射成这里要用的形状
func FromConfig(v config.Voice) Config {
	return Config{
		Name:       "voice",
		Protocol:   config.NormalizeVoiceProtocol(v.Protocol),
		BaseURL:    strings.TrimSpace(v.BaseURL),
		APIKey:     strings.TrimSpace(v.APIKey),
		TTSModel:   strings.TrimSpace(v.TTSModel),
		STTModel:   strings.TrimSpace(v.STTModel),
		Voice:      strings.TrimSpace(v.Voice),
		Speed:      v.Speed,
		TimeoutSec: v.TimeoutSec,
	}
}

// Speech 一次合成的产物
type Speech struct {
	Audio    []byte // 音频字节（wav / mp3）
	MIME     string // audio/wav | audio/mpeg
	Protocol string // 实际走的协议
	Model    string
	Voice    string
	Format   string // wav | mp3
}

// Transcript 一次听写的产物
type Transcript struct {
	Text     string
	Protocol string
	Model    string
	Millis   int // 音频时长（毫秒），用来判断"是不是根本没录到声音"
}

// Preset 一个可选音色
type Preset struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	Gender string `json:"gender"`
}

// presets 各家协议下"确实存在"的音色。
// 阿里百炼 qwen-tts 只认这四个（试过别的会被接口按 InvalidParameter 拒掉），
// 所以这里只列这四个，不拿猜出来的名字充数。
var presets = map[string][]Preset{
	config.VoiceProtocolDashScope: {
		{ID: "Cherry", Name: "芊悦", Desc: "女声 · 通用播报，清晰自然", Gender: "女"},
		{ID: "Serena", Name: "苏瑶", Desc: "女声 · 温柔偏慢，适合长句", Gender: "女"},
		{ID: "Ethan", Name: "晨煦", Desc: "男声 · 沉稳，适合念结论", Gender: "男"},
		{ID: "Chelsie", Name: "千雪", Desc: "女声 · 偏甜，适合助手闲聊", Gender: "女"},
	},
	config.VoiceProtocolOpenAI: {
		{ID: "nova", Name: "Nova", Desc: "女声 · 明快", Gender: "女"},
		{ID: "shimmer", Name: "Shimmer", Desc: "女声 · 轻柔", Gender: "女"},
		{ID: "alloy", Name: "Alloy", Desc: "中性 · 平稳", Gender: "中性"},
		{ID: "echo", Name: "Echo", Desc: "男声 · 干净", Gender: "男"},
		{ID: "onyx", Name: "Onyx", Desc: "男声 · 低沉", Gender: "男"},
		{ID: "fable", Name: "Fable", Desc: "中性 · 叙事感", Gender: "中性"},
	},
}

// Presets 返回某协议下的可选音色（协议非法时按 openai 给）
func Presets(protocol string) []Preset {
	list, ok := presets[config.NormalizeVoiceProtocol(protocol)]
	if !ok {
		return presets[config.VoiceProtocolOpenAI]
	}
	out := make([]Preset, len(list))
	copy(out, list)
	return out
}

// PresetByID 找某个音色的描述；找不到返回零值
func PresetByID(protocol, id string) (Preset, bool) {
	for _, p := range Presets(protocol) {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// DefaultVoice 某协议的默认音色（吉祥物音色）
func DefaultVoice(protocol string) string {
	if config.NormalizeVoiceProtocol(protocol) == config.VoiceProtocolDashScope {
		return "Cherry"
	}
	return "nova"
}

func (c Config) timeout() time.Duration {
	if c.TimeoutSec <= 0 {
		return 120 * time.Second
	}
	return time.Duration(c.TimeoutSec) * time.Second
}

func (c Config) voiceOrDefault() string {
	if c.Voice != "" {
		return c.Voice
	}
	return DefaultVoice(c.Protocol)
}

// validate 调接口之前先把明显缺的东西挑出来，报清楚缺什么
func (c Config) validateTTS() error {
	if c.APIKey == "" {
		return errors.New("语音通道没配 API Key")
	}
	if c.TTSModel == "" {
		return errors.New("语音通道没配「合成模型」（dashscope 用 qwen-tts，openai 用 tts-1）")
	}
	if c.Protocol == config.VoiceProtocolOpenAI && c.BaseURL == "" {
		return errors.New("openai 协议的语音通道没配 baseUrl")
	}
	return nil
}

func (c Config) validateSTT() error {
	if c.APIKey == "" {
		return errors.New("语音通道没配 API Key")
	}
	if c.STTModel == "" {
		return errors.New("语音通道没配「听写模型」（dashscope 用 paraformer-realtime-v2，openai 用 whisper-1）")
	}
	if c.Protocol == config.VoiceProtocolOpenAI && c.BaseURL == "" {
		return errors.New("openai 协议的语音通道没配 baseUrl")
	}
	return nil
}

// Synthesize 把文字说成话。voiceOverride 非空时用它（手机端可以临时换音色）。
func Synthesize(ctx context.Context, cfg Config, text, voiceOverride string) (*Speech, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("要念的文字是空的")
	}
	if err := cfg.validateTTS(); err != nil {
		return nil, err
	}
	voiceID := strings.TrimSpace(voiceOverride)
	if voiceID == "" {
		voiceID = cfg.voiceOrDefault()
	}
	// 长回复要分段合成再拼起来：两家的单次输入都有长度上限，整段丢过去会被拒。
	// 两家都要求输出 WAV，这样拼接只有一条代码路径（WAV 拼接头是确定的）。
	parts := splitText(text, chunkRunes)
	audios := make([][]byte, 0, len(parts))
	for i, part := range parts {
		buf, err := synthOnce(ctx, cfg, part, voiceID)
		if err != nil {
			if len(parts) > 1 {
				return nil, fmt.Errorf("第 %d/%d 段没念出来：%w", i+1, len(parts), err)
			}
			return nil, err
		}
		audios = append(audios, buf)
	}
	audio, err := concatWAV(audios)
	if err != nil {
		return nil, err
	}
	return &Speech{
		Audio: audio, MIME: "audio/wav", Protocol: cfg.Protocol,
		Model: cfg.TTSModel, Voice: voiceID, Format: "wav",
	}, nil
}

// synthOnce 合成一段（各协议自己实现），产物必须是 16 位 PCM WAV
func synthOnce(ctx context.Context, cfg Config, text, voiceID string) ([]byte, error) {
	switch cfg.Protocol {
	case config.VoiceProtocolDashScope:
		return dashscopeTTSOnce(ctx, cfg, text, voiceID)
	default:
		return openaiTTSOnce(ctx, cfg, text, voiceID)
	}
}

// Transcribe 把一段录音听成字。audio 必须是能解码的音频（手机端统一转成 16k 单声道 WAV）。
func Transcribe(ctx context.Context, cfg Config, audio []byte, filename string) (*Transcript, error) {
	if len(audio) == 0 {
		return nil, errors.New("收到的录音是空的（手机端没录到声音？）")
	}
	if err := cfg.validateSTT(); err != nil {
		return nil, err
	}
	if filename == "" {
		filename = "speech.wav"
	}
	switch cfg.Protocol {
	case config.VoiceProtocolDashScope:
		return dashscopeSTT(ctx, cfg, audio)
	default:
		return openaiSTT(ctx, cfg, audio, filename)
	}
}

/* ================= 自检 ================= */

// StepResult 自检里的一步
type StepResult struct {
	OK     bool   `json:"ok"`
	Note   string `json:"note"`
	Millis int    `json:"millis,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
	Text   string `json:"text,omitempty"`
}

// SelfTestResult 一次语音自检的结果。
// 注意判成功的字段叫 passed 而不是 ok：这个接口"自检没过"也是一次成功的接口调用，
// 要是占用了 ok，前面那层统一的 {ok:false,error} 约定就会被误读成"请求失败"。
type SelfTestResult struct {
	Passed   bool       `json:"passed"`
	Protocol string     `json:"protocol"`
	Voice    string     `json:"voice"`
	TTS      StepResult `json:"tts"`
	STT      StepResult `json:"stt"`
}

// selfTestText 自检念的这句话。选它是因为够短、又好认——万一听写听回来不一样，
// 一眼就能看出是接口的问题还是"本来就没听清"。
const selfTestText = "白泽语音自检，一二三四五六七"

// SelfTest 真发一次请求做闭环验证：先合成一句，再把这段音频送回去听写。
// 不玩"配置齐全就算通过"那套——用户要的是"真能出声、真能听清"。
func SelfTest(ctx context.Context, cfg Config, voiceOverride string) *SelfTestResult {
	res := &SelfTestResult{Protocol: cfg.Protocol, Voice: cfg.voiceOrDefault()}
	if v := strings.TrimSpace(voiceOverride); v != "" {
		res.Voice = v
	}
	speech, err := Synthesize(ctx, cfg, selfTestText, res.Voice)
	if err != nil {
		res.TTS = StepResult{OK: false, Note: err.Error()}
		res.STT = StepResult{OK: false, Note: "合成没过，听写没测（先修好说话这一半）"}
		return res
	}
	wav, werr := parseWAV(speech.Audio)
	res.TTS = StepResult{OK: true, Bytes: len(speech.Audio), Note: "能出声", Text: selfTestText}
	if werr == nil {
		res.TTS.Millis = pcmMillis(toPCM16kMono(wav))
	}

	tr, err := Transcribe(ctx, cfg, speech.Audio, "selftest.wav")
	if err != nil {
		res.STT = StepResult{OK: false, Note: err.Error()}
		return res
	}
	res.STT = StepResult{OK: true, Text: tr.Text, Millis: tr.Millis, Note: "能听懂"}
	res.Passed = true
	return res
}

// hintForMissingAudioEndpoint 给"这家没有音频接口"这种情况一句能直接照做的提示。
// 白泽实际踩过的坑：把百炼的 compatible-mode 地址配成 openai 协议，音频接口 404。
func hintForMissingAudioEndpoint(protocol, base string, status int, body string) error {
	if status != 404 && status != 405 {
		return fmt.Errorf("语音接口返回 HTTP %d：%s", status, snippet([]byte(body), 300))
	}
	if strings.Contains(strings.ToLower(base), "compatible-mode") {
		return fmt.Errorf("这个地址（%s）没有音频接口（HTTP %d）。百炼的 compatible-mode 不支持 /audio/*，"+
			"请把语音通道的协议改成 dashscope（那样会走百炼原生的 qwen-tts / paraformer）", base, status)
	}
	return fmt.Errorf("这个地址没有音频接口（HTTP %d）；检查 baseUrl 是否指到了能提供语音服务的站", status)
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
