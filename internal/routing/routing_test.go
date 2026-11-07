// Package routing_test 覆盖 internal/routing 的决策管线, 包括:
// 轻量通道(别名精确命中与启发式)、子 agent 兜底、路由表、默认供应商、
// 无可用目标、供应商启用/密钥校验, 以及 manual / same_name 两种限额切换链
// 的追加顺序、去重、格式端点与已拉取模型过滤、MaxAttempts 上限。
package routing_test

import (
	"testing"

	"cluster-route/internal/config"
	"cluster-route/internal/crypto"
	"cluster-route/internal/rewrite"
	"cluster-route/internal/routing"
)

// newTestEngine 创建临时配置目录与决策引擎。
func newTestEngine(t *testing.T) (*routing.Engine, *config.Manager) {
	t.Helper()
	dir := t.TempDir()
	mgr, err := config.Load(dir, crypto.New(dir))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return routing.New(mgr), mgr
}

// addProvider 新增供应商并返回其生成的 ID; key 为空表示未配置密钥。
func addProvider(t *testing.T, mgr *config.Manager, name, key string) string {
	t.Helper()
	p := config.Provider{
		Name:         name,
		AnthropicURL: "https://" + name + ".example/anthropic",
		OpenAIURL:    "https://" + name + ".example/openai",
		Enabled:      true,
	}
	if err := mgr.UpsertProvider(p, key); err != nil {
		t.Fatalf("UpsertProvider(%s): %v", name, err)
	}
	for _, it := range mgr.Cfg().Providers {
		if it.Name == name {
			return it.ID
		}
	}
	t.Fatalf("供应商 %s 未出现在配置中", name)
	return ""
}

// patchProvider 复制当前供应商配置后修改并写回(保留密钥等系统字段)。
func patchProvider(t *testing.T, mgr *config.Manager, id string, fn func(*config.Provider)) {
	t.Helper()
	var cur config.Provider
	found := false
	for _, p := range mgr.Cfg().Providers {
		if p.ID == id {
			cur, found = p, true
			break
		}
	}
	if !found {
		t.Fatalf("供应商 %s 不存在", id)
	}
	fn(&cur)
	if err := mgr.UpsertProvider(cur, ""); err != nil {
		t.Fatalf("UpsertProvider(%s): %v", id, err)
	}
}

// setSettings 批量修改全局设置。
func setSettings(t *testing.T, mgr *config.Manager, fn func(*config.Settings)) {
	t.Helper()
	if err := mgr.UpdateSettings(fn); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
}

// setRoutes 整体替换路由表。
func setRoutes(t *testing.T, mgr *config.Manager, routes ...config.Route) {
	t.Helper()
	if err := mgr.SetRoutes(routes); err != nil {
		t.Fatalf("SetRoutes: %v", err)
	}
}

// chainIDs 返回链上每个目标的 "供应商ID/模型" 便于整链比较。
func chainIDs(dec routing.Decision) []string {
	out := make([]string, 0, len(dec.Chain))
	for _, t := range dec.Chain {
		out = append(out, t.Provider.ID+"/"+t.Model)
	}
	return out
}

func assertChain(t *testing.T, dec routing.Decision, want ...string) {
	t.Helper()
	got := chainIDs(dec)
	if len(got) != len(want) {
		t.Fatalf("链长度 = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("链[%d] = %q, want %q (整链 %v)", i, got[i], want[i], got)
		}
	}
}

