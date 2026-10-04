package voice

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// wavData 一份解开的 PCM WAV
type wavData struct {
	SampleRate int
	Channels   int
	Bits       int
	PCM        []byte // 交错采样，16 位小端
}

// parseWAV 解 WAV：只认 PCM（format=1）+ 16 位。
// 别的格式明确报错，不做"猜一下试试"——猜错会变成听不清的乱码，比报错更坑。
func parseWAV(b []byte) (*wavData, error) {
	if len(b) < 44 {
		return nil, errors.New("音频太短，不是有效的 WAV")
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("不是 WAV 文件（缺少 RIFF/WAVE 标识）")
	}
	out := &wavData{}
	pos := 12
	for pos+8 <= len(b) {
		id := string(b[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		body := pos + 8
		if size < 0 || body+size > len(b) {
			size = len(b) - body // 有些编码器会把长度写错，按实际剩余算
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("WAV 的 fmt 段不完整")
			}
			format := int(binary.LittleEndian.Uint16(b[body : body+2]))
			if format != 1 {
				return nil, fmt.Errorf("这个 WAV 不是未压缩 PCM（format=%d），先转成 PCM WAV 再发", format)
			}
			out.Channels = int(binary.LittleEndian.Uint16(b[body+2 : body+4]))
			out.SampleRate = int(binary.LittleEndian.Uint32(b[body+4 : body+8]))
			out.Bits = int(binary.LittleEndian.Uint16(b[body+14 : body+16]))
		case "data":
			out.PCM = append(out.PCM, b[body:body+size]...)
		}
		pos = body + size
		if size%2 == 1 {
			pos++ // chunk 按偶数字节对齐
		}
	}
	if out.SampleRate <= 0 || out.Channels <= 0 || out.Bits == 0 {
		return nil, errors.New("WAV 里没有可用的 fmt 段")
	}
	if out.Bits != 16 {
		return nil, fmt.Errorf("只支持 16 位的 WAV（这段是 %d 位），先转成 16 位再发", out.Bits)
	}
	if len(out.PCM) == 0 {
		return nil, errors.New("WAV 里没有音频数据")
	}
	return out, nil
}

// encodeWAV 按 PCM16 拼一份标准 44 字节头的 WAV
func encodeWAV(pcm []byte, sampleRate, channels int) []byte {
	out := make([]byte, 44+len(pcm))
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(36+len(pcm)))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:20], 16)
	binary.LittleEndian.PutUint16(out[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(out[24:28], uint32(sampleRate))
	byteRate := sampleRate * channels * 2
	binary.LittleEndian.PutUint32(out[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(out[32:34], uint16(channels*2)) // block align
	binary.LittleEndian.PutUint16(out[34:36], 16)
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:44], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}

// concatWAV 把多段 WAV 接成一段（长文分段合成后用）。
// 采样率/声道/位深必须一致，不一致就直接报错——硬拼会变调。
func concatWAV(parts [][]byte) ([]byte, error) {
	if len(parts) == 0 {
		return nil, errors.New("没有可拼接的音频")
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	first, err := parseWAV(parts[0])
	if err != nil {
		return nil, err
	}
	pcm := first.PCM
	for i, p := range parts[1:] {
		w, err := parseWAV(p)
		if err != nil {
			return nil, fmt.Errorf("第 %d 段音频有问题：%w", i+2, err)
		}
		if w.SampleRate != first.SampleRate || w.Channels != first.Channels || w.Bits != first.Bits {
			return nil, fmt.Errorf("第 %d 段音频的采样率/声道和第一段不一致，没法拼", i+2)
		}
		pcm = append(pcm, w.PCM...)
	}
	return encodeWAV(pcm, first.SampleRate, first.Channels), nil
}

// toPCM16kMono 把任意 PCM WAV 变成 16kHz 单声道 16 位 PCM（百炼实时听写只认这个）。
// 重采样用线性插值：够用，且不会因为多引入一个重采样库把交叉编译搞复杂。
func toPCM16kMono(w *wavData) []byte {
	mono := w.PCM
	if w.Channels > 1 {
		frames := len(w.PCM) / 2 / w.Channels
		mono = make([]byte, frames*2)
		for f := 0; f < frames; f++ {
			var sum int32
			for c := 0; c < w.Channels; c++ {
				off := (f*w.Channels + c) * 2
				sum += int32(int16(binary.LittleEndian.Uint16(w.PCM[off : off+2])))
			}
			binary.LittleEndian.PutUint16(mono[f*2:f*2+2], uint16(int16(sum/int32(w.Channels))))
		}
	}
	const target = 16000
	srcRate := w.SampleRate
	srcSamples := len(mono) / 2
	if srcRate == target || srcSamples == 0 {
		return mono
	}
	dstSamples := int(math.Round(float64(srcSamples) * target / float64(srcRate)))
	if dstSamples <= 0 {
		return nil
	}
	out := make([]byte, dstSamples*2)
	ratio := float64(srcRate) / float64(target)
	for i := 0; i < dstSamples; i++ {
		pos := float64(i) * ratio
		i0 := int(pos)
		if i0 >= srcSamples-1 {
			i0 = srcSamples - 1
		}
		i1 := i0 + 1
		if i1 >= srcSamples {
			i1 = srcSamples - 1
		}
		frac := pos - float64(i0)
		s0 := float64(int16(binary.LittleEndian.Uint16(mono[i0*2 : i0*2+2])))
		s1 := float64(int16(binary.LittleEndian.Uint16(mono[i1*2 : i1*2+2])))
		v := int16(math.Round(s0 + (s1-s0)*frac))
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(v))
	}
	return out
}

// pcmMillis 这段 PCM 有多长（毫秒），用来判断"是不是根本没录到声音"
func pcmMillis(pcm []byte) int {
	return len(pcm) / 2 * 1000 / 16000
}

// hasVoice 极简能量门：整段几乎全是静音就别往接口送了（省一次调用，也免得接口回一句幻觉）
func hasVoice(pcm []byte) bool {
	if len(pcm) < 2 {
		return false
	}
	var peak int
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(pcm[i : i+2])))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
			if peak > 400 {
				return true
			}
		}
	}
	return peak > 400
}
