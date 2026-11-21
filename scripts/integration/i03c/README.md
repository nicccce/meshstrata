# I03-C local CLI acceptance

Run `local.py` with explicitly built ordinary Controller/Agent binaries, the fixed
Mihomo binary and an evidence output under `.local`. No dependency downloads,
SSH, SQL fixture injection, host TUN/DNS/routes/firewall changes or T02 files.
The script creates a private short temporary root, separate Controller/Agent
states and managed kernels, controlled HTTP/echo/proxy/TLS endpoints and its own
client processes. It terminates only its recorded children and uses the normal
Agent stop-runtime command. Secrets are stdin/private files and process memory.

`--scenario all` runs four separate client ingress protocol cases with real
cleanup, mixed two/three-hop routes, authenticated HTTP/SOCKS5 terminal proxies
and subscription lifecycle. `matrix`, `egress` and `lifecycle` can reproduce a
specific failure independently. `restart` is a bounded one-node diagnostic for
profile adoption followed by a committed renewal and Controller restart; evidence names the scenario, so partial runs
must not be counted as all. Offline expiry must reject actual forwarding within the runtime gate's existing
12-second reconciliation bound; it does not claim instantaneous enforcement.
The protocol matrix tests TCP through outer Hy2
QUIC; it never labels business UDP verified.

Full HTTPS configuration is fetched with ordinary CA verification and then
passed to the real CLI `-f` file option. The unmodified default TUN document is
checked with `-t`; actual requests disable TUN, use an isolated mixed-port and
an explicit test.local hosts mapping. This is not GUI-native full URL import.
The lifecycle scenario separately tests Mihomo's native HTTPS proxy-provider
fetch (with a process-local trusted CA and explicit DIRECT bootstrap for the resource fetch), which is a distinct proxies-only output.

The evidence contains labels, pinned binary digest and test mode only. It never
contains capability URLs, token hashes, credentials, private material or raw
configuration. The native provider client uses silent logging because third-party HTTP errors can contain its capability URL. Real traffic assertions stay enabled. Private temporary content is removed. Sanitized process logs are
scanned for all generated secrets before any failure diagnostics are copied to
`.local/i03c-last-logs`; a detected leak prevents that copy. The script does not
modify existing deployments, spawn a T03 run or claim cross-machine evidence.

After committing the reviewed source and sanitized `I03-C-evidence.json`, run
`bash scripts/integration/i03c/build.sh /absolute/path/to/pinned-mihomo`.
The builder takes the backend lock, embeds the normal frontend, sets full commit
and version on both static Linux amd64 CLIs and writes a per-commit manifest and
SHA256SUMS. It refuses tracked source changes and an existing output directory.

The pinned kernel's process-local provider CA option is `tls.custom-certifactes`
(the spelling belongs to v1.19.33); see its [configuration source](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/config/config.go#L369)
and [certificate installation before provider loading](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/hub/executor/executor.go#L86).
Only the isolated native-provider client adds this CA and a direct resource-fetch
bootstrap. Proxy traffic remains selected through the downloaded entry.
