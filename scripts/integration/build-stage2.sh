#!/usr/bin/env bash
set -euo pipefail
umask 077
if [[ $# != 1 ]]; then
    printf 'Usage: %s /absolute/path/to/pinned-mihomo\n' "$0" >&2
    exit 2
fi
kernel=$(realpath "$1")
cd "$(dirname "$0")/../.."
if ! git diff --quiet HEAD --; then
    printf '%s\n' 'Build stage-two artifacts from the committed product.' >&2
    exit 1
fi
commit=$(git rev-parse HEAD)
out="$PWD/.local/stage2-artifacts/$commit"
mkdir -p .local/cache/go-tmp "$(dirname "$out")"
exec 9>.local/cache/backend-check.lock
flock 9
export GOMAXPROCS=1 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1
export GOCACHE="$PWD/.local/cache/go" GOTMPDIR="$PWD/.local/cache/go-tmp"
lock=scripts/compat/mihomo/version-lock.json
expected=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["binary_sha256"])' "$lock")
printf '%s  %s\n' "$expected" "$kernel" | sha256sum -c -
stage=$(mktemp -d "$(dirname "$out")/.build.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT
make build GO_PARALLEL=1 VERSION="i02-$commit"
install -m 0700 bin/meshstrata-{controller,agent} "$stage/"
install -m 0700 "$kernel" "$stage/mihomo"
cp "$lock" "$stage/version-lock.json"
printf '%s\n' "$commit" > "$stage/COMMIT"
(
    cd "$stage"
    ./meshstrata-controller version > controller-version.json
    ./meshstrata-agent version > agent-version.json
    sha256sum COMMIT meshstrata-controller meshstrata-agent mihomo version-lock.json \
        controller-version.json agent-version.json > SHA256SUMS
)
mkdir "$stage/test"
go build -p 1 -trimpath -o "$stage/test/stage2-inspect" ./agent/integration/stage2-inspect
go build -p 1 -trimpath -o "$stage/test/stage2-peer" ./agent/integration/stage2-peer
go build -p 1 -trimpath -o "$stage/test/netprobe" ./scripts/integration/t01/netprobe
go test -p 1 -c -trimpath -o "$stage/test/runtime.test" ./agent/internal/runtime
(
    cd "$stage/test"
    sha256sum stage2-inspect stage2-peer netprobe runtime.test > SHA256SUMS
)
for part in "$stage/meshstrata-controller" "$stage/meshstrata-agent" "$stage/mihomo" "$stage/test/"{stage2-inspect,stage2-peer,netprobe,runtime.test}; do
    headers=$(readelf -l "$part")
    if [[ "$headers" == *INTERP* ]]; then
        printf 'Unexpected ELF interpreter in %s\n' "$part" >&2
        exit 1
    fi
done
if [[ -e "$out" ]]; then
    printf 'Artifact directory already exists: %s\n' "$out" >&2
    exit 1
fi
mv "$stage" "$out"
printf '%s\n' "$out"
