#!/bin/sh
# 安装后重载 systemd, 但不自动启动 —— 使用者自行
#   systemctl enable --now cluster-route-server
# 以避免在其未预期的情况下占用 3721 端口并接管本地模型入口。
set -e
systemctl daemon-reload >/dev/null 2>&1 || true
echo "cluster-route-server installed. Start it with:"
echo "  systemctl enable --now cluster-route-server"
