// Package pricing_test 覆盖 internal/pricing 的计费逻辑, 包括:
// 平价计费、峰/谷档位命中与峰优先、跨午夜时段、窗口边界语义(start 含 end 不含,
// start==end 视为永不命中)、multiplier/absolute 两种档位模式、未启用/非法档位回落,
// 未知模型不计费, 以及缓存读/写 token 的分别计价。
package pricing_test

import (
	"math"
	"testing"
	"time"

	"cluster-route/internal/pricing"
	"cluster-route/internal/store"
)

// costTol 浮点比较容差。
const costTol = 1e-12

// newTestPricer 创建临时 SQLite 单价库与 Pricer, 测试结束自动关闭数据库。
func newTestPricer(t *testing.T) (*pricing.Pricer, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p, err := pricing.New(st)
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	return p, st
}

// putPrice 写库后显式 Reload, 与 API 层更新单价的路径一致。
func putPrice(t *testing.T, p *pricing.Pricer, st *store.Store, pr store.Price) {
	t.Helper()
	if err := st.UpsertPrice(pr); err != nil {
		t.Fatalf("UpsertPrice: %v", err)
	}
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
}

// clock 构造本地时区的时刻(分钟精度, 与档位窗口判定一致)。
func clock(h, m int) time.Time {
	return time.Date(2026, time.March, 1, h, m, 0, 0, time.Local)
}

// assertCost 按容差比较成本。
func assertCost(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > costTol {
		t.Fatalf("Cost() = %v, want %v (容差 %v)", got, want, costTol)
	}
}

// TestCostFlat 平价计费: 四项单价分别生效。
func TestCostFlat(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "flat", Input: 3, Output: 15, CacheRead: 0.3, CacheCreation: 3.75,
	})
	got := p.Cost("flat", clock(12, 0), 1_000_000, 2_000_000, 400_000, 500_000)
	want := 3.0 + 7.5 + 0.6 + 1.5 // in + out + cacheRead + cacheCreate
	assertCost(t, got, want)
}

// TestCostCacheTokensSeparately 缓存读/写 token 必须各按对应单价计, 不串用输入/输出单价。
func TestCostCacheTokensSeparately(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "cache", Input: 100, Output: 100, CacheRead: 0.3, CacheCreation: 3.75,
	})
	got := p.Cost("cache", clock(12, 0), 0, 1_000_000, 1_000_000, 0)
	assertCost(t, got, 0.3+3.75)
}

// TestCostPeakHit 峰时段命中时使用峰档 absolute 单价。
func TestCostPeakHit(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3, Output: 15, CacheRead: 0.3, CacheCreation: 3.75,
		Peak: &store.Tier{
			Enabled: true, Start: "08:00", End: "20:00", Mode: "absolute",
			Input: 6, Output: 30, CacheRead: 0.6, CacheCreation: 7.5,
		},
	})
	got := p.Cost("m", clock(10, 0), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	assertCost(t, got, 6+30+0.6+7.5)
}

// TestCostValleyHit 谷时段命中时使用谷档 absolute 单价。
func TestCostValleyHit(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3, Output: 15, CacheRead: 0.3, CacheCreation: 3.75,
		Valley: &store.Tier{
			Enabled: true, Start: "00:00", End: "08:00", Mode: "absolute",
			Input: 1.5, Output: 7.5, CacheRead: 0.15, CacheCreation: 1.875,
		},
	})
	got := p.Cost("m", clock(3, 0), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	assertCost(t, got, 1.5+7.5+0.15+1.875)
}

// TestCostPeakPrecedenceOverValley 峰谷时段重叠时峰档优先。
func TestCostPeakPrecedenceOverValley(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3,
		Peak: &store.Tier{
			Enabled: true, Start: "09:00", End: "17:00", Mode: "absolute", Input: 6,
		},
		Valley: &store.Tier{
			Enabled: true, Start: "00:00", End: "23:59", Mode: "absolute", Input: 1.5,
		},
	})
	assertCost(t, p.Cost("m", clock(10, 0), 1_000_000, 0, 0, 0), 6)   // 峰谷重叠 → 峰
	assertCost(t, p.Cost("m", clock(20, 0), 1_000_000, 0, 0, 0), 1.5) // 仅谷命中
}

