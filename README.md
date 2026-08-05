# crons

A small read-only web dashboard for OpenClaw cron jobs. Built in Go,
server-rendered HTML, plain CSS, no JavaScript, no frontend framework,
no database writes.

The dashboard reads the OpenClaw SQLite state file and renders:

* An index of every cron job with its enabled state, schedule, next run,
  last run, last error, duration, consecutive errors, and delivery status.
* A per-job detail page with recent run history when the database provides it.

Payload messages, delivery recipients, account IDs, session keys, and the
`job_json` / `state_json` blobs are deliberately not exposed.

## Configuration

| Environment variable   | Default                                 | Purpose                                                                      |
| ---------------------- | --------------------------------------- | ---------------------------------------------------------------------------- |
| `OPENCLAW_DB_PATH`     | `/data/openclaw-state/openclaw.sqlite` | Path to the OpenClaw SQLite database.                                        |
| `BASIC_AUTH_USER`      | unset                                   | When set together with `BASIC_AUTH_PASSWORD`, requires HTTP Basic Auth.      |
| `BASIC_AUTH_PASSWORD`  | unset                                   | When set together with `BASIC_AUTH_USER`, requires HTTP Basic Auth.          |
| `HTTP_ADDR`            | `:8080`                                 | HTTP listen address.                                                         |

`/healthz` is always public so container and external probes can reach
it. Every other route requires Basic Auth when both auth variables are
set.

If only one of the auth variables is set the process refuses to start.
If neither is set, a warning is logged and authentication is disabled.

## Endpoints

* `GET /` — overview table of every job (HTML).
* `GET /jobs/{job_id}` — per-job detail page with recent run history (HTML).
  A missing job renders an HTML 404 page with status 404.
* `GET /healthz` — JSON health check. `200 {"status":"ok"}` when the
  database can be opened and queried; `503 {"status":"degraded"}`
  otherwise.

## Architecture

```
cmd/crons                  # main entrypoint, env wiring, signal handling
internal/domain            # plain types + Repository interface
internal/openclaw          # SQLite + schema adapter (the only place
                           # that knows about the OpenClaw schema)
internal/server            # HTTP handlers, auth middleware, html/template
internal/server/templates  # layout.html, index.html, job.html, notfound.html
                           # (embedded into the binary via //go:embed)
```

The `domain` package has no SQL knowledge. The `openclaw` package is the
only place that knows about the OpenClaw schema. The `server` package
talks only to the `domain.Repository` interface.

### Schema adapter

The adapter verifies that the database has the required tables and
columns:

* `cron_jobs` requires: `job_id`, `name`, `enabled`, `schedule_kind`,
  `next_run_at_ms`.
* `cron_run_logs` is optional. When present, it requires `job_id`, `seq`, and `ts`.

Unknown additional columns are tolerated — the adapter only `SELECT`s
the columns it knows about. The schema version is read from
`PRAGMA user_version`; when the value differs from the version this
build is pinned to, the adapter logs a warning but still starts.

## Development

```
go test ./...
go build ./cmd/crons
```

Tests use `t.TempDir()` to create throwaway SQLite databases with the
fixture schema. The `modernc.org/sqlite` driver is CGO-free, so no C
toolchain is needed.

## Deployment

The production runtime is defined in `compose.yaml`.

It provides:

* a localhost-only port binding
* required Basic Auth credentials
* a read-only bind mount of the live OpenClaw state directory
* a read-only container filesystem
* dropped Linux capabilities
* `no-new-privileges`
* a health check that queries the public `/healthz` endpoint

Required production variables:

    BASIC_AUTH_USER=...
    BASIC_AUTH_PASSWORD=...

Optional variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `CRONS_HOST_PORT` | `18080` | Host loopback port forwarded to container port 8080. |
| `CRONS_IMAGE_TAG` | `local` | Image tag used by Docker Compose. |
| `OPENCLAW_STATE_DIR` | `/srv/openclaw/state/state` | Host directory containing the SQLite database and WAL files. |

Validate and start locally:

    BASIC_AUTH_USER=admin \
    BASIC_AUTH_PASSWORD=development-only \
    docker compose config --quiet

    BASIC_AUTH_USER=admin \
    BASIC_AUTH_PASSWORD=development-only \
    docker compose up --build

Mounting the directory rather than only `openclaw.sqlite` is required because
the database uses WAL mode and the matching `-wal` and `-shm` files must be
available.

The application opens SQLite with `mode=ro`, enables `PRAGMA query_only`, and
receives the state directory through a read-only bind mount.

### Production delivery

Application changes follow this path:

1. an agent or developer creates a feature branch
2. a pull request runs required CI
3. Simon reviews and merges into protected `main`
4. GitHub Actions sends the exact merged commit SHA to the VPS
5. a restricted service-specific script verifies that the SHA belongs to
   `origin/main`
6. the script checks out that exact SHA and runs Docker Compose
7. deployment succeeds only after the Compose health check passes

The application repository does not contain VPS credentials, production
secrets, dynamic Caddy configuration, or direct deployment authority.

Caddy is configured statically on the VPS to proxy the public hostname to the
localhost port defined by `CRONS_HOST_PORT`.

## License

MIT.
