package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cluster-route/internal/config"
)

func putSettings(t *testing.T, h http.Handler, body []byte, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/settings"+query, bytes.NewReader(body))
	req.Host = "127.0.0.1:3721"
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func TestSettingsScopeDoesNotClobber(t *testing.T) {
	deps, mgr, _ := newTestDeps(t)
	h := Router(deps)
	if err := mgr.UpdateSettings(func(s *config.Settings) {
		s.Inject.Enabled = true
		s.Inject.Text = "守则"
	}); err != nil {
		t.Fatal(err)
	}
	stale := mgr.Settings()
	stale.Inject = config.Inject{}
	stale.Lightweight.Enabled = true
	body, _ := json.Marshal(stale)
	rec := putSettings(t, h, body, "?scope=channels")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := mgr.Settings()
	if !got.Lightweight.Enabled {
		t.Fatal("通道开关未保存")
	}
	if !got.Inject.Enabled || got.Inject.Text != "守则" {
		t.Fatalf("路由页旧快照抹掉了注入守则: %+v", got.Inject)
	}
}

func TestRejectsBadPort(t *testing.T) {
	deps, mgr, _ := newTestDeps(t)
	h := Router(deps)
	s := mgr.Settings()
	s.Port = 70000
	body, _ := json.Marshal(s)
	rec := putSettings(t, h, body, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if mgr.Settings().Port == 70000 {
		t.Fatal("非法端口被保存")
	}
}

func TestRejectsNegativePrice(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	h := Router(deps)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/pricing", strings.NewReader(`{"model":"m","input":-5,"output":-3}`))
	req.Host = "127.0.0.1:3721"
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	list, err := deps.Pricer.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("负数单价被写入: %+v", list)
	}
}

func TestConnectionUsesListenPort(t *testing.T) {
	deps, mgr, _ := newTestDeps(t)
	deps.ListenPort = 62910
	h := Router(deps)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/connection", nil)
	req.Host = "127.0.0.1:3721"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "62910") {
		t.Fatalf("应返回实际监听端口, body=%s configPort=%d", rec.Body.String(), mgr.Settings().Port)
	}
}

// TestTestAllKeepsEveryResult 并发测试多个供应商时, 每个结果都必须落到响应里
// 并持久化。此前 items 在循环里 append、goroutine 同时按下标写, 切片头存在数据
// 竞争: append 扩容后早先的 goroutine 会写进旧底层数组, 结果被静默丢弃,
// 前端却仍提示"全部正常"。启用 -race 时本用例可捕获该竞争。
func TestTestAllKeepsEveryResult(t *testing.T) {
	deps, mgr, _ := newTestDeps(t)

	// 不同延迟放大并发窗口, 让多个 goroutine 真正交错
	delays := []time.Duration{0, 15 * time.Millisecond, 5 * time.Millisecond, 20 * time.Millisecond, 10 * time.Millisecond}
	for i, d := range delays {
		delay := d
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(delay)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
		}))
		t.Cleanup(srv.Close)
		if err := mgr.UpsertProvider(config.Provider{
			Name:      "P" + strconv.Itoa(i),
			OpenAIURL: srv.URL,
			Enabled:   true,
		}, "sk-x"); err != nil {
			t.Fatalf("UpsertProvider: %v", err)
		}
	}

	h := Router(deps)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/providers/test_all", nil)
	req.Host = "127.0.0.1:3721"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if len(out) != len(delays) {
		t.Fatalf("结果丢失: 返回 %d 个, 期望 %d 个", len(out), len(delays))
	}

	persisted := 0
	for _, p := range mgr.Cfg().Providers {
		if p.LastTest != nil {
			persisted++
		}
	}
	if persisted != len(delays) {
		t.Fatalf("测试结果未全部持久化: %d/%d", persisted, len(delays))
	}
}
