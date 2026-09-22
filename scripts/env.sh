#!/usr/bin/env bash
# 载入 .env.local 里的环境变量。被 test.sh 与 demo.sh 复用——run.sh、
# run-im.sh、db-clean.sh 不 source 本文件，它们各自内联同一段"如果
# .env.local 存在就 source 一下"的逻辑，直接读 .env.local 本身。
#
# .env.local 现在放三类东西：测试库连接串（internal/testsupport 直读
# FP_TEST_POSTGRES_URL / FP_TEST_REDIS_URL）、examples/demo 的应用凭据、
# 以及图省事顺手放在这里的 fp/fp-im 本机启动配置（FP_POSTGRES_URL/
# FP_REDIS_URL/FP_ENV、FP_IM_REDIS_URL/FP_IM_FPSDK_ADDR/
# FP_IM_FPSDK_SECRET）——fp 和 fp-im 都不再有配置文件，全部走环境变量。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ ! -f "$ROOT/.env.local" ]; then
  echo "缺少 $ROOT/.env.local。它放测试库连接串、examples/demo 的应用凭据，" >&2
  echo "以及可选的 fp/fp-im 本机启动环境变量。" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
. "$ROOT/.env.local"
set +a
