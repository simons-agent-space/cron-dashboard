package openclaw

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	// modernc.org/sqlite is the production driver; tests share it so the
	// fixture database format matches what the adapter reads.
	_ "modernc.org/sqlite"
)

// fullSchemaV1 is the canonical OpenClaw schema version 1 used to build
// test fixtures. It is the verbatim form of the schema crons is built
// against; columns the adapter does not select are simply ignored.
const fullSchemaV1 = `
CREATE TABLE cron_jobs (
	store_key TEXT NOT NULL,
	job_id TEXT NOT NULL,
	declaration_key TEXT,
	display_name TEXT,
	owner_agent_id TEXT,
	owner_session_key TEXT,
	name TEXT NOT NULL,
	description TEXT,
	enabled INTEGER NOT NULL,
	delete_after_run INTEGER,
	created_at_ms INTEGER NOT NULL,
	agent_id TEXT,
	session_key TEXT,
	schedule_kind TEXT NOT NULL,
	schedule_expr TEXT,
	schedule_tz TEXT,
	every_ms INTEGER,
	anchor_ms INTEGER,
	at TEXT,
	stagger_ms INTEGER,
	session_target TEXT NOT NULL,
	wake_mode TEXT NOT NULL,
	trigger_script TEXT,
	trigger_once INTEGER,
	payload_kind TEXT NOT NULL,
	payload_message TEXT,
	payload_model TEXT,
	payload_fallbacks_json TEXT,
	payload_thinking TEXT,
	payload_timeout_seconds INTEGER,
	payload_allow_unsafe_external_content INTEGER,
	payload_external_content_source_json TEXT,
	payload_light_context INTEGER,
	payload_tools_allow_json TEXT,
	payload_tools_allow_is_default INTEGER,
	delivery_mode TEXT,
	delivery_channel TEXT,
	delivery_to TEXT,
	delivery_thread_id TEXT,
	delivery_thread_id_type TEXT,
	delivery_account_id TEXT,
	delivery_best_effort INTEGER,
	delivery_completion_mode TEXT,
	delivery_completion_to TEXT,
	failure_delivery_mode TEXT,
	failure_delivery_channel TEXT,
	failure_delivery_to TEXT,
	failure_delivery_account_id TEXT,
	failure_alert_disabled INTEGER,
	failure_alert_after INTEGER,
	failure_alert_channel TEXT,
	failure_alert_to TEXT,
	failure_alert_cooldown_ms INTEGER,
	failure_alert_include_skipped INTEGER,
	failure_alert_mode TEXT,
	failure_alert_account_id TEXT,
	next_run_at_ms INTEGER,
	running_at_ms INTEGER,
	last_run_at_ms INTEGER,
	last_run_status TEXT,
	last_error TEXT,
	last_duration_ms INTEGER,
	consecutive_errors INTEGER,
	consecutive_skipped INTEGER,
	schedule_error_count INTEGER,
	last_delivery_status TEXT,
	last_delivery_error TEXT,
	last_delivered INTEGER,
	last_failure_alert_at_ms INTEGER,
	job_json TEXT NOT NULL,
	state_json TEXT NOT NULL DEFAULT '{}',
	runtime_updated_at_ms INTEGER,
	schedule_identity TEXT,
	sort_order INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (store_key, job_id)
);

CREATE TABLE cron_run_logs (
	store_key TEXT NOT NULL,
	job_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	ts INTEGER NOT NULL,
	status TEXT,
	error TEXT,
	summary TEXT,
	diagnostics_summary TEXT,
	delivery_status TEXT,
	delivery_error TEXT,
	delivered INTEGER,
	session_id TEXT,
	session_key TEXT,
	run_id TEXT,
	run_at_ms INTEGER,
	duration_ms INTEGER,
	next_run_at_ms INTEGER,
	model TEXT,
	provider TEXT,
	total_tokens INTEGER,
	entry_json TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (store_key, job_id, seq)
);
`

// minimalSchemaV1 contains only the required columns plus a tiny handful of
// optional ones. It exists to prove the adapter handles databases that do
// not have the full column set.
const minimalSchemaV1 = `
CREATE TABLE cron_jobs (
	store_key TEXT NOT NULL,
	job_id TEXT NOT NULL,
	name TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	schedule_kind TEXT NOT NULL,
	next_run_at_ms INTEGER,
	updated_at INTEGER,
	PRIMARY KEY (store_key, job_id)
);

CREATE TABLE cron_run_logs (
	store_key TEXT NOT NULL,
	job_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	ts INTEGER NOT NULL,
	PRIMARY KEY (store_key, job_id, seq)
);
`

