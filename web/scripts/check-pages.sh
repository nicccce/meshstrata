#!/usr/bin/env bash
set -euo pipefail
web_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$web_root/../.local"
# Every page chat uses this lock; do not run competing npm/Vite builds.
exec flock "$web_root/../.local/frontend-check.lock" bash -c '
  set -euo pipefail
  npm --prefix "$1" run check:foundation
  npm --prefix "$1" run build
' _ "$web_root"
