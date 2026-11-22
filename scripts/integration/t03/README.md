# T03 independent cross-host acceptance

The product is fixed at `b71ee3a094265a91d1e671f8a89ffa45df30c35d`.
These helpers exercise normal Controller/Agent CLIs, administrator HTTPS, node
mTLS, persistent worker publication, and the locked Mihomo v1.19.33. They do
not import Go internals, write SQL, invent observations, or modify product code.

Run from the repository root with `PYTHONDONTWRITEBYTECODE=1`. `preflight.py`
reads only the ignored `.local/test-servers.md` for host addresses and the exact
SSH identity. SSH uses existing trust with `StrictHostKeyChecking=yes`.

1. `python3 scripts/integration/t03/preflight.py` saves read-only host identity,
   service/PID/listener, memory/disk/clock/interface inventories in a new private
   `.local/meshstrata-t03-<UTC-id>/` root, and records a unique remote `/root` root.
2. `python3 scripts/integration/t03/prepare.py PRIVATE_ROOT` uploads the exact
   per-commit artifacts and checks all nine hashes and normal CLI execution.
   It installs Python3 on Alpine only if absent. Transfer is compressed and
   bounded; installation/transfer output stays private. It does not install Go,
   Node, or service units. A transfer retry may replace only this run's files.
3. `python3 scripts/integration/t03/network.py PRIVATE_ROOT` binds only TCP
   8443, 9443, 28443–28445, 38443 and UDP 28446. It checks loopback, peer random-byte and
   build-host fixed-marker echoes, restricting incoming sources to the two hosts,
   actual SSH source and loopback. All probe listeners are then closed.
4. `python3 scripts/integration/t03/run.py PRIVATE_ROOT --controller 0 --phase UNIQUE --scenario matrix`
   uses host A as Controller and host B as node; `--controller 1` reverses roles.
   The client resides on the Controller host, so ingress traffic crosses hosts.
   Every protocol is normally deleted, cleanup-confirmed and archived before
   port reuse. Ordinary HTTPS certificate validation and business leaf pins stay
   enabled; management private keys never leave their owning Controller.
5. Independent `--scenario observe`, `egress` (optionally `--egress-kind socks5`),
   `lifecycle`, and `extended` select bounded cases. `extended` uses three nodes
   across two physical hosts, keeping the extra node on Ubuntu in a separate
   state/socket/port pool. A prerequisite failure stops that scenario and remains
   a failure; subsequent scenarios use fresh state and report their own scope.
6. `python3 scripts/integration/t03/sharing.py PRIVATE_ROOT UNIQUE` checks concurrent
   same-suffix listeners, policy revision isolation and last-reference port release.
   `socks.py PRIVATE_ROOT UNIQUE` is the bounded correct-first control; `restart.py`
   checks exact original task DTO equality and retained traffic across normal restarts.
7. `python3 scripts/integration/t03/collect.py PRIVATE_ROOT` aggregates all phases
   and scans logs after shutdown, including private-material/JSON/base64/hex forms,
   before copying a private log archive.
8. `python3 scripts/integration/t03/audit.py PRIVATE_ROOT` compares original
   host listeners/PIDs and checks owned executables, socket files and OOM counts.
   `--remove-owned-root` removes only the exact recorded remote root after the
   safety audit. It never stops unrelated processes or changes host networking.

Each remote worker is a private SSH stdio session, creates only recorded
children, and verifies PID start identity and command/executable before stopping
its children. It then uses normal `stop-runtime` for each owned Agent. Stale
owned socket files may be removed only after `/proc/net/unix` shows no activity.
No broad process killing, host route/DNS/firewall changes, reboots or system
clock changes occur. Helpers run serially and keep at most one node kernel on
Alpine. Third-party client logs are silent; platform logs are independently
scanned against generated secrets. Raw RPC/configuration/URLs/credentials and
machine mappings remain in 0700/0600 ignored directories. `diagnose.py` copies
process logs only after that phase's generated-secret scan passes.

Actual full configuration is downloaded from standard trusted HTTPS and loaded
with the fixed CLI `-f`; runtime uses TUN-off and an explicit `test.local` hosts
mapping. The original default TUN document is separately checked with `-t`.
This does not establish GUI-native URL import, isolated default TUN, IPv6,
public DNS or three-layer DNS origin. Hy2 outer UDP and V2 business UDP are
reported separately. Native provider fetch/refresh is a separate proxies-only
path with process-local CA trust and explicit resource bootstrap.

See `documents/backend-delivery/T03.md` and `T03-evidence.json` for actual runs,
failures, skips, evidence roots and cleanup. Do not infer that the availability
of a scenario means it was executed or passed. T02 remains partial/interrupted;
formal systemd/OpenRC installation lifecycle remains T07.
