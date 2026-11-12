# Meshstrata

Meshstrata is a distributed proxy management project. This repository currently
provides a **local development foundation**: an independent Go Controller and
Agent, SQLite migrations, shared v1 contracts, a transactional task ledger foundation,
and an embedded Vue workspace.

I01 implements explicit HTTPS authentication, encrypted CA/node enrollment and
mTLS control. Proxy publication, subscriptions and production installation remain
unimplemented; the Mihomo adapter has isolated opt-in compatibility tests. An Agent health response only confirms the local Agent process.

## Requirements

- Linux; the Agent uses Unix sockets and both processes use advisory file locks.
- Go 1.27.3, Node.js 26.13.0 with npm, and GNU Make.
- sqlc 1.31.1 for generation/checks; Python 3 for generation checks and smoke tests.

Frontend and Go dependency versions are pinned in the manifests and lock files.
SQLite uses `modernc.org/sqlite` without CGO. No running database service, Mihomo,
or systemd service is needed for these development commands.

## Build and run

From the repository root:

```sh
make build
./bin/meshstrata-controller run -config controller/configs/controller.dev.json
```

Open <http://127.0.0.1:8443>. In a separate terminal:

```sh
./bin/meshstrata-agent run -config agent/configs/agent.dev.json
./bin/meshstrata-agent healthcheck -config agent/configs/agent.dev.json
```

`make build` installs locked frontend dependencies with `npm ci`, type-checks and
builds the frontend, and creates `bin/meshstrata-controller` and
`bin/meshstrata-agent` with `CGO_ENABLED=0`. Builds use two Go workers by default.
Override `VERSION`, `GO_PARALLEL`, or `GOMAXPROCS` when needed:

```sh
make build VERSION=0.1.0
```

Each binary can run outside this checkout. Copy it and provide a JSON configuration
with an absolute `state_dir` path. Controller serves its embedded frontend without
Node.js or the source files. Agent does not link Controller's frontend or SQLite.

Both programs default to `run` when no subcommand is supplied. Use `version` for
JSON build information, `check-config -config FILE` to validate configuration
without creating state, or `-h` for help. Exit codes are 0 for success, 1 for a
runtime/health failure, and 2 for invalid command arguments or configuration.
SIGINT and SIGTERM trigger graceful shutdown; each process stops independently.

## Development

Run these targets in separate terminals:

```sh
make dev-controller
make dev-agent
make dev-web
```

The Vite server at <http://127.0.0.1:5173> supports frontend hot reload and proxies
`/api`, `/healthz`, and `/readyz` to Controller at `127.0.0.1:8443`.
If the Controller address changes, update the Vite proxy accordingly.

The frontend now opens an offline **Prototype · Demo data** workspace with hash
URLs, Overview/Nodes/Routes/Client Config/Activity navigation, and Settings at the
bottom of the sidebar. All P01–P10 modules now provide layouts, navigation,
forms, saved drafts and bounded local workflow simulations.
Controller information is available only through the optional query button in
Settings; opening or refreshing any page makes no business API/probe request.

For frontend-only work (Controller optional):

```sh
npm --prefix controller/web ci
npm --prefix controller/web run dev
bash controller/web/scripts/check-pages.sh
```

Open `http://127.0.0.1:5173/#/overview`. Demo Controls in the footer provide six
repeatable scenarios and Reset Demo. The route wizard leads through exit, paths,
options and release review; subscriptions can visit the rule editor and return
with their draft intact. Explicitly saved drafts survive refresh; secrets stay
in memory. English and Simplified Chinese resources live in each feature's
`locales/en.ts` and `locales/zh-CN.ts`. Choose **English / 简体中文** under
**Settings → General → Interface language**; it applies immediately and survives
refresh and Reset Demo. Without a saved choice, the browser language is used.
See the [frontend contract](controller/web/FRONTEND_CONTRACT.md) and
[integration acceptance record](documents/frontend-delivery/集成验收.md) before wiring APIs.

## Configuration and local state

Configuration is a single JSON object. Omitted fields retain defaults; unknown
fields and malformed files are rejected. If `-config` is omitted, all defaults
apply. Relative paths resolve against the process working directory, including
paths inside a configuration file.

| Field | Controller default | Agent default |
| --- | --- | --- |
| `state_dir` | `.local/controller` | `.local/agent` |
| `log_level` | `warn` | `warn` |
| `listen_address` | `127.0.0.1:8443` | Not supported |

