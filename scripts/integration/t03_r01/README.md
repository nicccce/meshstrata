# T03-R01 independent fixed-release cross-host retest

This suite requires `MESHSTRATA_PRODUCT_COMMIT=a9398959b19cc5ffa0ab71e8a03efbe73aa14aa8`
and `PYTHONDONTWRITEBYTECODE=1`. It reuses T03 normal CLI/API/Mihomo and wire
helpers. It never builds/replaces products, writes SQL, changes host networking,
or touches T02. Raw evidence is private and ignored; public output is stage labels.
`preflight.py` creates a unique `meshstrata-t03-r01-<UTC>` local root and matching
`/root/ms-t03-r01-<UTC>` remote root, checks all nine local artifact hashes and
full CLI commits, and inventories both hosts. Existing T03 `prepare.py` and
`network.py` accept that root. The additional Ubuntu 29443–29445 pool is checked
before use. Remote drivers explicitly inherit the fixed product commit.

Execute the following cases **serially, once per fresh state**, in order. A failure
stops its dependent branch; another listed fresh case remains independent.

| Phase | Controller / node | Scenario | Required gate |
| --- | --- | --- | --- |
| r01-observe | Alpine / Ubuntu | observe | cold inventory ≤55s; admin bad auth/new Pin; background refresh; normal reconnect |
| r02-observe | Ubuntu / Alpine | observe | same independent direction |
| r03-correct | Alpine / Ubuntu | correct-first | SOCKS5 positive echo + negative auth + exact Pin/current ≤30s; real TCP; stop exit/canary |
| r04-correct | Ubuntu / Alpine | correct-first | same fresh direction |
| r05-sequence | Alpine / Ubuntu | sequence | HTTP real TCP → stop → cleanup ≤90s/archive → SOCKS5 wrong/new correct Pin/current → real TCP/stop |
| r06-sequence | Ubuntu / Alpine | sequence | same independent direction; old immutable route history |
| r07-restart | Alpine / Ubuntu | restart | Node Default prepared → real task/confirmation ≤90s; exact task/artifact/confirmed facts; retained + new TCP after both restarts |
| r08-restart | Ubuntu / Alpine | restart | same independent direction |
| r09-history | Ubuntu / Alpine first node | history | original all publication history, four protocols/wrongpin, mixed 2/3 hops, both exits, provider, renewal/readd/rules/restart, offline expiry ≤12s, delete cleanup ≤90s |
| r10-history | Alpine / Ubuntu first node | history | independent Ubuntu first-node control; three nodes/two hosts, third node on Ubuntu |
| r11-sharing | Ubuntu / three nodes | sharing | same suffix/one listener; different exact policy revision/two listeners; active Pin retained; last reference port release |
| r12-sharing | Alpine / three nodes | sharing | independent Controller direction of same topology |

Example: `python3 scripts/integration/t03_r01/run.py PRIVATE_ROOT --phase r01-observe --controller 1 --scenario observe`.
No phase is passed merely because this command exists. See T03-R01 evidence for
actual execution and partial failures. Observe admission retries only known
BUSY/BACKOFF/UNAVAILABLE within the original total30s; route review stays50s,
publication/confirmation/cleanup90s, first inventory55s. Deterministic late-reply
coverage belongs to R03 local tests unless naturally seen across hosts.

`run.py` snapshots every tool hash/source before each phase. `remote.py` adds
independent target/64-byte echo liveness and read-only private runtime file Pin
presence/hash proofs to the unchanged T03 worker. API timeline DTOs are private;
no capability URL, credentials, configuration or raw RPC is public evidence.
Normal Controller/Agent log level is info to correlate safe observation IDs.
The client remains locked Mihomo v1.19.33, `-f`, TUN off, IPv4 and explicit hosts.
Default TUN `-t` is only syntax validation. Outer Hy2 UDP is not business UDP.

Finish each phase with recorded PID identity/command checks and normal
`stop-runtime`. After every phase has stopped, use this suite's `collect.py` for aggregate
literal/JSON/base64/hex secret scanning and private log archives, then this
suite's `audit.py PRIVATE_ROOT --remove-owned-root` for all nine hashes,
baseline listeners/OOM/owned process/socket checks and exact-root deletion.
Do not remove a remote root with active resources. Preserve local raw evidence.

During this actual run r11 reached sharing/Pin isolation/fork-port release,
then a driver loop shadowed subscription objects with port lists. It is
incomplete due to a test error. Only explicit `suite.py ... --start 13 --end 13`
dispatches one fresh `r13-sharing-fixed` repair. Default suite end remains12.
r12 failed earlier at actual first-subscription traffic and never reached the
shadowing bug; it is preserved and is not rerun. `network_extra.py` diagnoses
only the designated Ubuntu29443–29445 pool after all product phases stop,
without replaying the failed product case.

`collect.py` also scans local helper logs and aggregates locally recorded API
secrets, including JSON/base64/base64url/hex forms. `proofs.py` exports only
whitelisted observation identifiers/stages/codes after final scan. Read-only
runtime proofs query the owned kernel's authenticated Unix API `/proxies` and
verify its SO_PEERCRED PID; process `-f` content is startup evidence only.
