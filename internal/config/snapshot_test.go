package config

import "testing"

func TestCfgSnapshotSurvivesDelete(t *testing.T) {
	m, _ := newManager(t)
	a := addProvider(t, m, "A", "sk-a")
	b := addProvider(t, m, "B", "sk-b")
	snap := m.Cfg()
	if len(snap.Providers) != 2 {
		t.Fatalf("快照供应商数=%d", len(snap.Providers))
	}
	if err := m.DeleteProvider(a.ID); err != nil {
		t.Fatal(err)
	}
	if len(snap.Providers) != 2 || snap.Providers[0].ID == "" && snap.Providers[1].ID == "" {
		t.Fatalf("删除后旧快照被改写: %+v", snap.Providers)
	}
	found := false
	for _, p := range snap.Providers {
		if p.ID == b.ID || p.ID == a.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("快照内容丢失")
	}
	if len(m.Cfg().Providers) != 1 {
		t.Fatalf("删除后现存供应商数=%d", len(m.Cfg().Providers))
	}
}
