package haproxy

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const ensureHAProxyConfigSchemaSQL = `
CREATE TABLE IF NOT EXISTS erawan_haproxy_configs (
	port       integer     PRIMARY KEY,
	content    text        NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO erawan_schema_migrations(version)
VALUES (2)
ON CONFLICT (version) DO NOTHING;
`

// EnsureHAProxyConfigSchema creates the haproxy config table when it does not
// already exist. Called at startup so the table is always present when
// DB_CONNECTION is set, even without running make migration first.
func EnsureHAProxyConfigSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database handle is required")
	}
	if _, err := db.ExecContext(ctx, ensureHAProxyConfigSchemaSQL); err != nil {
		return fmt.Errorf("apply haproxy config schema: %w", err)
	}
	return nil
}

// ConfigEntry is one persisted HAProxy tenant config.
type ConfigEntry struct {
	Port      int
	Content   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ConfigStore persists HAProxy tenant configs so a standby node can reconcile
// its local HAProxy state after a VIP failover without operator intervention.
type ConfigStore interface {
	Save(ctx context.Context, port int, content string) error
	Delete(ctx context.Context, port int) error
	List(ctx context.Context) ([]ConfigEntry, error)
}

// DBConfigStore persists HAProxy configs in PostgreSQL.
type DBConfigStore struct {
	db *sql.DB
}

func NewDBConfigStore(db *sql.DB) (*DBConfigStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database handle is required")
	}
	return &DBConfigStore{db: db}, nil
}

func (s *DBConfigStore) Save(ctx context.Context, port int, content string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO erawan_haproxy_configs (port, content, created_at, updated_at)
		VALUES ($1, $2, now(), now())
		ON CONFLICT (port) DO UPDATE SET
			content    = EXCLUDED.content,
			updated_at = now()
	`, port, content)
	if err != nil {
		return fmt.Errorf("save haproxy config port %d: %w", port, err)
	}
	return nil
}

func (s *DBConfigStore) Delete(ctx context.Context, port int) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM erawan_haproxy_configs WHERE port = $1
	`, port)
	if err != nil {
		return fmt.Errorf("delete haproxy config port %d: %w", port, err)
	}
	return nil
}

func (s *DBConfigStore) List(ctx context.Context) ([]ConfigEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT port, content, created_at, updated_at
		FROM erawan_haproxy_configs
		ORDER BY port
	`)
	if err != nil {
		return nil, fmt.Errorf("list haproxy configs: %w", err)
	}
	defer rows.Close()

	var out []ConfigEntry
	for rows.Next() {
		var e ConfigEntry
		if err := rows.Scan(&e.Port, &e.Content, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan haproxy config: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list haproxy configs: %w", err)
	}
	return out, nil
}
