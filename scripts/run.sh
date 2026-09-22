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
go run ./cmd/fp "$@"
