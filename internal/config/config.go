// Package config 定义 cluster-router 的持久化配置(供应商/路由/通道策略)
// 并提供线程安全的读写管理。API Key 以密文形式落盘, 明文仅驻内存。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cluster-router/internal/crypto"
)

// TestResult 最近一次连通性测试结果(持久化, 供列表页展示)。
type TestResult struct {
	OK        bool     `json:"ok"`
	LatencyMs int64    `json:"latency_ms"`
	Errors    []string `json:"errors,omitempty"`
	TestedAt  int64    `json:"tested_at"`
}

// Provider 上游供应商。anthropic_url 与 openai_url 分别是两种协议的
// BASE_URL, 只需填实际支持的格式(通常至少填一个)。
type Provider struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	AnthropicURL  string      `json:"anthropic_url,omitempty"`
	OpenAIURL     string      `json:"openai_url,omitempty"`
	APIKeyEnc     string      `json:"api_key_enc,omitempty"`
	TimeoutSec    int         `json:"timeout_sec,omitempty"` // 响应头超时, 0=默认 600s
	Priority      int         `json:"priority"`              // 同名切换与展示排序, 小者优先
	Enabled       bool        `json:"enabled"`
	Note          string      `json:"note,omitempty"`
	FetchedModels []string    `json:"fetched_models,omitempty"` // 模型池: 拉取合并 + 手动管理
	FetchedAt     int64       `json:"fetched_at,omitempty"`     // 最近一次拉取时间
	LastTest      *TestResult `json:"last_test,omitempty"`      // 最近一次连通性测试
	CreatedAt     int64       `json:"created_at"`
}

// Route 模型路由表: 客户端可见的模型别名 → 供应商 + 上游真实模型名。
type Route struct {
	Alias         string `json:"alias"`
	ProviderID    string `json:"provider_id"`
	UpstreamModel string `json:"upstream_model"`
	Enabled       bool   `json:"enabled"`
}

// Target 一次转发目标。
type Target struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"` // 上游模型名; 同名模式下忽略
}

// Lightweight 轻量通道: 命中触发条件的请求按 targets 优先级依次转发。
type Lightweight struct {
	Enabled bool     `json:"enabled"`
	Targets []Target `json:"targets"`
	Aliases []string `json:"aliases"` // 触发别名(客户端把小模型槽位配成这些名字)
	Heur    struct {
		Enabled        bool `json:"enabled"`
		MaxBodyBytes   int  `json:"max_body_bytes"`    // 0=默认 32KB
		RequireNoTools bool `json:"require_no_tools"`  // 无工具定义才算轻量
	} `json:"heuristic"`
}

// Subagent 子 agent 兜底通道: system 提示词不含主标记词时转发到指定目标。
type Subagent struct {
	Enabled bool   `json:"enabled"`
	Marker  string `json:"marker"` // 主 agent 标记词, 默认 "You are Claude Code"
	Target  Target `json:"target"`
}

// Failover 限额切换策略。
type Failover struct {
	Enabled     bool     `json:"enabled"`
	Mode        string   `json:"mode"` // "manual" | "same_name"
	Targets     []Target `json:"targets"` // manual 模式的有序列表
	On5xx       bool     `json:"on_5xx"`  // 服务端错误也切换
	MaxAttempts int      `json:"max_attempts"`
}

// Inject 全局提示词注入。
type Inject struct {
	Enabled bool   `json:"enabled"`
	Text    string `json:"text"`
}

// Archive 会话上下文留档策略。
type Archive struct {
	Enabled        bool `json:"enabled"`
	KeepPerSession int  `json:"keep_per_session"` // 每会话保留快照数, 0=默认 3
	MaxSnapshotMB  int  `json:"max_snapshot_mb"`  // 单份上限, 0=默认 20MB
	GlobalCapGB    int  `json:"global_cap_gb"`    // 总占用上限, 0=默认 5GB
}

