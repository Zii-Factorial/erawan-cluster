DROP INDEX IF EXISTS erawan_cluster_jobs_engine_status_idx;
CREATE INDEX IF NOT EXISTS erawan_cluster_jobs_status_idx ON erawan_cluster_jobs (status);
ALTER TABLE erawan_cluster_jobs ADD COLUMN IF NOT EXISTS config jsonb NOT NULL DEFAULT '{}'::jsonb;

DELETE FROM erawan_schema_migrations WHERE version = 3;
