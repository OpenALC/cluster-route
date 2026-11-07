// 配置管理层回归测试。
// 本模块是配置写入的唯一入口, 已暴露过两类事故:
//  1. 设置接口整体覆盖把 router_key_enc 抹成空串(见 internal/api 的同名测试);
//  2. 切片为 nil 时序列化成 null, 前端 push/splice 直接崩溃。
//
// 这里覆盖供应商增删改、密钥轮换、引用清理与落盘/重载语义。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cluster-route/internal/crypto"
)

func newManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := Load(dir, crypto.New(dir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return m, dir
}

func addProvider(t *testing.T, m *Manager, name string, key string) Provider {
	t.Helper()
	if err := m.UpsertProvider(Provider{
		Name: name, AnthropicURL: "https://" + name, OpenAIURL: "https://" + name, Enabled: true,
	}, key); err != nil {
		t.Fatalf("UpsertProvider(%s): %v", name, err)
	}
	for _, p := range m.Cfg().Providers {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("供应商 %s 未出现在配置中", name)
	return Provider{}
}

// TestBootstrapGeneratesRouterKeyAndDefaults 首次启动必须生成 sk-cr 密钥与合理默认值。
func TestBootstrapGeneratesRouterKeyAndDefaults(t *testing.T) {
	m, _ := newManager(t)
	if key := m.RouterKey(); !strings.HasPrefix(key, "sk-cr-") || len(key) != 70 {
		t.Fatalf("路由密钥格式异常: %q", key)
	}
	s := m.Settings()
	if s.Port != 3721 {
		t.Errorf("默认端口=%d, want 3721", s.Port)
	}
	if s.RetentionDays != 90 {
		t.Errorf("默认保留天数=%d, want 90", s.RetentionDays)
	}
	if s.Subagent.Marker != "You are Claude Code" {
		t.Errorf("默认主标记词=%q", s.Subagent.Marker)
	}
	if s.Failover.MaxAttempts != 3 || s.Failover.Mode != "manual" {
		t.Errorf("failover 默认值异常: %+v", s.Failover)
	}
	if s.Archive.KeepPerSession != 3 || s.Archive.MaxSnapshotMB != 20 || s.Archive.GlobalCapGB != 5 {
		t.Errorf("归档默认值异常: %+v", s.Archive)
	}
	// 切片必须为空数组而非 nil(JSON 里不能是 null)
	if s.Lightweight.Targets == nil || s.Lightweight.Aliases == nil || s.Failover.Targets == nil {
		t.Fatal("默认配置的切片为 nil, 序列化后是 null, 会让前端崩溃")
	}
}

// TestNilSlicesNormalizedOnLoad 老版本可能把切片存成 null, 加载后必须规范化为空数组。
func TestNilSlicesNormalizedOnLoad(t *testing.T) {
	dir := t.TempDir()
	raw := `{"providers":null,"routes":null,"settings":{"lightweight":{"targets":null,"aliases":null},"failover":{"targets":null}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("写入旧配置: %v", err)
	}
	m, err := Load(dir, crypto.New(dir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := m.Settings()
	if s.Lightweight.Targets == nil || s.Lightweight.Aliases == nil || s.Failover.Targets == nil {
		t.Fatal("加载后切片仍为 nil")
	}
}

// TestUpsertProviderPreservesEncryptedKeyAndSystemFields
// 前端编辑供应商时回传的是不含密钥的表单: 密钥密文与系统管理字段必须保留。
func TestUpsertProviderPreservesEncryptedKeyAndSystemFields(t *testing.T) {
	m, _ := newManager(t)
	p := addProvider(t, m, "A", "sk-secret-a")
	if got := m.ProviderKey(p.ID); got != "sk-secret-a" {
		t.Fatalf("解密后的 Key=%q", got)
	}

	// 模拟系统写入的管理字段
	if err := m.SetProviderTestResult(p.ID, &TestResult{OK: true, LatencyMs: 12, TestedAt: 111}); err != nil {
		t.Fatalf("SetProviderTestResult: %v", err)
	}
	if _, err := m.MergeFetchedModels(p.ID, []string{"m1", "m2"}); err != nil {
		t.Fatalf("MergeFetchedModels: %v", err)
	}
	before := m.Cfg().Providers[0]

	// 前端回传: 无 api_key, 无 fetched_models/last_test/created_at
	if err := m.UpsertProvider(Provider{
		ID: p.ID, Name: "A-renamed", AnthropicURL: "https://a2", Enabled: false,
	}, ""); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	after := m.Cfg().Providers[0]

	if after.Name != "A-renamed" || after.Enabled {
		t.Fatalf("用户可编辑字段未生效: %+v", after)
	}
	if after.APIKeyEnc != before.APIKeyEnc {
		t.Fatal("密钥密文被表单覆盖")
	}
	if got := m.ProviderKey(p.ID); got != "sk-secret-a" {
		t.Fatalf("运行期密钥被清除: %q", got)
	}
	if len(after.FetchedModels) != 2 || after.FetchedAt == 0 {
		t.Fatalf("模型池被表单覆盖: %+v", after)
	}
	if after.LastTest == nil || after.LastTest.LatencyMs != 12 {
		t.Fatalf("连通性测试结果被覆盖: %+v", after.LastTest)
	}
	if after.CreatedAt != before.CreatedAt {
		t.Fatal("创建时间被覆盖")
	}
}

// TestUpsertProviderWithNewKey 显式传入新密钥时必须替换并更新内存明文。
func TestUpsertProviderWithNewKey(t *testing.T) {
	m, _ := newManager(t)
	p := addProvider(t, m, "A", "sk-old")
	if err := m.UpsertProvider(Provider{ID: p.ID, Name: "A", Enabled: true}, "sk-new"); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if got := m.ProviderKey(p.ID); got != "sk-new" {
		t.Fatalf("运行期密钥=%q, want sk-new", got)
	}
	if m.Cfg().Providers[0].APIKeyEnc == p.APIKeyEnc {
		t.Fatal("密文未更新")
	}
}

// TestDeleteProviderCleansAllReferences 删除供应商必须同时清除所有引用,
// 否则路由/通道会指向不存在的供应商, 转发时静默失效。
func TestDeleteProviderCleansAllReferences(t *testing.T) {
	m, _ := newManager(t)
	a := addProvider(t, m, "A", "sk-a")
	b := addProvider(t, m, "B", "sk-b")

	if err := m.SetRoutes([]Route{
		{Alias: "keep", ProviderID: b.ID, UpstreamModel: "m", Enabled: true},
		{Alias: "drop", ProviderID: a.ID, UpstreamModel: "m", Enabled: true},
	}); err != nil {
		t.Fatalf("SetRoutes: %v", err)
	}
	if err := m.UpdateSettings(func(s *Settings) {
		s.DefaultProviderID = a.ID
		s.Lightweight.Targets = []Target{{ProviderID: a.ID, Model: "x"}, {ProviderID: b.ID, Model: "y"}}
		s.Failover.Targets = []Target{{ProviderID: a.ID, Model: "x"}}
		s.Subagent.Target = Target{ProviderID: a.ID, Model: "x"}
	}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	if err := m.DeleteProvider(a.ID); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}

	cfg := m.Cfg()
	if len(cfg.Providers) != 1 || cfg.Providers[0].ID != b.ID {
		t.Fatalf("供应商未被删除: %+v", cfg.Providers)
	}
	if m.ProviderKey(a.ID) != "" {
		t.Fatal("内存中的密钥未被清除")
	}
	if cfg.Settings.DefaultProviderID != "" {
		t.Fatal("默认供应商引用未清除")
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].ProviderID != b.ID {
		t.Fatalf("路由引用未清除: %+v", cfg.Routes)
	}
	if len(cfg.Settings.Lightweight.Targets) != 1 || cfg.Settings.Lightweight.Targets[0].ProviderID != b.ID {
		t.Fatalf("轻量通道引用未清除: %+v", cfg.Settings.Lightweight.Targets)
	}
	if len(cfg.Settings.Failover.Targets) != 0 {
		t.Fatalf("限额切换引用未清除: %+v", cfg.Settings.Failover.Targets)
	}
	if cfg.Settings.Subagent.Target.ProviderID != "" {
		t.Fatalf("子agent 引用未清除: %+v", cfg.Settings.Subagent.Target)
	}
}

// TestRotateRouterKeyPersists 轮换后内存与磁盘都必须换新, 且重启仍能解密。
func TestRotateRouterKeyPersists(t *testing.T) {
	m, dir := newManager(t)
	old := m.RouterKey()
	rotated, err := m.RotateRouterKey()
	if err != nil {
		t.Fatalf("RotateRouterKey: %v", err)
	}
	if rotated == old || !strings.HasPrefix(rotated, "sk-cr-") {
		t.Fatalf("轮换后的密钥异常: %q", rotated)
	}
	if m.RouterKey() != rotated {
		t.Fatal("内存密钥未更新")
	}
	m2, err := Load(dir, crypto.New(dir))
	if err != nil {
		t.Fatalf("重启加载: %v", err)
	}
	if m2.RouterKey() != rotated {
		t.Fatalf("重启后密钥不一致: %q != %q", m2.RouterKey(), rotated)
	}
}

// TestSaveReloadRoundTrip 全部可持久化状态必须完整往返。
func TestSaveReloadRoundTrip(t *testing.T) {
	m, dir := newManager(t)
	p := addProvider(t, m, "A", "sk-a")
	if err := m.SetRoutes([]Route{{Alias: "x", ProviderID: p.ID, UpstreamModel: "m1", Enabled: true}}); err != nil {
		t.Fatalf("SetRoutes: %v", err)
	}
	if err := m.UpdateSettings(func(s *Settings) {
		s.Inject.Enabled = true
		s.Inject.Text = "守则"
		s.Lightweight.Aliases = []string{"cr-light"}
	}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	m2, err := Load(dir, crypto.New(dir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg := m2.Cfg()
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "A" {
		t.Fatalf("供应商未往返: %+v", cfg.Providers)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].UpstreamModel != "m1" {
		t.Fatalf("路由未往返: %+v", cfg.Routes)
	}
	if !cfg.Settings.Inject.Enabled || cfg.Settings.Inject.Text != "守则" {
		t.Fatalf("注入设置未往返: %+v", cfg.Settings.Inject)
	}
	if len(cfg.Settings.Lightweight.Aliases) != 1 || cfg.Settings.Lightweight.Aliases[0] != "cr-light" {
		t.Fatalf("轻量别名未往返: %+v", cfg.Settings.Lightweight.Aliases)
	}
	if got := m2.ProviderKey(p.ID); got != "sk-a" {
		t.Fatalf("密钥未往返: %q", got)
	}
}

// TestConfigFileHasNoPlaintextKeys 落盘文件里绝不能出现明文密钥。
func TestConfigFileHasNoPlaintextKeys(t *testing.T) {
	m, dir := newManager(t)
	addProvider(t, m, "A", "sk-plaintext-should-not-leak")
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("读取配置: %v", err)
	}
	if strings.Contains(string(raw), "sk-plaintext-should-not-leak") {
		t.Fatal("明文密钥被写入 config.json")
	}
	if strings.Contains(string(raw), m.RouterKey()) {
		t.Fatal("路由密钥明文被写入 config.json")
	}
	var parsed Config
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("配置不是合法 JSON: %v", err)
	}
	if !strings.HasPrefix(parsed.Providers[0].APIKeyEnc, "enc1:") {
		t.Fatalf("密钥未加密落盘: %q", parsed.Providers[0].APIKeyEnc)
	}
}

// TestBackupRotationKeepsTen 每次启动滚动备份, 只保留最近 10 份。
func TestBackupRotationKeepsTen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"settings":{}}`), 0o600); err != nil {
		t.Fatalf("写入配置: %v", err)
	}
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	// 预置 14 份旧备份, 时间戳递增
	for i := 0; i < 14; i++ {
		name := filepath.Join(backupDir, "config-20260101-0000"+string(rune('0'+i/10))+string(rune('0'+i%10))+".json")
		if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
			t.Fatalf("写入备份: %v", err)
		}
	}
	backupConfig(path)

	entries, err := filepath.Glob(filepath.Join(backupDir, "config-*.json"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(entries) > 10 {
		t.Fatalf("备份数量=%d, 超过 10 份上限", len(entries))
	}
	// 被删掉的应是最旧的
	if _, err := os.Stat(filepath.Join(backupDir, "config-20260101-000000.json")); err == nil {
		t.Fatal("最旧的备份未被清理")
	}
}
