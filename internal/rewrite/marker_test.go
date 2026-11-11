package rewrite_test

import (
	"strings"
	"testing"

	"cluster-route/internal/rewrite"
)

func TestSubagentMarkerBeyondScanCap(t *testing.T) {
	marker := "You are Claude Code"
	prefix := strings.Repeat("背景说明。", 5000)
	body := `{"model":"m","system":` + jsonQuote(prefix+"\n"+marker) + `,"messages":[]}`
	info := rewrite.Extract([]byte(body), "anthropic", marker)
	if info.IsSubagent {
		t.Fatal("标记在 system 后部时被误判成子 agent")
	}
}
