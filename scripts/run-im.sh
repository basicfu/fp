#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source ./scripts/env.sh
exec go run ./cmd/fp-im
