package pgsql

import "erawan-cluster/internal/cluster/core"

// Store persists PostgreSQL cluster jobs and their secret sidecars. It is the
// generic core file store specialized to this engine's spec and secret types.
type Store = core.Store[StoredSpec, StoredSecret]

// NewStore creates (or opens) the on-disk job store rooted at dir.
func NewStore(dir string) (*Store, error) {
	return core.NewStore[StoredSpec, StoredSecret](dir)
}
