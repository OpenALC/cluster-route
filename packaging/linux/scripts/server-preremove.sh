#!/bin/sh
set -e
systemctl disable --now cluster-route-server >/dev/null 2>&1 || true
