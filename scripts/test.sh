#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
. "$ROOT/scripts/env.sh"
cd "$ROOT"
# 参数分流：看起来像包路径的（./ 开头或就是 .）归包，其余归 go test 的标志。
#
# 不能简单写成 `go test ./... "$@"`：那样传进来的包路径会变成第二个包，
# 而 -run 之类的标志会被静默忽略——调用者以为自己在跑单个测试，
# 实际跑的是全量套件。TDD 流程里"先确认这个测试失败"那一步会因此失真。
pkgs=()
flags=()
for arg in "$@"; do
  case "$arg" in
    ./*|.) pkgs+=("$arg") ;;
    *)     flags+=("$arg") ;;
  esac
done
[ ${#pkgs[@]} -eq 0 ] && pkgs=("./...")

# -p 1：所有测试包共用同一个局域网 Postgres/Redis（testsupport 在每次调用时
# TRUNCATE / FLUSHDB），Go 默认会并发运行不同包的测试二进制，导致包之间互相
# 冲掉对方刚写入的数据。串行执行各包可避免这种跨包竞争。
go test "${pkgs[@]}" -p 1 -count=1 ${flags[@]+"${flags[@]}"}
