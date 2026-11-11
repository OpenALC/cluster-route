// 管理接口处理器: 统计查询、供应商/路由/设置/定价/会话 CRUD。
package api

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cluster-route/internal/archive"
	"cluster-route/internal/config"
	"cluster-route/internal/store"
)

type handlers struct{ Deps }

// ---- 基础 ----

func (h *handlers) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"version": h.Version})
}

func (h *handlers) connection(w http.ResponseWriter, r *http.Request) {
	port := h.ListenPort
	if port == 0 {
		port = h.Mgr.Settings().Port
	}
	writeJSON(w, 200, map[string]any{
		"base_url":   "http://127.0.0.1:" + strconv.Itoa(port),
		"router_key": h.Mgr.RouterKey(),
		"port":       port,
	})
}

func (h *handlers) rotateKey(w http.ResponseWriter, r *http.Request) {
	key, err := h.Mgr.RotateRouterKey()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"router_key": key})
}

// ---- 统计 ----

func rangeStart(q string) int64 {
	now := time.Now().Unix()
	switch q {
	case "1h":
		return now - 3600
	case "24h":
		return now - 86400
	case "7d":
		return now - 7*86400
	case "30d":
		return now - 30*86400
	default:
		return 0 // all
	}
}

func (h *handlers) overview(w http.ResponseWriter, r *http.Request) {
	rangeKey := r.URL.Query().Get("range")
	start := rangeStart(rangeKey)
	channel := r.URL.Query().Get("channel")
	useDaily := channel == "" && (rangeKey == "" || rangeKey == "all" || rangeKey == "30d")
	var totals store.Totals
	var err error
	var byProvider, byModel []store.GroupAgg
	if useDaily {
		totals, err = h.Store.TotalsDaily(start)
		byProvider = h.dailyGroups("provider_id", start)
		byModel = h.dailyGroups("model", start)
	} else {
		totals, err = h.Store.TotalsByRange(start, channel)
		byProvider = must(h, w, "provider_name", start, channel)
		byModel = must(h, w, "model", start, channel)
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var dropped int64
	if h.Archive != nil {
		dropped = h.Archive.Dropped()
	}
	writeJSON(w, 200, map[string]any{
		"totals":          totals,
		"by_provider":     byProvider,
		"by_model":        byModel,
		"by_channel":      must(h, w, "channel", start, channel),
		"stats_dropped":   h.Recorder.Dropped(),
		"archive_dropped": dropped,
	})
}

func (h *handlers) dailyGroups(field string, start int64) []store.GroupAgg {
	g, err := h.Store.GroupDaily(field, start)
	if err != nil || g == nil {
		return []store.GroupAgg{}
	}
	if field != "provider_id" {
		return g
	}
	names := map[string]string{}
	for _, p := range h.Mgr.Cfg().Providers {
		names[p.ID] = p.Name
	}
	for i := range g {
		if n := names[g[i].Key]; n != "" {
			g[i].Key = n
		}
	}
	return g
}

func must(h *handlers, w http.ResponseWriter, by string, start int64, channel string) []store.GroupAgg {
	g, err := h.Store.GroupBy(start, by, channel)
	if err != nil {
		log.Printf("[admin] 聚合查询失败(%s): %v", by, err)
		return []store.GroupAgg{}
	}
	if g == nil {
		return []store.GroupAgg{}
	}
	return g
}

func (h *handlers) series(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start := rangeStart(q.Get("range"))
	bucket := int64(3600)
	switch q.Get("bucket") {
	case "day":
		bucket = 86400
	case "week":
		writeErr(w, 400, "不支持按周聚合")
		return
	}
	// 天级桶(含全部)读永久日表, 与 30天/全部 的汇总卡片同一窗口。
	if bucket >= 86400 {
		if daily, err := h.Store.TimeseriesDaily(); err == nil {
			if pts := filterSince(daily, start); len(pts) > 0 {
				writeJSON(w, 200, pts)
				return
			}
		}
	}
	pts, err := h.Store.Timeseries(start, bucket)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if pts == nil {
		pts = []store.Point{}
	}
	writeJSON(w, 200, pts)
}

// filterSince 过滤掉 start 之前的点(start<=0 表示全部)。
func filterSince(pts []store.Point, start int64) []store.Point {
	out := make([]store.Point, 0, len(pts))
	for _, p := range pts {
		if start <= 0 || p.Bucket >= start {
			out = append(out, p)
		}
	}
	return out
}

func (h *handlers) listRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, err := h.Store.ListRequests(rangeStart(q.Get("range")), q.Get("provider"),
		q.Get("model"), q.Get("channel"), q.Get("status"), limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rows == nil {
		rows = []store.RequestRow{}
	}
	writeJSON(w, 200, rows)
}

// ---- 供应商 ----

type providerIn struct {
	config.Provider
	APIKey string `json:"api_key"` // 明文, 仅经此接口加密落盘
}

func (h *handlers) listProviders(w http.ResponseWriter, r *http.Request) {
	cfg := h.Mgr.Cfg()
	out := make([]map[string]any, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		fm := p.FetchedModels
		if fm == nil {
			fm = []string{}
		}
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "anthropic_url": p.AnthropicURL,
			"openai_url": p.OpenAIURL, "timeout_sec": p.TimeoutSec,
			"priority": p.Priority, "enabled": p.Enabled, "note": p.Note,
			"has_key":        h.Mgr.ProviderKey(p.ID) != "",
			"fetched_models": fm, "fetched_at": p.FetchedAt,
			"last_test": p.LastTest, "created_at": p.CreatedAt,
		})
	}
	writeJSON(w, 200, out)
}

