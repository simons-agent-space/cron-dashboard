package openclaw

import "database/sql"

// TestDB returns the underlying *sql.DB handle for tests that need to
// inspect connection-level state (e.g. PRAGMA query_only). It exists
// only in the test build; production code never reaches the raw handle.
func (a *Adapter) TestDB() *sql.DB { return a.db }