// TestDecideLightweightWinsOverRoute 轻量通道别名命中优先于路由表。
func TestDecideLightweightWinsOverRoute(t *testing.T) {
	e, mgr := newTestEngine(t)
	lwID := addProvider(t, mgr, "lw", "sk-lw")
	mainID := addProvider(t, mgr, "main", "sk-main")
	setRoutes(t, mgr, config.Route{
		Alias: "claude-haiku-4-5", ProviderID: mainID, UpstreamModel: "up-main", Enabled: true,
	})
	setSettings(t, mgr, func(s *config.Settings) {
		s.Lightweight.Enabled = true
		s.Lightweight.Aliases = []string{"claude-haiku-4-5"}
		s.Lightweight.Targets = []config.Target{{ProviderID: lwID, Model: "up-lw"}}
	})

	dec := e.Decide("claude-haiku-4-5", "anthropic", &rewrite.BodyInfo{Root: true})
	if dec.Channel != routing.ChannelLightweight {
		t.Fatalf("Channel = %q, want %q", dec.Channel, routing.ChannelLightweight)
	}
	assertChain(t, dec, lwID+"/up-lw")
}

// TestDecideLightweightHeuristic 启发式命中: Root + 体积阈值 + 可选的工具要求。
func TestDecideLightweightHeuristic(t *testing.T) {
	cases := []struct {
		name         string
		requireNoTls bool
		hasTools     bool
		bodyBytes    int
		wantLW       bool
	}{
		{"小请求且无工具要求", false, false, 1000, true},
		{"小请求但有工具", false, true, 1000, true},
		{"要求无工具且无工具", true, false, 1000, true},
		{"要求无工具但含工具", true, true, 1000, false},
		{"体积超限", false, false, 40_000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, mgr := newTestEngine(t)
			lwID := addProvider(t, mgr, "lw", "sk-lw")
			mainID := addProvider(t, mgr, "main", "sk-main")
			setRoutes(t, mgr, config.Route{
				Alias: "req", ProviderID: mainID, UpstreamModel: "up-main", Enabled: true,
			})
			setSettings(t, mgr, func(s *config.Settings) {
				s.Lightweight.Enabled = true
				s.Lightweight.Targets = []config.Target{{ProviderID: lwID, Model: "up-lw"}}
				s.Lightweight.Heur.Enabled = true
				s.Lightweight.Heur.MaxBodyBytes = 32 * 1024
				s.Lightweight.Heur.RequireNoTools = tc.requireNoTls
			})
			dec := e.Decide("req", "anthropic", &rewrite.BodyInfo{
				Root: true, HasTools: tc.hasTools, BodyBytes: tc.bodyBytes,
			})
			wantChannel := routing.ChannelMain
			if tc.wantLW {
				wantChannel = routing.ChannelLightweight
			}
			if dec.Channel != wantChannel {
				t.Fatalf("Channel = %q, want %q", dec.Channel, wantChannel)
			}
		})
	}
}

// TestDecideLightweightHeuristicRequiresRoot 非 JSON 根对象不触发启发式。
func TestDecideLightweightHeuristicRequiresRoot(t *testing.T) {
	e, mgr := newTestEngine(t)
	lwID := addProvider(t, mgr, "lw", "sk-lw")
	mainID := addProvider(t, mgr, "main", "sk-main")
	setRoutes(t, mgr, config.Route{Alias: "req", ProviderID: mainID, UpstreamModel: "up-main", Enabled: true})
	setSettings(t, mgr, func(s *config.Settings) {
		s.Lightweight.Enabled = true
		s.Lightweight.Targets = []config.Target{{ProviderID: lwID, Model: "up-lw"}}
		s.Lightweight.Heur.Enabled = true
		s.Lightweight.Heur.MaxBodyBytes = 32 * 1024
	})
	dec := e.Decide("req", "anthropic", &rewrite.BodyInfo{Root: false, BodyBytes: 100})
	if dec.Channel != routing.ChannelMain {
		t.Fatalf("Channel = %q, want %q", dec.Channel, routing.ChannelMain)
	}
}

