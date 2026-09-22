#!/usr/bin/env bash
# 构建 fp-im 并发布成 docker image。是 scripts/build.sh 的 fp-im 版本，
# 差异只有 IMAGE、CMD_DIR，以及不需要先构建前端（fp-im 没有管理控制台）。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

IMAGE=registry.cn-shanghai.aliyuncs.com/shwlkj/fp-im
CMD_DIR=./cmd/fp-im

echo "==> 编译 $CMD_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o bootstrap-upx "$CMD_DIR"
upx -1 -o bootstrap bootstrap-upx

echo "==> 构建并推送 $IMAGE:latest"
docker build -f Dockerfile -t "$IMAGE" .
rm -f bootstrap bootstrap-upx
docker push "$IMAGE"

# 同时备份一个带时间戳的版本到仓库，方便按版本回滚。
VERSION=$(date +%Y%m%d%H%M)
docker tag "$IMAGE:latest" "$IMAGE:$VERSION"
docker push "$IMAGE:$VERSION"
