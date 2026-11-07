// 天级聚合读取的回归测试: daily_stats 的存在意义是「明细被清理后仍能看历史」,
// 若 TimeseriesDaily 解析失败返回空, 前端 30 天/全年趋势会静默变空白。
package store

import (
	"testing"
	"time"
)

func TestTimeseriesDailyAggregatesAcrossProviders(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// 两天, 每天两个供应商同名模型 → 应按天合并为两组
	day1 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.Local).Unix()
	day2 := time.Date(2026, 1, 3, 10, 0, 0, 0, time.Local).Unix()
	recs := []Record{
		{Ts: day1, ProviderID: "p1", ProviderNam: "A", Model: "m", Upstream: "m", Channel: "main", Input: 100, Output: 10, Cost: 0.1},
		{Ts: day1, ProviderID: "p2", ProviderNam: "B", Model: "m", Upstream: "m", Channel: "main", Input: 200, Output: 20, Cost: 0.2},
		{Ts: day2, ProviderID: "p1", ProviderNam: "A", Model: "m", Upstream: "m", Channel: "main", Input: 300, Output: 30, Cost: 0.3},
	}
	if err := st.InsertBatch(recs); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	pts, err := st.TimeseriesDaily()
	if err != nil {
		t.Fatalf("TimeseriesDaily: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("天级点数=%d, want 2: %+v", len(pts), pts)
	}
	if pts[0].Requests != 2 || pts[0].Input != 300 || pts[0].Output != 30 {
		t.Fatalf("第 1 天聚合错误: %+v", pts[0])
	}
	if pts[1].Requests != 1 || pts[1].Input != 300 {
		t.Fatalf("第 2 天聚合错误: %+v", pts[1])
	}
	if pts[0].Bucket >= pts[1].Bucket {
		t.Fatalf("未按日期升序: %d, %d", pts[0].Bucket, pts[1].Bucket)
	}
	if want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local).Unix(); pts[0].Bucket != want {
		t.Fatalf("bucket=%d, want 当日零点 %d", pts[0].Bucket, want)
	}

	// 明细被清理后, 天级聚合仍应保留
	if _, err := st.CleanupRequests(0); err != nil { // 保留 0 天 = 删除全部过往明细
		t.Fatalf("CleanupRequests: %v", err)
	}
	pts, err = st.TimeseriesDaily()
	if err != nil {
		t.Fatalf("TimeseriesDaily after cleanup: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("明细清理后天级聚合丢失: %+v", pts)
	}
}
