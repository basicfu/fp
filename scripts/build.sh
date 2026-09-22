#!/usr/bin/env bash
# 构建 fp 并发布成 docker image。
#
# 只有 IMAGE 这一处硬编码了服务名——scripts/build-im.sh 是它的 fp-im 版本，
# 两份脚本除了 IMAGE、CMD_DIR、要不要先构建前端这三处，其余完全一样。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

IMAGE=registry.cn-shanghai.aliyuncs.com/shwlkj/fp
CMD_DIR=./cmd/fp

# fp 的管理控制台前端是 go:embed 进二进制的（web/embed.go），编译 Go 代码
# 之前必须先有构建产物，否则镜像里的控制台只会显示"尚未构建"的占位页。
./scripts/build-web.sh

echo "==> 编译 $CMD_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o bootstrap-upx "$CMD_DIR"
upx -1 -f -o bootstrap bootstrap-upx

echo "==> 构建并推送 $IMAGE:latest"
docker build -f Dockerfile -t "$IMAGE" .
rm -f bootstrap bootstrap-upx
docker push "$IMAGE"

# 同时备份一个带时间戳的版本到仓库，方便按版本回滚。
VERSION=$(date +%Y%m%d%H%M)
docker tag "$IMAGE:latest" "$IMAGE:$VERSION"
docker push "$IMAGE:$VERSION"
