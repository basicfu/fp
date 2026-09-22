#!/usr/bin/env bash
# 构建管理控制台前端。产物落在 web/dist/，由 web/embed.go 嵌进二进制。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/web"

# 不只是"node_modules 不存在才装"——package-lock.json 比 node_modules 新
# 也得重装：比如刚拉完一个改了依赖（加了 cmdk/radix-ui 这种）的提交，
# node_modules 还是拉代码前装的那份，缺新包，构建会在 tsc 那步才报
# "Cannot find module"，离真正原因（该重装依赖了）很远。
if [ ! -d node_modules ] || [ package-lock.json -nt node_modules ]; then
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
