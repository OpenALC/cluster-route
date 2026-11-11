package pricing_test

import (
	"testing"

	"cluster-route/internal/store"
)

func TestImportKeepsPeakValley(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3,
		Peak: &store.Tier{Enabled: true, Start: "08:00", End: "20:00", Mode: "absolute", Input: 6},
	})
	n, err := p.ImportCCSwitch([]byte(`{"models":[{"modelId":"m","inputCostPerMillion":"9","outputCostPerMillion":"1"}]}`))
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	list, err := st.ListPrices()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Input != 9 || list[0].Peak == nil || list[0].Peak.Input != 6 {
		t.Fatalf("导入抹掉了峰谷: %+v", list)
	}
}
