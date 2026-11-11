package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"cluster-route/internal/config"
)

// TestConcurrentTestAllNoRace 并发测试多个供应商。配合 -race 运行时,
// 可检测 items 切片头是否被并发读写(旧实现的风险点)。
func TestConcurrentTestAllNoRace(t *testing.T) {
	deps, mgr, _ := newTestDeps(t)
	for i := 0; i < 6; i++ {
		delay := time.Duration(i%3) * 5 * time.Millisecond
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(delay)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
		}))
		t.Cleanup(srv.Close)
		if err := mgr.UpsertProvider(config.Provider{
			Name: "P" + strconv.Itoa(i), OpenAIURL: srv.URL, Enabled: true,
		}, "sk-x"); err != nil {
			t.Fatal(err)
		}
	}
	h := Router(deps)
	req := httptest.NewRequest(http.MethodPost, "/admin/providers/test_all", nil)
	req.Host = "127.0.0.1:3721"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
}
