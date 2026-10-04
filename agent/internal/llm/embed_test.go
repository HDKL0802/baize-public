package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 各家的批量上限差别很大（百炼只允许 10 条）。撞到 400 必须自动拆半重试，
// 而不是把「batch size is invalid」直接甩给用户。
func TestEmbedSplitsWhenBatchTooLarge(t *testing.T) {
	const limit = 3
	var calls, maxSeen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		calls++
		if len(req.Input) > maxSeen {
			maxSeen = len(req.Input)
		}
		if len(req.Input) > limit {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Value error, batch size is invalid, it should not be larger than 3."}}`))
			return
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []item `json:"data"`
		}{}
		for i := range req.Input {
			out.Data = append(out.Data, item{Index: i, Embedding: []float32{float32(i), 1, 0}})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(Config{BaseURL: srv.URL + "/v1", Model: "m"})
	if err != nil {
		t.Fatalf("建通道失败：%v", err)
	}
	texts := []string{"a", "b", "c", "d", "e"}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("应当自动拆批成功，实际：%v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("条数不对：要 %d，实得 %d", len(texts), len(vecs))
	}
	if maxSeen > defaultEmbedBatch {
		t.Fatalf("单次请求条数不该超过默认批量：%d", maxSeen)
	}
	if calls < 3 {
		t.Fatalf("应当发生了拆分重试：calls=%d", calls)
	}
	// 拆出来能用的批量要记住，后续不再反复撞 400
	if got := e.batchSize(); got > limit {
		t.Fatalf("应当记住实测出来的批量上限：batch=%d，实际上限 %d", got, limit)
	}
}

// 认证失败（401）这类错误不该被当成"批量太大"去拆重试——那样只会把错误刷更多次
func TestEmbedDoesNotSplitOnAuthError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	e, _ := NewOpenAIEmbedder(Config{BaseURL: srv.URL + "/v1", Model: "m", APIKey: "bad"})
	_, err := e.Embed(context.Background(), []string{"a", "b", "c", "d"})
	if err == nil {
		t.Fatal("401 应当报错")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误里应当带状态码：%v", err)
	}
	if calls != 1 {
		t.Fatalf("认证错误不该重试：calls=%d", calls)
	}
}

// 单条都失败时（比如模型名写错）要把真实错误抛出来，不能无限拆
func TestEmbedSurfacesErrorOnSingleText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	defer srv.Close()

	e, _ := NewOpenAIEmbedder(Config{BaseURL: srv.URL + "/v1", Model: "nope"})
	_, err := e.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("应当把接口的真实报错带出来：%v", err)
	}
}

// 向量编解码往返必须逐位一致（存的是 BLOB，写错就是静默算错）
func TestVectorRoundTrip(t *testing.T) {
	v := []float32{0, 1, -1, 0.5, -0.25, 3.14159}
	got := DecodeVec(EncodeVec(v))
	if len(got) != len(v) {
		t.Fatalf("长度不对：%d vs %d", len(got), len(v))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("第 %d 位不一致：%v vs %v", i, got[i], v[i])
		}
	}
	if DecodeVec([]byte{1, 2, 3}) != nil {
		t.Fatal("长度不是 4 的倍数应当返回 nil，而不是乱解")
	}
	if s := CosineSimilarity(v, v); s < 0.999 {
		t.Fatalf("自相似度应当接近 1：%v", s)
	}
	if s := CosineSimilarity(v, []float32{1, 1}); s != 0 {
		t.Fatalf("维度不一致应当返回 0（不能瞎算）：%v", s)
	}
}
