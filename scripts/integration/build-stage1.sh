#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
mkdir -p .local/cache/go-tmp .local/stage1-artifacts
exec 9>.local/cache/backend-check.lock
flock 9
export GOMAXPROCS=1 CGO_ENABLED=0 GOCACHE="$PWD/.local/cache/go" GOTMPDIR="$PWD/.local/cache/go-tmp"
make build GO_PARALLEL=1
cp bin/meshstrata-{controller,agent} .local/stage1-artifacts/
go test -p 1 -c -o .local/stage1-artifacts/b03.test ./agent/internal/mihomo
git rev-parse HEAD > .local/stage1-artifacts/COMMIT
cp scripts/compat/mihomo/version-lock.json .local/stage1-artifacts/
(cd .local/stage1-artifacts && sha256sum meshstrata-controller meshstrata-agent b03.test version-lock.json > SHA256SUMS)
