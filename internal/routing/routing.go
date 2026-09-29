// Package routing 实现目标决策管线:
// 轻量通道 → 子agent兜底 → 常规路由表 → 默认供应商, 并附加限额切换链。
package routing

import (
	"sort"
	"strings"

	"cluster-router/internal/config"
	"cluster-router/internal/rewrite"
)

// 通道常量。
const (
	ChannelMain        = "main"
	ChannelLightweight = "lightweight"
	ChannelSubagent    = "subagent"
	ChannelDefault     = "default"
)

// Target 一次转发目标(含解密后的密钥, 仅驻内存)。
type Target struct {
	Provider config.Provider
	Model    string
	Key      string
}

// Decision 决策结果: 通道 + 目标链(链首为主目标, 其余为限额切换目标)。
type Decision struct {
	Channel string
	Chain   []Target
}

// Attempt 一次尝试的轨迹(统计用)。
type Attempt struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Status   int    `json:"status,omitempty"`
	Err      string `json:"err,omitempty"`
}

// Engine 决策引擎。
type Engine struct {
	mgr *config.Manager
}

// New 创建引擎。
func New(mgr *config.Manager) *Engine { return &Engine{mgr: mgr} }

// Decide 按优先级决策主目标并构建切换链。
func (e *Engine) Decide(alias, format string, info *rewrite.BodyInfo) Decision {
	cfg := e.mgr.Cfg()
	s := cfg.Settings

	dec := Decision{Channel: ChannelMain}
	setPrimary := func(t Target, channel string) {
		dec.Channel = channel
		dec.Chain = append(dec.Chain, t)
	}

	switch {
	case s.Lightweight.Enabled && e.lightHit(&s.Lightweight, alias, info):
		if t, ok := e.firstTarget(s.Lightweight.Targets, alias); ok {
			setPrimary(t, ChannelLightweight)
		}
	}
	if len(dec.Chain) == 0 && s.Subagent.Enabled && info.IsSubagent {
		if t, ok := e.resolve(s.Subagent.Target, alias); ok {
			setPrimary(t, ChannelSubagent)
		}
	}
	if len(dec.Chain) == 0 {
		if r, ok := e.routeHit(cfg.Routes, alias); ok {
			setPrimary(r, ChannelMain)
		} else if s.DefaultProviderID != "" {
			if t, ok := e.resolve(config.Target{ProviderID: s.DefaultProviderID}, alias); ok {
				setPrimary(t, ChannelDefault)
			}
		}
	}
	if len(dec.Chain) == 0 {
		return dec // 无可用目标
	}
	e.appendFailover(&dec, s, format, alias)
	return dec
}

// lightHit 轻量通道触发判定: 别名精确命中, 或启发式(小请求)。
func (e *Engine) lightHit(lw *config.Lightweight, alias string, info *rewrite.BodyInfo) bool {
	for _, a := range lw.Aliases {
		if a != "" && a == alias {
			return true
		}
	}
	if lw.Heur.Enabled && info.Root {
		if lw.Heur.RequireNoTools && info.HasTools {
			return false
		}
		return info.BodyBytes <= lw.Heur.MaxBodyBytes
	}
	return false
}

func (e *Engine) routeHit(routes []config.Route, alias string) (Target, bool) {
	if alias == "" {
		return Target{}, false
	}
	for _, r := range routes {
		if r.Enabled && r.Alias == alias {
			return e.resolve(config.Target{ProviderID: r.ProviderID, Model: r.UpstreamModel}, alias)
		}
	}
	return Target{}, false
}

// firstTarget 返回轻量通道列表中第一个可用目标, 其模型缺省用请求别名。
func (e *Engine) firstTarget(ts []config.Target, alias string) (Target, bool) {
	for _, t := range ts {
		if tt, ok := e.resolve(t, alias); ok {
			return tt, true
		}
	}
	return Target{}, false
}

// resolve 将配置目标解析为可执行目标(供应商启用 + 有 Key + 有模型名)。
func (e *Engine) resolve(t config.Target, fallbackModel string) (Target, bool) {
	cfg := e.mgr.Cfg()
	for _, p := range cfg.Providers {
		if p.ID == t.ProviderID {
			if !p.Enabled {
				return Target{}, false
			}
			key := e.mgr.ProviderKey(p.ID)
			if key == "" {
				return Target{}, false
			}
			model := t.Model
			if model == "" {
				model = fallbackModel
			}
			if model == "" {
				return Target{}, false
			}
			return Target{Provider: p, Model: model, Key: key}, true
		}
	}
	return Target{}, false
}

// appendFailover 依据限额切换配置扩展目标链。
func (e *Engine) appendFailover(dec *Decision, s config.Settings, format, alias string) {
	if !s.Failover.Enabled || len(dec.Chain) == 0 {
		return
	}
	max := s.Failover.MaxAttempts
	if max <= 0 {
		max = 3
	}
	if max > 5 {
		max = 5
	}
	primary := dec.Chain[0]

	add := func(t Target) {
		if len(dec.Chain) >= max {
			return
		}
		for _, ex := range dec.Chain {
			if ex.Provider.ID == t.Provider.ID && ex.Model == t.Model {
				return
			}
		}
		dec.Chain = append(dec.Chain, t)
	}

	if s.Failover.Mode == "same_name" {
		provs := append([]config.Provider(nil), e.mgr.Cfg().Providers...)
		sort.SliceStable(provs, func(i, j int) bool {
			if provs[i].Priority != provs[j].Priority {
				return provs[i].Priority < provs[j].Priority
			}
			return provs[i].Name < provs[j].Name
		})
		for _, p := range provs {
			if len(dec.Chain) >= max {
				break
			}
			if p.ID == primary.Provider.ID || !p.Enabled {
				continue
			}
			if format == "anthropic" && strings.TrimSpace(p.AnthropicURL) == "" {
				continue
			}
			if format == "openai" && strings.TrimSpace(p.OpenAIURL) == "" {
				continue
			}
			key := e.mgr.ProviderKey(p.ID)
			if key == "" {
				continue
			}
			// 宽松匹配: 仅当已拉取的模型列表明确不含该模型时跳过
			if len(p.FetchedModels) > 0 && !containsModel(p.FetchedModels, primary.Model) {
				continue
			}
			add(Target{Provider: p, Model: primary.Model, Key: key})
		}
		return
	}
	for _, t := range s.Failover.Targets {
		if len(dec.Chain) >= max {
			break
		}
		if tt, ok := e.resolve(t, alias); ok {
			add(tt)
		}
	}
}

func containsModel(list []string, m string) bool {
	for _, it := range list {
		if it == m {
			return true
		}
	}
	return false
}
