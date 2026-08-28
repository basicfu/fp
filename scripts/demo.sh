#!/usr/bin/env bash
# 启动 examples/demo：一个接入 fp 的最小业务服务。与 run.sh 同构——同样
# 从 .env.local 载入凭据（这里用得到的是 FP_APP_ID / FP_APP_SECRET）。
#
# 前提：fp 已经在跑（./scripts/run.sh），且已经在管理端建好应用、启用了
# sms_code、把 appId/appSecret 填进了 .env.local。完整步骤见
# examples/demo/README.md。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
. "$ROOT/scripts/env.sh"
cd "$ROOT"

# FP_APP_ID / FP_APP_SECRET 没有默认值——它们是凭据，只能来自 .env.local，
# 缺了就该让 fpsdk.New 报出清楚的错误，而不是在这里悄悄放行。
export FP_ADDR="${FP_ADDR:-127.0.0.1:9090}"
export FP_INSECURE="${FP_INSECURE:-1}"
go run ./examples/demo
