package voice

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"baize/internal/config"
)

// 真机联调：默认跳过（单元测试不许连外网），要跑就带上 key：
//
//	$env:BAIZE_DASHSCOPE_KEY="sk-..."   # PowerShell
//	go test ./internal/voice/ -run Integration -v
//
// 这条用例是"接口真的能用"的证据：它会让百炼念一句、再把念出来的音频听回来，
// 还会试一次长文分段（长回复靠这条路径，分段拼接写错的话只会播到一半）。
func TestIntegrationDashScope(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("BAIZE_DASHSCOPE_KEY"))
	if key == "" {
		t.Skip("没给 BAIZE_DASHSCOPE_KEY，跳过真机联调")
	}
	cfg := Config{
		Protocol: config.VoiceProtocolDashScope, APIKey: key,
		TTSModel: "qwen-tts", STTModel: "paraformer-realtime-v2",
		Voice: "Cherry", TimeoutSec: 180,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// 1) 说话
	sp, err := Synthesize(ctx, cfg, selfTestText, "")
	if err != nil {
		t.Fatalf("合成失败：%v", err)
	}
	wav, err := parseWAV(sp.Audio)
	if err != nil {
		t.Fatalf("合成回来的不是 PCM WAV：%v", err)
	}
	ms := pcmMillis(toPCM16kMono(wav))
	t.Logf("合成 OK：%d 字节 / %d Hz / 约 %d 毫秒", len(sp.Audio), wav.SampleRate, ms)
	if ms < 800 {
		t.Fatalf("念出来只有 %d 毫秒，明显不对", ms)
	}

	// 2) 听话：把上面那段念出来的音频原样听回去
	tr, err := Transcribe(ctx, cfg, sp.Audio, "selftest.wav")
	if err != nil {
		t.Fatalf("听写失败：%v", err)
	}
	t.Logf("听写 OK：%q", tr.Text)
	if !strings.Contains(tr.Text, "自检") {
		t.Logf("注意：听回来的内容没有「自检」两字，可能是识别偏差（原文：%s）", selfTestText)
	}

	// 3) 长文分段：60 句 × 8 字 = 480 字，必然超过单次上限，走分段拼接
	long := strings.Repeat("今天有三件事要做。", 60)
	sp2, err := Synthesize(ctx, cfg, long, "")
	if err != nil {
		t.Fatalf("长文合成失败：%v", err)
	}
	wav2, err := parseWAV(sp2.Audio)
	if err != nil {
		t.Fatalf("长文合成回来的是坏音频：%v", err)
	}
	longMs := pcmMillis(toPCM16kMono(wav2))
	t.Logf("长文合成 OK：%d 字 → %d 字节 / 约 %d 毫秒", len([]rune(long)), len(sp2.Audio), longMs)
	if longMs <= ms {
		t.Fatalf("长文（%d 毫秒）居然不比短句（%d 毫秒）长，分段拼接有问题", longMs, ms)
	}

	// 4) 自检封装也要给出"全通过"
	res := SelfTest(ctx, cfg, "Serena")
	if !res.Passed {
		t.Fatalf("自检没过：说话=%+v 听话=%+v", res.TTS, res.STT)
	}
	t.Logf("自检 OK：音色 %s，听回 %q", res.Voice, res.STT.Text)
}
