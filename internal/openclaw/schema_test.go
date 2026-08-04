package openclaw

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// openTestDB opens the database at path with the production driver and
// returns it for use by VerifySchema. The returned *sql.DB is the test's
// responsibility to close.
func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestVerifySchema_FullSchema(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, nil)
	ctx := context.Background()

	schema, err := VerifySchema(ctx, openTestDB(t, path))
	if err != nil {
		t.Fatalf("verify schema: %v", err)
	}
	if !schema.Compatible() {
		t.Fatalf("expected compatible schema, missing %v", schema.MissingRequired)
	}
	if schema.VersionMismatch {
		t.Fatalf("expected default version match, got mismatch")
	}
}

func TestVerifySchema_MinimalSchema(t *testing.T) {
	// Database with only the required columns should still be compatible.
	path := newFixtureDB(t, minimalSchemaV1, nil)
	ctx := context.Background()

	schema, err := VerifySchema(ctx, openTestDB(t, path))
	if err != nil {
		t.Fatalf("verify schema: %v", err)
	}
	if !schema.Compatible() {
		t.Fatalf("expected minimal schema to be compatible, missing %v", schema.MissingRequired)
	}
	// The minimal schema has only the required columns plus a couple of
	// stragglers; ensure the optional columns we care about are absent.
	for _, c := range []string{"description", "schedule_expr", "last_run_status"} {
		if contains(schema.FoundColumns["cron_jobs"], c) {
			t.Errorf("expected %q to be absent from minimal schema, found it", c)
		}
	}
}

func TestVerifySchema_EmptyDatabase(t *testing.T) {
	path := newFixtureDB(t, "", nil)
	ctx := context.Background()

	schema, err := VerifySchema(ctx, openTestDB(t, path))
	if err != nil {
		t.Fatalf("verify schema: %v", err)
	}
	if schema.Compatible() {
		t.Fatalf("expected empty database to be incompatible")
	}
	if len(schema.MissingRequired) != len(Required) {
		t.Fatalf("expected %d missing columns, got %d", len(Required), len(schema.MissingRequired))
	}
}

func TestVerifySchema_MissingRequiredColumn(t *testing.T) {
	bad := strings.Replace(fullSchemaV1,
		"enabled INTEGER NOT NULL,",
		"", 1)
	path := newFixtureDB(t, bad, nil)
	ctx := context.Background()

	schema, err := VerifySchema(ctx, openTestDB(t, path))
	if err != nil {
		t.Fatalf("verify schema: %v", err)
	}
	if schema.Compatible() {
		t.Fatalf("expected schema with missing `enabled` to be incompatible")
	}
	found := false
	for _, m := range schema.MissingRequired {
		if m.Table == "cron_jobs" && m.Column == "enabled" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected `cron_jobs.enabled` to be reported as missing, got %v", schema.MissingRequired)
	}

	// The adapter must also refuse to start against this schema.
	adapter := New("file:" + path + "?mode=ro")
	err = adapter.Open(ctx)
	defer adapter.Close()
	if err == nil {
		t.Fatalf("expected adapter to refuse incompatible schema")
	}
	if !IsSchemaError(err) {
		t.Fatalf("expected SchemaError, got %T: %v", err, err)
	}
	var se *SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("expected errors.As to unwrap SchemaError")
	}
	notFound := true
	for _, m := range se.Missing {
		if m.Table == "cron_jobs" && m.Column == "enabled" {
			notFound = false
		}
	}
	if notFound {
		t.Fatalf("expected `cron_jobs.enabled` in SchemaError.Missing, got %v", se.Missing)
	}
}

func TestVerifySchema_UnknownAdditionalColumn(t *testing.T) {
	extra := strings.Replace(fullSchemaV1,
		"updated_at INTEGER NOT NULL,",
		"updated_at INTEGER NOT NULL,\n\tnote_from_operator TEXT,", 1)

	path := newFixtureDB(t, extra, nil)
	ctx := context.Background()

	schema, err := VerifySchema(ctx, openTestDB(t, path))
	if err != nil {
		t.Fatalf("verify schema: %v", err)
	}
	if !schema.Compatible() {
		t.Fatalf("expected schema with extra column to be compatible, missing %v", schema.MissingRequired)
	}
	cols, ok := schema.FoundColumns["cron_jobs"]
	if !ok {
		t.Fatalf("expected FoundColumns to include cron_jobs")
	}
	if !contains(cols, "note_from_operator") {
		t.Fatalf("expected FoundColumns to include the extra column, got %v", cols)
	}
}

func TestVerifySchema_UnknownVersionWarnsButContinues(t *testing.T) {
	path := newFixtureDB(t, fullSchemaV1, nil)
	ctx := context.Background()

	db := openTestDB(t, path)
	if _, err := db.ExecContext(ctx, "PRAGMA user_version = 5"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	schema, err := VerifySchema(ctx, db)
	if err != nil {
		t.Fatalf("verify schema: %v", err)
	}
	if !schema.Compatible() {
		t.Fatalf("expected schema with new version to remain compatible")
	}
	if !schema.VersionMismatch {
		t.Fatalf("expected VersionMismatch to be true")
	}
	if schema.Version != 5 {
		t.Fatalf("expected Version=5, got %d", schema.Version)
	}
}