// TestDecideLightweightSkipsUnusableTarget 轻量通道首个目标不可用时用下一个。
func TestDecideLightweightSkipsUnusableTarget(t *testing.T) {
	e, mgr := newTestEngine(t)
	badID := addProvider(t, mgr, "bad", "sk-bad")
	goodID := addProvider(t, mgr, "good", "sk-good")
	patchProvider(t, mgr, badID, func(p *config.Provider) { p.Enabled = false })
	setSettings(t, mgr, func(s *config.Settings) {
		s.Lightweight.Enabled = true
		s.Lightweight.Aliases = []string{"alias"}
		s.Lightweight.Targets = []config.Target{
			{ProviderID: badID, Model: "up-bad"},
			{ProviderID: goodID, Model: "up-good"},
		}
	})
	dec := e.Decide("alias", "anthropic", &rewrite.BodyInfo{Root: true})
	if dec.Channel != routing.ChannelLightweight {
		t.Fatalf("Channel = %q, want %q", dec.Channel, routing.ChannelLightweight)
	}
	assertChain(t, dec, goodID+"/up-good")
}

// TestDecideSubagent 轻量未命中时, 子 agent 请求走 subagent 通道。
func TestDecideSubagent(t *testing.T) {
	e, mgr := newTestEngine(t)
	subID := addProvider(t, mgr, "sub", "sk-sub")
	mainID := addProvider(t, mgr, "main", "sk-main")
	setRoutes(t, mgr, config.Route{Alias: "req", ProviderID: mainID, UpstreamModel: "up-main", Enabled: true})
	setSettings(t, mgr, func(s *config.Settings) {
		s.Subagent.Enabled = true
		s.Subagent.Target = config.Target{ProviderID: subID, Model: "up-sub"}
	})

	dec := e.Decide("req", "anthropic", &rewrite.BodyInfo{Root: true, IsSubagent: true})
	if dec.Channel != routing.ChannelSubagent {
		t.Fatalf("Channel = %q, want %q", dec.Channel, routing.ChannelSubagent)
	}
	assertChain(t, dec, subID+"/up-sub")

	// 非子 agent 走路由表
	dec = e.Decide("req", "anthropic", &rewrite.BodyInfo{Root: true, IsSubagent: false})
	if dec.Channel != routing.ChannelMain {
		t.Fatalf("非子 agent 的 Channel = %q, want %q", dec.Channel, routing.ChannelMain)
	}
}

// TestDecideRouteHit 路由表命中: main 通道, 使用路由的上游模型名。
func TestDecideRouteHit(t *testing.T) {
	e, mgr := newTestEngine(t)
	id := addProvider(t, mgr, "p", "sk-p")
	setRoutes(t, mgr, config.Route{Alias: "alias-a", ProviderID: id, UpstreamModel: "real-a", Enabled: true})

	dec := e.Decide("alias-a", "anthropic", &rewrite.BodyInfo{Root: true})
	if dec.Channel != routing.ChannelMain {
		t.Fatalf("Channel = %q, want %q", dec.Channel, routing.ChannelMain)
	}
	assertChain(t, dec, id+"/real-a")
	if dec.Chain[0].Key != "sk-p" {
		t.Fatalf("Key = %q, want sk-p", dec.Chain[0].Key)
	}
}

// TestDecideRouteDisabledOrEmptyAlias 停用路由与空别名都不命中。
func TestDecideRouteDisabledOrEmptyAlias(t *testing.T) {
	e, mgr := newTestEngine(t)
	id := addProvider(t, mgr, "p", "sk-p")
	setRoutes(t, mgr,
		config.Route{Alias: "off", ProviderID: id, UpstreamModel: "x", Enabled: false},
	)
	setSettings(t, mgr, func(s *config.Settings) { s.DefaultProviderID = "" })

	if dec := e.Decide("off", "anthropic", &rewrite.BodyInfo{Root: true}); len(dec.Chain) != 0 {
		t.Fatalf("停用路由不应命中, got %v", chainIDs(dec))
	}
	if dec := e.Decide("", "anthropic", &rewrite.BodyInfo{Root: true}); len(dec.Chain) != 0 {
		t.Fatalf("空别名不应命中, got %v", chainIDs(dec))
	}
}

