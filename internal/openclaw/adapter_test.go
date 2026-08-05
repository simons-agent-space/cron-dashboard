package openclaw

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simons-agent-space/cron-dashboard/internal/domain"
)

// epochMs is a small Unix millisecond timestamp used to make fixtures
// deterministic. 2026-08-04T12:00:00Z = 1785940800000 ms.
const epochMs int64 = 1785940800000

func TestAdapter_MissingRunLogTableReturnsEmptyHistory(t *testing.T) {
	path := newFixtureDB(t, `
CREATE TABLE cron_jobs (
	store_key TEXT NOT NULL,
	job_id TEXT NOT NULL,
	name TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	schedule_kind TEXT NOT NULL,
	next_run_at_ms INTEGER,
	PRIMARY KEY (store_key, job_id)
);
`, nil)

	adapter := New("file:" + path + "?mode=ro")
	ctx := context.Background()

	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter without run-log table: %v", err)
	}
	defer adapter.Close()

	logs, err := adapter.ListRunLogs(ctx, "job-1", 20)
	if err != nil {
		t.Fatalf("list logs without run-log table: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected empty run history, got %d entries", len(logs))
	}
}

func TestAdapter_EmptyDatabase(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, nil)
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	jobs, err := adapter.ListJobs(ctx)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected empty list, got %d jobs", len(jobs))
	}

	logs, err := adapter.ListRunLogs(ctx, "any", 20)
	if err != nil {
		t.Fatalf("list run logs: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected empty logs, got %d", len(logs))
	}

	if err := adapter.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestAdapter_OneCronJob_FullSchema(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, func(ctx context.Context, db *sql.DB) {
		insertJob(t, db, jobSeed{
			JobID:              "job-1",
			Name:               "nightly-cleanup",
			Description:        "delete stale tmp files",
			Enabled:            1,
			ScheduleKind:       "cron",
			ScheduleExpr:       "0 3 * * *",
			ScheduleTZ:         "Europe/Berlin",
			NextRunAtMS:        epochMs + 3600_000,
			LastRunAtMS:        epochMs - 3600_000,
			LastRunStatus:      "ok",
			LastDurationMS:     1200,
			ConsecutiveErrors:  0,
			LastDeliveryStatus: "delivered",
			UpdatedAt:          epochMs,
			SortOrder:          5,
		})
	})
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	jobs, err := adapter.ListJobs(ctx)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.JobID != "job-1" || j.Name != "nightly-cleanup" {
		t.Fatalf("unexpected job: %+v", j)
	}
	if !j.Enabled {
		t.Fatalf("expected enabled")
	}
	if j.ScheduleKind != "cron" || j.ScheduleExpr != "0 3 * * *" || j.ScheduleTZ != "Europe/Berlin" {
		t.Fatalf("unexpected schedule: %+v", j)
	}
	if j.NextRunAt == nil || !j.NextRunAt.Equal(time.UnixMilli(epochMs+3600_000).UTC()) {
		t.Fatalf("unexpected next run: %v", j.NextRunAt)
	}
	if j.LastRunAt == nil || !j.LastRunAt.Equal(time.UnixMilli(epochMs-3600_000).UTC()) {
		t.Fatalf("unexpected last run: %v", j.LastRunAt)
	}
}

func TestAdapter_OneCronJob_MinimalSchema(t *testing.T) {
	// Minimal schema lacks every optional column. The adapter must still
	// return a Job with empty optional fields rather than erroring.
	path := newFixtureDB(t, minimalSchemaV1, func(ctx context.Context, db *sql.DB) {
		_, err := db.ExecContext(ctx, `
INSERT INTO cron_jobs (store_key, job_id, name, enabled, schedule_kind, next_run_at_ms, updated_at)
VALUES ('default', 'job-min', 'minimal', 1, 'every', ?, ?)`,
			epochMs+60_000, epochMs)
		if err != nil {
			t.Fatalf("insert minimal job: %v", err)
		}
	})
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	jobs, err := adapter.ListJobs(ctx)
	if err != nil {
		t.Fatalf("list jobs on minimal schema: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job on minimal schema, got %d", len(jobs))
	}
	j := jobs[0]
	if !j.Enabled || j.ScheduleKind != "every" {
		t.Fatalf("unexpected job: %+v", j)
	}
	if j.Description != "" || j.LastRunStatus != "" {
		t.Fatalf("expected empty optional fields, got %+v", j)
	}
	if j.NextRunAt == nil {
		t.Fatalf("expected next_run_at to be set even on minimal schema")
	}
}

