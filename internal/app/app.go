// Package app 抽取可复用的启动流程: 配置/存储/统计/归档/代理/管理 API。
// 供桌面应用(Wails)与无头服务两种形态共用。
package app

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"cluster-route/internal/api"
	"cluster-route/internal/archive"
	"cluster-route/internal/config"
	"cluster-route/internal/crypto"
	"cluster-route/internal/pricing"
	"cluster-route/internal/proxy"
	"cluster-route/internal/routing"
	"cluster-route/internal/stats"
	"cluster-route/internal/store"
)

// App 运行中的应用实例。
type App struct {
	DataDir  string
	Port     int
	Mgr      *config.Manager
	Store    *store.Store
	Pricer   *pricing.Pricer
	Recorder *stats.Recorder
	Archive  *archive.Service
	Proxy    *proxy.Handler
	Mux      http.Handler // 管理 API + 代理(桌面模式同时作为 wails 资产服务器的回落 Handler)

	server *http.Server
	stop   chan struct{}
}

// Start 启动全部子系统并监听 127.0.0.1:port。
// webFS 非 nil 时同时在该端口伺服 Web UI; 桌面模式传 nil(UI 由 Wails 资产服务器提供)。
func Start(dataDir string, portOverride int, webFS fs.FS, version string) (*App, error) {
	cr := crypto.New(dataDir)
	mgr, err := config.Load(dataDir, cr)
	if err != nil {
		return nil, fmt.Errorf("配置加载失败: %w", err)
	}
	st, err := store.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("数据库打开失败: %w", err)
	}
	pricer, err := pricing.New(st)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("定价加载失败: %w", err)
	}
	recorder := stats.New(st, pricer, 10000)
	stop := make(chan struct{})
	go recorder.Run(stop)

	archiveDir := dataDir + "/sessions"
	arch := archive.New(mgr, st, archiveDir)
	go arch.Run(stop)

	eng := routing.New(mgr)
	px := proxy.New(mgr, eng, recorder, arch)

	s := mgr.Settings()
	if portOverride != 0 {
		s.Port = portOverride
	}
	deps := api.Deps{
		Mgr: mgr, Store: st, Pricer: pricer, Recorder: recorder,
		Proxy: px, Archive: arch, ArchiveDir: archiveDir,
		WebFS: webFS, Version: version, LogFile: filepath.Join(dataDir, "router.log"),
	}
	mux := api.Router(deps)
	addr := fmt.Sprintf("127.0.0.1:%d", s.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		close(stop)
		st.Close()
		return nil, fmt.Errorf("端口 %d 监听失败(可能已有实例在运行): %w", s.Port, err)
	}
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		_ = server.Serve(ln)
	}()

	a := &App{
		DataDir: dataDir, Port: s.Port,
		Mgr: mgr, Store: st, Pricer: pricer,
		Recorder: recorder, Archive: arch, Proxy: px,
		Mux: mux,
		server: server, stop: stop,
	}
	go a.janitor()
	return a, nil
}

// Stop 优雅停止全部子系统。
func (a *App) Stop() {
	close(a.stop)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = a.server.Shutdown(ctx)
	_ = a.Store.Close()
}

// janitor 每小时清理过期请求明细与超容量归档。
func (a *App) janitor() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	cleanup := func() {
		retention := a.Mgr.Settings().RetentionDays
		if retention <= 0 {
			retention = 90
		}
		if n, err := a.Store.CleanupRequests(retention); err != nil {
			log.Printf("[janitor] 明细清理失败: %v", err)
		} else if n > 0 {
			log.Printf("[janitor] 已清理 %d 条过期请求明细", n)
		}
		a.Archive.EnforceCap()
	}
	cleanup()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			cleanup()
		}
	}
}

// WebFSFromEmbed 从内嵌资源构造 Web UI 文件系统(子路径 web/dist)。
func WebFSFromEmbed(embedded embed.FS) (fs.FS, error) {
	return fs.Sub(embedded, "web/dist")
}
