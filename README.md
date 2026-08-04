# crons

A small read-only web dashboard for OpenClaw cron jobs. Built in Go,
server-rendered HTML, plain CSS, no JavaScript, no frontend framework,
no database writes.

The dashboard reads the OpenClaw SQLite state file and renders:

* An index of every cron job with its enabled state, schedule, next run,
  last run, last error, duration, consecutive errors, and delivery status.
* A per-job detail page with the most recent run history.

Payload messages, delivery recipients, account IDs, session keys, and the
`job_json` / `state_json` blobs are deliberately not exposed.

## Configuration

| Environment variable   | Default                                 | Purpose                                                                      |
| ---------------------- | --------------------------------------- | ---------------------------------------------------------------------------- |
| `OPENCLAW_DB_PATH`     | `/data/openclaw-state/openclaw.sqlite` | Path to the OpenClaw SQLite database.                                        |
| `BASIC_AUTH_USER`      | unset                                   | When set together with `BASIC_AUTH_PASSWORD`, requires HTTP Basic Auth.      |
| `BASIC_AUTH_PASSWORD`  | unset                                   | When set together with `BASIC_AUTH_USER`, requires HTTP Basic Auth.          |
| `HTTP_ADDR`            | `:8080`                                 | HTTP listen address.                                                         |

`/healthz` is always public so container and agentctld probes can reach
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
* `cron_run_logs` requires: `job_id`, `seq`, `ts`.

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

Built as a distroless `scratch`-style image. The data directory is
mounted read-only:

```
-v /srv/openclaw/state/state:/data/openclaw-state:ro
```

Mounting the **directory** (not just the `.sqlite` file) is required —
the database runs in WAL mode and needs the matching `-wal` and `-shm`
files to read the current state safely.

`deploy.json` is the agentctld manifest:

```json
{
  "version": 2,
  "app": "crons",
  "container_port": 8080,
  "health_path": "/healthz",
  "data": {
    "mount": "/data/openclaw-state",
    "read_only": true
  }
}
```

## Limitations

* **agentctld host-directory mount:** the manifest declares a read-only
  mount of `/data/openclaw-state`. The current agentctld contract only
  supports mounting a fresh container-specific directory; expressing an
  *existing* host directory (`/srv/openclaw/state/state`) is a known
  limitation that needs a manifest or daemon extension. See the build
  report.

## License

MIT.