// TestDecideDefaultProvider 路由未命中时回落到默认供应商, 通道为 default。
func TestDecideDefaultProvider(t *testing.T) {
	e, mgr := newTestEngine(t)
	id := addProvider(t, mgr, "def", "sk-def")
	setSettings(t, mgr, func(s *config.Settings) { s.DefaultProviderID = id })

	dec := e.Decide("whatever", "openai", &rewrite.BodyInfo{Root: true})
	if dec.Channel != routing.ChannelDefault {
		t.Fatalf("Channel = %q, want %q", dec.Channel, routing.ChannelDefault)
	}
	assertChain(t, dec, id+"/whatever") // 默认供应商沿用请求别名作为模型
}

// TestDecideNoTarget 无路由/默认供应商或供应商不可用时链为空。
func TestDecideNoTarget(t *testing.T) {
	t.Run("无任何配置", func(t *testing.T) {
		e, _ := newTestEngine(t)
		dec := e.Decide("m", "anthropic", &rewrite.BodyInfo{Root: true})
		if len(dec.Chain) != 0 {
			t.Fatalf("Chain = %v, want empty", chainIDs(dec))
		}
	})
	t.Run("默认供应商已停用", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		id := addProvider(t, mgr, "def", "sk-def")
		patchProvider(t, mgr, id, func(p *config.Provider) { p.Enabled = false })
		setSettings(t, mgr, func(s *config.Settings) { s.DefaultProviderID = id })
		dec := e.Decide("m", "anthropic", &rewrite.BodyInfo{Root: true})
		if len(dec.Chain) != 0 {
			t.Fatalf("Chain = %v, want empty", chainIDs(dec))
		}
	})
	t.Run("默认供应商无密钥", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		id := addProvider(t, mgr, "def", "")
		setSettings(t, mgr, func(s *config.Settings) { s.DefaultProviderID = id })
		dec := e.Decide("m", "anthropic", &rewrite.BodyInfo{Root: true})
		if len(dec.Chain) != 0 {
			t.Fatalf("Chain = %v, want empty", chainIDs(dec))
		}
	})
	t.Run("供应商不存在", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		setSettings(t, mgr, func(s *config.Settings) { s.DefaultProviderID = "pv-missing" })
		dec := e.Decide("m", "anthropic", &rewrite.BodyInfo{Root: true})
		if len(dec.Chain) != 0 {
			t.Fatalf("Chain = %v, want empty", chainIDs(dec))
		}
	})
	t.Run("默认供应商模型名为空", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		id := addProvider(t, mgr, "def", "sk-def")
		setSettings(t, mgr, func(s *config.Settings) { s.DefaultProviderID = id })
		dec := e.Decide("", "anthropic", &rewrite.BodyInfo{Root: true})
		if len(dec.Chain) != 0 {
			t.Fatalf("空别名且路由无模型名时应无目标, got %v", chainIDs(dec))
		}
	})
}

// TestDecideFailoverDisabled 关闭限额切换时不追加目标。
func TestDecideFailoverDisabled(t *testing.T) {
	e, mgr := newTestEngine(t)
	p1 := addProvider(t, mgr, "p1", "sk-1")
	p2 := addProvider(t, mgr, "p2", "sk-2")
	setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: p1, UpstreamModel: "m1", Enabled: true})
	setSettings(t, mgr, func(s *config.Settings) {
		s.Failover.Enabled = false
		s.Failover.Mode = "manual"
		s.Failover.Targets = []config.Target{{ProviderID: p2, Model: "m2"}}
	})
	dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
	assertChain(t, dec, p1+"/m1")
}

