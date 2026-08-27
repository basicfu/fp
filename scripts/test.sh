#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
. "$ROOT/scripts/env.sh"
cd "$ROOT"
# -p 1：所有测试包共用同一个局域网 Postgres/Redis（testsupport 在每次调用时
# TRUNCATE / FLUSHDB），Go 默认会并发运行不同包的测试二进制，导致包之间互相冲掉
# 对方刚写入的数据。串行执行各包可避免这种跨包竞争。
go test ./... -p 1 -count=1 "$@"
