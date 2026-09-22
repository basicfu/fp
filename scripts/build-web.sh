#!/usr/bin/env bash
# 构建管理控制台前端。产物落在 web/dist/，由 web/embed.go 嵌进二进制。
#
# 资源引用的地址默认是 /static/（fp 自己的 internal/httpapi/static.go
# 把构建产物挂在这个前缀下）。要接 CDN 回源，构建前设一下环境变量：
#   VITE_ASSET_BASE=https://static.example.com/fp/ ./scripts/build-web.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/web"

# 直接检查 tsc 这个二进制在不在，而不是只看 node_modules 目录存不存在——
# 目录存在不代表装对了：上一次如果是在 NODE_ENV=production（或
# .npmrc 的 production=true）下装的，node_modules 会"看起来装完了"，
# 实际上 devDependencies（tsc/vite/vitest 都在里面）一个都没装，
# 光看目录存不存在、mtime 新不新都测不出这种情况。
# package-lock.json 比 node_modules 新这一条另外保留：拉到一个改了
# 依赖（加了 cmdk/radix-ui 这种）的提交后，即使 tsc 还在，也可能缺了
# 新加的包，构建会在 vite build 或 tsc 类型检查那步才报
# "Cannot find module"，离真正原因（该重装依赖了）很远。
if [ ! -x node_modules/.bin/tsc ] || [ package-lock.json -nt node_modules ]; then
  echo "==> 安装前端依赖"
  # --include=dev 显式覆盖调用方 shell 里可能设置的 NODE_ENV=production
  # （或 .npmrc 里的 production=true）——那两者会让 npm ci 跳过全部
  # devDependencies，而 tsc/vite/vitest 这三个构建时必需的命令恰好都在
  # devDependencies 里，装完之后会在 npm run build 报 "command not found"，
  # 而不是在这一步就说清楚缺的是什么。构建脚本自己的行为不该受调用方
  # 环境变量摆布，这里强制覆盖，不依赖调用方记得先 unset。
  npm ci --include=dev
fi

echo "==> 构建前端"
npm run build

echo "==> 产物："
du -sh "$ROOT/web/dist"
