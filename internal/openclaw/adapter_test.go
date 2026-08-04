package openclaw

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/simons-agent-space/crons/internal/domain"
)

// epochMs is a small Unix millisecond timestamp used to make fixtures
// deterministic. 2026-08-04T12:00:00Z = 1785940800000 ms.
const epochMs int64 = 1785940800000


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
