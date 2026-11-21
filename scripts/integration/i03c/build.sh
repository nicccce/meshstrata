#!/usr/bin/env bash
set -euo pipefail
umask 077
if [[ $# != 1 ]]; then
  printf 'Usage: %s /absolute/path/to/pinned-mihomo\n' "$0" >&2
  exit 2
fi
kernel=$(realpath "$1")
cd "$(dirname "$0")/../../.."
if ! git diff --quiet HEAD --; then
  printf '%s\n' 'Build stage-three artifacts from the committed product.' >&2
  exit 1
fi
commit=$(git rev-parse HEAD)
version="i03c-$commit"
out="$PWD/.local/stage3-artifacts/$commit"
mkdir -p .local/cache/go-tmp "$(dirname "$out")"
exec 9>.local/cache/backend-check.lock
flock 9
export CGO_ENABLED=0 GOMAXPROCS=1 GOOS=linux GOARCH=amd64 GOAMD64=v1
export GOCACHE="$PWD/.local/cache/go" GOTMPDIR="$PWD/.local/cache/go-tmp"
export npm_config_cache="$PWD/.local/cache/npm" PYTHONDONTWRITEBYTECODE=1
lock=scripts/compat/mihomo/version-lock.json
expected=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["binary_sha256"])' "$lock")
printf '%s  %s\n' "$expected" "$kernel" | sha256sum -c -
if [[ -e "$out" ]]; then
  printf 'Artifact directory already exists: %s\n' "$out" >&2
  exit 1
fi
stage=$(mktemp -d "$(dirname "$out")/.build.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT
make build GO_PARALLEL=1 COMMIT="$commit" VERSION="$version"
install -m 0700 bin/meshstrata-controller bin/meshstrata-agent "$stage/"
install -m 0700 "$kernel" "$stage/mihomo"
cp "$lock" "$stage/version-lock.json"
printf '%s\n' "$commit" > "$stage/COMMIT"
"$stage/meshstrata-controller" version > "$stage/controller-version.json"
"$stage/meshstrata-agent" version > "$stage/agent-version.json"
for part in "$stage/meshstrata-controller" "$stage/meshstrata-agent" "$stage/mihomo"; do
  headers=$(readelf -l "$part")
  if [[ "$headers" == *INTERP* ]]; then
    printf 'Unexpected ELF interpreter in %s\n' "$part" >&2
    exit 1
  fi
done
cp documents/backend-delivery/I03-C-evidence.json "$stage/acceptance-evidence.json"
python3 - "$stage" "$commit" "$version" <<'PY'
import datetime,hashlib,json,sys
from pathlib import Path
stage,commit,version=Path(sys.argv[1]),sys.argv[2],sys.argv[3]
for component in ('controller','agent'):
    info=json.loads((stage/(component+'-version.json')).read_text())
    assert info['component']==component and info['commit']==commit and info['version']==version
    assert info['build_time'] not in ('','unknown')
files={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(stage.iterdir()) if p.is_file()}
manifest=dict(commit=commit,version=version,platform='linux/amd64',goamd64='v1',cgo_enabled=False,frontend='embedded in Controller',built_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),sha256=files)
(stage/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
PY
(cd "$stage" && sha256sum COMMIT meshstrata-controller meshstrata-agent mihomo version-lock.json controller-version.json agent-version.json acceptance-evidence.json manifest.json > SHA256SUMS && sha256sum -c SHA256SUMS)
mv "$stage" "$out"
printf '%s\n' "$out"
