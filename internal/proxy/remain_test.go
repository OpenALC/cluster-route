package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cluster-route/internal/config"
	"cluster-route/internal/rewrite"
	"cluster-route/internal/routing"
)

func TestUpstreamKeepsAttributionAndAzureKey(t *testing.T) {
	var referer, title, apiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer = r.Header.Get("HTTP-Referer")
		title = r.Header.Get("X-Title")
		apiKey = r.Header.Get("api-key")
		if r.Header.Get("Authorization") == "Bearer sk-client" {
			t.Error("客户端路由密钥不应透传到上游")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	h, mgr, _ := newTestHandler(t)
	addTestProvider(t, mgr, srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mgr.RouterKey())
	req.Header.Set("HTTP-Referer", "https://app.example")
	req.Header.Set("X-Title", "cluster-route")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if referer != "https://app.example" || title != "cluster-route" {
		t.Fatalf("归因头 referer=%q title=%q", referer, title)
	}
	if apiKey == "" || apiKey == mgr.RouterKey() {
		t.Fatalf("上游 api-key=%q, 应为供应商密钥", apiKey)
	}
}

func TestModelsHideUnusableProviders(t *testing.T) {
	h, mgr, _ := newTestHandler(t)
	if err := mgr.UpsertProvider(config.Provider{
		ID: "pv-off", Name: "off", OpenAIURL: "https://off.example", Enabled: false, TimeoutSec: 30,
	}, "sk-off"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetProviderModels("pv-off", []string{"hidden-model"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpsertProvider(config.Provider{
		ID: "pv-nokey", Name: "nokey", OpenAIURL: "https://nokey.example", Enabled: true, TimeoutSec: 30,
	}, ""); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetProviderModels("pv-nokey", []string{"nokey-model"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetRoutes([]config.Route{{
		Alias: "dead-alias", ProviderID: "pv-off", UpstreamModel: "hidden-model", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+mgr.RouterKey())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, bad := range []string{"hidden-model", "nokey-model", "dead-alias"} {
		if strings.Contains(body, bad) {
			t.Fatalf("不可用模型 %s 出现在列表: %s", bad, body)
		}
	}
}

func TestRecordUsesChosenTargetNotFirstSameName(t *testing.T) {
	h, _, st := newTestHandler(t)
	dec := routing.Decision{Channel: routing.ChannelMain, Chain: []routing.Target{
		{Provider: config.Provider{ID: "pv-a", Name: "same"}, Model: "model-a"},
		{Provider: config.Provider{ID: "pv-b", Name: "same"}, Model: "model-b"},
	}}
	used := dec.Chain[1]
	h.record(nil, &rewrite.BodyInfo{Model: "alias", Root: true}, dec, config.Settings{}, nil,
		&used, "openai", 200, Usage{Input: 1}, true, 1, 2, "", time.Unix(1_700_000_000, 0))
	rec := waitRecord(t, st)
	if rec.ProviderID != "pv-b" || rec.Upstream != "model-b" {
		t.Fatalf("归属到链中同名的第一个目标: %+v", rec)
	}
}
