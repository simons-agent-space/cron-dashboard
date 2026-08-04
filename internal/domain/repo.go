package domain

import (
	"context"
	"errors"
)

// ErrNotFound is returned by GetJob when the requested job does not exist.
var ErrNotFound = errors.New("job not found")

// Repository is the read-only storage interface crons depends on. The
// openclaw adapter is the production implementation; tests can substitute a
// fake.
type Repository interface {
	// ListJobs returns every job in storage order.
	ListJobs(ctx context.Context) ([]Job, error)

	// GetJob returns a single job by id.
	GetJob(ctx context.Context, jobID string) (*Job, error)

	// ListRunLogs returns the most recent run logs for a job, newest first.
	// Limit is the maximum number of rows to return.
	ListRunLogs(ctx context.Context, jobID string, limit int) ([]RunLog, error)

	// Ping verifies that the underlying storage is reachable and queryable.
	Ping(ctx context.Context) error
}
