# Cluster Route · 本地模型中转站

一个**本地常驻网关**:把多个模型供应商统一到一个入口,在 Claude Code 内用 `/model` 直接切换任意供应商的模型,并附带完整的用量统计、限额切换、会话留档与提示词注入。配合 [cc-switch](https://github.com/farion1231/cc-switch) 使用时,cc-switch 里只需要配置一次,之后增删供应商都不用再动它。

提供两种形态(同一份代码):

| 形态 | 构建 | 说明 |
|---|---|---|
| **桌面应用**(推荐) | `wails build` → `build/bin/Cluster Route.exe` | 原生窗口(WebView2)+ 新拟物图标,关窗即退出;构建后执行 `go run ./tools/patchversion -exe "build/bin/Cluster Route.exe"` 修补版本资源(见「品牌与签名」) |
| **Windows 安装包** | `.\build.ps1 -Installer` → `build/bin/Cluster Route-amd64-installer.exe` | 需要本机 NSIS;以独立 makensis 打包已修补的 exe(不要用 `wails build -nsis`,它会重新构建 exe 并覆盖版本资源修补) |
| 无头服务 | `go build -o cluster-router-server.exe .` | 控制台常驻,适合服务器/后台部署;同样需 patchversion 修补版本资源 |

```
Claude Code / Codex 等客户端
   │  (cc-switch 只配一次: BASE_URL + sk-cr 密钥)
   ▼
Cluster Route (127.0.0.1:3721)
   │  按「模型路由表 + 通道策略」分发, 透明转发不转换协议
   ├──► 供应商A (Anthropic 格式端点)
   ├──► 供应商B (OpenAI 格式端点)
   └──► 供应商C ...
```

## 特性

- **双协议透明转发**:Anthropic(`/v1/messages`)与 OpenAI(`/v1/chat/completions`)格式原样转发,不做协议转换;仅做外科手术式字段编辑(模型名改写、用量注入等,均可关)。另提供 `GET /v1/models` 向客户端返回已配置的路由别名与模型池并集(供 cc-switch「一键获取模型」等场景)
- **用量统计**:输入 / 缓存命中 / 缓存写入 / 输出四类 token,分供应商、分模型、分通道;缓存命中率趋势;峰/谷/平三档分时计价(峰谷可按平价倍率),支持导入 cc-switch 的 `model-pricing.json`
- **模型路由表**:客户端模型别名 → 供应商 + 上游真实模型名;未命中走默认供应商透传
- **模型池管理**:一键拉取供应商模型列表(并集合并),可删改、可手动添加,供路由与同名切换匹配
- **轻量通道(负载均衡)**:指定别名或启发式(无工具小请求)命中的轻量任务,按优先级列表转发到低成本模型
- **子 agent 兜底**:system 提示词不含主标记词的请求自动转发到指定模型(agent 定义里显式配 `model: 别名` 则 100% 精准且优先)
- **限额自动切换**:主目标 429/402/403(可选 5xx)时,在响应送达客户端之前无感切换到指定列表或同名模型的其他供应商
- **连通性测试**:单测/一键并发测试全部供应商,结果持久化到各供应商卡片
- **会话留档**:按会话归档完整上下文(每会话保留最近 N 份),按项目分组浏览,上下文被压缩/丢失后可找回,支持导出 Markdown
- **提示词注入**:全局守则追加到 system 末尾(不破坏前缀缓存),跨压缩存活
- **安全**:供应商 API Key 经 **Windows DPAPI** 加密落盘(拷贝到其他机器/用户无法解密);仅监听 127.0.0.1;管理接口带跨源防护;**每次启动自动滚动备份配置**(保留 10 份)
- **极低占用**:Go 单二进制 + 流式零缓存 + 有界队列批量落库,实测稳态内存约 **20MB**

## 快速开始

```bash
# 一键构建(推荐): 桌面应用 + 无头服务, 并自动修补 exe 版本资源
#   需要 Go 1.22+、Node 18+、Wails CLI、WebView2 运行时; 打安装包另需 NSIS
powershell -File build.ps1              # 加 -Installer 打包 NSIS 安装包

# 桌面应用(手动)
wails build
go run ./tools/patchversion -exe "build/bin/Cluster Route.exe"
"./build/bin/Cluster Route.exe"

# 无头服务模式
go build -o cluster-router-server.exe .
go run ./tools/patchversion -exe cluster-router-server.exe -original cluster-router-server.exe
./cluster-router-server.exe        # 可选: -port 8080 -data <dir> -open

# 仅改后端时可用 go 直接构建(需先构建前端)
cd web && npm install && npm run build && cd ..
go build -o cluster-router-server.exe .
```

数据目录:便携模式锚定 **exe 同目录下的 `data/`**;安装在不可写位置(Program Files)时自动回退到 `%APPDATA%\Cluster Route\data`(Linux 为 `~/.config/Cluster Route/data`)。可用 `-data` 覆盖。首次启动生成 `sk-cr-...` 密钥;**每次启动前自动备份 `config.json` 到 `data/backups/`**(保留最近 10 份)。

桌面应用启动后:

1. **指引** 页复制 BASE_URL 与 API Key → 在 cc-switch 中新增供应商粘贴即可
2. **供应商** 页添加真实上游(BASE_URL 分 Anthropic/OpenAI 两个格式字段,各填实际支持的),点「拉取模型」并在模型池中整理
3. **路由** 页建路由表(别名 → 供应商.上游模型),按需开启轻量通道/子agent/限额切换
4. Claude Code 会话内 `/model 别名` 切换;或在 `~/.claude/settings.json` 里持久化:

```json
"env": {
  "ANTHROPIC_BASE_URL": "http://127.0.0.1:3721",
  "ANTHROPIC_AUTH_TOKEN": "<指引页显示的 sk-cr-... 密钥>",
  "ANTHROPIC_DEFAULT_HAIKU_MODEL": "cr-light"
}
```

把 HAIKU(小模型)槽位配成轻量通道触发别名 `cr-light`,CC 的后台轻量任务就会走低成本模型。

## 定价(峰/谷/平)

每个模型可配置:

- **平价**(可选):四项单价($/百万 token),峰谷时段之外按此计费,未填则峰谷外不计费
- **峰时段 / 谷时段**(各自独立开关):起止时间支持跨午夜(如 23:00–07:00);价格支持**绝对价格**(四项)或**平价倍率**(该档 = 平价 × 倍率,此时平价必填)
- 计费按请求时刻选档:峰 > 谷 > 平;历史记录不重算

## 数据与文件

| 路径 | 说明 |
|---|---|
| `data/config.json` | 供应商/模型池/路由/通道策略/设置;API Key 为 DPAPI 密文(`enc1:` 前缀) |
| `data/backups/` | 每次启动自动滚动的配置备份(保留 10 份) |
| `data/stats.db` | 请求明细(默认保留 90 天)+ 天级聚合(永久)+ 定价 + 会话元数据 |
| `data/sessions/` | 会话上下文快照(明文,按会话保留最近 N 份,全局 5GB LRU) |

## 开发

```
main_desktop.go         桌面入口(Wails, 构建标签 desktop)
main_server.go          无头服务入口(!desktop)
internal/app            启动流程(两种形态共用)
internal/config  crypto 配置与 DPAPI 加密
internal/routing        通道决策 + 限额切换链
internal/rewrite        请求体提取与改写(模型名/注入)
internal/proxy          转发主管线 + SSE 用量解析
internal/archive        会话归档(指纹/快照/LRU)
internal/stats  pricing 统计落库 + 分时成本
internal/store          SQLite 迁移与查询
internal/api            管理接口(/admin/*)
web/                    Vue 3 + Vite + Tailwind + ECharts(Options API)
web/public/logo.svg     新拟物船舵 logo(favicon/侧边栏/应用图标同源)
web/scripts/gen-icons.mjs  生成 PNG/ICO(build/ 下, wails 打包引用)
cmd/…(无)              桌面/服务共用 root 包, 以构建标签区分
```


## Linux 支持

代码已跨平台(DPAPI 加密在 Linux 自动回退为机器指纹派生的 AES-256-GCM),无需大改。构建与打包须在 Linux/WSL2 上进行(webkit2gtk 走 CGO,无法从 Windows 交叉编译):

```bash
# Debian/Ubuntu
sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev build-essential
# Red Hat 系列
sudo dnf install gtk3-devel webkit2gtk4.1-devel gcc
# Arch
sudo pacman -S gtk3 webkit2gtk-4.1 go

wails build              # 便携二进制
wails build -deb         # Debian 系 .deb
wails build -rpm         # Red Hat 系 .rpm
wails build -appimage    # AppImage
# Arch: 建议 PKGBUILD/AUR 或直接分发便携二进制
```

安装到系统路径时,数据目录自动回退到 `~/.config/Cluster Route/data`。

## 品牌与签名(OpenALC)

- 元数据已配置: `wails.json`(info)+ `build/windows/info.json`(exe 版本资源)+ `build/windows/installer/wails_tools.nsh`(安装包属性)。出品方/公司名均为 **OpenALC**。
- **exe 版本资源修复(tools/patchversion)**: wails build 内部用 go-winres 生成的 `RT_VERSION` 资源块, Explorer 与 version.dll 能正常读取, 但 **.NET Framework 的 `FileVersionInfo`(即 PowerShell 的 `(Get-Item).VersionInfo`)无法解析**, 会显示 CompanyName 为空。因此构建脚本在链接完成后统一执行 `go run ./tools/patchversion`, 用 Win32 `UpdateResource` API 把版本资源重写为 RC/goversioninfo 兼容布局(语言 0409), 三类读取方均正常, 图标/清单不受影响。
- 签名(自签名, 本机可信): 本机有 Windows SDK signtool:
  ```powershell
  $cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject "CN=OpenALC" -CertStoreLocation Cert:\CurrentUser\My
  Export-PfxCertificate -Cert $cert -FilePath build\OpenALC-codesign.pfx -Password (ConvertTo-SecureString -String "OpenALC" -Force -AsPlainText)
  Import-Certificate -FilePath ($cert.PSPath) -CertStoreLocation Cert:\CurrentUser\Root   # 本机信任
  signtool sign /f build\OpenALC-codesign.pfx /p OpenALC /fd SHA256 "build\bin\Cluster Route.exe" "build\bin\Cluster Route-amd64-installer.exe"
  ```
  注: 自签名仅在本机(导入证书后)显示为有效; 对外分发需购买代码签名证书。

## 注意事项

- **会话留档会将对话内容明文写入本地磁盘**,不需要时在「设置」中关闭归档
- 换机器/重装系统后 DPAPI 无法解密旧配置,需重新录入供应商 Key
- 限额切换只在「尚未向客户端发出任何字节」时进行,流式响应一旦开始不再切换(避免破坏客户端流)
- 桌面应用关窗即退出(代理随之停止);需要后台常驻请使用无头服务模式
