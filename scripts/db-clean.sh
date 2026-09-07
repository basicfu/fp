#!/usr/bin/env bash
# 清空 fp 的数据库。破坏性操作，会要求把库名敲一遍确认。
#
#   ./scripts/db-clean.sh truncate   # 清数据，保表结构（不重跑迁移）
#   ./scripts/db-clean.sh reset      # 删掉全部表，下次启动重跑迁移
#
# 两种模式共用同一套目标打印与确认逻辑，所以合成一个脚本带模式参数，
# 而不是两个各自复制一遍确认代码的脚本——确认这一步正是最不该有分叉的地方。
#
# 清的是 FP_POSTGRES_URL / FP_REDIS_URL，也就是**开发库**（env.sh 里的 fp / DB 0），
# 不是测试库。测试库由 testsupport 在每次跑测试时自己清，不需要这个脚本。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
. "$ROOT/scripts/env.sh"
cd "$ROOT"
exec go run ./cmd/fp-dbclean "$@"
