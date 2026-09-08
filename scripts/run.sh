#!/usr/bin/env bash
# 起 fp。配置全部来自 ./config.yaml（从 config.example.yaml 复制一份填），
# 不再需要 scripts/env.sh —— 那里现在只剩测试与 examples/demo 用的环境变量。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [ ! -f config.yaml ]; then
  echo "缺少 $ROOT/config.yaml。请从 config.example.yaml 复制一份并填入凭据。" >&2
  exit 1
fi
go run ./cmd/fp "$@"
