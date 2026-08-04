// Package openclaw holds the only code in the project that knows about the
// OpenClaw SQLite schema. Everything else talks to the domain.Repository
// interface and never sees a column name.
package openclaw

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// SchemaVersion is the schema version the adapter was built against. Changing
// this value is a deliberate compatibility decision.
const SchemaVersion = 1

// RequiredColumn names a single required column on a single table.
type RequiredColumn struct {
	Table  string
	Column string
}

// Required is the closed set of columns the adapter refuses to start without.
var Required = []RequiredColumn{
	{Table: "cron_jobs", Column: "job_id"},
	{Table: "cron_jobs", Column: "name"},
	{Table: "cron_jobs", Column: "enabled"},
	{Table: "cron_jobs", Column: "schedule_kind"},
	{Table: "cron_jobs", Column: "next_run_at_ms"},
	{Table: "cron_run_logs", Column: "job_id"},
	{Table: "cron_run_logs", Column: "seq"},
	{Table: "cron_run_logs", Column: "ts"},
}

// Schema is the result of verifying the database against the adapter.
type Schema struct {
	Version         int
	FoundTables     []string
	FoundColumns    map[string][]string // table -> columns that exist
	MissingRequired []RequiredColumn
	VersionMismatch bool
}

// Compatible reports whether the schema satisfies the adapter's requirements.
func (s *Schema) Compatible() bool {
	return len(s.MissingRequired) == 0
}

// SchemaError reports the columns that are missing.
type SchemaError struct {
	Missing []RequiredColumn
}

func (e *SchemaError) Error() string {
	parts := make([]string, 0, len(e.Missing))
	for _, m := range e.Missing {
		parts = append(parts, fmt.Sprintf("%s.%s", m.Table, m.Column))
	}
	sort.Strings(parts)
	return "openclaw schema mismatch: missing required columns: " + strings.Join(parts, ", ")
}

// IsSchemaError reports whether err is a SchemaError.
func IsSchemaError(err error) bool {
	var se *SchemaError
	return errors.As(err, &se)
}

// VerifySchema inspects the database and returns a Schema describing what it
// found. It does not write to the database. The caller decides whether a
// schema with missing required columns is fatal.
func VerifySchema(ctx context.Context, db *sql.DB) (*Schema, error) {
	if db == nil {
		return nil, errors.New("openclaw: nil database")
	}

	schema := &Schema{
		FoundColumns: map[string][]string{},
	}

	// Detect schema version. OpenClaw stores it in PRAGMA user_version. When
	// the value is missing or zero, we treat that as "no version advertised"
	// and do not warn. When it is set to something other than the version
	// this adapter was built against, we warn but continue.
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, fmt.Errorf("openclaw: read schema version: %w", err)
	}
	schema.Version = version
	if version != 0 && version != SchemaVersion {
		schema.VersionMismatch = true
	}

	// Collect every table that exists, so the caller can sanity-check.
	rows, err := db.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, fmt.Errorf("openclaw: list tables: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("openclaw: scan table name: %w", err)
		}
		schema.FoundTables = append(schema.FoundTables, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("openclaw: list tables: %w", err)
	}
	rows.Close()
	sort.Strings(schema.FoundTables)

	// Index required columns by table for a single PRAGMA per table.
	requiredByTable := map[string][]string{}
	for _, c := range Required {
		requiredByTable[c.Table] = append(requiredByTable[c.Table], c.Column)
	}

	// For every required table, list its columns and check the required set.
	tables := []string{"cron_jobs", "cron_run_logs"}
	for _, table := range tables {
		cols, err := tableColumns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		schema.FoundColumns[table] = cols
		for _, col := range requiredByTable[table] {
			if !contains(cols, col) {
				schema.MissingRequired = append(schema.MissingRequired, RequiredColumn{Table: table, Column: col})
			}
		}
	}
	sort.Slice(schema.MissingRequired, func(i, j int) bool {
		if schema.MissingRequired[i].Table != schema.MissingRequired[j].Table {
			return schema.MissingRequired[i].Table < schema.MissingRequired[j].Table
		}
		return schema.MissingRequired[i].Column < schema.MissingRequired[j].Column
	})

	return schema, nil
}

// tableColumns returns the column names of the given table in declaration
// order. If the table does not exist, the returned slice is empty and no
// error is raised — the caller is expected to compare against the required
// set and decide what to do.
func tableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, fmt.Errorf("openclaw: read columns for %s: %w", table, err)
	}
	defer rows.Close()

	// PRAGMA table_info columns: cid, name, type, notnull, dflt_value, pk.
	colNames := []string{}
	for rows.Next() {
		var (
			cid                 int
			name, colType, dflt sql.NullString
			notnull, pk         sql.NullInt64
		)
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("openclaw: scan column info for %s: %w", table, err)
		}
		if name.Valid {
			colNames = append(colNames, name.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("openclaw: read columns for %s: %w", table, err)
	}
	return colNames, nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