func TestAdapter_FailedJob(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, func(ctx context.Context, db *sql.DB) {
		insertJob(t, db, jobSeed{
			JobID:              "job-failed",
			Name:               "broken-job",
			Enabled:            1,
			ScheduleKind:       "cron",
			ScheduleExpr:       "*/5 * * * *",
			LastRunStatus:      "failed",
			LastError:          "open /etc/secrets: permission denied",
			LastDurationMS:     420,
			ConsecutiveErrors:  3,
			LastDeliveryStatus: "delivery_failed",
			UpdatedAt:          epochMs,
		})
	})
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	job, err := adapter.GetJob(ctx, "job-failed")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.LastRunStatus != "failed" {
		t.Fatalf("expected status failed, got %q", job.LastRunStatus)
	}
	if job.LastError != "open /etc/secrets: permission denied" {
		t.Fatalf("unexpected last_error: %q", job.LastError)
	}
	if job.ConsecutiveErrors != 3 {
		t.Fatalf("expected consecutive_errors=3, got %d", job.ConsecutiveErrors)
	}
}

func TestAdapter_GetJobMissing(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, nil)
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	_, err := adapter.GetJob(ctx, "does-not-exist")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected domain.ErrNotFound, got %v", err)
	}
}

func TestAdapter_RecentRunHistory(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, func(ctx context.Context, db *sql.DB) {
		insertJob(t, db, jobSeed{
			JobID:        "job-history",
			Name:         "with-history",
			Enabled:      1,
			ScheduleKind: "cron",
			ScheduleExpr: "* * * * *",
			UpdatedAt:    epochMs,
		})
		for i := 1; i <= 5; i++ {
			insertRunLog(t, db, runLogSeed{
				JobID:          "job-history",
				Seq:            int64(i),
				Ts:             epochMs + int64(i)*60_000,
				Status:         "ok",
				DurationMS:     int64(100 * i),
				DeliveryStatus: "delivered",
				NextRunAtMS:    epochMs + int64(i+1)*60_000,
			})
		}
	})
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	logs, err := adapter.ListRunLogs(ctx, "job-history", 20)
	if err != nil {
		t.Fatalf("list run logs: %v", err)
	}
	if len(logs) != 5 {
		t.Fatalf("expected 5 runs, got %d", len(logs))
	}
	for i := 0; i < len(logs)-1; i++ {
		if logs[i].Ts.Before(logs[i+1].Ts) {
			t.Fatalf("expected descending order, got %v then %v", logs[i].Ts, logs[i+1].Ts)
		}
	}
	if logs[0].Seq != 5 {
		t.Fatalf("expected newest seq=5 first, got %d", logs[0].Seq)
	}

	// Limit honored.
	limited, err := adapter.ListRunLogs(ctx, "job-history", 2)
	if err != nil {
		t.Fatalf("list run logs (limit): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected 2 rows limited, got %d", len(limited))
	}
}

func TestAdapter_MultipleJobsOrderedBySortOrderThenName(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, func(ctx context.Context, db *sql.DB) {
		insertJob(t, db, jobSeed{
			JobID: "z", Name: "zebra", Enabled: 1, ScheduleKind: "cron",
			ScheduleExpr: "* * * * *", UpdatedAt: epochMs, SortOrder: 1,
		})
		insertJob(t, db, jobSeed{
			JobID: "a", Name: "alpha", Enabled: 1, ScheduleKind: "cron",
			ScheduleExpr: "* * * * *", UpdatedAt: epochMs, SortOrder: 1,
		})
		insertJob(t, db, jobSeed{
			JobID: "m", Name: "middle", Enabled: 1, ScheduleKind: "cron",
			ScheduleExpr: "* * * * *", UpdatedAt: epochMs, SortOrder: 2,
		})
	})
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	jobs, err := adapter.ListJobs(ctx)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(jobs))
	}
	wantOrder := []string{"a", "z", "m"}
	for i, want := range wantOrder {
		if jobs[i].JobID != want {
			t.Fatalf("position %d: want %q, got %q", i, want, jobs[i].JobID)
		}
	}
}

func TestAdapter_StripsTrailingNewlineFromLastError(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, func(ctx context.Context, db *sql.DB) {
		insertJob(t, db, jobSeed{
			JobID: "job-newline", Name: "with-newline",
			Enabled: 1, ScheduleKind: "cron", ScheduleExpr: "* * * * *",
			LastError: "boom\n", LastRunStatus: "failed", UpdatedAt: epochMs,
		})
	})
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	job, err := adapter.GetJob(ctx, "job-newline")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if strings.Contains(job.LastError, "\n") {
		t.Fatalf("expected trailing newline stripped, got %q", job.LastError)
	}
}

// TestAdapter_RecoversAfterTransientMissingFile proves that a missing
// database file is treated as transient: the first Ping fails, but once
// the file appears the adapter retries and recovers. This is the
// "missing mount arrives later" scenario /healthz must survive.
func TestAdapter_RecoversAfterTransientMissingFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "later.sqlite")
	ctx := context.Background()

	adapter := New("file:" + dbPath + "?mode=ro")

	// First Ping: file doesn't exist, must fail.
	if err := adapter.Ping(ctx); err == nil {
		t.Fatal("expected Ping to fail when DB file is missing")
	}

	// Create the file with the canonical schema.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, fullSchemaV1); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	_ = db.Close()

	// Second Ping: must recover, not be poisoned by the first failure.
	if err := adapter.Ping(ctx); err != nil {
		t.Fatalf("expected Ping to recover, got: %v", err)
	}
}