Log levels use Go slog syntax (`debug`, `info`, `warn`, `error`, optionally with a
numeric offset). Logs are English JSON on stderr. The development examples use
`info` so startup and shutdown are visible. No file logging or production log
rotation is configured in this milestone.

Without `secure`, Controller supports HTTP on literal loopback IPs only and
exposes no authentication/node write routes or 9443. Explicit secure mode below
enables HTTPS management and an independent node TLS listener.

State directories have mode `0700`. Controller stores `controller.db` using WAL,
foreign keys, a 5-second busy timeout, and one SQL connection. Versioned embedded
migrations run transactionally before HTTP starts. Migration 001 retains instance
metadata; incremental migration 002 adds relational
nodes, DNS/route/egress revisions, listeners/ports/references, rules/DomainSets,
subscriptions/isolated entries, credential references, task/node evidence, measurements,
events and settings. The instance ID survives upgrades and restarts. Migrations003–005 add authentication and node identity persistence. Credential
encryption/authentication are available only in secure mode; business execution
remains future work.

Agent serves health only on `<state_dir>/health.sock` with mode `0600`.
Advisory locks prevent duplicate processes using the same component state. Agent
recovers stale sockets after a crash, refuses to replace non-socket files, and
removes its socket after graceful shutdown. Its health status does not imply
Controller connectivity, enrollment, or Mihomo availability.

`.local/`, `bin/`, frontend dependencies, and generated frontend assets are ignored
by Git. Make places writable Go/npm caches and Go compilation temporary files
under `.local/cache/` by default, avoiding large builds in a small `/tmp` tmpfs.

## HTTP interfaces

| Endpoint | Behavior |
| --- | --- |
| Controller `GET /healthz` | Process liveness; independent of database readiness. |
| Controller `GET /readyz` | Database reachable and migration history current; otherwise HTTP 503 with `STORAGE_NOT_READY`. |
| Controller `GET /api/v1/system` | `component`, `version`, `commit`, `build_time`, and persistent `instance_id`. |
| Agent Unix socket `GET /healthz` | `status` and Agent build information. |

Unknown Controller API paths return HTTP 404 with
`{"error":{"code":"NOT_FOUND"}}`. Unsupported methods on known endpoints return
HTTP 405 with `METHOD_NOT_ALLOWED`. Endpoints also accept HEAD. Missing static
files return HTTP 404; frontend routes use hash history and do not require a server fallback.

Build information defaults to `dev`/`unknown` when using Go directly. Make injects
the version, source commit, and UTC build time. The frontend formats dates using
the active locale.

## Checks and generation

```sh
make check
```

This checks sqlc output consistency without rewriting source files, runs Go tests
and vet, type-checks/builds the frontend, builds both binaries without CGO, and
runs isolated smoke tests. Smoke tests start only the new project binaries on a
temporary loopback port and Unix socket, from outside the source checkout. They
check embedded assets, database integrity and persistence, duplicate startup,
independent lifecycle behavior, health checks, and socket cleanup.

```sh
make generate       # Regenerate committed SQL access code after changing SQL
make test           # Go tests and vet; frontend tooling is not required
make smoke          # Build and exercise standalone binaries
```

Migration files are ordered `NNN_description.sql` starting at 001. Applied
migrations must remain immutable; add a new file for schema changes. Generation
requires the pinned sqlc version for reproducible output.

A clean checkout can compile and test Go code before building the frontend, using
the tracked embed placeholder. Running Controller without embedded `index.html`
fails with `WEB_ASSETS_MISSING`; use `make build` or `make dev-controller` first.

## Design references

- [Product design](documents/Meshstrata_产品设计_v1.0.md) (v1.0).
- [Frontend pages and interaction design](documents/Meshstrata_前端交互设计_v1.0.md).
- [Frontend implementation tasks](documents/Meshstrata_前端实施任务_v1.0.md).
- [Development log and technical decisions](documents/Meshstrata_开发日志.md).

## B00 contracts and delivery boundary

The [v1 contract](controller/api/CONTRACT.md), [OpenAPI](controller/api/openapi.json),
[control message schema](controller/api/control.schema.json) and shared Go types in
`internal/domain`, `internal/control` and `internal/platform` define the next stages.
Reserved operations remain unregistered. The new frontend adapters in
`controller/web/src/data` are inert and are not imported by pages; the existing
prototype and explicit system query remain unchanged.

