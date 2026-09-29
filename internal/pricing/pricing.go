// Package pricing 维护每百万 token 单价并计算请求成本,
// 支持导入 cc-switch 的 model-pricing.json。
package pricing

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"cluster-route/internal/store"
)

// Pricer 内存单价表, 落库于 SQLite, 请求路径只读无锁竞争极小。
type Pricer struct {
	mu     sync.RWMutex
	prices map[string]store.Price
	store  *store.Store
}

// New 从数据库加载单价表。
func New(st *store.Store) (*Pricer, error) {
	p := &Pricer{prices: map[string]store.Price{}, store: st}
	if err := p.Reload(); err != nil {
		return nil, err
	}
	return p, nil
}

// Reload 从数据库刷新内存表。
func (p *Pricer) Reload() error {
	list, err := p.store.ListPrices()
	if err != nil {
		return err
	}
	m := make(map[string]store.Price, len(list))
	for _, it := range list {
		m[it.Model] = it
	}
	p.mu.Lock()
	p.prices = m
	p.mu.Unlock()
	return nil
}

// Cost 按请求时刻选择峰/谷/平档位计算成本(美元); 无定价或无对应档位时返回 0。
// 档位选择: 命中峰时段用峰价, 否则命中谷时段用谷价, 否则平价;
// 平价未配置且档位为倍率模式时视为不计费。
func (p *Pricer) Cost(model string, at time.Time, in, cacheRead, cacheCreate, out int64) float64 {
	p.mu.RLock()
	pr, ok := p.prices[model]
	p.mu.RUnlock()
	if !ok {
		return 0
	}
	prIn, prOut, prCr, prCc, priced := selectTier(pr, at)
	if !priced {
		return 0
	}
	return (float64(in)*prIn +
		float64(out)*prOut +
		float64(cacheRead)*prCr +
		float64(cacheCreate)*prCc) / 1e6
}

// selectTier 依时刻选择档位, 返回四项单价与是否可计费。
func selectTier(pr store.Price, at time.Time) (prIn, prOut, prCr, prCc float64, priced bool) {
	flatSet := pr.Input != 0 || pr.Output != 0 || pr.CacheRead != 0 || pr.CacheCreation != 0
	for _, tier := range []*store.Tier{pr.Peak, pr.Valley} { // 峰优先于谷
		if tier == nil || !tier.Enabled || !inWindow(at, tier.Start, tier.End) {
			continue
		}
		if tier.Mode == "multiplier" {
			if !flatSet || tier.Rate <= 0 {
				return 0, 0, 0, 0, false
			}
			return pr.Input * tier.Rate, pr.Output * tier.Rate,
				pr.CacheRead * tier.Rate, pr.CacheCreation * tier.Rate, true
		}
		return tier.Input, tier.Output, tier.CacheRead, tier.CacheCreation,
			tier.Input != 0 || tier.Output != 0 || tier.CacheRead != 0 || tier.CacheCreation != 0
	}
	return pr.Input, pr.Output, pr.CacheRead, pr.CacheCreation, flatSet
}

// inWindow 判断时刻是否落在 [start,end) 内; 支持 start>end 的跨午夜时段。
func inWindow(at time.Time, start, end string) bool {
	sm, ok1 := parseHM(start)
	em, ok2 := parseHM(end)
	if !ok1 || !ok2 {
		return false
	}
	cur := at.Hour()*60 + at.Minute()
	if sm == em {
		return false
	}
	if sm < em {
		return cur >= sm && cur < em
	}
	return cur >= sm || cur < em // 跨午夜
}

func parseHM(s string) (int, bool) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// Upsert 写入单价并刷新内存。
func (p *Pricer) Upsert(pr store.Price) error {
	if err := p.store.UpsertPrice(pr); err != nil {
		return err
	}
	return p.Reload()
}

// Delete 删除单价并刷新内存。
func (p *Pricer) Delete(model string) error {
	if err := p.store.DeletePrice(model); err != nil {
		return err
	}
	return p.Reload()
}

// List 全部单价。
func (p *Pricer) List() ([]store.Price, error) { return p.store.ListPrices() }

// ccPricing 兼容 cc-switch model-pricing.json 结构(仅取所需字段)。
type ccPricing struct {
	Version int `json:"version"`
	Models  []struct {
		ModelID                  string `json:"modelId"`
		DisplayName              string `json:"displayName"`
		InputCostPerMillion      string `json:"inputCostPerMillion"`
		OutputCostPerMillion     string `json:"outputCostPerMillion"`
		CacheReadCostPerMillion  string `json:"cacheReadCostPerMillion"`
		CacheCreationCostPerMillion string `json:"cacheCreationCostPerMillion"`
	} `json:"models"`
}

// ImportCCSwitch 导入 cc-switch 的 model-pricing.json 内容, 返回导入条数。
func (p *Pricer) ImportCCSwitch(data []byte) (int, error) {
	var cc ccPricing
	if err := json.Unmarshal(data, &cc); err != nil {
		return 0, fmt.Errorf("JSON 解析失败: %w", err)
	}
	n := 0
	for _, m := range cc.Models {
		if strings.TrimSpace(m.ModelID) == "" {
			continue
		}
		pr := store.Price{
			Model:         m.ModelID,
			Input:         parseNum(m.InputCostPerMillion),
			Output:        parseNum(m.OutputCostPerMillion),
			CacheRead:     parseNum(m.CacheReadCostPerMillion),
			CacheCreation: parseNum(m.CacheCreationCostPerMillion),
		}
		if err := p.store.UpsertPrice(pr); err != nil {
			return n, err
		}
		n++
	}
	if err := p.Reload(); err != nil {
		return n, err
	}
	return n, nil
}

func parseNum(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	return f
}
