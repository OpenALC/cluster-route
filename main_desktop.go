//go:build desktop

// cluster-router 桌面应用(Wails): 原生窗口 + 内嵌 UI,
// 后端代理服务仍在 127.0.0.1 监听, 供 cc-switch / Claude Code 连接。
package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"cluster-router/internal/app"
)

//go:embed all:web/dist
var webEmbed embed.FS

const version = "0.3.0"

// CrApp 暴露给前端的绑定对象。
type CrApp struct {
	ctx     context.Context
	inner   *app.App
	apiBase string
	dataDir string
}

func NewCrApp() *CrApp { return &CrApp{} }

// startup 启动后端子系统(代理/统计/归档 + 管理 API)。
func (c *CrApp) startup(ctx context.Context) {
	c.ctx = ctx
	dataDir := app.ResolveDataDir()
	c.dataDir = dataDir
	// HTTP 服务同样伺服内嵌 UI: 允许浏览器直接访问 127.0.0.1:端口 使用控制台
	webFS, err := app.WebFSFromEmbed(webEmbed)
	if err != nil {
		wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{Type: "error", Title: "启动失败", Message: "内嵌 UI 加载失败: " + err.Error()})
		wruntime.Quit(ctx)
		return
	}
	a, err := app.Start(dataDir, 0, webFS, version)
	if err != nil {
		_, _ = wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{
			Type:    "error",
			Title:   "启动失败",
			Message: err.Error() + "\n\n若端口被占用, 请先退出已在运行的 Cluster Route 实例。",
		})
		wruntime.Quit(ctx)
		return
	}
	c.inner = a
	c.apiBase = fmt.Sprintf("http://127.0.0.1:%d", a.Port)
	go c.watchMinimise()
}

// watchMinimise 监视窗口最小化: 设置开启时, 最小化即卸载前端页面
// (释放渲染进程中的 DOM/JS 内存), 还原窗口时自动重载并回到原页面。
// Wails v2 未暴露 WebView2 的进程级挂起接口, 卸载页面是可用的最强等效手段。
func (c *CrApp) watchMinimise() {
	wasMin := false
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(800 * time.Millisecond):
		}
		minimised := wruntime.WindowIsMinimised(c.ctx)
		if minimised == wasMin || c.inner == nil {
			wasMin = minimised
			continue
		}
		enabled := c.inner.Mgr.Settings().UnloadOnMinimise
		if minimised && enabled {
			wruntime.WindowExecJS(c.ctx,
				`try{sessionStorage.setItem('__cr_restore', location.hash)}catch(e){};`+
					`setTimeout(function(){window.location.replace('about:blank')},0)`)
		} else if !minimised && wasMin {
			wruntime.WindowReload(c.ctx)
		}
		wasMin = minimised
	}
}

// shutdown 优雅停止子系统。
func (c *CrApp) shutdown(ctx context.Context) {
	if c.inner != nil {
		c.inner.Stop()
	}
}

// GetAPIBase 供前端在 Wails 环境下获取管理 API 基址(浏览器模式下前端不走此绑定)。
func (c *CrApp) GetAPIBase() string { return c.apiBase }

// GetDataDir 数据目录(展示用)。
func (c *CrApp) GetDataDir() string { return c.dataDir }

// GetVersion 版本。
func (c *CrApp) GetVersion() string { return version }

func main() {
	crApp := NewCrApp()
	// 资产服务器的回落 Handler 挂我们的管理 API + 代理:
	// 前端在桌面模式下使用同源相对路径请求 /admin/*, 不依赖绑定注入, 彻底避免 404。
	lateBoundHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if crApp.inner != nil {
			crApp.inner.Mux.ServeHTTP(w, r)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	err := wails.Run(&options.App{
		Title:     "Cluster Route",
		Width:     1200,
		Height:    800,
		MinWidth:  980,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets:  mustSub(webEmbed, "web/dist"),
			Handler: lateBoundHandler,
		},
		BackgroundColour: &options.RGBA{R: 241, G: 245, B: 249, A: 255},
		OnStartup:        crApp.startup,
		OnShutdown:       crApp.shutdown,
		Bind:             []interface{}{crApp},
	})
	if err != nil {
		println("启动失败:", err.Error())
	}
}

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
