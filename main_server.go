//go:build !desktop

// cluster-route 无头服务模式: 控制台常驻, 供服务器/后台场景使用。
// 桌面应用请使用 wails build(构建标签 desktop)。
package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"cluster-route/internal/app"
)

//go:embed all:web/dist
var webEmbed embed.FS

const version = "0.3.2"

func main() {
	dataFlag := flag.String("data", "", "数据目录(默认便携模式用 exe 同目录 data, 不可写时回退到用户配置目录)")
	port := flag.Int("port", 0, "覆盖配置中的监听端口")
	openBrowser := flag.Bool("open", false, "启动后打开浏览器")
	flag.Parse()

	// 默认数据目录: 便携模式用 exe 同目录, 不可写(安装版)时回退到用户配置目录
	dataDir := ""
	if *dataFlag != "" {
		dataDir = *dataFlag
	} else {
		dataDir = app.ResolveDataDir()
	}

	// 内存软上限(环境变量优先)
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(80 << 20)
	}
	log.SetFlags(log.Ltime)

	webFS, err := app.WebFSFromEmbed(webEmbed)
	if err != nil {
		log.Fatalf("内嵌 UI 加载失败: %v", err)
	}
	a, err := app.Start(dataDir, *port, webFS, version)
	if err != nil {
		log.Fatalf("%v", err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d", a.Port)
	fmt.Println("┌─────────────────────────────────────────────────┐")
	fmt.Println("│  CLUSTER ROUTE (服务模式)                         │")
	fmt.Printf("│  版本: %-42s│\n", version)
	fmt.Printf("│  控制台: %-41s│\n", url)
	fmt.Printf("│  数据目录: %-39s│\n", dataDir)
	fmt.Printf("│  API Key: %-41s│\n", a.Mgr.RouterKey())
	fmt.Println("└─────────────────────────────────────────────────┘")

	if *openBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			openURL(url)
		}()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	a.Stop()
	fmt.Println("Cluster Route 已退出")
}

func openURL(url string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	} else if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", url)
	} else {
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
