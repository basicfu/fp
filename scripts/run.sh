#!/usr/bin/env bash
# 起 fp。FP_POSTGRES_URL / FP_REDIS_URL 必须在环境变量里，FP_ENV 可选
# （缺省 DEV）；其余启动配置都在数据库里的系统配置表，通过控制台「系统
# 配置」页面维护——不再有 config.yaml。本机开发图省事可以把这两条连接串
# 写进 .env.local，这里顺手 source 一下；CI / 生产环境直接在进程环境里
# 传，不依赖这个文件。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [ -f .env.local ]; then
  set -a
  # shellcheck disable=SC1091
  . .env.local
  set +a
fi
if [ -z "${FP_POSTGRES_URL:-}" ] || [ -z "${FP_REDIS_URL:-}" ]; then
  echo "缺少 FP_POSTGRES_URL 或 FP_REDIS_URL。写进 $ROOT/.env.local，或在环境变量里传。" >&2
  exit 1
fi
# 顺带起前端开发服务器（http://localhost:5173，改前端代码即时生效）：8080 上的
# 控制台是编译时嵌进二进制的 web/dist，不重新构建就看不到前端改动。
# 带参数时是子命令（如 reset-password），不起。
if [ $# -eq 0 ]; then
  if ! command -v node >/dev/null 2>&1; then
    echo "未找到 node，跳过前端开发服务器（装好 Node.js 后重开终端）。" >&2
  else
    [ -d web/node_modules ] || (cd web && npm ci)
    # 直接用 node 跑 vite 而不是 npm run dev：npm 在 Windows 上是 cmd 包装，
    # 退出时 kill 只杀得掉外层，vite 会变成占着 5173 的孤儿进程。
    (cd web && exec node node_modules/vite/bin/vite.js) &
    VITE_PID=$!
    trap 'kill "$VITE_PID" 2>/dev/null || true' EXIT
  fi
fi
go run ./cmd/fp "$@"