// TestAdapter_PermanentSchemaErrorIsCached proves that a true schema
// mismatch (missing required column) IS cached permanently: a fixed DB
// at the same path cannot make the adapter succeed. This guards the
// non-sticky policy above from over-correcting.
func TestAdapter_PermanentSchemaErrorIsCached(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bad.sqlite")

	// Write a schema that is missing a required column.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE cron_jobs (job_id TEXT NOT NULL, name TEXT NOT NULL, enabled INTEGER NOT NULL, schedule_kind TEXT NOT NULL);`+
			`CREATE TABLE cron_run_logs (job_id TEXT NOT NULL, seq INTEGER NOT NULL, ts INTEGER NOT NULL);`+
			`PRAGMA user_version = 1;`); err != nil {
		t.Fatalf("create bad schema: %v", err)
	}
	_ = db.Close()

	adapter := New("file:" + dbPath + "?mode=ro")
	if err := adapter.Open(ctx); !IsSchemaError(err) {
		t.Fatalf("expected SchemaError on first Open, got %v", err)
	}

	// Even after fixing the schema, the cached SchemaError must persist.
	db, _ = sql.Open("sqlite", dbPath)
	if _, err := db.ExecContext(ctx,
		`DROP TABLE IF EXISTS cron_jobs;
`+
			`DROP TABLE IF EXISTS cron_run_logs;
`+
			fullSchemaV1); err != nil {
		t.Fatalf("fix schema: %v", err)
	}
	_ = db.Close()

	if err := adapter.Ping(ctx); !IsSchemaError(err) {
		t.Fatalf("expected SchemaError to be cached, got %v", err)
	}
}

// TestAdapter_QueryOnlyBlocksWrites proves that PRAGMA query_only is
// active on the adapter's connection: an INSERT must fail with a
// write-related error. mode=ro alone may not cover all code paths, so
// the explicit pragma is belt-and-braces.
func TestAdapter_QueryOnlyBlocksWrites(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, nil)
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	db := adapter.TestDB()
	_, err := db.ExecContext(ctx, `INSERT INTO cron_jobs
		(store_key, job_id, name, enabled, schedule_kind, created_at_ms, job_json, session_target, wake_mode, payload_kind, updated_at)
		VALUES ('default', 'write-attempt', 'x', 1, 'cron', 0, '{}', 'main', 'next-heartbeat', 'systemEvent', 0)`)
	if err == nil {
		t.Fatal("expected query_only to block INSERT")
	}
	t.Logf("INSERT blocked as expected: %v", err)
}

// TestAdapter_DisplayNameFallback proves that cron_jobs.display_name is
// optional: when present it is read; when absent the dashboard falls
// back to cron_jobs.name.
func TestAdapter_DisplayNameFallback(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, func(ctx context.Context, db *sql.DB) {
		_, err := db.ExecContext(ctx, `INSERT INTO cron_jobs
			(store_key, job_id, name, display_name, enabled, schedule_kind, created_at_ms, job_json, session_target, wake_mode, payload_kind, updated_at)
			VALUES ('default', 'with-dn', 'name-only', 'Pretty Display', 1, 'cron', 0, '{}', 'main', 'next-heartbeat', 'systemEvent', 0)`)
		if err != nil {
			t.Fatalf("insert with display_name: %v", err)
		}
		_, err = db.ExecContext(ctx, `INSERT INTO cron_jobs
			(store_key, job_id, name, enabled, schedule_kind, created_at_ms, job_json, session_target, wake_mode, payload_kind, updated_at)
			VALUES ('default', 'no-dn', 'plain', 1, 'cron', 0, '{}', 'main', 'next-heartbeat', 'systemEvent', 0)`)
		if err != nil {
			t.Fatalf("insert without display_name: %v", err)
		}
	})
	ctx := context.Background()

	adapter := New("file:" + path + "?mode=ro")
	if err := adapter.Open(ctx); err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	defer adapter.Close()

	jobs, err := adapter.ListJobs(ctx)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	var withDN, withoutDN *domain.Job
	for i, j := range jobs {
		switch j.JobID {
		case "with-dn":
			withDN = &jobs[i]
		case "no-dn":
			withoutDN = &jobs[i]
		}
	}
	if withDN == nil || withoutDN == nil {
		t.Fatal("expected both with-dn and no-dn jobs to be returned")
	}
	if withDN.DisplayName != "Pretty Display" {
		t.Errorf("with-dn DisplayName = %q, want %q", withDN.DisplayName, "Pretty Display")
	}
	if withDN.Name != "name-only" {
		t.Errorf("with-dn Name = %q, want %q", withDN.Name, "name-only")
	}
	if withoutDN.DisplayName != "" {
		t.Errorf("no-dn DisplayName = %q, want empty", withoutDN.DisplayName)
	}
	if withoutDN.Name != "plain" {
		t.Errorf("no-dn Name = %q, want %q", withoutDN.Name, "plain")
	}
}
