#!/usr/bin/env bash
# 构建管理控制台前端。产物落在 web/dist/，由 web/embed.go 嵌进二进制。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/web"

if [ ! -d node_modules ]; then
  echo "==> 安装前端依赖"
  npm ci
fi

echo "==> 构建前端"
npm run build

echo "==> 产物："
du -sh "$ROOT/web/dist"
