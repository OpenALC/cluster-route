package pricing_test

import (
	"strings"
	"testing"

	"cluster-route/internal/store"
)

// TestImportRejectsNonNumericPrices 带 $ 或千分位的单价必须报错, 不能静默写成 0。
func TestImportRejectsNonNumericPrices(t *testing.T) {
	p, st := newTestPricer(t)
	data := []byte(`{"models":[
		{"modelId":"ok","inputCostPerMillion":"3","outputCostPerMillion":"1"},
		{"modelId":"bad-dollar","inputCostPerMillion":"$3.00","outputCostPerMillion":"15"},
		{"modelId":"bad-comma","inputCostPerMillion":"1,000","outputCostPerMillion":"2"}
	]}`)
	n, err := p.ImportCCSwitch(data)
	if err == nil {
		t.Fatal("非数字单价应返回错误")
	}
	if !strings.Contains(err.Error(), "bad-dollar") || !strings.Contains(err.Error(), "bad-comma") {
		t.Fatalf("错误应汇总失败项, got %v", err)
	}
	if n != 0 {
		t.Fatalf("imported=%d, 解析失败时不应写入", n)
	}
	list, err := st.ListPrices()
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("解析失败仍写入了 %d 条(含 0 定价)", len(list))
	}
}

// TestImportEmptyPriceIsZero 空字段表示未配置, 不是解析失败。
func TestImportEmptyPriceIsZero(t *testing.T) {
	p, st := newTestPricer(t)
	data := []byte(`{"models":[{"modelId":"m","inputCostPerMillion":"3.5","outputCostPerMillion":"","cacheReadCostPerMillion":"  "}]}`)
	n, err := p.ImportCCSwitch(data)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	list, err := st.ListPrices()
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	if len(list) != 1 || list[0].Input != 3.5 || list[0].Output != 0 || list[0].CacheRead != 0 {
		t.Fatalf("空字段应记 0, got %+v", list)
	}
}

// TestCostModelKeyFold 定价键忽略大小写与首尾空白, 否则静默 $0。
func TestCostModelKeyFold(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{Model: " Claude-Opus ", Input: 3})
	assertCost(t, p.Cost("claude-opus", clock(12, 0), 1_000_000, 0, 0, 0), 3)
	assertCost(t, p.Cost("CLAUDE-OPUS", clock(12, 0), 1_000_000, 0, 0, 0), 3)
}