// newFixtureDB creates a fresh SQLite database in a temp directory, applies
// the given schema, and runs the optional populate callback. The returned
// path is suitable for opening with the adapter in read-only mode.
func newFixtureDB(t *testing.T, schema string, populate func(context.Context, *sql.DB)) string {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "fixture.sqlite")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()

	if schema != "" {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	if populate != nil {
		populate(ctx, db)
	}
	return dbPath
}

// jobSeed is the minimum row needed to populate a cron_jobs entry in a
// fixture. Tests only set the fields they care about; the rest stay zero
// (with sensible defaults applied for the NOT NULL columns that are not
// interesting to the dashboard).
type jobSeed struct {
	JobID              string
	Name               string
	Description        string
	Enabled            int
	ScheduleKind       string
	ScheduleExpr       string
	ScheduleTZ         string
	EveryMS            int64
	At                 string
	NextRunAtMS        int64
	RunningAtMS        int64
	LastRunAtMS        int64
	LastRunStatus      string
	LastError          string
	LastDurationMS     int64
	ConsecutiveErrors  int64
	LastDeliveryStatus string
	UpdatedAt          int64
	CreatedAtMS        int64
	SortOrder          int
	StoreKey           string
}

// insertJob writes one jobSeed into a fixture database built from
// fullSchemaV1. It supplies sensible defaults for the NOT NULL columns the
// dashboard does not read (created_at_ms, job_json, payload_kind,
// session_target, state_json, wake_mode).
func insertJob(t *testing.T, db *sql.DB, j jobSeed) {
	t.Helper()
	if j.StoreKey == "" {
		j.StoreKey = "default"
	}
	if j.CreatedAtMS == 0 {
		j.CreatedAtMS = j.UpdatedAt
	}
	_, err := db.Exec(`
INSERT INTO cron_jobs (
	store_key, job_id, name, description, enabled,
	schedule_kind, schedule_expr, schedule_tz, every_ms, at,
	next_run_at_ms, running_at_ms, last_run_at_ms, last_run_status,
	last_error, last_duration_ms, consecutive_errors, last_delivery_status,
	job_json, state_json, sort_order, updated_at,
	created_at_ms, session_target, wake_mode, payload_kind
) VALUES (
	?, ?, ?, ?, ?,
	?, ?, ?, ?, ?,
	?, ?, ?, ?,
	?, ?, ?, ?,
	'{}', '{}', ?, ?,
	?, 'main', 'next-heartbeat', 'systemEvent'
)`, j.StoreKey, j.JobID, j.Name, j.Description, j.Enabled,
		j.ScheduleKind, j.ScheduleExpr, j.ScheduleTZ, j.EveryMS, j.At,
		j.NextRunAtMS, j.RunningAtMS, j.LastRunAtMS, j.LastRunStatus,
		j.LastError, j.LastDurationMS, j.ConsecutiveErrors, j.LastDeliveryStatus,
		j.SortOrder, j.UpdatedAt, j.CreatedAtMS)
	if err != nil {
		t.Fatalf("insert job %q: %v", j.JobID, err)
	}
}

// runLogSeed is the minimum row needed to populate a cron_run_logs entry.
type runLogSeed struct {
	JobID          string
	Seq            int64
	Ts             int64
	Status         string
	Error          string
	Summary        string
	DeliveryStatus string
	DurationMS     int64
	NextRunAtMS    int64
	StoreKey       string
}

// insertRunLog writes one runLogSeed into the fixture database.
func insertRunLog(t *testing.T, db *sql.DB, l runLogSeed) {
	t.Helper()
	if l.StoreKey == "" {
		l.StoreKey = "default"
	}
	_, err := db.Exec(`
INSERT INTO cron_run_logs (
	store_key, job_id, seq, ts, status, error, summary,
	delivery_status, duration_ms, next_run_at_ms,
	entry_json, created_at
) VALUES (
	?, ?, ?, ?, ?, ?, ?,
	?, ?, ?,
	'{}', ?
)`, l.StoreKey, l.JobID, l.Seq, l.Ts, l.Status, l.Error, l.Summary,
		l.DeliveryStatus, l.DurationMS, l.NextRunAtMS, l.Ts)
	if err != nil {
		t.Fatalf("insert run log %q/%d: %v", l.JobID, l.Seq, err)
	}
}
