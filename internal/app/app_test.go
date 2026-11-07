// 启动/停机回归测试: Stop 必须在关闭数据库之前让统计与归档把队列落盘,
// 否则最后一批统计和最新会话快照会丢失。
package app

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"cluster-route/internal/archive"
	"cluster-route/internal/store"
)

// TestStartStopFlushesPending 启动真实服务 → 投递统计与归档 → Stop → 校验已落库。
func TestStartStopFlushesPending(t *testing.T) {
	dir := t.TempDir()
	a, err := Start(dir, 0, nil, "test")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !a.Mgr.Settings().Archive.Enabled {
		t.Fatal("归档默认应开启")
	}

	// 统计: 投递后立即 Stop, 队列中的记录必须被 flush 到库
	a.Recorder.Submit(store.Record{
		Ts: time.Now().Unix(), ProviderNam: "test", Model: "m", Upstream: "m",
		Channel: "main", Status: 200, OK: true, Input: 10, Output: 5,
	})
	// 归档: 通道容量仅 2, 通过 Drain 路径落盘
	a.Archive.Submit(archive.Snapshot{
		Key: "sess-stop", Project: "p", Title: "停机测试",
		Model: "m", MsgCount: 1, Body: []byte(`{"model":"m","messages":[]}`),
		Ts: time.Now().Unix(),
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", a.Port)
	resp, err := http.Get(base + "/admin/version")
	if err != nil {
		t.Fatalf("服务未就绪: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/admin/version = %d", resp.StatusCode)
	}

	begin := time.Now()
	a.Stop()
	if el := time.Since(begin); el > 6*time.Second {
		t.Fatalf("Stop 耗时过长: %v", el)
	}
	if resp, err := http.Get(base + "/admin/version"); err == nil {
		resp.Body.Close()
		t.Fatal("Stop 后端口仍在监听")
	}

	// 重新打开数据库校验落盘结果(Store 已被 Stop 关闭)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("重新打开数据库: %v", err)
	}
	defer st.Close()
	rows, err := st.ListRequests(0, "", "", "", "", 10, 0)
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("停机时统计未落盘: got %d 条", len(rows))
	}
	snaps, err := st.ListSnapshots("sess-stop")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("停机时归档未落盘: got %d 份", len(snaps))
	}
}

// TestStopIsIdempotent 桌面模式下 Wails OnShutdown 与异常路径可能重复调用 Stop,
// 二次关闭已关闭的 channel / 数据库会 panic。
func TestStopIsIdempotent(t *testing.T) {
	a, err := Start(t.TempDir(), 0, nil, "test")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	a.Stop()
	a.Stop() // 不应 panic
	// 并发重复调用同样安全
	done := make(chan struct{})
	go func() { a.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("并发的 Stop 挂起")
	}
}
