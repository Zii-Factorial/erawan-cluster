DROP INDEX IF EXISTS erawan_cluster_jobs_status_idx;
DROP INDEX IF EXISTS erawan_cluster_jobs_engine_updated_idx;
DROP TABLE IF EXISTS erawan_cluster_jobs;
DELETE FROM erawan_schema_migrations WHERE version = 1;
DROP TABLE IF EXISTS erawan_schema_migrations;
