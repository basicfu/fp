#!/usr/bin/env bash
# 起 fp-im。FP_IM_REDIS_URL / FP_IM_FPSDK_ADDR / FP_IM_FPSDK_SECRET 必须在
# 环境变量里，其余启动配置全部是代码里写死的默认值——不再有
# config-im.yaml。本机开发图省事可以把这三个写进 .env.local，这里顺手
# source 一下；CI / 生产环境直接在进程环境里传，不依赖这个文件。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [ -f .env.local ]; then
  set -a
  # shellcheck disable=SC1091
  . .env.local
  set +a
fi
if [ -z "${FP_IM_REDIS_URL:-}" ] || [ -z "${FP_IM_FPSDK_ADDR:-}" ] || [ -z "${FP_IM_FPSDK_SECRET:-}" ]; then
  echo "缺少 FP_IM_REDIS_URL / FP_IM_FPSDK_ADDR / FP_IM_FPSDK_SECRET 其中一个。写进 $ROOT/.env.local，或在环境变量里传。" >&2
  exit 1
fi
exec go run ./cmd/fp-im "$@"
