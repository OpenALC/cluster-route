// 代理管线回归测试: 上游压缩、超大响应与非流式错误语义、供应商超时缓存。
package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cluster-route/internal/config"
	"cluster-route/internal/crypto"
	"cluster-route/internal/pricing"
	"cluster-route/internal/routing"
	"cluster-route/internal/stats"
	"cluster-route/internal/store"
)

// newTestHandler 组装一套真实依赖(临时目录 SQLite + 异步统计), 不监听端口。
func newTestHandler(t *testing.T) (*Handler, *config.Manager, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	mgr, err := config.Load(dir, crypto.New(dir))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	pr, err := pricing.New(st)
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	rec := stats.New(st, pr, 64)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.Run(stop)
	}()
	t.Cleanup(func() {
		close(stop)
		<-done // 等最后一次 flush 完成, 再关闭数据库
		_ = st.Close()
	})
	return New(mgr, routing.New(mgr), rec, nil), mgr, st
}

// addTestProvider 注册唯一供应商并设为默认目标。
func addTestProvider(t *testing.T, mgr *config.Manager, baseURL string) {
	t.Helper()
	err := mgr.UpsertProvider(config.Provider{
		ID:           "pv-test",
		Name:         "test-upstream",
		AnthropicURL: baseURL,
		OpenAIURL:    baseURL,
		Enabled:      true,
		TimeoutSec:   30,
	}, "sk-upstream")
	if err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if err := mgr.UpdateSettings(func(s *config.Settings) {
		s.DefaultProviderID = "pv-test"
	}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
}

// doProxy 以路由器密钥发起一次代理请求。
func doProxy(t *testing.T, h *Handler, mgr *config.Manager, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", mgr.RouterKey())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// waitRecord 等待异步统计落库(Recorder 每秒 flush 一次)。
func waitRecord(t *testing.T, st *store.Store) store.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.ListRequests(0, "", "", "", "", 10, 0)
		if err != nil {
			t.Fatalf("ListRequests: %v", err)
		}
		if len(rows) > 0 {
			return rows[0].Record
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("等待统计落库超时")
	return store.Record{}
}

// TestUpstreamRequestAsksForIdentity 上游请求必须固定 Accept-Encoding: identity,
// 否则 Transport 不会自动解压, 用量解析会拿到二进制。
func TestUpstreamRequestAsksForIdentity(t *testing.T) {
	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Accept-Encoding"))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	defer srv.Close()

	h, mgr, _ := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)

	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码=%d, body=%s", rr.Code, rr.Body.String())
	}
	if got := seen.Load(); got != "identity" {
		t.Fatalf("上游收到的 Accept-Encoding=%q, want identity", got)
	}
}

// TestGzipSSEUsageRecorded 上游无视 identity 返回 gzip 时, 客户端仍收到可解析的明文 SSE,
// Content-Encoding 不透传, usage 被正确记录。
func TestGzipSSEUsageRecorded(t *testing.T) {
	const payload = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11,\"cache_read_input_tokens\":3}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"usage\":{\"output_tokens\":7}}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, payload)
		_ = zw.Close()
	}))
	defer srv.Close()

	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)

	rr := doProxy(t, h, mgr, "/v1/messages",
		[]byte(`{"model":"claude-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码=%d, body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding 不应透传, 实际=%q", got)
	}
	if rr.Body.String() != payload {
		t.Fatalf("客户端收到的不是解压后的明文:\n got=%q\nwant=%q", rr.Body.String(), payload)
	}

	rec := waitRecord(t, st)
	if rec.Input != 11 || rec.CacheRead != 3 || rec.Output != 7 {
		t.Fatalf("用量记录错误: %+v", rec)
	}
	if !rec.OK {
		t.Fatalf("成功请求应标记 OK: %+v", rec)
	}
}

// TestGzipNonStreamDecompressed 非流式压缩响应的 body 为明文且用量可解析。
func TestGzipNonStreamDecompressed(t *testing.T) {
	const payload = `{"id":"x","usage":{"prompt_tokens":5,"completion_tokens":9}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, payload)
		_ = zw.Close()
	}))
	defer srv.Close()

	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)

	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码=%d, body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding 不应透传, 实际=%q", got)
	}
	if rr.Body.String() != payload {
		t.Fatalf("响应体应为解压后的明文, 实际=%q", rr.Body.String())
	}
	rec := waitRecord(t, st)
	if rec.Input != 5 || rec.Output != 9 {
		t.Fatalf("用量记录错误: %+v", rec)
	}
}

// TestDeflateZlibWrapped deflate 按 RFC 应为 zlib 封装, 应能解压并解析用量。
func TestDeflateZlibWrapped(t *testing.T) {
	const payload = `{"id":"x","usage":{"prompt_tokens":7,"completion_tokens":11}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "deflate")
		zw := zlib.NewWriter(w)
		_, _ = io.WriteString(zw, payload)
		_ = zw.Close()
	}))
	defer srv.Close()

	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)

	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK || rr.Body.String() != payload {
		t.Fatalf("状态码=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding 不应透传, 实际=%q", got)
	}
	rec := waitRecord(t, st)
	if rec.Input != 7 || rec.Output != 11 {
		t.Fatalf("用量记录错误: %+v", rec)
	}
}

// TestDeflateRawFallback 部分服务端实际发送裸 DEFLATE(无 zlib 头),
// zlib 解码失败后必须回退到裸流, 否则整段响应被当成二进制透传且用量归零。
func TestDeflateRawFallback(t *testing.T) {
	const payload = `{"id":"x","usage":{"prompt_tokens":13,"completion_tokens":17}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "deflate")
		zw, _ := flate.NewWriter(w, flate.DefaultCompression)
		_, _ = io.WriteString(zw, payload)
		_ = zw.Close()
	}))
	defer srv.Close()

	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)

	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK || rr.Body.String() != payload {
		t.Fatalf("裸 DEFLATE 未被解压, 状态码=%d body=%q", rr.Code, rr.Body.String())
	}
	rec := waitRecord(t, st)
	if rec.Input != 13 || rec.Output != 17 {
		t.Fatalf("用量记录错误: %+v", rec)
	}
}