// TestCostCrossMidnight 跨午夜时段(start>end)的命中判定。
func TestCostCrossMidnight(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3,
		Peak: &store.Tier{
			Enabled: true, Start: "23:00", End: "07:00", Mode: "absolute", Input: 9,
		},
	})
	cases := []struct {
		name string
		h, m int
		want float64
	}{
		{"start 命中", 23, 0, 9},
		{"午夜前命中", 23, 30, 9},
		{"午夜整点命中", 0, 0, 9},
		{"午夜后命中", 6, 30, 9},
		{"end 不含", 7, 0, 3},
		{"白天不命中", 12, 0, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertCost(t, p.Cost("m", clock(tc.h, tc.m), 1_000_000, 0, 0, 0), tc.want)
		})
	}
}

// TestCostWindowBoundaries 窗口语义: start 含、end 不含。
func TestCostWindowBoundaries(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3,
		Peak: &store.Tier{
			Enabled: true, Start: "09:00", End: "17:00", Mode: "absolute", Input: 6,
		},
	})
	cases := []struct {
		name string
		h, m int
		want float64
	}{
		{"start 包含", 9, 0, 6},
		{"end 前一分钟", 16, 59, 6},
		{"end 不含", 17, 0, 3},
		{"start 前一分钟", 8, 59, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertCost(t, p.Cost("m", clock(tc.h, tc.m), 1_000_000, 0, 0, 0), tc.want)
		})
	}
}

// TestCostZeroLengthWindow start==end 视为永不命中(刻意固化的设计行为):
// 该写法无法表达"全天", 需要全天请用 00:00-23:59。
func TestCostZeroLengthWindow(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3,
		Peak: &store.Tier{
			Enabled: true, Start: "10:00", End: "10:00", Mode: "absolute", Input: 99,
		},
	})
	assertCost(t, p.Cost("m", clock(10, 0), 1_000_000, 0, 0, 0), 3)
}

// TestCostInvalidWindow 非法时间串视为不命中, 回落到平价(静默容错)。
func TestCostInvalidWindow(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3,
		Peak: &store.Tier{
			Enabled: true, Start: "nope", End: "17:00", Mode: "absolute", Input: 99,
		},
	})
	assertCost(t, p.Cost("m", clock(10, 0), 1_000_000, 0, 0, 0), 3)
}

// TestCostUnknownModel 无定价的模型成本为 0。
func TestCostUnknownModel(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{Model: "known", Input: 3})
	assertCost(t, p.Cost("unknown", clock(12, 0), 1_000_000, 1_000_000, 1_000_000, 1_000_000), 0)
}

// TestCostTierButFlatUnset 档位未命中且平价四项全 0 → 不计费。
func TestCostTierButFlatUnset(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m",
		Peak: &store.Tier{
			Enabled: true, Start: "00:00", End: "08:00", Mode: "absolute", Input: 9,
		},
	})
	assertCost(t, p.Cost("m", clock(12, 0), 1_000_000, 1_000_000, 1_000_000, 1_000_000), 0)
}

// TestCostMultiplier multiplier 档位按倍率放大平价四项; 平价缺失或倍率非正时不计费。
func TestCostMultiplier(t *testing.T) {
	cases := []struct {
		name string
		flat store.Price
		rate float64
		want float64
	}{
		{
			name: "平价配置时按倍率放大",
			flat: store.Price{Model: "m1", Input: 2, Output: 10, CacheRead: 0.2, CacheCreation: 2.5},
			rate: 1.5,
			want: 3 + 15 + 0.3 + 3.75,
		},
		{
			name: "平价未配置时不计费",
			flat: store.Price{Model: "m2"},
			rate: 2,
			want: 0,
		},
		{
			name: "倍率非正时不计费",
			flat: store.Price{Model: "m3", Input: 2, Output: 10, CacheRead: 0.2, CacheCreation: 2.5},
			rate: 0,
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, st := newTestPricer(t)
			pr := tc.flat
			pr.Peak = &store.Tier{
				Enabled: true, Start: "00:00", End: "23:59", Mode: "multiplier", Rate: tc.rate,
			}
			putPrice(t, p, st, pr)
			got := p.Cost(pr.Model, clock(10, 0), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
			assertCost(t, got, tc.want)
		})
	}
}

