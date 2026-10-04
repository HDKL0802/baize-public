package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Embedder 文本向量化通道（记忆的语义检索用）。
//
// 与 Chat 分开成两条通道：向量模型和对话模型往往不是同一家（DeepSeek 就没有
// embedding 接口），所以允许单独配一条。没配 embedding 时记忆退回关键词检索，
// 绝不假装"有语义检索"。
type Embedder interface {
	Name() string
	Dim() int // 0 表示还不知道（还没成功调用过）
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// defaultEmbedBatch 一次请求默认塞多少条文本。
// 各家上限差别很大（OpenAI 兼容的中转常见 16，阿里百炼只允许 10），
// 所以这个值只是"起手值"：真超限了会自动拆半重试（见 embedChunk）。
const defaultEmbedBatch = 10

// OpenAIEmbedder 走 OpenAI 兼容的 /embeddings 协议
type OpenAIEmbedder struct {
	cfg    Config
	client *http.Client

	mu    sync.Mutex
	dim   int
	batch int // 实测能用的批量（撞过 400 之后会变小，之后就一直用它）
}

// NewOpenAIEmbedder 创建 embedding 通道；缺 baseUrl / model 时直接报错，不静默降级
func NewOpenAIEmbedder(cfg Config) (*OpenAIEmbedder, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("embedding 通道缺少 baseUrl")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("embedding 通道缺少 model")
	}
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &OpenAIEmbedder{cfg: cfg, client: &http.Client{Timeout: timeout}, dim: cfg.Dim}, nil
}

// Name 通道名
func (e *OpenAIEmbedder) Name() string {
	if e.cfg.Name != "" {
		return e.cfg.Name
	}
	return "openai-embed:" + e.cfg.Model
}

// Dim 向量维度（0 = 还没探到）
func (e *OpenAIEmbedder) Dim() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dim
}

func (e *OpenAIEmbedder) setDim(n int) {
	if n <= 0 {
		return
	}
	e.mu.Lock()
	if e.dim == 0 {
		e.dim = n
	}
	e.mu.Unlock()
}

type oaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type oaEmbedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed 把一批文本转成向量（内部自动分批）
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			return nil, fmt.Errorf("第 %d 条待向量化文本为空", i+1)
		}
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); {
		size := e.batchSize()
		end := start + size
		if end > len(texts) {
			end = len(texts)
		}
		part, err := e.embedChunk(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
		start = end
	}
	if len(out) != len(texts) {
		return nil, fmt.Errorf("embedding 返回条数不对：要 %d 条，实得 %d 条", len(texts), len(out))
	}
	return out, nil
}

func (e *OpenAIEmbedder) batchSize() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.batch <= 0 {
		return defaultEmbedBatch
	}
	return e.batch
}

func (e *OpenAIEmbedder) rememberBatch(n int) {
	if n <= 0 {
		return
	}
	e.mu.Lock()
	if e.batch == 0 || n < e.batch {
		e.batch = n
	}
	e.mu.Unlock()
}

// embedChunk 送一小批；如果这家服务的批量上限比我们猜的小（返回 400），
// 就**拆一半重试**，直到单条为止——换厂商不用改代码，也不用用户去猜它的上限。
func (e *OpenAIEmbedder) embedChunk(ctx context.Context, texts []string) ([][]float32, error) {
	vecs, err := e.embedOnce(ctx, texts)
	if err == nil {
		e.rememberBatch(len(texts))
		return vecs, nil
	}
	var se *httpStatusError
	if len(texts) <= 1 || !errors.As(err, &se) || se.Code != http.StatusBadRequest {
		return nil, err
	}
	mid := len(texts) / 2
	left, err := e.embedChunk(ctx, texts[:mid])
	if err != nil {
		return nil, err
	}
	right, err := e.embedChunk(ctx, texts[mid:])
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

// httpStatusError 带状态码的接口错误（用来判断"是不是批量太大"）
type httpStatusError struct {
	Code int
	Msg  string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("embedding 接口返回 HTTP %d：%s", e.Code, e.Msg)
}

func (e *OpenAIEmbedder) embedOnce(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(oaEmbedRequest{Model: e.cfg.Model, Input: texts})
	if err != nil {
		return nil, err
	}
	url := joinURL(e.cfg.BaseURL, "/embeddings")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败：%w", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 带上状态码：批量太大（400）时上层会拆半重试
		return nil, &httpStatusError{Code: resp.StatusCode, Msg: snippet(data, 400)}
	}
	var parsed oaEmbedResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("embedding 响应解析失败：%w（原始内容：%s）", err, snippet(data, 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("embedding 接口报错：%s", parsed.Error.Message)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embedding 接口返回 %d 条，期望 %d 条", len(parsed.Data), len(texts))
	}
	// 按 index 归位（有的服务不保证顺序）
	out := make([][]float32, len(texts))
	for i, d := range parsed.Data {
		idx := d.Index
		if idx < 0 || idx >= len(texts) {
			idx = i
		}
		out[idx] = d.Embedding
	}
	for i, v := range out {
		if len(v) == 0 {
			return nil, fmt.Errorf("embedding 第 %d 条是空的", i+1)
		}
	}
	e.setDim(len(out[0]))
	return out, nil
}

// EmbedPing 探活：真发一次极小请求，用来验证通道连通（会消耗一点点额度）
func (e *OpenAIEmbedder) EmbedPing(ctx context.Context) (int, error) {
	vecs, err := e.Embed(ctx, []string{"连通性自检"})
	if err != nil {
		return 0, err
	}
	return len(vecs[0]), nil
}

// CheckEndpointEmbedding 校验 embedding 通道地址是否符合本地优先策略
func (r *Router) CheckEndpointEmbedding(cfg Config) error {
	if r.allowRemote || isLocalURL(cfg.BaseURL) {
		return nil
	}
	return fmt.Errorf("embedding 通道 %s 的地址 %s 看起来不是本机；确实要用远端请显式打开 allowRemote", cfg.Name, cfg.BaseURL)
}

/* ---------- 向量工具 ---------- */

// EncodeVec 把向量打包成小端 float32 字节串（直接存进 sqlite 的 BLOB）
func EncodeVec(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		bits := math.Float32bits(f)
		buf[4*i] = byte(bits)
		buf[4*i+1] = byte(bits >> 8)
		buf[4*i+2] = byte(bits >> 16)
		buf[4*i+3] = byte(bits >> 24)
	}
	return buf
}

// DecodeVec 把 BLOB 解回向量；长度不是 4 的倍数时返回 nil
func DecodeVec(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		bits := uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24
		out[i] = math.Float32frombits(bits)
	}
	return out
}

// CosineSimilarity 余弦相似度；维度不一致或零向量返回 0
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
