#!/bin/sh
# Cluster Route 便携包自安装脚本
#
#   用法: ./install.sh [prefix]     (默认装到 ~/.local)
#
# 不需要 root: 二进制装进 $prefix/bin, desktop 文件与 hicolor 图标
# 装进 $prefix/share。若要装到 /usr/local 请自行 sudo 调用本脚本。
set -eu

PREFIX="${1:-$HOME/.local}"
HERE="$(cd "$(dirname "$0")" && pwd)"

if [ ! -x "$HERE/cluster-route" ] || [ ! -x "$HERE/cluster-route-server" ]; then
    echo "!! 请在便携包根目录运行本脚本(未找到 cluster-route / cluster-route-server)"
    exit 1
fi

echo "安装到: $PREFIX"

install -Dm755 "$HERE/cluster-route"        "$PREFIX/bin/cluster-route"
install -Dm755 "$HERE/cluster-route-server" "$PREFIX/bin/cluster-route-server"
install -Dm644 "$HERE/cluster-route.desktop" \
              "$PREFIX/share/applications/cluster-route.desktop"

for d in "$HERE"/icons/hicolor/*/apps; do
    [ -d "$d" ] || continue
    SZ="$(basename "$(dirname "$d")")"
    install -Dm644 "$d/cluster-route.png" \
                  "$PREFIX/share/icons/hicolor/$SZ/apps/cluster-route.png"
done

if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database -q "$PREFIX/share/applications" || true
else
    echo "(提示: 未找到 update-desktop-database, 桌面菜单可能要重新登录后才会出现)"
fi

echo
echo "完成。桌面入口: $PREFIX/share/applications/cluster-route.desktop"
echo "无头服务可这样常驻(二选一):"
echo "  $PREFIX/bin/cluster-route-server"
echo "  或复制 packaging 里的 cluster-route-server.service 到 ~/.config/systemd/user"