// TestCostAbsoluteTier absolute 模式直接使用档位四项单价。
func TestCostAbsoluteTier(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 1,
		Peak: &store.Tier{
			Enabled: true, Start: "00:00", End: "23:59", Mode: "absolute",
			Input: 4, Output: 20, CacheRead: 0.4, CacheCreation: 5,
		},
	})
	got := p.Cost("m", clock(10, 0), 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	assertCost(t, got, 4+20+0.4+5)
}

// TestCostTierModeEmptyIsAbsolute Mode 为空时按 absolute 处理(实现约定)。
func TestCostTierModeEmptyIsAbsolute(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 1,
		Peak: &store.Tier{
			Enabled: true, Start: "00:00", End: "23:59", Input: 5,
		},
	})
	assertCost(t, p.Cost("m", clock(10, 0), 1_000_000, 0, 0, 0), 5)
}

// TestCostDisabledTier 未启用的档位被忽略, 回落到平价。
func TestCostDisabledTier(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3,
		Peak: &store.Tier{
			Enabled: false, Start: "00:00", End: "23:59", Mode: "absolute", Input: 99,
		},
	})
	assertCost(t, p.Cost("m", clock(10, 0), 1_000_000, 0, 0, 0), 3)
}

// TestCostUpsertReplacesAndDelete 更新单价即时生效; 删除后不再计费。
func TestCostUpsertReplacesAndDelete(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{Model: "m", Input: 3})
	assertCost(t, p.Cost("m", clock(12, 0), 1_000_000, 0, 0, 0), 3)

	putPrice(t, p, st, store.Price{Model: "m", Input: 7})
	assertCost(t, p.Cost("m", clock(12, 0), 1_000_000, 0, 0, 0), 7)

	if err := p.Delete("m"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	assertCost(t, p.Cost("m", clock(12, 0), 1_000_000, 0, 0, 0), 0)
}

// TestCostTierHitButAllZero 命中 absolute 档位但四项全 0 → 视为不可计费, 不回落平价。
// 这是实现的刻意行为: 档位一旦命中就接管计价, 全 0 意味着该时段免费/不计费。
func TestCostTierHitButAllZero(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3, Output: 15,
		Peak: &store.Tier{
			Enabled: true, Start: "00:00", End: "23:59", Mode: "absolute",
		},
	})
	assertCost(t, p.Cost("m", clock(10, 0), 1_000_000, 1_000_000, 1_000_000, 1_000_000), 0)
}

// TestCostFlatAllZero 全部单价为 0 的模型计费为 0 且不 panic。
func TestCostFlatAllZero(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{Model: "free"})
	assertCost(t, p.Cost("free", clock(10, 0), 1_000_000, 1_000_000, 1_000_000, 1_000_000), 0)
}

// TestCostMinutesPrecision 档位判定精确到分钟, 秒不影响边界结果。
func TestCostMinutesPrecision(t *testing.T) {
	p, st := newTestPricer(t)
	putPrice(t, p, st, store.Price{
		Model: "m", Input: 3,
		Peak: &store.Tier{
			Enabled: true, Start: "09:00", End: "17:00", Mode: "absolute", Input: 6,
		},
	})
	// 09:00:59 仍属于 [09:00,17:00)
	at := time.Date(2026, time.March, 1, 9, 0, 59, 0, time.Local)
	assertCost(t, p.Cost("m", at, 1_000_000, 0, 0, 0), 6)
	// 16:59:59 属于窗口内
	at = time.Date(2026, time.March, 1, 16, 59, 59, 0, time.Local)
	assertCost(t, p.Cost("m", at, 1_000_000, 0, 0, 0), 6)
}
