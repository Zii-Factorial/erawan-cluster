CREATE TABLE IF NOT EXISTS erawan_schema_migrations (
	version integer PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS erawan_cluster_jobs (
	engine         text        NOT NULL,
	job_id         text        NOT NULL,
	status         text        NOT NULL,
	created_at     timestamptz NOT NULL,
	updated_at     timestamptz NOT NULL,
	job_payload    jsonb       NOT NULL,
	secret_payload jsonb,
	PRIMARY KEY (engine, job_id)
);

CREATE INDEX IF NOT EXISTS erawan_cluster_jobs_engine_updated_idx
	ON erawan_cluster_jobs (engine, updated_at DESC);

CREATE INDEX IF NOT EXISTS erawan_cluster_jobs_engine_status_idx
	ON erawan_cluster_jobs (engine, status);

INSERT INTO erawan_schema_migrations(version)
VALUES (1)
ON CONFLICT (version) DO NOTHING;
