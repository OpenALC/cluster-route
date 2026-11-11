package store

import (
	"testing"
	"time"
)

func TestGroupByOrdersByTotalCost(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recs := []Record{
		{Ts: 1, ProviderNam: "A", Model: "A", Cost: 0.2, Channel: "main"},
		{Ts: 2, ProviderNam: "A", Model: "A", Cost: 0.2, Channel: "main"},
		{Ts: 3, ProviderNam: "A", Model: "A", Cost: 0.2, Channel: "main"},
		{Ts: 4, ProviderNam: "B", Model: "B", Cost: 0.5, Channel: "main"},
	}
	if err := st.InsertBatch(recs); err != nil {
		t.Fatal(err)
	}
	rows, err := st.GroupBy(0, "provider_name", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != "A" || rows[0].Cost < 0.59 {
		t.Fatalf("应按总费用排序, got %+v", rows)
	}
}

func TestTimeseriesUsesLocalDay(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 3, 1, 1, 0, 0, 0, time.Local).Unix()
	if err := st.InsertBatch([]Record{{Ts: at, ProviderNam: "A", Model: "m", Channel: "main", Input: 1}}); err != nil {
		t.Fatal(err)
	}
	pts, err := st.Timeseries(0, 86400)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("点数=%d", len(pts))
	}
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local).Unix()
	if pts[0].Bucket != want {
		t.Fatalf("本地天桶=%d, want %d", pts[0].Bucket, want)
	}
}
