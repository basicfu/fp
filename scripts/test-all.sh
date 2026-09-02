#!/usr/bin/env bash
# 跑全部测试：Go 侧 + 前端。
#
# scripts/test.sh 只跑 Go，且要接受 -run 之类的参数转发，所以不在那里
# 混入前端。但只有一条命令能一次跑完全部，前端测试才不会被忘掉。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "==> Go 测试"
"$ROOT/scripts/test.sh"

echo
echo "==> 前端测试"
cd "$ROOT/web"
if [ ! -d node_modules ]; then
  npm ci
fi
npm test
