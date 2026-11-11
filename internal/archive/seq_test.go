package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeqSurvivesRestart(t *testing.T) {
	svc, st := newTestService(t)
	const ts = int64(1000)
	svc.write(Snapshot{Key: "s", Body: []byte(`{"messages":[]}`), Ts: ts, Title: "a"})
	svc.write(Snapshot{Key: "s", Body: []byte(`{"messages":[1]}`), Ts: ts, Title: "b"})
	restarted := New(svc.mgr, st, svc.dir)
	restarted.write(Snapshot{Key: "s", Body: []byte(`{"messages":[2]}`), Ts: ts, Title: "c"})
	rows, err := st.ListSnapshots("s")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	seen := map[int64]bool{}
	for _, r := range rows {
		if seen[r.Seq] {
			t.Fatalf("重启后 seq 撞号: %d", r.Seq)
		}
		seen[r.Seq] = true
		if _, err := os.Stat(filepath.Join(svc.dir, filepath.FromSlash(r.Path))); err != nil {
			t.Fatalf("seq %d 文件缺失: %v", r.Seq, err)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("保留份数=%d, want 3", len(rows))
	}
}

func TestReadSnapshotRejectsMissingMessages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"model":"m"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(path); err == nil {
		t.Fatal("缺少 messages 应报错")
	}
}
