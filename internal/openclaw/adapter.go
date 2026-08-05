package openclaw

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/simons-agent-space/cron-dashboard/internal/domain"

	// Register the modernc.org/sqlite driver under the name "sqlite" so
	// sql.Open("sqlite", ...) works in the production binary. The tests
	// already do this through the fixtures; the production code must do
	// it too.
	_ "modernc.org/sqlite"
)

// Adapter is the production domain.Repository backed by an OpenClaw SQLite
// database. It is lazy: the database file is opened and the schema is
// verified on the first operation, so the binary can start before the
// data directory is mounted (and /healthz can report the degraded state
// until it is).
//
// Error policy: only permanent errors are cached. The only permanent
// error today is a schema mismatch (SchemaError); everything else is
// treated as transient — a missing mount or an I/O error returns the
// failure to the caller but does not poison the adapter. The next
// call retries from scratch (sql.Open + schema verify). When Ping
// fails on an already-open connection, the connection is dropped so
// the next call re-opens the database.
type Adapter struct {
	dsn string

	// Detected column presence for optional fields. Populated at Open() and
	// used to build SELECTs that only reference columns the database
	// actually has. Missing optional columns are tolerated; unknown extra
	// columns are ignored.
	jobCols map[string]bool
	runCols map[string]bool

	mu        sync.Mutex
	db        *sql.DB
	schemaErr *SchemaError // permanent: missing required columns
}

// New stores the DSN for later use. It does not touch the filesystem.
// Use Open or call any domain.Repository method to actually open the
// database.
func New(dsn string) *Adapter {
	return &Adapter{
		dsn:     dsn,
		jobCols: map[string]bool{},
		runCols: map[string]bool{},
	}
}

// Open eagerly opens the database and verifies the schema. It returns a
// SchemaError when required columns are missing and a generic error for
// other failures. The handler can be wrapped in a goroutine at startup
// to verify the database while still serving /healthz.
func (a *Adapter) Open(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.openLocked(ctx)
}

// openLocked is the inner open that callers must invoke while holding a.mu.
func (a *Adapter) openLocked(ctx context.Context) error {
	if a.db != nil {
		return nil
	}
	// Only the permanent schema error is cached. Every other failure is
	// returned to the caller and the next call retries from scratch.
	if a.schemaErr != nil {
		return a.schemaErr
	}
	db, err := sql.Open("sqlite", a.dsn)
	if err != nil {
		return fmt.Errorf("openclaw: open database: %w", err)
	}
	// SQLite is single-writer; ensuring a single connection avoids
	// shared-cache pitfalls in read-only mode.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Belt and braces: the DSN already requests mode=ro, but PRAGMA
	// query_only makes any accidental write fail loudly regardless of
	// how the connection was reached (e.g. by an unrelated migration
	// script sharing the file).
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		_ = db.Close()
		return fmt.Errorf("openclaw: enable query_only: %w", err)
	}

	schema, err := VerifySchema(ctx, db)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("openclaw: verify schema: %w", err)
	}
	if !schema.Compatible() {
		_ = db.Close()
		a.schemaErr = &SchemaError{Missing: schema.MissingRequired}
		return a.schemaErr
	}

	// Record which optional columns the schema actually has so SELECTs can
	// be tailored per database.
	for _, c := range schema.FoundColumns["cron_jobs"] {
		a.jobCols[c] = true
	}
	for _, c := range schema.FoundColumns["cron_run_logs"] {
		a.runCols[c] = true
	}

	a.db = db
	return nil
}

// Close releases the underlying database handle.
func (a *Adapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.db == nil {
		return nil
	}
	err := a.db.Close()
	a.db = nil
	return err
}