// Settings 全局设置。
type Settings struct {
	Port              int    `json:"port"`
	RouterKeyEnc      string `json:"router_key_enc"`
	DefaultProviderID string `json:"default_provider_id"`
	RetentionDays     int    `json:"retention_days"`      // 请求明细保留天数, 0=默认 90
	InjectOpenAIUsage bool   `json:"inject_openai_usage"` // OpenAI 流式注入 include_usage
	// UnloadOnMinimise 桌面版: 最小化到后台时卸载前端页面, 释放渲染内存(还原时自动恢复)。
	UnloadOnMinimise bool        `json:"unload_on_minimise"`
	Lightweight      Lightweight `json:"lightweight"`
	Subagent         Subagent    `json:"subagent"`
	Failover         Failover    `json:"failover"`
	Inject           Inject      `json:"inject"`
	Archive          Archive     `json:"archive"`
}

// Config 配置根对象。
type Config struct {
	Providers []Provider `json:"providers"`
	Routes    []Route    `json:"routes"`
	Settings  Settings   `json:"settings"`
}

// Manager 线程安全的配置管理器, 持有解密后的密钥缓存(仅内存)。
type Manager struct {
	mu        sync.RWMutex
	path      string
	cryptor   crypto.Cryptor
	cfg       Config
	keys      map[string]string // providerID -> 明文 key
	routerKey string
}

// Load 读取配置文件; 不存在时创建默认配置并生成 sk-cr 密钥。
func Load(dataDir string, cr crypto.Cryptor) (*Manager, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "config.json")
	m := &Manager{path: path, cryptor: cr, keys: map[string]string{}}
	backupConfig(path)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := m.bootstrap(); err != nil {
			return nil, err
		}
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m.cfg); err != nil {
		return nil, fmt.Errorf("config.json 解析失败: %w", err)
	}
	m.ensureDefaults()
	if m.cfg.Settings.RouterKeyEnc == "" {
		if err := m.rotateRouterKeyLocked(); err != nil {
			return nil, err
		}
	} else {
		k, err := crypto.Open(cr, m.cfg.Settings.RouterKeyEnc)
		if err != nil {
			return nil, fmt.Errorf("路由密钥解密失败(是否跨机拷贝了 data 目录?): %w", err)
		}
		m.routerKey = k
	}
	for _, p := range m.cfg.Providers {
		k, err := crypto.Open(cr, p.APIKeyEnc)
		if err != nil {
			// 单个 key 损坏不阻断启动, 记为空并提示
			fmt.Printf("[config] 警告: 供应商 %s 的 API Key 解密失败: %v\n", p.Name, err)
			continue
		}
		m.keys[p.ID] = k
	}
	return m, nil
}

// backupConfig 每次启动前滚动备份 config.json 到 data/backups/, 保留最近 10 份。
func backupConfig(path string) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return
	}
	backupDir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return
	}
	name := filepath.Join(backupDir, "config-"+time.Now().Format("20060102-150405")+".json")
	if err := os.WriteFile(name, b, 0o600); err != nil {
		return
	}
	olds, err := filepath.Glob(filepath.Join(backupDir, "config-*.json"))
	if err != nil || len(olds) <= 10 {
		return
	}
	sort.Strings(olds)
	for _, old := range olds[:len(olds)-10] {
		_ = os.Remove(old)
	}
}

func (m *Manager) bootstrap() error {
	// 布尔型默认值只在首次初始化时设置, 此后完全尊重用户配置
	m.cfg.Settings.InjectOpenAIUsage = true
	m.cfg.Settings.Archive.Enabled = true
	m.ensureDefaults()
	if err := m.rotateRouterKeyLocked(); err != nil {
		return err
	}
	return m.Save()
}

func (m *Manager) rotateRouterKeyLocked() error {
	k, err := NewRouterKey()
	if err != nil {
		return err
	}
	enc, err := crypto.Seal(m.cryptor, k)
	if err != nil {
		return err
	}
	m.cfg.Settings.RouterKeyEnc = enc
	m.routerKey = k
	return nil
}

