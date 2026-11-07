// 归档与统计的停机/顺序行为测试: 快照 seq 必须严格递增(裁剪依赖它),
// 标题按 rune 截断不能切坏 UTF-8。
package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cluster-route/internal/config"
	"cluster-route/internal/crypto"
	"cluster-route/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
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
	t.Cleanup(func() { _ = st.Close() })
	return New(mgr, st, filepath.Join(dir, "sessions")), st
}

// TestWriteSeqStrictlyIncreasing 同一秒内的多个快照必须拿到不同的 seq,
// 否则文件互相覆盖, 且「保留最近 N 份」的 seq DESC 排序失去意义。
func TestWriteSeqStrictlyIncreasing(t *testing.T) {
	svc, st := newTestService(t)
	const ts = 1_700_000_000 // 同一秒
	for i := 0; i < 5; i++ {
		svc.write(Snapshot{
			Key: "sess-1", Project: "p", Title: "t", Model: "m",
			MsgCount: int64(i + 1), Body: []byte(`{"messages":[]}`), Ts: ts,
		})
	}
	rows, err := st.ListSnapshots("sess-1")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	// 默认每会话保留 3 份
	if len(rows) != 3 {
		t.Fatalf("保留份数=%d, want 3", len(rows))
	}
	seen := map[int64]bool{}
	for i, r := range rows {
		if seen[r.Seq] {
			t.Fatalf("seq 重复: %d", r.Seq)
		}
		seen[r.Seq] = true
		if i > 0 && r.Seq >= rows[i-1].Seq {
			t.Fatalf("快照未按 seq 严格递减排列: %d, %d", rows[i-1].Seq, r.Seq)
		}
		if _, err := os.Stat(filepath.Join(svc.dir, filepath.FromSlash(r.Path))); err != nil {
			t.Fatalf("快照文件不存在: %v", err)
		}
	}
	// 最旧的 2 份文件应已被裁剪
	if rows[0].Seq-rows[len(rows)-1].Seq != 2 {
		t.Fatalf("seq 间隔异常, 可能发生覆盖: %+v", rows)
	}
}

// TestTitleTruncatesByRune 中文标题按字节截断会产生非法 UTF-8。
func TestTitleTruncatesByRune(t *testing.T) {
	svc, st := newTestService(t)
	title := strings.Repeat("模型会话上下文", 20) // 140 个汉字, 远超 80
	svc.write(Snapshot{Key: "sess-2", Project: "p", Title: title, Body: []byte("{}"), Ts: 1})

	sessions, err := st.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("会话数=%d", len(sessions))
	}
	got := sessions[0].Title
	if r := []rune(got); len(r) != 80 {
		t.Fatalf("标题 rune 数=%d, want 80", len(r))
	}
	if !strings.HasPrefix(got, "模型会话上下文") || strings.ContainsRune(got, '�') {
		t.Fatalf("标题被截断成非法内容: %q", got)
	}
}

// TestDrainWritesPending 停机时队列中未处理的快照必须落盘。
func TestDrainWritesPending(t *testing.T) {
	svc, st := newTestService(t)
	svc.Submit(Snapshot{Key: "sess-3", Project: "p", Title: "t", Body: []byte(`{"model":"m"}`), Ts: 123})
	svc.Drain()
	rows, err := st.ListSnapshots("sess-3")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Drain 后快照数=%d, want 1", len(rows))
	}
}
