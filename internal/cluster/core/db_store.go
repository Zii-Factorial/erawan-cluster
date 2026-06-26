package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const createJobStoreSchemaSQL = `
CREATE TABLE IF NOT EXISTS erawan_schema_migrations (
	version integer PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS erawan_cluster_jobs (
	engine text NOT NULL,
	job_id text NOT NULL,
	status text NOT NULL,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL,
	config jsonb NOT NULL DEFAULT '{}'::jsonb,
	job_payload jsonb NOT NULL,
	secret_payload jsonb,
	PRIMARY KEY (engine, job_id)
);

CREATE INDEX IF NOT EXISTS erawan_cluster_jobs_engine_updated_idx
	ON erawan_cluster_jobs (engine, updated_at DESC);

CREATE INDEX IF NOT EXISTS erawan_cluster_jobs_status_idx
	ON erawan_cluster_jobs (status);

INSERT INTO erawan_schema_migrations(version)
VALUES (1)
ON CONFLICT (version) DO NOTHING;
`

// EnsureJobStoreSchema creates the database tables required by DBStore.
func EnsureJobStoreSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database handle is required")
	}
	if _, err := db.ExecContext(ctx, createJobStoreSchemaSQL); err != nil {
		return fmt.Errorf("apply job store schema: %w", err)
	}
	return nil
}

// DBStore persists jobs for one engine in PostgreSQL. The full job and secret
// JSON are kept intact, while selected metadata is duplicated into columns for
// efficient listing and auditing.
type DBStore[Spec any, Sec any] struct {
	db     *sql.DB
	engine string
	mu     sync.Mutex
}

func NewDBStore[Spec any, Sec any](db *sql.DB, engine string) (*DBStore[Spec, Sec], error) {
	engine = strings.TrimSpace(strings.ToLower(engine))
	if db == nil {
		return nil, fmt.Errorf("database handle is required")
	}
	if engine == "" {
		return nil, fmt.Errorf("engine is required")
	}
	return &DBStore[Spec, Sec]{db: db, engine: engine}, nil
}

func (s *DBStore[Spec, Sec]) Save(job *Job[Spec]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(context.Background(), job)
}

