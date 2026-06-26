package pgsql

import (
	"database/sql"

	"erawan-cluster/internal/cluster/core"
)

// Store persists PostgreSQL cluster jobs and their secret sidecars. It may be
// backed by local JSON files or by PostgreSQL when DB_CONNECTION is configured.
type Store = core.JobStore[StoredSpec, StoredSecret]
type FileStore = core.Store[StoredSpec, StoredSecret]

/**
 * NewStore creates (or opens) the on-disk job store rooted at dir.
 *
 * Params:
 *   dir string - the dir string
 *
 * Returns:
 *   Store - the resulting Store
 *   error - error value; non-nil when the operation fails
 */
func NewStore(dir string) (Store, error) {
	return core.NewStore[StoredSpec, StoredSecret](dir)
}

func NewDBStore(db *sql.DB) (Store, error) {
	return core.NewDBStore[StoredSpec, StoredSecret](db, "pgsql")
}
