package voice

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"baize/internal/config"
)

// pcmOf 造一段简单的 16 位 PCM（方波），用来验证解码/重采样/拼接
func pcmOf(n int, amp int16) []byte {
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := amp
		if i%2 == 1 {
			v = -amp
		}
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(v))
	}
	return out
}

func TestSplitTextKeepsEverything(t *testing.T) {
	short := "一句话"
	if got := splitText(short, 300); len(got) != 1 || got[0] != short {
		t.Fatalf("短文本不该被切：%#v", got)
	}

	// 造一段 1000 字的、每 10 字一个句号的长文
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("零一二三四五六七八九。")
	}
	text := sb.String()
	parts := splitText(text, 300)
	if len(parts) < 3 {
		t.Fatalf("1000 字按 300 切至少该有 3 段，实得 %d 段", len(parts))
	}
	var joined strings.Builder
	for i, p := range parts {
		if len([]rune(p)) > 300 {
			t.Fatalf("第 %d 段超过上限：%d 字", i+1, len([]rune(p)))
		}
		if strings.TrimSpace(p) == "" {
			t.Fatalf("第 %d 段是空的", i+1)
		}
		joined.WriteString(p)
	}
	if joined.String() != text {
		t.Fatal("切分后拼回来和原文不一致（丢字或改字了）")
	}
}

func TestWAVEncodeParseRoundTrip(t *testing.T) {
	pcm := pcmOf(1000, 8000)
	raw := encodeWAV(pcm, 24000, 1)
	got, err := parseWAV(raw)
	if err != nil {
		t.Fatalf("解自己编的 WAV 失败：%v", err)
	}
	if got.SampleRate != 24000 || got.Channels != 1 || got.Bits != 16 {
		t.Fatalf("格式读错了：%d Hz / %d 声道 / %d 位", got.SampleRate, got.Channels, got.Bits)
	}
	if len(got.PCM) != len(pcm) {
		t.Fatalf("PCM 长度对不上：%d vs %d", len(got.PCM), len(pcm))
	}
	for i := range pcm {
		if got.PCM[i] != pcm[i] {
			t.Fatalf("第 %d 个字节不一致", i)
		}
	}
}

func TestParseWAVRejectsNonPCM(t *testing.T) {
	// 手工把 fmt 段里的 audioFormat 从 1 改成 3（IEEE float）
	raw := encodeWAV(pcmOf(100, 1000), 16000, 1)
	raw[20] = 3
	raw[21] = 0
	if _, err := parseWAV(raw); err == nil {
		t.Fatal("非 PCM 的 WAV 应该报错，而不是硬着头皮解")
	}
	if _, err := parseWAV([]byte("RIFFxxxxWAVE")); err == nil {
		t.Fatal("残缺的 WAV 应该报错")
	}
}

func TestConcatWAV(t *testing.T) {
	a := encodeWAV(pcmOf(500, 5000), 24000, 1)
	b := encodeWAV(pcmOf(700, 5000), 24000, 1)
	merged, err := concatWAV([][]byte{a, b})
	if err != nil {
		t.Fatalf("拼接失败：%v", err)
	}
	got, err := parseWAV(merged)
	if err != nil {
		t.Fatalf("拼出来的不是合法 WAV：%v", err)
	}
	if len(got.PCM) != 1200*2 {
		t.Fatalf("拼接后应有 2400 字节 PCM，实得 %d", len(got.PCM))
	}
	if got.SampleRate != 24000 {
		t.Fatalf("采样率应保持 24000，实得 %d", got.SampleRate)
	}
	// 单段直接原样返回，不做无谓重写
	same, err := concatWAV([][]byte{a})
	if err != nil || len(same) != len(a) {
		t.Fatalf("单段拼接应原样返回：%v", err)
	}
}

func TestConcatWAVRejectsMismatch(t *testing.T) {
	a := encodeWAV(pcmOf(100, 5000), 24000, 1)
	b := encodeWAV(pcmOf(100, 5000), 16000, 1)
	if _, err := concatWAV([][]byte{a, b}); err == nil {
		t.Fatal("采样率不一致还在硬拼，会变调，必须报错")
	}
}

func TestToPCM16kMono(t *testing.T) {
	// 24k 单声道 → 16k：样本数按 2/3 缩
	src := &wavData{SampleRate: 24000, Channels: 1, Bits: 16, PCM: pcmOf(2400, 6000)}
	out := toPCM16kMono(src)
	if n := len(out) / 2; n != 1600 {
		t.Fatalf("24k→16k 应有 1600 个样本，实得 %d", n)
	}
	// 已经是 16k 就原样返回，不做无谓重采样
	same := toPCM16kMono(&wavData{SampleRate: 16000, Channels: 1, Bits: 16, PCM: pcmOf(100, 6000)})
	if len(same) != 200 {
		t.Fatalf("16k 不该被改动，实得 %d 字节", len(same))
	}
}

