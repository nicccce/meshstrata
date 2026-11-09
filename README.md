# Meshstrata

Meshstrata is a distributed proxy management project. This repository currently
provides a **local development foundation**: an independent Go Controller and
Agent, SQLite migrations, and an embedded Vue workspace.

Node enrollment, mTLS/WebSocket control, authentication, proxy configuration,
Mihomo management, subscriptions, and production installation are not implemented
in this milestone. An Agent health response only confirms the local Agent process.

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
./bin/meshstrata-controller run -config configs/controller.dev.json
```

Open <http://127.0.0.1:8443>. In a separate terminal:

```sh
./bin/meshstrata-agent run -config configs/agent.dev.json
./bin/meshstrata-agent healthcheck -config configs/agent.dev.json
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
bottom of the sidebar. All P01–P10 business pages are explicit placeholders.
Controller information is available only through the optional query button in
Settings; opening or refreshing any page makes no business API/probe request.

For frontend-only work (Controller optional):

```sh
npm --prefix web ci
npm --prefix web run dev
npm --prefix web run check:foundation
npm --prefix web run typecheck
npm --prefix web run build
```

Open `http://127.0.0.1:5173/#/overview`. Demo Controls in the footer provide six
repeatable scenarios and Reset Demo. Settings includes a non-sensitive draft and
cancellable-task sandbox. Explicitly saved drafts survive refresh; secrets stay
in memory. English feature resources live in each feature's `locales/en.ts`.
See [frontend contract](web/FRONTEND_CONTRACT.md) before implementing business pages.

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

Controller supports HTTP on literal loopback IP addresses only. This development
mode differs from the planned production HTTPS endpoint on port 8443. It does not
bind port 9443 or expose node control endpoints.

State directories have mode `0700`. Controller stores `controller.db` using WAL,
foreign keys, a 5-second busy timeout, and one SQL connection. Versioned embedded
migrations run transactionally before HTTP starts. The initial schema contains
only migration history and instance metadata; the instance ID survives restarts.

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