The formal deployment target includes Ubuntu/systemd and Alpine/OpenRC for all
three roles and optional independent frps/frpc. Production service adapters and
installers are planned for B17, with real two-platform acceptance in T07; they are
not implemented by B00. Servers will run precompiled binaries without Go/Node.js.
Static/musl compatibility of Meshstrata and pinned external binaries must be verified.
See the [complete stage plan](documents/Meshstrata_剩余开发流程_v1.0.md) and
[B00 evidence](documents/backend-delivery/B00.md).

## Stage 1 secure runtime (I01)

Authentication, encrypted Controller credentials/CA, node enrollment, renewal and
mTLS WebSocket control are now implemented in **explicit secure mode**. The offline
frontend remains a prototype; use the API/CLI below. Publication, local expiry,
subscriptions, Reverse and production installers remain later stages.

Prepare a Controller JSON with an absolute `state_dir`, `listen_address`, and:

```json
{"secure":{
  "tls_cert_file":"/absolute/private/web-cert.pem",
  "tls_key_file":"/absolute/private/web-key.pem",
  "web_ca_file":"/absolute/private/web-ca.pem",
  "allowed_origins":["https://127.0.0.1:8443"],
  "current_key_id":"v1",
  "key_files":{"v1":"/absolute/private/master.key"},
  "bootstrap_file":"/absolute/private/bootstrap.json",
  "control_listen_address":"127.0.0.1:9443",
  "controller_url":"https://127.0.0.1:9443",
  "server_names":["127.0.0.1"]
}}
```

All sensitive inputs use owned 0600 files with immediate 0700 parent; no symlink
components. Ordinary JSON contains paths/versions/public metadata only. For local
trust testing, `meshstrata-controller prepare-local -config FILE` creates the new
private directory and the exact canonical files above, plus `bootstrap-proof`.
Its web leaf lasts 24 hours, proof 30 minutes; it never replaces existing files.
Configure imported HTTPS certificate/key paths for durable deployment. Include all
web/control hostnames in `server_names` when using prepare-local. Keep historical
master key versions available; never regenerate missing keys against an existing DB.

Start `meshstrata-controller run -config FILE`, then run
`meshstrata-controller initialize-admin -config FILE` with stdin JSON
`{"username":"admin","password":"a private password of at least 12 bytes"}`.
Supply real input through a private file/stdin, not argv or shell history. The CLI
verifies HTTPS with `web_ca_file` and sends the local bootstrap proof. After success,
remove the proof file and omit `bootstrap_file` for ordinary restarts. Expired proof
files disable initialization, preserving existing authentication.

Login using `POST /api/v1/auth/login` with Origin and JSON credentials. Read
`GET /api/v1/auth/session` for CSRF; node writes require cookie, matching Origin and
`X-CSRF-Token`. `POST /api/v1/nodes` returns sensitive one-time materials: node_id,
controller_url, CA PEM, optional fingerprint and token. See OpenAPI for exact inputs.
Store returned CA/token in private files, transfer via a trusted channel, then use:

```json
{"state_dir":"/absolute/agent","control":{
  "mode":"control_only","controller_url":"https://controller.example:9443",
  "ca_file":"/absolute/private/ca.pem","node_id":"node_REPLACE",
  "token_file":"/absolute/private/token"
}}
```

Run `meshstrata-agent enroll -config FILE`, then `meshstrata-agent run -config FILE`.
Remove `token_file` and its contents after successful enrollment; retries with the
same persisted identity/CSR recover safely. CA PEM is required; a fingerprint alone
is insufficient. Agent renews valid certificates and reconnects independently.

`control_only` accepts only persisted pristine stage-1 state (initial epoch 1,
no applied config or pending result). Unknown/business state fails closed. Commands
and receipts are rejected with no success result. Health confirms only local process;
node `control=online` confirms control presence; proxy `verification=not_checked`
remains separate. Removal disconnects/revokes management identity but leaves actual
proxy cleanup pending for B04/B05. Neither program starts/stops Mihomo or frp.

Reproduce local integration after building with `python3 scripts/integration/stage1.py`.
It uses ordinary binaries, temporary trusted CAs, real auth/cipher/single database,
TLS/mTLS, restart/reconnect, renewal, removal and failure cleanup; private evidence
stays in `.local/i01-*/private`. Run the pinned kernel suite with
`python3 scripts/compat/mihomo/run.py --workspace "$PWD" --integrated`.
`scripts/integration/build-stage1.sh` prepares CGO=0 binaries and the opt-in test
executable for servers without Go/Node. See the [T01 plan](documents/backend-delivery/T01计划.md).
