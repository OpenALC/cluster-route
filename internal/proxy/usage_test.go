package proxy

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cluster-route/internal/pricing"
	"cluster-route/internal/store"
)

// TestOpenAICachedTokensNotDoubleBilled OpenAI 的 prompt_tokens 已包含 cached_tokens。
// prompt=1000, cached=800, 输出 10, 输入单价 1, 缓存读 0.1, 输出单价 1
// 必须得到 0.00029, 而不是把 800 再按输入单价乘一遍的 0.00109。
func TestOpenAICachedTokensNotDoubleBilled(t *testing.T) {
	const raw = `{"usage":{"prompt_tokens":1000,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":800}}}`
	cases := []struct {
		name string
		u    Usage
	}{
		{"non-stream", ParseNonStream([]byte(raw), "openai")},
		{"stream", feedOpenAI(raw)},
	}
	pr := priced(t, store.Price{Model: "m", Input: 1, Output: 1, CacheRead: 0.1})
	const want = 0.00029
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pr.Cost("m", time.Now(), tc.u.Input, tc.u.CacheRead, tc.u.CacheCreate, tc.u.Output)
			if math.Abs(got-want) > 1e-12 {
				t.Fatalf("Cost=%v, want %v (重复计费会得到 0.00109); usage=%+v", got, want, tc.u)
			}
		})
	}
}

// TestResponsesUsageParsed /v1/responses 使用 input_tokens/output_tokens, 不是 prompt_tokens。
func TestResponsesUsageParsed(t *testing.T) {
	u := ParseNonStream([]byte(`{"usage":{"input_tokens":100,"output_tokens":50}}`), "openai")
	if u != (Usage{Input: 100, Output: 50}) {
		t.Fatalf("non-stream responses usage=%+v, want input=100 output=50", u)
	}
	cached := ParseNonStream([]byte(`{"usage":{"input_tokens":100,"output_tokens":50,"input_tokens_details":{"cached_tokens":20}}}`), "openai")
	if cached != (Usage{Input: 80, Output: 50, CacheRead: 20}) {
		t.Fatalf("responses cached subset=%+v, want input=80 cache_read=20 output=50", cached)
	}
	stream := feedSSE("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":50}}}\n")
	if stream != (Usage{Input: 100, Output: 50}) {
		t.Fatalf("stream responses usage=%+v, want input=100 output=50", stream)
	}
}

// TestCacheReadInputTokensBothPaths 中转把 cache_read_input_tokens 塞进 OpenAI usage 时,
// 流式与非流式必须得到同一个缓存读数量。
func TestCacheReadInputTokensBothPaths(t *testing.T) {
	const raw = `{"usage":{"prompt_tokens":100,"completion_tokens":10,"cache_read_input_tokens":80}}`
	non := ParseNonStream([]byte(raw), "openai")
	stream := feedOpenAI(raw)
	if non.CacheRead != 80 || stream.CacheRead != 80 || non.CacheRead != stream.CacheRead {
		t.Fatalf("non=%+v stream=%+v, want cache_read=80 on both", non, stream)
	}
}

// TestSSELastFrameWithoutNewline 上游最后一帧不带换行时, 用量不能被丢掉。
func TestSSELastFrameWithoutNewline(t *testing.T) {
	const payload = `data: {"usage":{"prompt_tokens":5,"completion_tokens":9}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(payload))
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
	if rec.Input != 5 || rec.Output != 9 {
		t.Fatalf("末帧无换行时用量丢失: %+v", rec)
	}
}

// TestRecordTimestampIsRequestStart 峰谷按请求开始时刻计价, 不按响应结束时刻。
func TestRecordTimestampIsRequestStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	h, mgr, st := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)
	before := time.Now()
	rr := doProxy(t, h, mgr, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	ended := time.Now()
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	if ended.Unix() < before.Unix()+3 {
		t.Fatal("测试未能拉开开始与结束的秒级差距")
	}
	rec := waitRecord(t, st)
	if rec.Ts > before.Unix()+1 {
		t.Fatalf("Ts=%d 落在响应结束附近(start=%d end=%d), 峰谷会按结束时刻计", rec.Ts, before.Unix(), ended.Unix())
	}
}

func feedOpenAI(raw string) Usage {
	return feedSSE("data: " + raw + "\n")
}

func feedSSE(chunk string) Usage {
	p := NewSSEParser("openai")
	p.FeedChunk([]byte(chunk))
	return p.Usage()
}

func priced(t *testing.T, pr store.Price) *pricing.Pricer {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p, err := pricing.New(st)
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	if err := p.Upsert(pr); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	return p
}