func TestToPCM16kMonoDownmix(t *testing.T) {
	// 双声道：左 +1000、右 -1000 → 单声道应趋近 0（均值）
	inter := make([]byte, 4*2)
	putI16 := func(off int, v int16) { binary.LittleEndian.PutUint16(inter[off:off+2], uint16(v)) }
	putI16(0, 1000)
	putI16(2, -1000)
	putI16(4, 1000)
	putI16(6, -1000)
	out := toPCM16kMono(&wavData{SampleRate: 16000, Channels: 2, Bits: 16, PCM: inter})
	if len(out) != 4 {
		t.Fatalf("双声道降单声道后应有 2 个样本（4 字节），实得 %d", len(out))
	}
	for i := 0; i < 2; i++ {
		if v := int16(binary.LittleEndian.Uint16(out[i*2 : i*2+2])); v != 0 {
			t.Fatalf("左右相反的声道取平均应为 0，实得 %d", v)
		}
	}
}

func TestHasVoiceGate(t *testing.T) {
	if hasVoice(pcmOf(1600, 0)) {
		t.Fatal("全静音不该被判定为有人声")
	}
	if !hasVoice(pcmOf(1600, 6000)) {
		t.Fatal("有明显波形就该放行")
	}
}

func TestPresetsAreOnlyKnownVoices(t *testing.T) {
	// 阿里百炼 qwen-tts 只认这四个（别的会被 InvalidParameter 拒掉），
	// 所以预设清单不许"猜"别的名字进来
	want := map[string]bool{"Cherry": true, "Serena": true, "Ethan": true, "Chelsie": true}
	list := Presets(config.VoiceProtocolDashScope)
	if len(list) != len(want) {
		t.Fatalf("百炼音色应为 %d 个，实得 %d 个", len(want), len(list))
	}
	for _, p := range list {
		if !want[p.ID] {
			t.Fatalf("预设里出现了没验证过的音色：%s", p.ID)
		}
		if p.Name == "" || p.Desc == "" {
			t.Fatalf("音色 %s 缺名字或说明", p.ID)
		}
	}
	if _, ok := PresetByID(config.VoiceProtocolDashScope, "Cherry"); !ok {
		t.Fatal("Cherry 应该在预设里")
	}
	if _, ok := PresetByID(config.VoiceProtocolDashScope, "Nofish"); ok {
		t.Fatal("Nofish 不是 qwen-tts 的合法音色，不该出现在预设里")
	}
	// 协议名容错
	if DefaultVoice("百炼") != "Cherry" || DefaultVoice("") != "nova" {
		t.Fatal("默认音色按协议取错了")
	}
}

func TestSynthesizeWithoutKeyExplainsWhy(t *testing.T) {
	_, err := Synthesize(context.Background(), Config{Protocol: config.VoiceProtocolDashScope, TTSModel: "qwen-tts"}, "你好", "")
	if err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("没配 key 时应明确说缺 key，实得：%v", err)
	}
	_, err = Transcribe(context.Background(), Config{Protocol: config.VoiceProtocolDashScope, STTModel: "paraformer-realtime-v2", APIKey: "k"}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "空") {
		t.Fatalf("空录音应明确说是空的，实得：%v", err)
	}
}

func TestTranscribeRejectsNonWAVBeforeCallingOut(t *testing.T) {
	cfg := Config{Protocol: config.VoiceProtocolDashScope, STTModel: "paraformer-realtime-v2", APIKey: "fake"}
	_, err := Transcribe(context.Background(), cfg, []byte("这不是音频"), "x.webm")
	if err == nil || !strings.Contains(err.Error(), "WAV") {
		t.Fatalf("非 WAV 输入应报「要 WAV」，而不是发出去撞接口，实得：%v", err)
	}
}

func TestSelfTestReportsUnconfigured(t *testing.T) {
	res := SelfTest(context.Background(), Config{Protocol: config.VoiceProtocolOpenAI, TTSModel: "tts-1"}, "")
	if res.Passed {
		t.Fatal("没配 key 的自检不该报通过")
	}
	if res.TTS.OK || res.TTS.Note == "" {
		t.Fatalf("自检应写清说话这半为什么没过：%#v", res.TTS)
	}
	if res.STT.Note == "" {
		t.Fatalf("听写这半也要给一句说明：%#v", res.STT)
	}
}

func TestDashscopeWSURLFromBase(t *testing.T) {
	got, err := dashscopeWSURL(Config{BaseURL: "https://dashscope.aliyuncs.com"})
	if err != nil || got != "wss://dashscope.aliyuncs.com/api-ws/v1/inference" {
		t.Fatalf("WS 地址推导不对：%s（%v）", got, err)
	}
	// 留空走官方地址
	got, err = dashscopeWSURL(Config{})
	if err != nil || got != "wss://dashscope.aliyuncs.com/api-ws/v1/inference" {
		t.Fatalf("留空时应回落到官方地址：%s（%v）", got, err)
	}
	// 国际站也要能推导
	got, err = dashscopeWSURL(Config{BaseURL: "https://dashscope-intl.aliyuncs.com"})
	if err != nil || got != "wss://dashscope-intl.aliyuncs.com/api-ws/v1/inference" {
		t.Fatalf("国际站地址推导不对：%s（%v）", got, err)
	}
}

func TestFromConfigNormalizesProtocol(t *testing.T) {
	cfg := FromConfig(config.Voice{Protocol: "百炼", APIKey: " k ", TTSModel: " qwen-tts "})
	if cfg.Protocol != config.VoiceProtocolDashScope {
		t.Fatalf("协议名该被收敛成 dashscope，实得 %s", cfg.Protocol)
	}
	if cfg.APIKey != "k" || cfg.TTSModel != "qwen-tts" {
		t.Fatalf("首尾空格没清掉：%#v", cfg)
	}
}