// TestNonStreamTooLarge 非流式响应超过上限时返回完整 JSON 错误, 不返回半截内容。
func TestNonStreamTooLarge(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(big)))
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)
	h.respLimitBytes = 512 // 测试用上限, 避免构造 33MB 响应

	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("状态码=%d, want 502, body=%q", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("应返回 JSON 错误, Content-Type=%q", ct)
	}
	var out struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("错误体不是合法 JSON: %v, body=%q", err, rr.Body.String())
	}
	if !strings.Contains(out.Error.Message, "32MB") {
		t.Fatalf("错误信息未说明上限: %q", out.Error.Message)
	}
	if got := rr.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("错误响应不应带 Content-Encoding: %q", got)
	}

	rec := waitRecord(t, st)
	if rec.OK {
		t.Fatalf("超限响应应记录为失败: %+v", rec)
	}
	if !strings.Contains(rec.Err, "32MB") {
		t.Fatalf("失败原因未说明上限: %q", rec.Err)
	}
}

// TestUnsupportedEncodingPassThrough 无法解码的压缩编码(br)原样透传, 至少保证客户端能用。
func TestUnsupportedEncodingPassThrough(t *testing.T) {
	const raw = "\x0b\x02\x80not-real-brotli"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, raw)
	}))
	defer srv.Close()

	h, mgr, _ := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)

	rr := doProxy(t, h, mgr, "/v1/chat/completions", []byte(`{"model":"m"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码=%d, body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("不支持的编码应原样保留, 实际=%q", got)
	}
	if rr.Body.String() != raw {
		t.Fatalf("响应体应原样透传, 实际=%q", rr.Body.String())
	}
}

// TestDecompressBodyKeepsRawOnBadMagic 声明 gzip 但内容不是合法 gzip 时不得吞字节。
// gzip.NewReader 在头部校验前可能已消费若干字节, 必须完整回放。
func TestDecompressBodyKeepsRawOnBadMagic(t *testing.T) {
	var enc bytes.Buffer
	zw := gzip.NewWriter(&enc)
	_, _ = zw.Write([]byte(strings.Repeat("payload", 100)))
	_ = zw.Close()

	// 只有 gzip 魔数的残头: NewReader 会先消费掉这些字节再报错。
	truncated := enc.Bytes()[:5]

	cases := []struct {
		name string
		raw  string
	}{
		{"纯文本", "plain text, not gzip"},
		{"截断的 gzip 头", string(truncated)},
		{"空体", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &http.Response{
				Header: http.Header{"Content-Encoding": []string{"gzip"}},
				Body:   io.NopCloser(strings.NewReader(c.raw)),
			}
			got, err := io.ReadAll(decompressBody(resp))
			if err != nil {
				t.Fatalf("读取失败: %v", err)
			}
			if string(got) != c.raw {
				t.Fatalf("原始内容被破坏: got=%q want=%q", string(got), c.raw)
			}
			if resp.Header.Get("Content-Encoding") != "gzip" {
				t.Fatalf("解压失败时不应删除 Content-Encoding")
			}
		})
	}
}

// TestClientForRebuildsOnTimeoutChange 超时变更后必须重建 client, 未变更时复用同一对象。
func TestClientForRebuildsOnTimeoutChange(t *testing.T) {
	h := &Handler{tpl: &http.Transport{}}
	p := config.Provider{ID: "pv-1", TimeoutSec: 10}

	c1 := h.clientFor(p)
	c2 := h.clientFor(p)
	if c1 != c2 {
		t.Fatal("超时未变时应复用同一 client")
	}
	if got := c1.Transport.(*http.Transport).ResponseHeaderTimeout; got != 10*time.Second {
		t.Fatalf("ResponseHeaderTimeout=%v, want 10s", got)
	}

	p.TimeoutSec = 20
	c3 := h.clientFor(p)
	if c3 == c1 {
		t.Fatal("超时变更后应重建 client")
	}
	if got := c3.Transport.(*http.Transport).ResponseHeaderTimeout; got != 20*time.Second {
		t.Fatalf("重建后 ResponseHeaderTimeout=%v, want 20s", got)
	}
	if c4 := h.clientFor(p); c4 != c3 {
		t.Fatal("新超时下应稳定复用重建后的 client")
	}

	// 并发重建后必须收敛到同一个 client(避免连接池泄漏)。
	var wg sync.WaitGroup
	got := make([]*http.Client, 8)
	p.TimeoutSec = 30
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = h.clientFor(p)
		}(i)
	}
	wg.Wait()
	for i := 1; i < len(got); i++ {
		if got[i] != got[0] {
			t.Fatalf("并发重建未收敛: got[%d]!=got[0]", i)
		}
	}
}
