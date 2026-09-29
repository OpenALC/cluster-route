package proxy

import (
	"encoding/json"
	"net/http"
	"sort"
)

// serveModels 响应 GET /v1/models: 把已配置的启用路由别名与启用供应商模型池
// 合并去重后按请求协议格式返回, 供 cc-switch「一键获取模型」等客户端发现可用模型。
func (h *Handler) serveModels(w http.ResponseWriter, format string) {
	cfg := h.mgr.Cfg()
	seen := make(map[string]bool)
	var ids []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, r := range cfg.Routes {
		if r.Enabled {
			add(r.Alias)
		}
	}
	for _, p := range cfg.Providers {
		if !p.Enabled {
			continue
		}
		for _, m := range p.FetchedModels {
			add(m)
		}
	}
	sort.Strings(ids)

	w.Header().Set("Content-Type", "application/json")
	if format == "anthropic" {
		type model struct {
			ID          string `json:"id"`
			Type        string `json:"type"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
		}
		data := make([]model, 0, len(ids))
		for _, id := range ids {
			data = append(data, model{ID: id, Type: "model", DisplayName: id, CreatedAt: "1970-01-01T00:00:00Z"})
		}
		resp := map[string]any{"data": data, "has_more": false}
		if len(ids) > 0 {
			resp["first_id"] = ids[0]
			resp["last_id"] = ids[len(ids)-1]
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	data := make([]model, 0, len(ids))
	for _, id := range ids {
		data = append(data, model{ID: id, Object: "model", Created: 0, OwnedBy: "cluster-route"})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
