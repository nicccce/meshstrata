# T01 remote acceptance tools

These are test drivers, not production runtime or installation code. The product
under test is fixed at `f5702078aa48f946fb3c9ee35af65e991a4fc493`.
No product implementation is changed. Read `documents/backend-delivery/T01计划.md`
and the final T01 report before running. Remote execution requires the user's
existing authorization for the two hosts in ignored `.local/test-servers.md`.

Build `scripts/integration/build-stage1.sh` from the clean product commit first.
Build supplemental tests with the same `.local/cache/backend-check.lock`,
`GOMAXPROCS=1 CGO_ENABLED=0`, existing `.local/cache/go` and `go-tmp`, and `-p 1`:

- `go test -p 1 -c -o .local/stage1-artifacts/control.test ./agent/internal/control`
- `go test -p 1 -c -o .local/stage1-artifacts/agent-enrollment.test ./agent/internal/enrollment`
- `go test -p 1 -c -o .local/stage1-artifacts/enrollment.test ./controller/internal/enrollment`
- `go test -p 1 -c -o .local/stage1-artifacts/auth.test ./controller/internal/auth`
- `go test -p 1 -c -o .local/stage1-artifacts/t01-kernel.test ./scripts/integration/t01`

Copy the exact locked Mihomo binary after checking both compressed and binary
SHA256 against `scripts/compat/mihomo/version-lock.json`. Include every executable,
COMMIT and lock file in SHA256SUMS. Do not modify artifacts during archiving.
Create `.local/t01-artifacts.tar.gz` from `.local/stage1-artifacts` (gzip level 1
is sufficient). Neither archive nor executables belong in Git.

Use a unique `meshstrata-t01-YYYYMMDD-HHMM` run name. The drivers create only
`/root/<run>` on the two hosts, with private directories and files. The development
machine retains evidence at `.local/<run>`; raw logs/configs are private. Never
publish the private tree. The only public listeners used are 8443/9443 for the
secure Controller and 28443–28446 for authenticated independent kernel tests.
Check all ports and existing listeners first. No firewall/SSH/package/service
changes are made. TCP/UDP proxy tests send tiny random echo payloads only.

```
python3 scripts/integration/t01/remote.py --run RUN --phase deploy
python3 scripts/integration/t01/remote.py --run RUN --phase modules
python3 scripts/integration/t01/remote.py --run RUN --phase topologies
python3 scripts/integration/t01/extra.py --run RUN --phase faults
python3 scripts/integration/t01/extra.py --run RUN --phase cross
python3 scripts/integration/t01/remote.py --run RUN --phase cleanup
```

Run phases sequentially: they intentionally reuse dedicated ports. Inspect JSON
and first-failure logs after every phase. Module tests use loopback *on each remote
host*; they do not prove cross-host connectivity. Topologies and cross tests use
real public addresses without SSH tunnels. The latter uses a controlled TLS1.3
REALITY target on the proxy server and does not establish external-target support.

`kernel_test.go` derives socket/certificate/SOCKS helpers from the frozen product
compatibility test. It compiles to a standalone CGO=0 test executable and supports
bounded server/client roles, private Unix socket denial, and listener conflict
probes. It does not perform production publication, authorization, or revocation.
The unchanged `b03.test` performs the full loopback revocation/false-success suite.

Supervisors record exact child PIDs and exit codes. Cleanup checks `/proc/PID/exe`
before signalling. Retain failed-state snapshots and private logs with 0700/0600
permissions; stop all test processes and confirm original listeners before ending.

The recorded run used `/root/RUN-alpine` for Alpine, to isolate its independent
transfer from the original slow Ubuntu-first deployment. The original deployment
may also leave an artifact-only `/root/RUN` on Alpine; neither is a system install.
A test-only helper was rebuilt after the initial archive froze. `--phase refresh`
transfers that helper plus the updated full SHA256SUMS and verifies *all* files
before execution. Product Controller/Agent/b03/module executables remain identical.

When public Web ports time out, record that separately and continue local checks:

```
python3 scripts/integration/t01/remote.py --run RUN --phase smoke-local --host alpine --attempt 1
python3 scripts/integration/t01/remote.py --run RUN --phase topologies --management ssh
python3 scripts/integration/t01/network.py --run RUN --host ubuntu
python3 scripts/integration/t01/network.py --run RUN --host alpine
python3 scripts/integration/t01/audit.py --run RUN
```

`smoke-local` runs curl **on the server**, with normal CA/SAN verification and
sensitive config on stdin. It is loopback HTTPS, never public connectivity proof.
The managed topology uses loopback admin/local Agent but requires the other Agent
to use real public 9443. The final smoke also drops the first enrollment response
body, races two independent local identities, and captures the actual Agent's
established control socket inode before removal, requiring it to disappear while
the Agent stays healthy. Node private keys are only read by a probe on their
original host. New local test CSR private keys remain on the build host.

`audit.py` independently scans every `/proc/PID/exe` under the unique test root,
checks no test Unix sockets remain active, verifies SHA256 again, compares normalized
host TCP/UDP listeners, and closes only this run's SSH ControlMaster sockets.
Failed-state directories remain inside the 0700 envelope. A first driver failure
may deliberately retain a child directory with wrong permissions; do not use it
as a valid deployment or silently repair the failure evidence.

Do not transfer proxy test credentials until explicitly authorized for both test
hosts. This run received that authorization after automatic approval rejected
that operation. The private node-key upload approach was cancelled entirely;
`TestNodeEndpoint` instead uses identities in place on the originating server.
