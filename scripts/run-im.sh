#!/usr/bin/env bash
# 起 fp-im。FP_IM_REDIS_URL / FP_IM_FPSDK_ADDR / FP_IM_FPSDK_SECRET 必须在
# 系统环境变量里，其余启动配置全部是代码里写死的默认值——不再有
# config-im.yaml。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [ -z "${FP_IM_REDIS_URL:-}" ] || [ -z "${FP_IM_FPSDK_ADDR:-}" ] || [ -z "${FP_IM_FPSDK_SECRET:-}" ]; then
  echo "缺少 FP_IM_REDIS_URL / FP_IM_FPSDK_ADDR / FP_IM_FPSDK_SECRET 其中一个，请在系统环境变量里设置。" >&2
  exit 1
fi
exec go run ./cmd/fpim "$@"
