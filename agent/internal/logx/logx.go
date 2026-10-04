// Package logx 提供：结构化日志（控制台+文件）与内存环形日志缓冲（供控制台页面拉取）。
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Line 一行日志（带自增序号，前端按 seq 增量拉取）
type Line struct {
	Seq   int64  `json:"seq"`
	At    int64  `json:"at"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

// Ring 固定容量的内存日志缓冲
type Ring struct {
	mu    sync.Mutex
	size  int
	seq   int64
	lines []Line
}

// NewRing 创建容量为 size 的环形缓冲
func NewRing(size int) *Ring {
	if size <= 0 {
		size = 500
	}
	return &Ring{size: size}
}

// Add 追加一行，返回其序号
func (r *Ring) Add(level, text string) Line {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	l := Line{Seq: r.seq, At: time.Now().UnixMilli(), Level: level, Text: text}
	r.lines = append(r.lines, l)
	if len(r.lines) > r.size {
		r.lines = r.lines[len(r.lines)-r.size:]
	}
	return l
}

// Since 返回序号大于 after 的日志行
func (r *Ring) Since(after int64) []Line {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Line, 0, len(r.lines))
	for _, l := range r.lines {
		if l.Seq > after {
			out = append(out, l)
		}
	}
	return out
}

// LastSeq 当前最大序号（前端首帧用它对齐）
func (r *Ring) LastSeq() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// Handler 把 slog 记录同时写到 out（控制台/文件）和环形缓冲
type Handler struct {
	mu     sync.Mutex
	out    io.Writer
	ring   *Ring
	level  slog.Level
	attrs  []slog.Attr
	groups []string
}

// NewHandler 创建 Handler；ring 可为 nil（不写内存缓冲）
func NewHandler(out io.Writer, ring *Ring, level slog.Level) *Handler {
	return &Handler{out: out, ring: ring, level: level}
}

func (h *Handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05.000"))
	b.WriteByte(' ')
	b.WriteString(levelName(r.Level))
	b.WriteByte(' ')
	b.WriteString(r.Message)

	appendAttr := func(a slog.Attr) bool {
		if a.Equal(slog.Attr{}) {
			return true
		}
		b.WriteByte(' ')
		b.WriteString(strings.Join(h.groups, "."))
		if len(h.groups) > 0 {
			b.WriteByte('.')
		}
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(formatValue(a.Value))
		return true
	}
	for _, a := range h.attrs {
		appendAttr(a)
	}
	r.Attrs(appendAttr)

	text := b.String()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ring != nil {
		h.ring.Add(levelName(r.Level), text)
	}
	if h.out != nil {
		if _, err := io.WriteString(h.out, text+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &Handler{out: h.out, ring: h.ring, level: h.level}
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	next.groups = append([]string{}, h.groups...)
	return next
}

func (h *Handler) WithGroup(name string) slog.Handler {
	next := &Handler{out: h.out, ring: h.ring, level: h.level, attrs: append([]slog.Attr{}, h.attrs...)}
	next.groups = append(append([]string{}, h.groups...), name)
	return next
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

func formatValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		s := v.String()
		if s == "" || strings.ContainsAny(s, " \t\n\"") {
			return fmt.Sprintf("%q", s)
		}
		return s
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format("2006-01-02T15:04:05")
	default:
		return fmt.Sprint(v.Any())
	}
}

// ParseLevel 解析日志级别字符串
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("不支持的日志级别：%s（可选 debug|info|warn|error）", s)
}