func (h *handlers) createProvider(w http.ResponseWriter, r *http.Request) {
	var in providerIn
	if err := readBodyJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体解析失败: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, 400, "供应商名称不能为空")
		return
	}
	if err := h.Mgr.UpsertProvider(in.Provider, in.APIKey); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "created"})
}

func (h *handlers) updateProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in providerIn
	if err := readBodyJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体解析失败: "+err.Error())
		return
	}
	in.ID = id
	if err := h.Mgr.UpsertProvider(in.Provider, in.APIKey); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "updated"})
}

func (h *handlers) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := h.Mgr.DeleteProvider(r.PathValue("id")); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}

func (h *handlers) fetchModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := h.Mgr.Cfg()
	var target *config.Provider
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == id {
			target = &cfg.Providers[i]
			break
		}
	}
	if target == nil {
		writeErr(w, 404, "供应商不存在")
		return
	}
	key := h.Mgr.ProviderKey(id)
	ctx := r.Context()
	var all []string
	errs := []string{}
	if models, err := fetchModelList(ctx, *target, key, "anthropic"); err == nil {
		all = append(all, models...)
	} else if strings.TrimSpace(target.AnthropicURL) != "" {
		errs = append(errs, "anthropic: "+err.Error())
	}
	if models, err := fetchModelList(ctx, *target, key, "openai"); err == nil {
		all = append(all, models...)
	} else if strings.TrimSpace(target.OpenAIURL) != "" {
		errs = append(errs, "openai: "+err.Error())
	}
	// 并集合并进模型池, 保留手动整理结果
	pool, mergeErr := []string{}, error(nil)
	if len(all) > 0 {
		pool, mergeErr = h.Mgr.MergeFetchedModels(id, all)
	}
	if mergeErr != nil {
		writeErr(w, 500, mergeErr.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": pool, "added": len(all), "errors": errs})
}

// setProviderModels 手动管理模型池(整体替换)。
func (h *handlers) setProviderModels(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Models []string `json:"models"`
	}
	if err := readBodyJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体解析失败: "+err.Error())
		return
	}
	if err := h.Mgr.SetProviderModels(r.PathValue("id"), in.Models); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "saved"})
}

func (h *handlers) testProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := h.Mgr.Cfg()
	var target *config.Provider
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == id {
			target = &cfg.Providers[i]
			break
		}
	}
	if target == nil {
		writeErr(w, 404, "供应商不存在")
		return
	}
	res := h.probeProvider(r.Context(), *target)
	_ = h.Mgr.SetProviderTestResult(id, res)
	writeJSON(w, 200, map[string]any{
		"ok": res.OK, "latency_ms": res.LatencyMs, "errors": res.Errors, "tested_at": res.TestedAt,
	})
}