func (m *Manager) ensureDefaults() {
	s := &m.cfg.Settings
	if s.Port == 0 {
		s.Port = 3721
	}
	if s.RetentionDays == 0 {
		s.RetentionDays = 90
	}
	if s.Subagent.Marker == "" {
		s.Subagent.Marker = "You are Claude Code"
	}
	if s.Failover.MaxAttempts == 0 {
		s.Failover.MaxAttempts = 3
	}
	if s.Failover.Mode == "" {
		s.Failover.Mode = "manual"
	}
	if s.Lightweight.Heur.MaxBodyBytes == 0 {
		s.Lightweight.Heur.MaxBodyBytes = 32 * 1024
	}
	if s.Archive.KeepPerSession == 0 {
		s.Archive.KeepPerSession = 3
	}
	if s.Archive.MaxSnapshotMB == 0 {
		s.Archive.MaxSnapshotMB = 20
	}
	if s.Archive.GlobalCapGB == 0 {
		s.Archive.GlobalCapGB = 5
	}
	for i := range m.cfg.Providers {
		if m.cfg.Providers[i].TimeoutSec == 0 {
			m.cfg.Providers[i].TimeoutSec = 600
		}
	}
}

// Save 原子写入配置文件(临时文件 + rename)。
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

func (m *Manager) saveLocked() error {
	b, err := json.MarshalIndent(&m.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// Cfg 返回配置快照(值拷贝; 切片为浅拷贝, 调用方不得修改)。
func (m *Manager) Cfg() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Settings 返回设置快照。
func (m *Manager) Settings() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Settings
}

// RouterKey 返回路由器自签密钥明文。
func (m *Manager) RouterKey() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.routerKey
}

// RotateRouterKey 重新生成路由密钥并落盘。
func (m *Manager) RotateRouterKey() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.rotateRouterKeyLocked(); err != nil {
		return "", err
	}
	return m.routerKey, m.saveLocked()
}

// ProviderKey 返回供应商解密后的 API Key 明文。
func (m *Manager) ProviderKey(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.keys[id]
}

// NewRouterKey 生成 sk-cr 前缀的 256bit 随机密钥。
func NewRouterKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-cr-" + hex.EncodeToString(b), nil
}

// UpsertProvider 新增或更新供应商; plainKey 非空时更新密钥。
func (m *Manager) UpsertProvider(p Provider, plainKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.ID == "" {
		p.ID = newID("pv")
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = time.Now().Unix()
	}
	if p.TimeoutSec == 0 {
		p.TimeoutSec = 600
	}
	if plainKey != "" {
		enc, err := crypto.Seal(m.cryptor, plainKey)
		if err != nil {
			return err
		}
		p.APIKeyEnc = enc
		m.keys[p.ID] = plainKey
	} else if old := m.findProviderLocked(p.ID); old != nil {
		p.APIKeyEnc = old.APIKeyEnc
	}
	// 系统管理字段以存量为准, 不被前端表单覆盖
	if old := m.findProviderLocked(p.ID); old != nil {
		p.FetchedModels = old.FetchedModels
		p.FetchedAt = old.FetchedAt
		p.LastTest = old.LastTest
		p.CreatedAt = old.CreatedAt
	}
	found := false
	for i := range m.cfg.Providers {
		if m.cfg.Providers[i].ID == p.ID {
			m.cfg.Providers[i] = p
			found = true
			break
		}
	}
	if !found {
		m.cfg.Providers = append(m.cfg.Providers, p)
	}
	m.ensureDefaults()
	return m.saveLocked()
}