// Ping reports whether the database is reachable and queryable. It also
// triggers the lazy open on the first call.
func (a *Adapter) Ping(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.openLocked(ctx); err != nil {
		return err
	}
	var n int
	if err := a.db.QueryRowContext(ctx, "SELECT 1").Scan(&n); err != nil {
		// Connection went bad (db file replaced, I/O error, etc.).
		// Drop it so the next call retries from sql.Open.
		_ = a.db.Close()
		a.db = nil
		return fmt.Errorf("openclaw: ping: %w", err)
	}
	if n != 1 {
		_ = a.db.Close()
		a.db = nil
		return fmt.Errorf("openclaw: ping returned %d, expected 1", n)
	}
	return nil
}

// ListJobs returns every job in storage order.
func (a *Adapter) ListJobs(ctx context.Context) ([]domain.Job, error) {
	db, err := a.dbOrError(ctx)
	if err != nil {
		return nil, err
	}
	sqlStr, present := a.listJobsSQL()
	rows, err := db.QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, fmt.Errorf("openclaw: list jobs: %w", err)
	}
	defer rows.Close()

	jobs := []domain.Job{}
	for rows.Next() {
		j, err := scanJob(rows, present)
		if err != nil {
			return nil, fmt.Errorf("openclaw: scan job: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("openclaw: list jobs: %w", err)
	}
	return jobs, nil
}

// GetJob returns the job with the given id.
func (a *Adapter) GetJob(ctx context.Context, jobID string) (*domain.Job, error) {
	db, err := a.dbOrError(ctx)
	if err != nil {
		return nil, err
	}
	sqlStr, present := a.getJobSQL()
	row := db.QueryRowContext(ctx, sqlStr, jobID)
	j, err := scanJob(row, present)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("openclaw: get job: %w", err)
	}
	return &j, nil
}

