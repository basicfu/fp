#!/usr/bin/env bash
# 起 fp-im。配置全部来自 ./config-im.yaml（从 config-im.example.yaml 复制
# 一份填），与 fp 的 config.yaml 完全独立。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [ ! -f config-im.yaml ]; then
  echo "缺少 $ROOT/config-im.yaml。请从 config-im.example.yaml 复制一份并填入凭据。" >&2
  exit 1
fi
exec go run ./cmd/fp-im "$@"