// DeleteProvider 删除供应商及其关联路由/通道引用。
func (m *Manager) DeleteProvider(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.cfg.Providers[:0]
	for _, p := range m.cfg.Providers {
		if p.ID != id {
			out = append(out, p)
		}
	}
	m.cfg.Providers = out
	delete(m.keys, id)
	if m.cfg.Settings.DefaultProviderID == id {
		m.cfg.Settings.DefaultProviderID = ""
	}
	m.cfg.Settings.Lightweight.Targets = dropTargets(m.cfg.Settings.Lightweight.Targets, id)
	m.cfg.Settings.Failover.Targets = dropTargets(m.cfg.Settings.Failover.Targets, id)
	if m.cfg.Settings.Subagent.Target.ProviderID == id {
		m.cfg.Settings.Subagent.Target = Target{}
	}
	m.cfg.Routes = filterRoutes(m.cfg.Routes, func(r Route) bool { return r.ProviderID != id })
	return m.saveLocked()
}

// SetFetchedModels 记录供应商最近一次拉取的模型列表(整体替换, 供旧逻辑兼容)。
func (m *Manager) SetFetchedModels(id string, models []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Providers {
		if m.cfg.Providers[i].ID == id {
			m.cfg.Providers[i].FetchedModels = models
			m.cfg.Providers[i].FetchedAt = time.Now().Unix()
			return m.saveLocked()
		}
	}
	return fmt.Errorf("provider %s not found", id)
}

// MergeFetchedModels 将上游拉取到的模型合并进模型池(并集, 保留手动整理结果),
// 返回合并后的模型池。
func (m *Manager) MergeFetchedModels(id string, upstream []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Providers {
		if m.cfg.Providers[i].ID == id {
			seen := map[string]bool{}
			pool := make([]string, 0, len(m.cfg.Providers[i].FetchedModels)+len(upstream))
			for _, list := range [][]string{m.cfg.Providers[i].FetchedModels, upstream} {
				for _, mo := range list {
					mo = strings.TrimSpace(mo)
					if mo != "" && !seen[mo] {
						seen[mo] = true
						pool = append(pool, mo)
					}
				}
			}
			m.cfg.Providers[i].FetchedModels = pool
			m.cfg.Providers[i].FetchedAt = time.Now().Unix()
			return pool, m.saveLocked()
		}
	}
	return nil, fmt.Errorf("provider %s not found", id)
}

// SetProviderModels 手动管理模型池(整体替换, 去重去空)。
func (m *Manager) SetProviderModels(id string, models []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	pool := make([]string, 0, len(models))
	for _, mo := range models {
		mo = strings.TrimSpace(mo)
		if mo != "" && !seen[mo] {
			seen[mo] = true
			pool = append(pool, mo)
		}
	}
	for i := range m.cfg.Providers {
		if m.cfg.Providers[i].ID == id {
			m.cfg.Providers[i].FetchedModels = pool
			return m.saveLocked()
		}
	}
	return fmt.Errorf("provider %s not found", id)
}

// SetProviderTestResult 持久化供应商连通性测试结果。
func (m *Manager) SetProviderTestResult(id string, res *TestResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Providers {
		if m.cfg.Providers[i].ID == id {
			m.cfg.Providers[i].LastTest = res
			return m.saveLocked()
		}
	}
	return fmt.Errorf("provider %s not found", id)
}

// SetRoutes 整体替换路由表。
func (m *Manager) SetRoutes(routes []Route) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Routes = routes
	return m.saveLocked()
}

// UpdateSettings 以函数式方式修改设置并落盘。
func (m *Manager) UpdateSettings(fn func(*Settings)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.cfg.Settings)
	m.ensureDefaults()
	return m.saveLocked()
}

func (m *Manager) findProviderLocked(id string) *Provider {
	for i := range m.cfg.Providers {
		if m.cfg.Providers[i].ID == id {
			return &m.cfg.Providers[i]
		}
	}
	return nil
}

func dropTargets(ts []Target, pid string) []Target {
	out := ts[:0]
	for _, t := range ts {
		if t.ProviderID != pid {
			out = append(out, t)
		}
	}
	return out
}

func filterRoutes(rs []Route, keep func(Route) bool) []Route {
	out := rs[:0]
	for _, r := range rs {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

var idCounter int64

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(b))
}
