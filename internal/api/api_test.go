// 管理接口回归测试: 重点覆盖「保存设置不得抹掉已加密的路由密钥」这一数据级缺陷,
// 以及跨源/Host 防护与 SPA 回退的行为边界。
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"cluster-route/internal/archive"
	"cluster-route/internal/config"
	"cluster-route/internal/crypto"
	"cluster-route/internal/pricing"
	"cluster-route/internal/proxy"
	"cluster-route/internal/routing"
	"cluster-route/internal/stats"
	"cluster-route/internal/store"
)

// newTestDeps 在临时目录里装配一套真实依赖(不监听端口)。
func newTestDeps(t *testing.T) (Deps, *config.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	cr := crypto.New(dir)
	mgr, err := config.Load(dir, cr)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	pr, err := pricing.New(st)
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	rec := stats.New(st, pr, 16)
	arch := archive.New(mgr, st, dir+"/sessions")
	px := proxy.New(mgr, routing.New(mgr), rec, arch)
	return Deps{
		Mgr: mgr, Store: st, Pricer: pr, Recorder: rec,
		Proxy: px, Archive: arch, ArchiveDir: dir + "/sessions",
		WebFS: nil, Version: "test",
	}, mgr, dir
}

// reloadConfig 模拟下次启动: 用同一数据目录与同一 Cryptor 重新加载配置。
func reloadConfig(t *testing.T, dir string) *config.Manager {
	t.Helper()
	mgr, err := config.Load(dir, crypto.New(dir))
	if err != nil {
		t.Fatalf("重启加载配置失败: %v", err)
	}
	return mgr
}

// TestPutSettingsKeepsRouterKey 深拷贝脱敏后的设置回传(前端真实行为),
// 保存后密钥密文必须保持不变; 否则重启会重新生成 sk-cr 密钥, 客户端配置全部失效。
func TestPutSettingsKeepsRouterKey(t *testing.T) {
	deps, mgr, dir := newTestDeps(t)
	h := Router(deps)

	before := mgr.Cfg().Settings.RouterKeyEnc
	if before == "" {
		t.Fatal("初始配置应已生成 router_key_enc")
	}
	keyBefore := mgr.RouterKey()

	// 模拟前端: GET 拿到脱敏设置(router_key_enc 为空) → 修改字段 → PUT 整体回传
	rec := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	getReq.Host = "127.0.0.1:3721"
	h.ServeHTTP(rec, getReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/settings = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got config.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析设置失败: %v", err)
	}
	if got.RouterKeyEnc != "" {
		t.Fatalf("设置接口不应回传密钥密文, 实际=%q", got.RouterKeyEnc)
	}
	got.Inject.Enabled = true
	got.Inject.Text = "测试注入"
	body, _ := json.Marshal(got)

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/settings", bytes.NewReader(body))
	req.Host = "127.0.0.1:3721"
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /admin/settings = %d, body=%s", rec.Code, rec.Body.String())
	}

	// 断言 1: 内存中的密钥未变
	if mgr.RouterKey() != keyBefore {
		t.Fatalf("保存设置后运行期密钥被改变")
	}
	// 断言 2: 落盘的密文未被抹空
	if after := mgr.Cfg().Settings.RouterKeyEnc; after != before {
		t.Fatalf("router_key_enc 被改写: 前=%q 后=%q", before, after)
	}
	// 断言 3: 普通字段确实保存了
	if !mgr.Settings().Inject.Enabled || mgr.Settings().Inject.Text != "测试注入" {
		t.Fatalf("普通设置字段未保存: %+v", mgr.Settings().Inject)
	}

	// 断言 4: 重新加载配置(等价于下次启动)后密钥仍可解密且一致
	if got := reloadConfig(t, dir).RouterKey(); got != keyBefore {
		t.Fatalf("重启后密钥发生变化: %q -> %q", keyBefore, got)
	}
}

// TestSettingsSlicesAreNeverNull 前端对 lightweight.targets / failover.targets
// 直接 push/splice; 全新配置或删掉最后一个供应商后后端会存成 nil,
// JSON 序列化成 null 会让页面在「添加目标」时抛 TypeError。
func TestSettingsSlicesAreNeverNull(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	h := Router(deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	req.Host = "127.0.0.1:3721"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/settings = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, field := range []string{`"targets":null`, `"aliases":null`} {
		if strings.Contains(body, field) {
			t.Fatalf("设置接口返回了 %s, 前端会崩溃: %s", field, body)
		}
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析设置: %v", err)
	}
	for _, path := range []struct{ obj, key string }{
		{"lightweight", "targets"}, {"lightweight", "aliases"}, {"failover", "targets"},
	} {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(got[path.obj], &nested); err != nil {
			t.Fatalf("解析 %s: %v", path.obj, err)
		}
		if string(nested[path.key]) != "[]" {
			t.Fatalf("%s.%s = %s, want []", path.obj, path.key, nested[path.key])
		}
	}
}

func TestLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:3721", true},
		{"127.0.0.1", true},
		{"localhost:3721", true},
		{"LOCALHOST:3721", true},
		{"[::1]:3721", true},
		{"wails.localhost", true},
		{"evil.example:3721", false},
		{"evil.example", false},
		{"192.168.1.10:3721", false},
		{"", false},
	}
	for _, c := range cases {
		if got := loopbackHost(c.host); got != c.want {
			t.Errorf("loopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestGuardRejectsForeignHost 防 DNS rebinding: 攻击者域名解析到 127.0.0.1 时
// Origin 与 Host 相同, 单看 Origin 会放行, 必须由 Host 白名单兜住。
func TestGuardRejectsForeignHost(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	h := Router(deps)

	cases := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"同源回环放行", "127.0.0.1:3721", "http://127.0.0.1:3721", http.StatusOK},
		{"无 Origin 放行", "127.0.0.1:3721", "", http.StatusOK},
		{"Wails 窗口放行", "127.0.0.1:3721", "http://wails.localhost", http.StatusOK},
		{"DNS rebinding 拒绝", "evil.example:3721", "http://evil.example:3721", http.StatusForbidden},
		{"跨源拒绝", "127.0.0.1:3721", "http://evil.example", http.StatusForbidden},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: 状态码=%d, want %d, body=%s", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
}

// TestSPARejectsUnknownWritePath 客户端 BASE_URL 配错时不应收到 HTML 200。
func TestSPARejectsUnknownWritePath(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	deps.WebFS = fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
	h := Router(deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader([]byte("{}")))
	req.Host = "127.0.0.1:3721"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST 未知路径 = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("应返回 JSON 错误, 实际 Content-Type=%q body=%s", ct, rec.Body.String())
	}

	// GET 未知路径仍回退 SPA 首页
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Host = "127.0.0.1:3721"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "<html></html>" {
		t.Fatalf("GET 未知路径应回退 index.html, 实际 %d %q", rec.Code, rec.Body.String())
	}
}