// TestDecideFailoverManual manual 模式按配置顺序追加, 并受 MaxAttempts 上限约束
// (<=0 取 3; >5 截断为 5)。
func TestDecideFailoverManual(t *testing.T) {
	setup := func(t *testing.T) (*routing.Engine, *config.Manager, []string) {
		t.Helper()
		e, mgr := newTestEngine(t)
		ids := make([]string, 0, 8)
		ids = append(ids, addProvider(t, mgr, "p0", "sk-0"))
		for i := 1; i <= 7; i++ {
			ids = append(ids, addProvider(t, mgr, "p"+string(rune('0'+i)), "sk-"+string(rune('0'+i))))
		}
		// 主目标走路由表
		setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: ids[0], UpstreamModel: "m", Enabled: true})
		return e, mgr, ids
	}

	t.Run("按顺序追加", func(t *testing.T) {
		e, mgr, ids := setup(t)
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "manual"
			s.Failover.MaxAttempts = 4
			s.Failover.Targets = []config.Target{
				{ProviderID: ids[1], Model: "m"},
				{ProviderID: ids[2], Model: "m"},
			}
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec, ids[0]+"/m", ids[1]+"/m", ids[2]+"/m")
	})

	t.Run("MaxAttempts 非正时取默认 3", func(t *testing.T) {
		e, mgr, ids := setup(t)
		// 注意: UpdateSettings 会把 0 归一为 3, 因此用 -1 才能到达引擎的 <=0 分支。
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "manual"
			s.Failover.MaxAttempts = -1
			s.Failover.Targets = []config.Target{
				{ProviderID: ids[1], Model: "m"},
				{ProviderID: ids[2], Model: "m"},
				{ProviderID: ids[3], Model: "m"},
				{ProviderID: ids[4], Model: "m"},
			}
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec, ids[0]+"/m", ids[1]+"/m", ids[2]+"/m")
	})

	t.Run("MaxAttempts 超过 5 时截断", func(t *testing.T) {
		e, mgr, ids := setup(t)
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "manual"
			s.Failover.MaxAttempts = 9
			s.Failover.Targets = []config.Target{
				{ProviderID: ids[1], Model: "m"},
				{ProviderID: ids[2], Model: "m"},
				{ProviderID: ids[3], Model: "m"},
				{ProviderID: ids[4], Model: "m"},
				{ProviderID: ids[5], Model: "m"},
				{ProviderID: ids[6], Model: "m"},
			}
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		if len(dec.Chain) != 5 {
			t.Fatalf("链长度 = %d, want 5", len(dec.Chain))
		}
	})

	t.Run("重复目标去重", func(t *testing.T) {
		e, mgr, ids := setup(t)
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "manual"
			s.Failover.MaxAttempts = 5
			s.Failover.Targets = []config.Target{
				{ProviderID: ids[1], Model: "m"},
				{ProviderID: ids[1], Model: "m"},     // 完全重复
				{ProviderID: ids[0], Model: "m"},     // 与主目标重复
				{ProviderID: ids[1], Model: "other"}, // 同供应商不同模型不去重
			}
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec, ids[0]+"/m", ids[1]+"/m", ids[1]+"/other")
	})

	t.Run("不可用目标被跳过", func(t *testing.T) {
		e, mgr, ids := setup(t)
		patchProvider(t, mgr, ids[1], func(p *config.Provider) { p.Enabled = false })
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "manual"
			s.Failover.MaxAttempts = 5
			s.Failover.Targets = []config.Target{
				{ProviderID: ids[1], Model: "m"}, // 停用
				{ProviderID: ids[2], Model: "m"},
			}
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec, ids[0]+"/m", ids[2]+"/m")
	})
}

