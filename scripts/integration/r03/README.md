# R03 local regressions and release artifacts

`local.py` reuses the normal I03-C CLI/HTTPS/mTLS driver and the unchanged
SOCKS5/HTTP wire helpers. It never imports Go internals, writes SQL, uses SSH,
or modifies host TUN/routes/DNS/firewall/clock. Run under the backend lock with
`CGO_ENABLED=0 GOMAXPROCS=1 PYTHONDONTWRITEBYTECODE=1` and explicitly built
Controller/Agent CLIs plus the SHA256-locked Mihomo v1.19.33:

```sh
flock .local/cache/backend-check.lock python3 scripts/integration/r03/local.py \
  --controller /absolute/path/meshstrata-controller \
  --agent /absolute/path/meshstrata-agent --mihomo /absolute/path/mihomo \
  --evidence .local/r03/unique-result.json --scenario sequence
```

Use separate output filenames for each run. `sequence` proves actual HTTP
forwarding, stopped-selected-exit failure, confirmed cleanup/archive, then
SOCKS5 wrong credentials followed by a new correct credential Pin, current
mTLS evidence, actual forwarding and stopped-exit failure. `correct-first`
starts fresh state and checks SOCKS5 without a prior negative credential.
`restart` publishes a normal Node Default route, waits for the new subscription's
real confirmed ingress task, keeps a TCP tunnel open across normal Controller
and Agent restarts, compares the original task IDs/artifact digests/confirmed
facts and verifies both retained and new traffic. Recovery may refresh
`confirmed_at`; comparisons require confirmation but exclude that mutable time.

The scenarios use private short temporary roots and only owned PIDs. Actual
runtime is IPv4 TCP with TUN off, numeric canary and explicit test.local hosts;
the original default TUN config is separately syntax checked. A proxy accept
counter alone is never treated as end-to-end success. Product positive echo and
negative authentication are independently required; failures never create
current evidence. A known busy/backoff control slot can be retried within 20s;
admission and current evidence share the original total 30s observation budget,
with the 90s subscription and 12s refusal bounds unchanged.
No arbitrary external error is retried as success.

Evidence contains only checks, results, binary/source hashes and scan counts.
After normal shutdown, logs are scanned against generated capabilities,
credentials and private business material (literal, JSON, base64 and hex)
before copying to an evidence-specific private log directory. Failed runs
remain failures. Do not reuse their filename or count a later pass as erasing
them. Candidate binaries are not release artifacts.

After the reviewed source and `R03-evidence.json` are committed, run:

```sh
bash scripts/integration/r03/build.sh /absolute/path/to/locked-mihomo
```

This takes the backend lock, refuses tracked changes or an existing output,
builds embedded-frontend static Linux/amd64 CLIs from that full commit, checks
actual versions and ELF headers, and writes nine SHA256SUMS entries under
`.local/stage3-artifacts/<fullcommit>/` with version `r03-<fullcommit>`.
The older b71ee3a artifacts are never overwritten.

T03-R01 is a separately authorized cross-host session. The T03 preflight helper
now accepts `MESHSTRATA_PRODUCT_COMMIT=<fullcommit>` (strict full SHA); its default
remains the original T03 product. Set this consistently for every helper process,
verify the matching manifest/COMMIT/version before uploads, use a fresh private
root and unique phases, and retain the independent Ubuntu/Alpine matrix in R03.md.
This README and the local scenarios do not start or pass T03-R01.