// ListRunLogs returns up to limit run logs for the given job, newest first.
func (a *Adapter) ListRunLogs(ctx context.Context, jobID string, limit int) ([]domain.RunLog, error) {
	if limit <= 0 {
		return nil, nil
	}

	db, err := a.dbOrError(ctx)
	if err != nil {
		return nil, err
	}

	// Older and current OpenClaw databases may not persist run history in a
	// cron_run_logs table. Job data remains usable, so return an empty history
	// instead of degrading the entire dashboard.
	if len(a.runCols) == 0 {
		return []domain.RunLog{}, nil
	}

	sqlStr, present := a.runLogsSelect()
	rows, err := db.QueryContext(ctx, sqlStr+" WHERE job_id = ? ORDER BY ts DESC, seq DESC LIMIT ?",
		jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("openclaw: list run logs: %w", err)
	}
	defer rows.Close()

	logs := []domain.RunLog{}
	for rows.Next() {
		l, err := scanRunLog(rows, present)
		if err != nil {
			return nil, fmt.Errorf("openclaw: scan run log: %w", err)
		}
		logs = append(logs, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("openclaw: list run logs: %w", err)
	}
	return logs, nil
}

// dbOrError returns the *sql.DB, opening it if necessary. Callers must
// not hold a.mu.
func (a *Adapter) dbOrError(ctx context.Context) (*sql.DB, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.openLocked(ctx); err != nil {
		return nil, err
	}
	return a.db, nil
}

// jobsSelectColumns returns the SELECT column list for cron_jobs and the
// matching "present" set of optional column names in the same order.
// Required columns come first; optional columns follow in a fixed order.
func (a *Adapter) jobsSelectColumns() ([]string, []string) {
	cols := []string{"job_id", "name", "enabled", "schedule_kind", "next_run_at_ms"}
	for _, c := range optionalJobColumns {
		if a.jobCols[c] {
			cols = append(cols, c)
		}
	}
	return cols, cols[5:]
}

// listJobsSQL builds the SELECT used by ListJobs. The ORDER BY tolerates a
// schema without sort_order; in that case we fall back to ordering by
// job_id.
func (a *Adapter) listJobsSQL() (string, []string) {
	cols, _ := a.jobsSelectColumns()
	orderBy := " ORDER BY job_id ASC"
	if a.jobCols["sort_order"] {
		orderBy = " ORDER BY sort_order ASC, name ASC"
	}
	return "SELECT " + strings.Join(cols, ", ") + " FROM cron_jobs" + orderBy, cols
}

// getJobSQL builds the SELECT used by GetJob.
func (a *Adapter) getJobSQL() (string, []string) {
	cols, _ := a.jobsSelectColumns()
	return "SELECT " + strings.Join(cols, ", ") + " FROM cron_jobs WHERE job_id = ?", cols
}

// runLogsSelect returns the SELECT prefix for cron_run_logs and the
// matching "present" set of optional column names.
func (a *Adapter) runLogsSelect() (string, []string) {
	cols := []string{"job_id", "seq", "ts"}
	for _, c := range optionalRunColumns {
		if a.runCols[c] {
			cols = append(cols, c)
		}
	}
	return "SELECT " + strings.Join(cols, ", ") + " FROM cron_run_logs", cols
}

// optionalJobColumns is the canonical list of optional cron_jobs columns
// the adapter reads, in SELECT order. The schema adapter detects which of
// these are actually present and only references the existing ones.
var optionalJobColumns = []string{
	"display_name",
	"description", "schedule_expr", "schedule_tz", "every_ms", "at",
	"running_at_ms", "last_run_at_ms", "last_run_status", "last_error",
	"last_duration_ms", "consecutive_errors", "last_delivery_status", "updated_at",
}

// optionalRunColumns is the canonical list of optional cron_run_logs
// columns the adapter reads, in SELECT order.
var optionalRunColumns = []string{
	"status", "error", "summary", "delivery_status", "duration_ms", "next_run_at_ms",
}

// rowScanner is the minimal Scan surface used by scanJob/scanRunLog.
type rowScanner interface {
	Scan(dest ...any) error
}

// jobScan holds the destinations scanJob uses. Required fields are filled
// directly; optional fields are filled via sql.NullX so that columns that
// the schema lacks are simply absent.
type jobScan struct {
	job       domain.Job
	enabled   sql.NullInt64
	nextRunAt sql.NullInt64

	displayName        sql.NullString
	description        sql.NullString
	scheduleExpr       sql.NullString
	scheduleTZ         sql.NullString
	everyMS            sql.NullInt64
	at                 sql.NullString
	runningAt          sql.NullInt64
	lastRunAt          sql.NullInt64
	lastRunStatus      sql.NullString
	lastError          sql.NullString
	lastDurationMS     sql.NullInt64
	consecutiveErrors  sql.NullInt64
	lastDeliveryStatus sql.NullString
	updatedAt          sql.NullInt64
}

// optionalTargets returns the scan targets for the optional columns,
// keyed by column name, in the order they should appear after the required
// columns.
func (s *jobScan) optionalTargets() []struct {
	name    string
	target  any
	scanCol string
} {
	return []struct {
		name    string
		target  any
		scanCol string
	}{
		{"display_name", &s.displayName, "display_name"},
		{"description", &s.description, "description"},
		{"schedule_expr", &s.scheduleExpr, "schedule_expr"},
		{"schedule_tz", &s.scheduleTZ, "schedule_tz"},
		{"every_ms", &s.everyMS, "every_ms"},
		{"at", &s.at, "at"},
		{"running_at_ms", &s.runningAt, "running_at_ms"},
		{"last_run_at_ms", &s.lastRunAt, "last_run_at_ms"},
		{"last_run_status", &s.lastRunStatus, "last_run_status"},
		{"last_error", &s.lastError, "last_error"},
		{"last_duration_ms", &s.lastDurationMS, "last_duration_ms"},
		{"consecutive_errors", &s.consecutiveErrors, "consecutive_errors"},
		{"last_delivery_status", &s.lastDeliveryStatus, "last_delivery_status"},
		{"updated_at", &s.updatedAt, "updated_at"},
	}
}

// scanJob reads one row from a cron_jobs query. The cols slice is the
// list of column names that the SELECT returned, in order; missing
// optional columns are skipped, unknown extra columns are discarded.
func scanJob(s rowScanner, cols []string) (domain.Job, error) {
	var sc jobScan
	targets := []any{&sc.job.JobID, &sc.job.Name, &sc.enabled, &sc.job.ScheduleKind, &sc.nextRunAt}

	// Build a quick lookup so we can ignore unknown columns that the
	// SELECT happened to include.
	optMap := map[string]any{}
	for _, ot := range sc.optionalTargets() {
		optMap[ot.scanCol] = ot.target
	}
	for _, c := range cols[5:] {
		if t, ok := optMap[c]; ok {
			targets = append(targets, t)
		} else {
			// Unknown extra column: discard its bytes so Scan does not
			// complain about an unfulfilled destination.
			var discard sql.RawBytes
			targets = append(targets, &discard)
		}
	}

	if err := s.Scan(targets...); err != nil {
		return domain.Job{}, err
	}

	sc.job.Enabled = sc.enabled.Valid && sc.enabled.Int64 != 0
	sc.job.DisplayName = sc.displayName.String
	sc.job.Description = sc.description.String
	sc.job.ScheduleExpr = sc.scheduleExpr.String
	sc.job.ScheduleTZ = sc.scheduleTZ.String
	if sc.everyMS.Valid {
		sc.job.EveryMS = sc.everyMS.Int64
	}
	sc.job.At = sc.at.String
	sc.job.NextRunAt = millisToTime(sc.nextRunAt)
	sc.job.RunningAt = millisToTime(sc.runningAt)
	sc.job.LastRunAt = millisToTime(sc.lastRunAt)
	sc.job.LastRunStatus = sc.lastRunStatus.String
	sc.job.LastError = strings.TrimRight(sc.lastError.String, "\n")
	if sc.lastDurationMS.Valid {
		sc.job.LastDurationMS = sc.lastDurationMS.Int64
	}
	if sc.consecutiveErrors.Valid {
		sc.job.ConsecutiveErrors = sc.consecutiveErrors.Int64
	}
	sc.job.LastDeliveryStatus = sc.lastDeliveryStatus.String
	sc.job.UpdatedAt = millisToTime(sc.updatedAt)

	return sc.job, nil
}

// runLogScan holds the destinations scanRunLog uses.
type runLogScan struct {
	log       domain.RunLog
	ts        sql.NullInt64
	status    sql.NullString
	errMsg    sql.NullString
	summary   sql.NullString
	delivery  sql.NullString
	duration  sql.NullInt64
	nextRunAt sql.NullInt64
}

// scanRunLog reads one row from a cron_run_logs query. ts is required; if
// it is missing from the row, scanRunLog returns an error so the caller
// can fail loudly instead of returning a malformed entry.
func scanRunLog(s rowScanner, cols []string) (domain.RunLog, error) {
	var sc runLogScan
	targets := []any{&sc.log.JobID, &sc.log.Seq, &sc.ts}

	optMap := map[string]any{
		"status":          &sc.status,
		"error":           &sc.errMsg,
		"summary":         &sc.summary,
		"delivery_status": &sc.delivery,
		"duration_ms":     &sc.duration,
		"next_run_at_ms":  &sc.nextRunAt,
	}
	for _, c := range cols[3:] {
		if t, ok := optMap[c]; ok {
			targets = append(targets, t)
		} else {
			var discard sql.RawBytes
			targets = append(targets, &discard)
		}
	}

	if err := s.Scan(targets...); err != nil {
		return domain.RunLog{}, err
	}
	if !sc.ts.Valid {
		return domain.RunLog{}, errors.New("openclaw: run log ts is null")
	}
	sc.log.Ts = time.UnixMilli(sc.ts.Int64).UTC()
	sc.log.Status = sc.status.String
	sc.log.Error = strings.TrimRight(sc.errMsg.String, "\n")
	sc.log.Summary = sc.summary.String
	sc.log.DeliveryStatus = sc.delivery.String
	if sc.duration.Valid {
		sc.log.DurationMS = sc.duration.Int64
	}
	sc.log.NextRunAt = millisToTime(sc.nextRunAt)
	return sc.log, nil
}

func millisToTime(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := time.UnixMilli(ms.Int64).UTC()
	return &t
}