// TestDecideFailoverSameName same_name 模式的链构建规则。
func TestDecideFailoverSameName(t *testing.T) {
	t.Run("按 Priority 升序且跳过主目标自身", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		main := addProvider(t, mgr, "main", "sk-main")
		b := addProvider(t, mgr, "b", "sk-b")
		a := addProvider(t, mgr, "a", "sk-a")
		c := addProvider(t, mgr, "c", "sk-c")
		patchProvider(t, mgr, main, func(p *config.Provider) { p.Priority = 0 })
		patchProvider(t, mgr, b, func(p *config.Provider) { p.Priority = 30 })
		patchProvider(t, mgr, a, func(p *config.Provider) { p.Priority = 10 })
		patchProvider(t, mgr, c, func(p *config.Provider) { p.Priority = 20 })
		setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: main, UpstreamModel: "same-model", Enabled: true})
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "same_name"
			s.Failover.MaxAttempts = 5
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec,
			main+"/same-model", a+"/same-model", c+"/same-model", b+"/same-model")
	})

	t.Run("同优先级按名称排序", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		main := addProvider(t, mgr, "main", "sk-main")
		zz := addProvider(t, mgr, "zz", "sk-zz")
		aa := addProvider(t, mgr, "aa", "sk-aa")
		for _, id := range []string{main, zz, aa} {
			patchProvider(t, mgr, id, func(p *config.Provider) { p.Priority = 5 })
		}
		setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: main, UpstreamModel: "m", Enabled: true})
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "same_name"
			s.Failover.MaxAttempts = 5
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec, main+"/m", aa+"/m", zz+"/m")
	})

	t.Run("跳过缺少对应格式端点的供应商", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		main := addProvider(t, mgr, "main", "sk-main")
		noOpenAI := addProvider(t, mgr, "no-openai", "sk-1")
		noAnthropic := addProvider(t, mgr, "no-anthropic", "sk-2")
		ok := addProvider(t, mgr, "ok", "sk-3")
		patchProvider(t, mgr, noOpenAI, func(p *config.Provider) { p.OpenAIURL = "" })
		patchProvider(t, mgr, noAnthropic, func(p *config.Provider) { p.AnthropicURL = "" })
		setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: main, UpstreamModel: "m", Enabled: true})
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "same_name"
			s.Failover.MaxAttempts = 5
		})

		anth := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, anth, main+"/m", noOpenAI+"/m", ok+"/m")

		oi := e.Decide("a", "openai", &rewrite.BodyInfo{Root: true})
		assertChain(t, oi, main+"/m", noAnthropic+"/m", ok+"/m")
	})

	t.Run("跳过已拉取模型列表明确不含该模型的供应商", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		main := addProvider(t, mgr, "main", "sk-main")
		other := addProvider(t, mgr, "other", "sk-other")
		hasIt := addProvider(t, mgr, "has-it", "sk-has")
		patchProvider(t, mgr, main, func(p *config.Provider) { p.Priority = 0 })
		patchProvider(t, mgr, other, func(p *config.Provider) { p.Priority = 10 })
		patchProvider(t, mgr, hasIt, func(p *config.Provider) { p.Priority = 20 })
		if err := mgr.SetProviderModels(other, []string{"different-model"}); err != nil {
			t.Fatalf("SetProviderModels: %v", err)
		}
		if err := mgr.SetProviderModels(hasIt, []string{"m", "m2"}); err != nil {
			t.Fatalf("SetProviderModels: %v", err)
		}
		setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: main, UpstreamModel: "m", Enabled: true})
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "same_name"
			s.Failover.MaxAttempts = 5
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec, main+"/m", hasIt+"/m")
	})

	t.Run("空模型列表不参与过滤且链内去重", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		main := addProvider(t, mgr, "main", "sk-main")
		x := addProvider(t, mgr, "x", "sk-x")
		y := addProvider(t, mgr, "y", "sk-y")
		for i, id := range []string{main, x, y} {
			patchProvider(t, mgr, id, func(p *config.Provider) { p.Priority = i })
		}
		setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: main, UpstreamModel: "m", Enabled: true})
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "same_name"
			s.Failover.MaxAttempts = 5
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec, main+"/m", x+"/m", y+"/m")
	})

	t.Run("供应商停用或无密钥时跳过", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		main := addProvider(t, mgr, "main", "sk-main")
		off := addProvider(t, mgr, "off", "sk-off")
		noKey := addProvider(t, mgr, "nokey", "")
		good := addProvider(t, mgr, "good", "sk-good")
		patchProvider(t, mgr, main, func(p *config.Provider) { p.Priority = 0 })
		patchProvider(t, mgr, off, func(p *config.Provider) { p.Priority = 10; p.Enabled = false })
		patchProvider(t, mgr, noKey, func(p *config.Provider) { p.Priority = 20 })
		patchProvider(t, mgr, good, func(p *config.Provider) { p.Priority = 30 })
		setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: main, UpstreamModel: "m", Enabled: true})
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "same_name"
			s.Failover.MaxAttempts = 5
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec, main+"/m", good+"/m")
	})

	t.Run("MaxAttempts 上限同样生效", func(t *testing.T) {
		e, mgr := newTestEngine(t)
		main := addProvider(t, mgr, "main", "sk-main")
		p1 := addProvider(t, mgr, "p1", "sk-1")
		p2 := addProvider(t, mgr, "p2", "sk-2")
		p3 := addProvider(t, mgr, "p3", "sk-3")
		patchProvider(t, mgr, main, func(p *config.Provider) { p.Priority = 0 })
		patchProvider(t, mgr, p1, func(p *config.Provider) { p.Priority = 1 })
		patchProvider(t, mgr, p2, func(p *config.Provider) { p.Priority = 2 })
		patchProvider(t, mgr, p3, func(p *config.Provider) { p.Priority = 3 })
		setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: main, UpstreamModel: "m", Enabled: true})
		setSettings(t, mgr, func(s *config.Settings) {
			s.Failover.Enabled = true
			s.Failover.Mode = "same_name"
			s.Failover.MaxAttempts = 2
		})
		dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
		assertChain(t, dec, main+"/m", p1+"/m")
	})
}

