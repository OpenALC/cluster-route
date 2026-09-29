// Package api 提供本地管理 REST API 与内嵌 Web UI 静态资源。
// 仅绑定 127.0.0.1; 对 /admin/* 做跨源防护(带 Origin 头且来源不符即拒绝)。
package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"cluster-router/internal/archive"
	"cluster-router/internal/config"
	"cluster-router/internal/pricing"
	"cluster-router/internal/proxy"
	"cluster-router/internal/stats"
	"cluster-router/internal/store"
)

// Deps 管理接口依赖集合。
type Deps struct {
	Mgr        *config.Manager
	Store      *store.Store
	Pricer     *pricing.Pricer
	Recorder   *stats.Recorder
	Proxy      *proxy.Handler
	Archive    *archive.Service
	ArchiveDir string // sessions 快照根目录(用于删除文件)
	WebFS      fs.FS  // 内嵌 UI (web/dist)
	LogFile    string // 管理 API 4xx 诊断日志(空则不记录)
	Version    string
}

// Router 构建完整 HTTP 路由(代理 + 管理 + 静态)。
func Router(d Deps) http.Handler {
	mux := http.NewServeMux()
	h := &handlers{Deps: d}

	mux.Handle("/v1/", d.Proxy)
	mux.Handle("/admin/", guard(log4xx(d, h.serveAdmin())))
	if d.WebFS != nil {
		mux.Handle("/", spa(d.WebFS))
	}
	return mux
}

// log4xx 记录管理接口的 4xx/5xx 响应到诊断日志(定位客户端请求路径用)。
func log4xx(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.LogFile == "" {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if sw.status >= 400 {
			line := fmt.Sprintf("%s %d %s %s origin=%q\n",
				time.Now().Format("2006-01-02 15:04:05"), sw.status, r.Method,
				r.URL.RequestURI(), r.Header.Get("Origin"))
			f, err := os.OpenFile(d.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err == nil {
				_, _ = f.WriteString(line)
				f.Close()
			}
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// guard 跨源防护: 放行同源与 Wails 桌面窗口(wails.localhost),
// 其余跨站请求(浏览器反匿名页伪造)一律拒绝; 为放行来源补 CORS 并处理预检。
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			host := hostOf(origin)
			if host != r.Host && !strings.HasPrefix(origin, "http://wails.") && !strings.HasPrefix(origin, "https://wails.") {
				http.Error(w, `{"error":"cross origin rejected"}`, http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostOf(origin string) string {
	s := origin
	for _, p := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

// serveAdmin 分发 /admin/* 子路径。
func (h *handlers) serveAdmin() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/version", h.version)
	mux.HandleFunc("GET /admin/connection", h.connection)
	mux.HandleFunc("POST /admin/routerkey/rotate", h.rotateKey)

	mux.HandleFunc("GET /admin/overview", h.overview)
	mux.HandleFunc("GET /admin/series", h.series)
	mux.HandleFunc("GET /admin/group", h.group)
	mux.HandleFunc("GET /admin/requests", h.listRequests)

	mux.HandleFunc("GET /admin/providers", h.listProviders)
	mux.HandleFunc("POST /admin/providers", h.createProvider)
	mux.HandleFunc("PUT /admin/providers/{id}", h.updateProvider)
	mux.HandleFunc("DELETE /admin/providers/{id}", h.deleteProvider)
	mux.HandleFunc("POST /admin/providers/test_all", h.testAllProviders)
	mux.HandleFunc("POST /admin/providers/{id}/fetch_models", h.fetchModels)
	mux.HandleFunc("PUT /admin/providers/{id}/models", h.setProviderModels)
	mux.HandleFunc("POST /admin/providers/{id}/test", h.testProvider)

	mux.HandleFunc("GET /admin/routes", h.getRoutes)
	mux.HandleFunc("PUT /admin/routes", h.putRoutes)

	mux.HandleFunc("GET /admin/settings", h.getSettings)
	mux.HandleFunc("PUT /admin/settings", h.putSettings)

	mux.HandleFunc("GET /admin/pricing", h.listPricing)
	mux.HandleFunc("POST /admin/pricing", h.upsertPricing)
	mux.HandleFunc("DELETE /admin/pricing/{model}", h.deletePricing)
	mux.HandleFunc("POST /admin/pricing/import", h.importPricing)

	mux.HandleFunc("GET /admin/sessions", h.listSessions)
	mux.HandleFunc("GET /admin/sessions/{key}/snapshots", h.listSnapshots)
	mux.HandleFunc("DELETE /admin/sessions/{key}", h.deleteSession)
	mux.HandleFunc("GET /admin/snapshots/{id}", h.getSnapshot)
	return mux
}

// spa 内嵌单页应用, 未知路径回退 index.html。
func spa(fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(fsys, p); err != nil {
			index, ierr := fs.ReadFile(fsys, "index.html")
			if ierr != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(index)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// ---- 工具 ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readBodyJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(v)
}
