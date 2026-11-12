# Pinned Mihomo compatibility suite

This suite uses the release locked in `version-lock.json` for **both** server
and temporary client processes. It neither installs a service nor touches any
existing kernel process, configuration, or port. No Go kernel library is linked.

Prepare/check only B03 on the frozen I00 baseline, from the workspace root:

```sh
python3 scripts/backend_check.py B03 --base aa993fbf817ed89a9bbe668018a78d3fc49258b9
```

Use the exact absolute snapshot path printed by that command:

```sh
python3 scripts/compat/mihomo/run.py \
  --workspace /root/meshstrata \
  --snapshot /root/meshstrata/.local/backend-check/B03/run-REPLACE
```

The runner acquires the same `.local/cache/backend-check.lock` itself. Do not
wrap it in another flock. It uses `GOMAXPROCS=1`, `CGO_ENABLED=0`, and `go test -p 1`.
The current Linux amd64 release binary must already exist at the locked path,
or be supplied with `--binary /absolute/path`. It is hashed before execution.
Network/socket permission is needed for these bounded loopback tests.

`RUN.json` records the exact baseline, snapshot, command, kernel lock, exit code,
and before/after host-listener comparison. `evidence.json` records per-protocol
results, timestamps, digests, PID-owned sockets, and forwarding verification.
Only these sanitized artifacts and `test.log` are suitable as test evidence.
**The `private/` tree contains real ephemeral private keys, credentials, full
configs, and kernel logs. Keep it private; never commit or publish it.** All
owned child processes are stopped after each case, including failed cases.

The suite checks SS2022, Trojan TLS, VLESS REALITY/Vision, and Hysteria2 with
independent listeners, a controlled TCP echo target, and a separate UDP echo
target. Hysteria2's QUIC socket does not count as proof of business UDP. TLS
clients verify a temporary test certificate; REALITY uses a controlled local
TLS 1.3 target. It also checks credential rotation, old/bad credentials not
forwarding, retained connections, exact listener-attributed cleanup, other
identity impact, and an occupied-port reload that is API-accepted but cannot
pass PID-owned listener inspection. All traffic and payloads are small.

`no_forward` observations are not automatically cryptographic denial evidence:
a timeout alone cannot make revocation Verified. Established-session closure
and the other identity's continued forwarding must be independently checked.
Listener sharing remains disabled for all protocols; these independent-listener
cases do not establish that multi-user sharing is safe.

The adapter accepts a restricted JSON configuration subset (JSON is valid YAML
for the fixed kernel). Its generator currently supports explicit built-in
DIRECT/REJECT rules only. Full routing/DNS/egress compilation, startup checkpoint
atomicity, authorization merges, rollback, scheduling, and outbox are later work.
Do not interpret this suite as integrated Agent or production deployment tests.

T01 must separately test the integrated I01 commit on Ubuntu/systemd and
Alpine/OpenRC, including external REALITY targets, firewall and existing TUN
interaction, IPv6/wildcard listeners, real deployment roles, and remote clients.
For a server without Go, compile the Go test executable on the build host under
the same build lock with `CGO_ENABLED=0 go test -c -o /private/output/b03.test
./agent/internal/mihomo`, transfer that executable plus the pinned kernel, and
run the single opt-in test with `B03_MIHOMO_BINARY` and a fresh private
`B03_ARTIFACT_DIR`. The runner above deliberately enforces the I00 development
snapshot; it does not authorize remote deployment or claim Alpine acceptance.

I01 and later integrated-source verification uses the current working tree explicitly:

```sh
python3 scripts/compat/mihomo/run.py --workspace /root/meshstrata --integrated
```

This records actual HEAD plus a SHA256 over the adapter/test/runner sources,
including uncommitted sources, instead of labeling integrated code as I00.
The original `--snapshot` mode remains for reproducing historical B03 evidence.
T01 scope is stage-1 authentication/enrollment/control and isolated kernel protocol
checks on Ubuntu and Alpine. Full publication/expiry/rollback belongs to T02,
Reverse to T05, three-role systemd/OpenRC installers and frp lifecycle to T07.
