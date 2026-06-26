-- Remove the write-only config column. The same data lives inside job_payload
-- (as job_payload.request), so dropping it saves ~N bytes per row and eliminates
-- a redundant marshal on every write.
ALTER TABLE erawan_cluster_jobs DROP COLUMN IF EXISTS config;

-- Replace the single-column (status) index with a composite (engine, status)
-- that matches the actual WHERE engine=$1 AND status=$2 predicate used by
-- MarkStaleRunningJobsFailed. The old index forced a separate heap lookup for
-- every candidate row because the engine filter was not covered.
DROP INDEX IF EXISTS erawan_cluster_jobs_status_idx;
CREATE INDEX IF NOT EXISTS erawan_cluster_jobs_engine_status_idx
	ON erawan_cluster_jobs (engine, status);

INSERT INTO erawan_schema_migrations(version)
VALUES (3)
ON CONFLICT (version) DO NOTHING;
