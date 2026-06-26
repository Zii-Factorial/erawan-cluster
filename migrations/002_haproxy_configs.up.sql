CREATE TABLE IF NOT EXISTS erawan_haproxy_configs (
	port       integer     PRIMARY KEY,
	content    text        NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO erawan_schema_migrations(version)
VALUES (2)
ON CONFLICT (version) DO NOTHING;
