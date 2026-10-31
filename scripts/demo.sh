#!/usr/bin/env bash
# 启动 examples/demo：一个接入 fp 的最小业务服务。凭据 FP_APP_ID /
# FP_APP_SECRET 从系统环境变量读。
#
# 前提：fp 已经在跑（./scripts/run.sh），且已经在管理端建好应用、启用了
# password、建好了测试用户、把 appId/appSecret 设成了系统环境变量。完整步骤见
# examples/demo/README.md。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# FP_APP_ID / FP_APP_SECRET 没有默认值——它们是凭据，只能来自系统环境变量，
# 缺了就该让 fpsdk.New 报出清楚的错误，而不是在这里悄悄放行。
export FP_ADDR="${FP_ADDR:-grpc://127.0.0.1:9090}"
go run ./examples/demo