// TestDecideRouteUnusableFallsBackToDefault 路由命中但目标不可用(resolve 失败)时,
// 实现直接回落到默认供应商, 不会再尝试后续同别名路由。此处固化该设计行为。
func TestDecideRouteUnusableFallsBackToDefault(t *testing.T) {
	e, mgr := newTestEngine(t)
	broken := addProvider(t, mgr, "broken", "sk-broken")
	def := addProvider(t, mgr, "def", "sk-def")
	patchProvider(t, mgr, broken, func(p *config.Provider) { p.Enabled = false })
	setRoutes(t, mgr,
		// 同别名两条: 首条不可用, 次条可用
		config.Route{Alias: "a", ProviderID: broken, UpstreamModel: "m1", Enabled: true},
		config.Route{Alias: "a", ProviderID: def, UpstreamModel: "m2", Enabled: true},
	)
	setSettings(t, mgr, func(s *config.Settings) { s.DefaultProviderID = def })

	dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
	if dec.Channel != routing.ChannelDefault {
		t.Fatalf("Channel = %q, want %q (不回落同别名次条路由)", dec.Channel, routing.ChannelDefault)
	}
	assertChain(t, dec, def+"/a") // 默认供应商沿用请求别名
}

// TestDecideFailoverResolveFailureSkipsTarget manual 模式下不可解析的目标被跳过, 链继续。
func TestDecideFailoverResolveFailureSkipsTarget(t *testing.T) {
	e, mgr := newTestEngine(t)
	main := addProvider(t, mgr, "main", "sk-main")
	missingKey := addProvider(t, mgr, "nokey", "")
	good := addProvider(t, mgr, "good", "sk-good")
	setRoutes(t, mgr, config.Route{Alias: "a", ProviderID: main, UpstreamModel: "m", Enabled: true})
	setSettings(t, mgr, func(s *config.Settings) {
		s.Failover.Enabled = true
		s.Failover.Mode = "manual"
		s.Failover.MaxAttempts = 5
		s.Failover.Targets = []config.Target{
			{ProviderID: missingKey, Model: "m"},
			{ProviderID: "pv-does-not-exist", Model: "m"},
			{ProviderID: good, Model: "m"},
		}
	})
	dec := e.Decide("a", "anthropic", &rewrite.BodyInfo{Root: true})
	assertChain(t, dec, main+"/m", good+"/m")
}
