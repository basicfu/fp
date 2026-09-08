#!/usr/bin/env bash
# 载入 .env.local 里的环境变量。被 test.sh 与 demo.sh 复用。
#
# 这里只剩两类东西：测试库连接串（internal/testsupport 直读
# FP_TEST_POSTGRES_URL / FP_TEST_REDIS_URL）和 examples/demo 的应用凭据。
# fp 与 fp-im 自己的启动配置在 config.yaml / config-im.yaml 里，不经过这里
# ——run.sh、run-im.sh、db-clean.sh 都不再 source 本文件。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ ! -f "$ROOT/.env.local" ]; then
  echo "缺少 $ROOT/.env.local。它只放测试库连接串与 examples/demo 的应用凭据；" >&2
  echo "fp / fp-im 的配置在 config.yaml / config-im.yaml（见 *.example.yaml）。" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
. "$ROOT/.env.local"
set +a
