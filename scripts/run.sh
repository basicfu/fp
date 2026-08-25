#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
. "$ROOT/scripts/env.sh"
cd "$ROOT"
export FP_BOOTSTRAP_ADMIN_USER="${FP_BOOTSTRAP_ADMIN_USER:-admin}"
export FP_BOOTSTRAP_ADMIN_PASSWORD="${FP_BOOTSTRAP_ADMIN_PASSWORD:-admin123456}"
go run ./cmd/fp