// testAllProviders 并发测试全部已启用供应商, 结果分别持久化到各供应商。
func (h *handlers) testAllProviders(w http.ResponseWriter, r *http.Request) {
	cfg := h.Mgr.Cfg()
	type item struct {
		id   string
		name string
		res  *config.TestResult
	}
	enabled := make([]config.Provider, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if p.Enabled {
			enabled = append(enabled, p)
		}
	}
	items := make([]item, len(enabled))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, p := range enabled {
		items[i] = item{id: p.ID, name: p.Name}
		wg.Add(1)
		go func(p config.Provider, idx int) {
			defer wg.Done()
			res := h.probeProvider(r.Context(), p)
			mu.Lock()
			items[idx].res = res
			mu.Unlock()
		}(p, i)
	}
	wg.Wait()
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if it.res == nil {
			continue
		}
		_ = h.Mgr.SetProviderTestResult(it.id, it.res)
		errs := it.res.Errors
		if errs == nil {
			errs = []string{}
		}
		out = append(out, map[string]any{
			"id": it.id, "name": it.name, "ok": it.res.OK,
			"latency_ms": it.res.LatencyMs, "errors": errs,
		})
	}
	writeJSON(w, 200, out)
}

// probeProvider 对供应商两种格式的 /v1/models 做探测并返回结果(不落盘)。
func (h *handlers) probeProvider(ctx context.Context, p config.Provider) *config.TestResult {
	key := h.Mgr.ProviderKey(p.ID)
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	start := time.Now()
	errs := []string{}
	okAny := false
	for _, format := range []string{"anthropic", "openai"} {
		base := p.AnthropicURL
		if format == "openai" {
			base = p.OpenAIURL
		}
		if strings.TrimSpace(base) == "" {
			continue
		}
		if _, err := fetchModelList(ctx, p, key, format); err != nil {
			errs = append(errs, format+": "+err.Error())
		} else {
			okAny = true
		}
	}
	return &config.TestResult{
		OK:        okAny,
		LatencyMs: time.Since(start).Milliseconds(),
		Errors:    errs,
		TestedAt:  time.Now().Unix(),
	}
}

// ---- 路由 ----

func (h *handlers) getRoutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, h.Mgr.Cfg().Routes)
}

func (h *handlers) putRoutes(w http.ResponseWriter, r *http.Request) {
	var routes []config.Route
	if err := readBodyJSON(r, &routes); err != nil {
		writeErr(w, 400, "请求体解析失败: "+err.Error())
		return
	}
	if err := h.Mgr.SetRoutes(routes); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "saved"})
}

// ---- 设置 ----

func (h *handlers) getSettings(w http.ResponseWriter, r *http.Request) {
	s := h.Mgr.Settings()
	s.RouterKeyEnc = "" // 不回传密文
	// 切片不得为 null: 前端拿到 null 后 push/splice 会直接抛 TypeError。
	if s.Lightweight.Targets == nil {
		s.Lightweight.Targets = []config.Target{}
	}
	if s.Lightweight.Aliases == nil {
		s.Lightweight.Aliases = []string{}
	}
	if s.Failover.Targets == nil {
		s.Failover.Targets = []config.Target{}
	}
	writeJSON(w, 200, s)
}

func (h *handlers) putSettings(w http.ResponseWriter, r *http.Request) {
	var in config.Settings
	if err := readBodyJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体解析失败: "+err.Error())
		return
	}
	in.RouterKeyEnc = "" // 防止经此接口改密钥
	if in.Port != 0 && !validListenPort(in.Port) {
		writeErr(w, 400, "端口必须在 1-65535")
		return
	}
	scope := r.URL.Query().Get("scope")
	if err := h.Mgr.UpdateSettings(func(cur *config.Settings) {
		port, keyEnc := cur.Port, cur.RouterKeyEnc
		switch scope {
		case "channels":
			cur.DefaultProviderID = in.DefaultProviderID
			cur.Lightweight = in.Lightweight
			cur.Subagent = in.Subagent
			cur.Failover = in.Failover
		case "general":
			cur.RetentionDays = in.RetentionDays
			cur.InjectOpenAIUsage = in.InjectOpenAIUsage
			cur.UnloadOnMinimise = in.UnloadOnMinimise
			cur.Inject = in.Inject
			cur.Archive = in.Archive
			if in.Port != 0 {
				cur.Port = in.Port
			}
		default:
			*cur = in
			cur.RouterKeyEnc = keyEnc
			if in.Port == 0 {
				cur.Port = port
			}
		}
	}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "saved"})
}

// ---- 定价 ----

