package pgsql

import "erawan-cluster/internal/cluster/core"

// Store persists PostgreSQL cluster jobs and their secret sidecars. It is the
// generic core file store specialized to this engine's spec and secret types.
type Store = core.Store[StoredSpec, StoredSecret]

/**
 * NewStore creates (or opens) the on-disk job store rooted at dir.
 *
 * Params:
 *   dir string - the dir string
 *
 * Returns:
 *   *Store - the resulting *Store
 *   error - error value; non-nil when the operation fails
 */
func NewStore(dir string) (*Store, error) {
	return core.NewStore[StoredSpec, StoredSecret](dir)
}
