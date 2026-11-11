package routing_test

import (
	"testing"

	"cluster-route/internal/config"
	"cluster-route/internal/rewrite"
)

func TestDecideLightweightKeepsLaterTargets(t *testing.T) {
	e, mgr := newTestEngine(t)
	a := addProvider(t, mgr, "lw-a", "sk-a")
	b := addProvider(t, mgr, "lw-b", "sk-b")
	setSettings(t, mgr, func(s *config.Settings) {
		s.Lightweight.Enabled = true
		s.Lightweight.Aliases = []string{"alias"}
		s.Lightweight.Targets = []config.Target{
			{ProviderID: a, Model: "up-a"},
			{ProviderID: b, Model: "up-b"},
		}
	})
	dec := e.Decide("alias", "anthropic", &rewrite.BodyInfo{Root: true, Model: "alias"})
	assertChain(t, dec, a+"/up-a", b+"/up-b")
}

func TestDecideSkipsProviderWithoutEndpoint(t *testing.T) {
	e, mgr := newTestEngine(t)
	onlyOpenAI := addProvider(t, mgr, "openai-only", "sk-o")
	patchProvider(t, mgr, onlyOpenAI, func(p *config.Provider) { p.AnthropicURL = "" })
	fallback := addProvider(t, mgr, "anth", "sk-a")
	setRoutes(t, mgr, config.Route{
		Alias: "claude-x", ProviderID: onlyOpenAI, UpstreamModel: "up-x", Enabled: true,
	})
	setSettings(t, mgr, func(s *config.Settings) { s.DefaultProviderID = fallback })
	dec := e.Decide("claude-x", "anthropic", &rewrite.BodyInfo{Root: true, Model: "claude-x"})
	assertChain(t, dec, fallback+"/claude-x")
}

func TestDecideEmptyModelUsesDefault(t *testing.T) {
	e, mgr := newTestEngine(t)
	id := addProvider(t, mgr, "def", "sk-d")
	setSettings(t, mgr, func(s *config.Settings) { s.DefaultProviderID = id })
	dec := e.Decide("", "openai", &rewrite.BodyInfo{})
	if dec.Channel != "default" || len(dec.Chain) != 1 || dec.Chain[0].Provider.ID != id {
		t.Fatalf("空模型应走默认透传, got %+v", dec)
	}
	if dec.Chain[0].Model != "" {
		t.Fatalf("透传不应编造模型名, got %q", dec.Chain[0].Model)
	}
}
