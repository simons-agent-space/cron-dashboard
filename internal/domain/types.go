// Package domain holds the pure data types and the storage interface that
// crons renders. Nothing in this package knows about SQLite, OpenClaw, or
// HTTP. The openclaw package implements Repository against the live
// database; other implementations (e.g. an in-memory fake for tests) live
// elsewhere.
package domain

import "time"

// Job is the operational view of a single cron_jobs row. Only fields that
// are safe to render are exposed here. The underlying adapter is responsible
// for excluding payload, delivery, and session-keyed fields.
type Job struct {
	JobID        string
	Name         string
	DisplayName  string
	Description  string
	Enabled      bool
	ScheduleKind string
	ScheduleExpr string
	ScheduleTZ   string
	EveryMS      int64
	At           string

	NextRunAt          *time.Time
	RunningAt          *time.Time
	LastRunAt          *time.Time
	LastRunStatus      string
	LastError          string
	LastDurationMS     int64
	ConsecutiveErrors  int64
	LastDeliveryStatus string
	UpdatedAt          *time.Time
}

// RunLog is one entry from cron_run_logs.
type RunLog struct {
	JobID          string
	Seq            int64
	Ts             time.Time
	Status         string
	Error          string
	Summary        string
	DeliveryStatus string
	DurationMS     int64
	NextRunAt      *time.Time
}
