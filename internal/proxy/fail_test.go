package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cluster-route/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errAfter struct {
	rest []byte
	err  error
}

func (e *errAfter) Read(p []byte) (int, error) {
	if len(e.rest) == 0 {
		return 0, e.err
	}
	n := copy(p, e.rest)
	e.rest = e.rest[n:]
	return n, nil
}

func TestSSEErrorEventMarksFailure(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "anthropic",
			payload: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"boom\"}}\n\n",
			want:    "boom",
		},
		{
			name:    "openai",
			payload: "data: {\"error\":{\"message\":\"nope\",\"type\":\"invalid_request_error\"}}\n\n",
			want:    "nope",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewSSEParser("openai")
			p.FeedChunk([]byte(tc.payload))
			p.Flush()
			if p.ErrMsg() != tc.want {
				t.Fatalf("ErrMsg=%q, want %q", p.ErrMsg(), tc.want)
			}
		})
	}
}

func TestSSEErrorEventRecordedAsFailure(t *testing.T) {
	const payload = "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"boom\"}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()
	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)
	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	rec := waitRecord(t, st)
	if rec.OK || !strings.Contains(rec.Err, "boom") {
		t.Fatalf("错误流被记成成功: ok=%v err=%q", rec.OK, rec.Err)
	}
}

func TestStreamReadErrorIsNotSuccess(t *testing.T) {
	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, "http://127.0.0.1:1")
	h.clients.Store("pv-test", &clientEntry{
		timeoutSec: 30,
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(&errAfter{rest: []byte("data: {\"usage\":{\"prompt_tokens\":1}}\n"), err: io.ErrUnexpectedEOF}),
				Request:    r,
			}, nil
		})},
	})
	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	rec := waitRecord(t, st)
	if rec.OK || !strings.Contains(rec.Err, "读取上游流失败") {
		t.Fatalf("上游掉线被记成成功: ok=%v err=%q", rec.OK, rec.Err)
	}
}

func TestUnparsedModelPassesThroughDefault(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"file_1"}`)
	}))
	defer srv.Close()
	h, mgr, _ := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)
	req := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	req.Header.Set("x-api-key", mgr.RouterKey())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("解析不出模型时应透传, 状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("上游命中 %d 次, want 1", hits.Load())
	}
}

func TestMissingEndpointFallsThroughToDefault(t *testing.T) {
	var openaiHits, anthHits atomic.Int32
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiHits.Add(1)
		http.Error(w, "should not be selected", http.StatusBadGateway)
	}))
	defer openai.Close()
	anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer anth.Close()

	h, mgr, _ := newTestHandler(t)
	if err := mgr.UpsertProvider(config.Provider{
		ID: "pv-openai", Name: "openai-only", OpenAIURL: openai.URL, Enabled: true, TimeoutSec: 30,
	}, "sk-o"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpsertProvider(config.Provider{
		ID: "pv-anth", Name: "anth-default", AnthropicURL: anth.URL, Enabled: true, TimeoutSec: 30,
	}, "sk-a"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetRoutes([]config.Route{{
		Alias: "claude-x", ProviderID: "pv-openai", UpstreamModel: "up-x", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateSettings(func(s *config.Settings) {
		s.DefaultProviderID = "pv-anth"
	}); err != nil {
		t.Fatal(err)
	}
	rr := doProxy(t, h, mgr, "/v1/messages",
		[]byte(`{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("无对应端点的路由目标应让给默认供应商, 状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	if openaiHits.Load() != 0 || anthHits.Load() != 1 {
		t.Fatalf("openai hits=%d anth hits=%d", openaiHits.Load(), anthHits.Load())
	}
}

func TestSameName404FailsOver(t *testing.T) {
	var firstHits, secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		http.Error(w, "missing model", http.StatusNotFound)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer second.Close()

	h, mgr, _ := newTestHandler(t)
	if err := mgr.UpsertProvider(config.Provider{
		ID: "pv-a", Name: "a-primary", OpenAIURL: first.URL, Enabled: true, TimeoutSec: 30,
	}, "sk-a"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpsertProvider(config.Provider{
		ID: "pv-b", Name: "b-backup", OpenAIURL: second.URL, Enabled: true, TimeoutSec: 30,
	}, "sk-b"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateSettings(func(s *config.Settings) {
		s.DefaultProviderID = "pv-a"
		s.Failover.Enabled = true
		s.Failover.Mode = "same_name"
		s.Failover.MaxAttempts = 3
	}); err != nil {
		t.Fatal(err)
	}
	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("same_name 遇到 404 应切换, 状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("first=%d second=%d", firstHits.Load(), secondHits.Load())
	}
}

func TestSSEContentTypeCaseAndCreated(t *testing.T) {
	const payload = "data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":9}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "Text/Event-Stream")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()
	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)
	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	rec := waitRecord(t, st)
	if rec.Input != 5 || rec.Output != 9 {
		t.Fatalf("大小写/201 的 SSE 被当成非流式, 用量=%+v", rec)
	}
}

func TestLightweightSecondTargetOnLimit(t *testing.T) {
	var firstHits, secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		http.Error(w, "rate", http.StatusTooManyRequests)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer second.Close()
	h, mgr, _ := newTestHandler(t)
	if err := mgr.UpsertProvider(config.Provider{
		ID: "pv-1", Name: "lw-1", OpenAIURL: first.URL, Enabled: true, TimeoutSec: 30,
	}, "sk-1"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpsertProvider(config.Provider{
		ID: "pv-2", Name: "lw-2", OpenAIURL: second.URL, Enabled: true, TimeoutSec: 30,
	}, "sk-2"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateSettings(func(s *config.Settings) {
		s.Lightweight.Enabled = true
		s.Lightweight.Aliases = []string{"m"}
		s.Lightweight.Targets = []config.Target{
			{ProviderID: "pv-1", Model: "cheap-1"},
			{ProviderID: "pv-2", Model: "cheap-2"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("轻量通道应顺延下一目标, 状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("first=%d second=%d", firstHits.Load(), secondHits.Load())
	}
}

func TestMislabeledDeflatePassesThrough(t *testing.T) {
	const payload = `{"id":"x","usage":{"prompt_tokens":4,"completion_tokens":6}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "deflate")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()
	h, mgr, _ := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)
	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("错标 deflate 应原样透传, 状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != payload {
		t.Fatalf("透传体=%q", rr.Body.String())
	}
	if got := rr.Header().Get("Content-Encoding"); got != "deflate" {
		t.Fatalf("原样透传应保留 Content-Encoding, got %q", got)
	}
}

func TestStreamWithoutUsageIsVisible(t *testing.T) {
	const payload = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()
	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)
	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码=%d", rr.Code)
	}
	rec := waitRecord(t, st)
	if !rec.OK || !strings.Contains(rec.Err, "usage") {
		t.Fatalf("用量缺失应可见且仍算成功: ok=%v err=%q", rec.OK, rec.Err)
	}
}

func TestUpstreamKeepsClientContentType(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	h, mgr, _ := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader("raw-bytes"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=abc")
	req.Header.Set("x-api-key", mgr.RouterKey())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	if got != "multipart/form-data; boundary=abc" {
		t.Fatalf("Content-Type=%q", got)
	}
}
