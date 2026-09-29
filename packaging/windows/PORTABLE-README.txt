Cluster Route — 便携版 (Portable)
================================

本压缩包包含同一产品的两种形态, 按需选用其一:

  Cluster Route.exe          桌面应用: 原生窗口(WebView2), 关窗即退出。
                             这是日常使用的推荐形态。

  cluster-route-server.exe   无头服务: 控制台常驻, 无窗口。
                             适合放进计划任务/后台服务长期运行。

  data/                      数据目录。留空即可 —— 程序首次启动会自动建库
                             (config.json 与 stats.db)。删除此目录即恢复出厂设置。
                             注意: 不要与上面任一程序复制到别的机器使用,
                             供应商密钥用 Windows DPAPI 加密, 换机器/换用户
                             无法解密。

运行
----
  .\Cluster Route.exe                # 桌面应用
  .\cluster-route-server.exe        # 无头服务(后台)
  .\cluster-route-server.exe -open  # 无头服务, 启动后自动打开浏览器

无头服务可选参数:

  -data string   数据目录(默认取 exe 同目录下的 data)
  -port int      覆盖配置中的监听端口(默认 3721)
  -open          启动后打开浏览器

接通 Claude Code / cc-switch
----------------------------
cc-switch 里只配一次, 之后增删供应商都不用再动它:

  BASE_URL : http://127.0.0.1:3721
  API Key  : sk-cr            (随入口密钥, 见界面「设置」)

然后在 Claude Code 里用 /model 即可切换任意供应商的模型。

安全说明
--------
  * 两种形态都只监听 127.0.0.1, 不对局域网开放。
  * WebView2 运行时为必需依赖: Windows 11 / 较新的 Windows 10 均已内置;
    若启动后白屏, 请安装 Evergreen WebView2 Runtime:
    https://developer.microsoft.com/microsoft-edge/webview2/

卸载
----
直接删除本文件夹即可, 无注册表残留。(桌面应用会写入 WebView2 的
用户级缓存目录 %LOCALAPPDATA%\Cluster Route, 如需一并清理请手动删除。)