func (s *DBStore[Spec, Sec]) save(ctx context.Context, job *Job[Spec]) error {
	if job == nil {
		return fmt.Errorf("job is required")
	}
	if err := validateJobID(job.ID); err != nil {
		return err
	}
	job.UpdatedAt = time.Now().UTC()
	jobPayload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	configPayload, err := json.Marshal(job.Request)
	if err != nil {
		return fmt.Errorf("marshal job config: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO erawan_cluster_jobs (
			engine, job_id, status, created_at, updated_at, config, job_payload
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb)
		ON CONFLICT (engine, job_id) DO UPDATE SET
			status = EXCLUDED.status,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at,
			config = EXCLUDED.config,
			job_payload = EXCLUDED.job_payload
	`, s.engine, job.ID, job.Status, job.CreatedAt, job.UpdatedAt, string(configPayload), string(jobPayload))
	if err != nil {
		return fmt.Errorf("save job: %w", err)
	}
	return nil
}

func (s *DBStore[Spec, Sec]) Update(jobID string, mutate func(*Job[Spec]) error) error {
	if err := validateJobID(jobID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin job update: %w", err)
	}
	defer tx.Rollback()

	job, err := s.loadWithQuerier(ctx, tx, jobID, true)
	if err != nil {
		return err
	}
	if err := mutate(job); err != nil {
		return err
	}
	if err := s.saveWithQuerier(ctx, tx, job); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit job update: %w", err)
	}
	return nil
}

func (s *DBStore[Spec, Sec]) SaveSecret(jobID string, secret Sec) error {
	if err := validateJobID(jobID); err != nil {
		return err
	}
	payload, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("marshal job secret: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(context.Background(), `
		UPDATE erawan_cluster_jobs
		SET secret_payload = $3::jsonb, updated_at = now()
		WHERE engine = $1 AND job_id = $2
	`, s.engine, jobID, string(payload))
	if err != nil {
		return fmt.Errorf("save job secret: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("job %s not found", jobID)
	}
	return nil
}

func (s *DBStore[Spec, Sec]) Load(jobID string) (*Job[Spec], error) {
	if err := validateJobID(jobID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadWithQuerier(context.Background(), s.db, jobID, false)
}

func (s *DBStore[Spec, Sec]) LoadSecret(jobID string) (Sec, error) {
	if err := validateJobID(jobID); err != nil {
		var zero Sec
		return zero, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var secret Sec
	var payload []byte
	err := s.db.QueryRowContext(context.Background(), `
		SELECT secret_payload
		FROM erawan_cluster_jobs
		WHERE engine = $1 AND job_id = $2 AND secret_payload IS NOT NULL
	`, s.engine, jobID).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return secret, fmt.Errorf("job %s secret not found", jobID)
		}
		return secret, fmt.Errorf("read job secret: %w", err)
	}
	if err := json.Unmarshal(payload, &secret); err != nil {
		return secret, fmt.Errorf("decode job secret: %w", err)
	}
	return secret, nil
}

func (s *DBStore[Spec, Sec]) List(limit int) ([]Job[Spec], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		SELECT job_payload
		FROM erawan_cluster_jobs
		WHERE engine = $1
		ORDER BY updated_at DESC
	`
	args := []any{s.engine}
	if limit > 0 {
		query += " LIMIT $2"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job[Spec]
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		var job Job[Spec]
		if err := json.Unmarshal(payload, &job); err != nil {
			continue
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return jobs, nil
}

func (s *DBStore[Spec, Sec]) MarkStaleRunningJobsFailed() {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(context.Background(), `
		SELECT job_id, job_payload
		FROM erawan_cluster_jobs
		WHERE engine = $1 AND status = $2
	`, s.engine, JobStatusRunning)
	if err != nil {
		return
	}
	type staleJob struct {
		id      string
		payload []byte
	}
	var stale []staleJob
	for rows.Next() {
		var item staleJob
		if err := rows.Scan(&item.id, &item.payload); err != nil {
			continue
		}
		stale = append(stale, item)
	}
	_ = rows.Close()

	for _, item := range stale {
		var job Job[Spec]
		if err := json.Unmarshal(item.payload, &job); err != nil {
			continue
		}
		job.Status = JobStatusFailed
		job.Error = "service restarted while job was in progress"
		job.UpdatedAt = time.Now().UTC()
		_ = s.saveWithQuerier(context.Background(), s.db, &job)
	}
}

type dbQuerier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *DBStore[Spec, Sec]) loadWithQuerier(ctx context.Context, q dbQuerier, jobID string, lock bool) (*Job[Spec], error) {
	query := `
		SELECT job_payload
		FROM erawan_cluster_jobs
		WHERE engine = $1 AND job_id = $2
	`
	if lock {
		query += " FOR UPDATE"
	}
	var payload []byte
	err := q.QueryRowContext(ctx, query, s.engine, jobID).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("job %s not found", jobID)
		}
		return nil, fmt.Errorf("read job state: %w", err)
	}
	var job Job[Spec]
	if err := json.Unmarshal(payload, &job); err != nil {
		return nil, fmt.Errorf("decode job state: %w", err)
	}
	return &job, nil
}

func (s *DBStore[Spec, Sec]) saveWithQuerier(ctx context.Context, q dbQuerier, job *Job[Spec]) error {
	if job == nil {
		return fmt.Errorf("job is required")
	}
	if err := validateJobID(job.ID); err != nil {
		return err
	}
	job.UpdatedAt = time.Now().UTC()
	jobPayload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	configPayload, err := json.Marshal(job.Request)
	if err != nil {
		return fmt.Errorf("marshal job config: %w", err)
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO erawan_cluster_jobs (
			engine, job_id, status, created_at, updated_at, config, job_payload
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb)
		ON CONFLICT (engine, job_id) DO UPDATE SET
			status = EXCLUDED.status,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at,
			config = EXCLUDED.config,
			job_payload = EXCLUDED.job_payload
	`, s.engine, job.ID, job.Status, job.CreatedAt, job.UpdatedAt, string(configPayload), string(jobPayload))
	if err != nil {
		return fmt.Errorf("save job: %w", err)
	}
	return nil
}