func (h *handlers) listPricing(w http.ResponseWriter, r *http.Request) {
	list, err := h.Pricer.List()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []store.Price{}
	}
	writeJSON(w, 200, list)
}

func (h *handlers) upsertPricing(w http.ResponseWriter, r *http.Request) {
	var p store.Price
	if err := readBodyJSON(r, &p); err != nil || strings.TrimSpace(p.Model) == "" {
		writeErr(w, 400, "需要 model 与单价")
		return
	}
	if priceNegative(p) {
		writeErr(w, 400, "单价不能为负")
		return
	}
	flatSet := p.Input != 0 || p.Output != 0 || p.CacheRead != 0 || p.CacheCreation != 0
	for _, t := range []*store.Tier{p.Peak, p.Valley} {
		if t == nil || !t.Enabled {
			continue
		}
		if !validHM(t.Start) || !validHM(t.End) {
			writeErr(w, 400, "峰/谷时段格式应为 HH:MM")
			return
		}
		if t.Mode != "absolute" && t.Mode != "multiplier" {
			t.Mode = "absolute"
		}
		if t.Mode == "multiplier" {
			if t.Rate <= 0 {
				writeErr(w, 400, "倍率模式下倍率必须大于 0")
				return
			}
			if !flatSet {
				writeErr(w, 400, "倍率模式基于平价计算, 请先填写平价四项单价")
				return
			}
		} else if t.Input == 0 && t.Output == 0 && t.CacheRead == 0 && t.CacheCreation == 0 {
			writeErr(w, 400, "绝对价格模式下请填写该档位的四项单价")
			return
		}
	}
	if err := h.Pricer.Upsert(p); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "saved"})
}

func validListenPort(p int) bool { return p >= 1 && p <= 65535 }

func priceNegative(p store.Price) bool {
	if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheCreation < 0 {
		return true
	}
	for _, tier := range []*store.Tier{p.Peak, p.Valley} {
		if tier != nil && (tier.Input < 0 || tier.Output < 0 || tier.CacheRead < 0 || tier.CacheCreation < 0 || tier.Rate < 0) {
			return true
		}
	}
	return false
}

func validHM(s string) bool {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) != 2 {
		return false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59
}

func (h *handlers) deletePricing(w http.ResponseWriter, r *http.Request) {
	if err := h.Pricer.Delete(r.PathValue("model")); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}

func (h *handlers) importPricing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		JSON string `json:"json"`
	}
	if err := readBodyJSON(r, &in); err != nil || strings.TrimSpace(in.JSON) == "" {
		writeErr(w, 400, "需要 json 字段(cc-switch model-pricing.json 内容)")
		return
	}
	n, err := h.Pricer.ImportCCSwitch([]byte(in.JSON))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "imported": n})
}

// ---- 会话 ----

func (h *handlers) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.ListSessions()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []store.SessionRow{}
	}
	writeJSON(w, 200, list)
}

func (h *handlers) deleteSession(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	paths, err := h.Store.DeleteSession(key)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, p := range paths {
		_ = os.Remove(filepath.Join(h.ArchiveDir, filepath.FromSlash(p)))
	}
	_ = os.RemoveAll(filepath.Join(h.ArchiveDir, sanitize(key)))
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}

func sanitize(key string) string {
	out := make([]rune, 0, len(key))
	for _, c := range key {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func (h *handlers) listSnapshots(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.ListSnapshots(r.PathValue("key"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []store.SnapshotRow{}
	}
	writeJSON(w, 200, list)
}

func (h *handlers) getSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "非法快照 ID")
		return
	}
	row, err := h.Store.GetSnapshot(id)
	if err != nil {
		writeErr(w, 404, "快照不存在")
		return
	}
	abs := filepath.Join(h.ArchiveDir, filepath.FromSlash(row.Path))
	content, err := archive.ReadSnapshot(abs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	content.Project = row.Project
	content.Captured = row.Ts
	writeJSON(w, 200, content)
}

func (h *handlers) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "非法快照 ID")
		return
	}
	row, err := h.Store.GetSnapshot(id)
	if err != nil {
		writeErr(w, 404, "快照不存在")
		return
	}
	path, err := h.Store.DeleteSnapshotRow(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if path != "" {
		_ = os.Remove(filepath.Join(h.ArchiveDir, filepath.FromSlash(path)))
	}
	_ = h.Store.DeleteSessionIfEmpty(row.SessionKy)
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}
