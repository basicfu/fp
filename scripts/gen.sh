#!/usr/bin/env bash
# 生成 protobuf / gRPC 代码。
#
# 产物提交进仓库：接入方 go get 之后应该直接能编译，不该被要求装 buf。
# 本机没有也不需要 protoc——buf 自带纯 Go 的 protobuf 编译器。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GOBIN="$(go env GOPATH)/bin"
export PATH="$GOBIN:$PATH"

# 与 go.mod 里的运行时依赖对齐，升级时两处一起改。
BUF_VERSION=v1.72.0
PROTOC_GEN_GO_VERSION=v1.36.12
PROTOC_GEN_GO_GRPC_VERSION=v1.6.2

# 只检查存在性，不检查版本：已装旧版时请自行 go install 覆盖。
ensure() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "安装 $1 ..."
    GOBIN="$GOBIN" go install "$2"
  fi
}

ensure buf                "github.com/bufbuild/buf/cmd/buf@${BUF_VERSION}"
ensure protoc-gen-go      "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}"
ensure protoc-gen-go-grpc "google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION}"

buf lint
buf generate

# 生成产物本应已经是 gofmt 干净的；不干净说明插件版本不对。
unformatted="$(gofmt -l sdk/gen)"
if [ -n "$unformatted" ]; then
  echo "生成产物未通过 gofmt：$unformatted" >&2
  exit 1
fi

echo "生成完成。"
