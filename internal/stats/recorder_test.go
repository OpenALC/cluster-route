// 统计采集器回归测试: 成本必须在落库时按上游模型名与请求时刻计算,
// 队列满时只能丢弃统计不能阻塞转发, 停机时队列必须落盘。
package stats

import (
	"testing"
	"time"

	"cluster-route/internal/pricing"
	"cluster-route/internal/store"
)

// newRecorder 构造记录器并预置一条单价。默认不启动 Run, 由调用方决定
// 走同步 Flush 还是后台 Run 路径, 避免后台协程与测试抢同一条记录。
func newRecorder(t *testing.T, queueCap int) (*Recorder, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	pr, err := pricing.New(st)
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	if err := st.UpsertPrice(store.Price{Model: "up-m", Input: 1, Output: 2, CacheRead: 0.1, CacheCreation: 0.2}); err != nil {
		t.Fatalf("UpsertPrice: %v", err)
	}
	if err := pr.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return New(st, pr, queueCap), st
}

// TestCostUsesUpstreamModel 计费键是上游真实模型名(upstream_model), 不是客户端别名。
func TestCostUsesUpstreamModel(t *testing.T) {
	r, st := newRecorder(t, 8)
	defer st.Close()
	r.Submit(store.Record{
		Ts: time.Now().Unix(), Model: "alias", Upstream: "up-m",
		Channel: "main", Status: 200, OK: true,
		Input: 1_000_000, Output: 1_000_000,
	})
	r.Flush()
	rows, err := st.ListRequests(0, "", "", "", "", 5, 0)
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("落库条数=%d", len(rows))
	}
	// 1M 输入 × $1 + 1M 输出 × $2 = $3
	if got := rows[0].Cost; got < 2.999 || got > 3.001 {
		t.Fatalf("成本=%v, want≈3", got)
	}
}

// TestCostUsesRequestTimestamp 峰谷定价按请求时刻选择档位, 而不是按落库时刻。
func TestCostUsesRequestTimestamp(t *testing.T) {
	r, st := newRecorder(t, 8)
	defer st.Close()
	if err := st.UpsertPrice(store.Price{
		Model: "up-m", Input: 1, Output: 2,
		Peak: &store.Tier{Enabled: true, Start: "00:00", End: "01:00", Mode: "multiplier", Rate: 10},
	}); err != nil {
		t.Fatalf("UpsertPrice: %v", err)
	}
	if err := r.pricer.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	peak := time.Date(2026, 3, 1, 0, 30, 0, 0, time.Local)
	off := time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local)
	r.Submit(store.Record{Ts: peak.Unix(), Upstream: "up-m", OK: true, Input: 1_000_000})
	r.Submit(store.Record{Ts: off.Unix(), Upstream: "up-m", OK: true, Input: 1_000_000})
	r.Flush()

	rows, err := st.ListRequests(0, "", "", "", "", 5, 0)
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("落库条数=%d", len(rows))
	}
	// 列表按 ts DESC: 第一条是 12:00
	if got := rows[0].Cost; got < 0.999 || got > 1.001 {
		t.Fatalf("平价档成本=%v, want≈1", got)
	}
	if got := rows[1].Cost; got < 9.99 || got > 10.01 {
		t.Fatalf("峰时档成本=%v, want≈10", got)
	}
}

// TestSubmitNeverBlocksWhenFull 队列满时必须立刻返回(丢弃统计), 不能拖慢转发。
func TestSubmitNeverBlocksWhenFull(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	pr, _ := pricing.New(st)
	r := New(st, pr, 1) // 不启动 Run, 队列很快填满

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			r.Submit(store.Record{Ts: time.Now().Unix()})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("队列满时 Submit 阻塞了")
	}
}

// TestStopFlushesBatch 停机信号到达时, 仍在 channel 中、未达到批量阈值的记录必须落盘。
func TestStopFlushesBatch(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	pr, _ := pricing.New(st)
	r := New(st, pr, 8)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { r.Run(stop); close(done) }()

	r.Submit(store.Record{Ts: time.Now().Unix(), Upstream: "m", OK: true})
	close(stop)
	<-done

	rows, err := st.ListRequests(0, "", "", "", "", 5, 0)
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("停机时未落盘: got %d", len(rows))
	}
}
