#!/usr/bin/env bash
# 载入本机凭据并拼出连接串。被 test.sh 与 run.sh 复用。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ ! -f "$ROOT/.env.local" ]; then
  echo "缺少 $ROOT/.env.local。请从 .env.example 复制一份并填入凭据。" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
. "$ROOT/.env.local"
set +a

PG_BASE="postgres://${FP_PG_USER}:${FP_PG_PASSWORD}@${FP_PG_HOST}:${FP_PG_PORT}"
RD_BASE="redis://:${FP_REDIS_PASSWORD}@${FP_REDIS_HOST}:${FP_REDIS_PORT}"

# 开发库与测试库分开：测试会 TRUNCATE 全部业务表，绝不能跑在开发库上。
export FP_POSTGRES_URL="${PG_BASE}/fp?sslmode=disable"
export FP_REDIS_URL="${RD_BASE}/0"
export FP_TEST_POSTGRES_URL="${PG_BASE}/fp_test?sslmode=disable"
export FP_TEST_REDIS_URL="${RD_BASE}/1"
