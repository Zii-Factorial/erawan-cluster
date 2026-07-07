# Cluster Management API — Reference

All requests require the `X-API-Key` header. All request and response bodies are `application/json`.

---

## Common Response Envelope

Every endpoint returns the same wrapper:

```json
{
  "status": "ok" | "error",
  "message": "human-readable description",
  "data": { ... }
}
```

Successful create/deploy operations return `202 Accepted`. Reads and updates return `200 OK`. Errors return the appropriate 4xx/5xx with `"status": "error"`.

---

## Health

### `GET /health`

Returns API status and version.

**Response `data`:**
```json
{
  "service": "erawan-cluster",
  "version": "1.02"
}
```

---

## HAProxy

### `POST /haproxy/config/mysql`

Create a new HAProxy frontend+backend for a MySQL InnoDB Cluster. Includes a
primary-check health-check backend (this deployment does not use MySQL
Router — see [doc/mysql.md](mysql.md#primary-check-endpoint)).

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `port` | int | yes | HAProxy frontend listen port (e.g. `25041`) |
| `node_ips` | string[] or string | yes | DB node IPs to add as backends |
| `db_port` | int | yes | MySQL port on the DB nodes (default `3306`) |
| `primary_check_port` | int | no | Primary-check HTTP port for health checks (default `9200`) |

```json
{
  "port": 25041,
  "node_ips": ["10.0.0.1", "10.0.0.2"],
  "db_port": 3306,
  "primary_check_port": 9200
}
```

**Response `data`:**
```json
{ "port": 25041, "node_ips": ["10.0.0.1", "10.0.0.2"], "db_port": 3306, "primary_check_port": 9200 }
```

---

### `PATCH /haproxy/config/mysql`

Add a single node to an existing MySQL HAProxy backend.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `port` | int | yes | Existing frontend port to update |
| `node_ip` | string | yes | New DB node IP to add |

```json
{ "port": 25041, "node_ip": "10.0.0.3" }
```

---

### `POST /haproxy/config/pgsql`

Create a new HAProxy frontend+backend for a PostgreSQL cluster. Includes a Patroni health-check backend.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `port` | int | yes | HAProxy frontend listen port (e.g. `25042`) |
| `node_ips` | string[] or string | yes | DB node IPs |
| `db_port` | int | yes | PostgreSQL port on the DB nodes (default `5432`) |
| `patroni_port` | int | yes | Patroni REST API port for health checks (default `8008`) |

```json
{
  "port": 25042,
  "node_ips": ["10.0.0.1", "10.0.0.2"],
  "db_port": 5432,
  "patroni_port": 8008
}
```

---

### `PATCH /haproxy/config/pgsql`

Add a single node to an existing PostgreSQL HAProxy backend.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `port` | int | yes | Existing frontend port to update |
| `node_ip` | string | yes | New DB node IP to add |

---

### `DELETE /haproxy/config`

Remove a HAProxy tenant config and reload.

**Request:**
```json
{ "port": 25041 }
```

---

### `GET /haproxy/configs`

List all tenant config file names.

**Response `data`:** `["25041.cfg", "25042.cfg"]`

---

### `GET /haproxy/configs/download`

Download all tenant configs as a zip file. Returns `application/zip`.

---

### `POST /haproxy/reload`

Trigger HAProxy reload (uses `HAPROXY_RELOAD_CMD` from env).

---

## MySQL Cluster

### `POST /cluster/mysql/deploy`

Deploy a MySQL InnoDB Cluster. Returns immediately with a job ID; the deployment runs asynchronously.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cluster_name` | string | yes | InnoDB Cluster name (alphanumeric, no spaces) |
| `primary_ip` | string | yes | IP of the primary node |
| `standby_ips` | string[] | yes | IPs of secondary nodes; empty `[]` for single-node |
| `admin_username` | string | yes | Cluster admin user to create (e.g. `clusteradmin`) |
| `admin_password` | string | yes | Password for the admin user |
| `new_user` | string | no | Application user to create |
| `new_user_password` | string | no | Password for the application user |
| `new_user_ssl_required` | bool | no | Require SSL for the application user. Omit to use default (`true`) |
| `new_user_superuser` | bool | no | `true` = grant `ALL PRIVILEGES ON *.*` + all dynamic privileges (full server-level superuser). `false` = `ALL PRIVILEGES ON new_db.*` only. Default `true` |
| `new_db` | string | no | Application database to create |
| `assume_prepared` | bool | no | Skip node preparation steps if already prepared (default `false`) |
| `reset_host_keys` | bool | no | Forget any previously pinned SSH host key for `primary_ip`/`standby_ips` and trust their current key. Use when a node was rebuilt/reimaged and its host key changed; otherwise the deploy fails with a host-key verification error (default `false`) |
| `ssh_port` | int | no | SSH port for Ansible (default `22`) |
| `mysql_port` | int | no | MySQL port on DB nodes (default `3306`) |
| `mysql_version` | int | no | Major version: `8` = 8.x, `9` = 9.x (default `8`) |
| `step_timeout_seconds` | int | no | Per-step Ansible timeout (default `900`) |

```json
{
  "cluster_name": "prodCluster",
  "primary_ip": "10.0.0.1",
  "standby_ips": ["10.0.0.2", "10.0.0.3"],
  "admin_username": "clusteradmin",
  "admin_password": "AdminPass#2026",
  "new_user": "appuser",
  "new_user_password": "AppUser#2026",
  "new_user_superuser": true,
  "new_db": "appdb",
  "mysql_version": 8,
  "step_timeout_seconds": 900
}
```

**Response `data`:**

The response contains the job object plus a `secret` block with the cluster admin credentials. Store these — they are not returned again by default.

```json
{
  "id": "abc123",
  "status": "running",
  "created_at": "2026-06-19T10:00:00Z",
  "updated_at": "2026-06-19T10:00:05Z",
  "current_step": "Configure primary node",
  "completed_steps": 1,
  "total_steps": 12,
  "progress_percent": 8,
  "request": { ... },
  "steps": [ ... ],
  "secret": {
    "admin_user": "clusteradmin",
    "admin_password": "AdminPass#2026"
  }
}
```

**Job fields:**
| Field | Description |
|-------|-------------|
| `id` | Unique job ID |
| `status` | `pending` / `running` / `completed` / `failed` / `rolled_back` |
| `current_step` | Name of the step currently executing |
| `last_completed_step` | Index of the last successfully completed step |
| `completed_steps` | Count of completed steps |
| `total_steps` | Total steps in the playbook |
| `progress_percent` | `completed_steps / total_steps * 100` |
| `error` | Error message if `status` is `failed` |
| `steps` | Array of step results (see below) |
| `member_op` | Present for add/remove member jobs |

**Step result fields:**
| Field | Description |
|-------|-------------|
| `name` | Step name |
| `status` | `completed` / `failed` / `skipped` |
| `started_at` | Step start time |
| `ended_at` | Step end time |
| `exit_code` | Ansible exit code |
| `stdout` | Step stdout (only when `MYSQL_ANSIBLE_DEBUG=true`) |
| `stderr` | Step stderr on failure |
| `message` | Human-readable summary |

---

### `GET /cluster/mysql/jobs`

List recent MySQL deploy jobs.

**Query params:**
- `limit` — max results (default `20`)

**Response `data`:** array of job objects.

---

### `GET /cluster/mysql/jobs/{jobID}`

Get full details of a single MySQL job including all step results and the stored secret.

---

### `POST /cluster/mysql/jobs/{jobID}/resume`

Resume a failed MySQL deploy job from the last completed step.

**Request:**
| Field | Type | Description |
|-------|------|-------------|
| `admin_password` | string | Cluster admin password |
| `new_user_password` | string | Application user password (if applicable) |
| `reset_host_keys` | bool | Forget any previously pinned SSH host key for this cluster's nodes and trust their current key (default `false`) |

```json
{
  "admin_password": "AdminPass#2026"
}
```

---

### `POST /cluster/mysql/jobs/{jobID}/rollback`

Roll back a MySQL cluster deployment (removes cluster config, unjoins nodes).

**Request:**
```json
{
  "admin_password": "AdminPass#2026"
}
```

---

### `POST /cluster/mysql/members`

Add one or more secondary nodes to an existing MySQL InnoDB Cluster.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID (provides cluster config) |
| `member_ips` | string[] | yes | IPs of the new nodes to join |
| `assume_prepared` | bool | no | Skip node preparation if already done |
| `reset_host_keys` | bool | no | Forget any previously pinned SSH host key for the new node(s) and trust their current key (default `false`) |

```json
{
  "job_id": "abc123",
  "member_ips": ["10.0.0.4"]
}
```

---

### `DELETE /cluster/mysql/members`

Remove a secondary node from the MySQL InnoDB Cluster.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `member_ip` | string | yes | IP of the node to remove |
| `force` | bool | no | Force removal even if the node is unreachable |

```json
{
  "job_id": "abc123",
  "member_ip": "10.0.0.4"
}
```

---

### `POST /cluster/mysql/metrics`

Collect live metrics from a MySQL cluster. Data is sourced from **Prometheus exporters** (`mysqld_exporter` on `:9104` and `node_exporter` on `:9100`) running on each DB node — no database credentials are required.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | no | Resolves `node_ips` from the stored deploy job |
| `proxy_port` | int | yes | HAProxy frontend port for this cluster (e.g. `25041`). Used only to populate `port` in the response |
| `db_metric_exporter_port` | int | no | `mysqld_exporter` port on DB nodes (default `9104`) |
| `node_exporter_port` | int | no | `node_exporter` port on DB nodes (default `9100`) |
| `node_ips` | string[] | if no job_id | Cluster member IPs to scrape exporters from |
| `categories` | string[] | no | Categories to collect; empty = all 8 |

```json
{
  "job_id": "abc123",
  "proxy_port": 25041,
  "categories": ["cluster", "connections", "replication"]
}
```

**Categories:**
| Category | Description |
|----------|-------------|
| `cluster` | InnoDB Cluster / Group Replication membership state, primary, member roles |
| `uptime` | MySQL server process uptime |
| `connections` | Active/sleeping threads, utilization %, aborted connects |
| `replication` | Replica applier thread state and lag per standby |
| `performance` | InnoDB buffer pool, QPS/TPS, temp-table and sort pressure, index hit ratio, network I/O |
| `query` | Slow query count, deadlocks, lock waits, table scan ratio |
| `maintenance` | InnoDB purge lag, open tables, log waits, table lock contention |
| `system` | Per-node OS metrics (CPU, memory, disk, network) from `node_exporter` |

**Response `data`:**
```json
{
  "collected_at": "2026-06-19T10:00:00Z",
  "engine": "mysql",
  "host": "127.0.0.1",
  "port": 25041,
  "users": [],
  "nodes": [
    {
      "host": "10.0.0.1",
      "uptime_seconds": 86400,
      "cpu_usage_pct": 12.5,
      "memory_total_bytes": 8589934592,
      "memory_available_bytes": 6000000000,
      "memory_used_pct": 30.1,
      "load1": 0.4,
      "load5": 0.3,
      "load15": 0.2,
      "disks": [ ... ],
      "network_interfaces": [ ... ]
    }
  ],
  "categories": {
    "cluster": { ... },
    "connections": { ... }
  },
  "errors": {
    "query": "exporter unreachable"
  }
}
```

The `categories` object contains one key per collected category. Per-category failures are reported in `errors` — the other categories still return. `users` is always an empty array; user data is managed via the dedicated user endpoints.

---

### `POST /cluster/mysql/users`

Create (or update) a MySQL user. Idempotent — re-running with the same username updates the password, SSL requirement, and grants.

Connects as the **cluster admin** user (`clusteradmin`) resolved from the deploy job.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `username` | string | yes | New user name |
| `password` | string | yes | Password |
| `superuser` | bool | no | `true` = `GRANT ALL PRIVILEGES ON *.*` + all dynamic privileges (same as deploy-time superuser). `false` = scoped grants on `database` only. Default `false` |
| `ssl_required` | bool | no | Require SSL (`REQUIRE SSL`) for this user. Omit to use default (`true`) |
| `database` | string | no | Database to grant scoped access on (ignored when `superuser: true`) |

**Protection rules** — the following users cannot be deleted or renamed via this API:
- Built-in system users: `root`, `mysql.sys`, `mysql.session`, `mysql.infoschema`
- The cluster admin user created at deploy time (e.g. `clusteradmin`)

Users created with `superuser: true` are **not** protected and can be freely deleted or renamed.

---

### `PATCH /cluster/mysql/users`

Rename a MySQL user.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `username` | string | yes | Current user name |
| `new_username` | string | yes | New user name |

---

### `PUT /cluster/mysql/users/password`

Reset a MySQL user's password.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `username` | string | yes | User name |
| `password` | string | yes | New password |

---

### `DELETE /cluster/mysql/users`

Drop a MySQL user.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `username` | string | yes | User to drop |

---

### `POST /cluster/mysql/databases`

Create a MySQL database on a cluster.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `dbname` | string | yes | Database name to create |

---

### `PATCH /cluster/mysql/databases`

Rename a MySQL database.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `dbname` | string | yes | Current database name |
| `new_dbname` | string | yes | New database name |

---

### `DELETE /cluster/mysql/databases`

Drop a MySQL database.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `dbname` | string | yes | Database to drop |

---

## PostgreSQL Cluster

### `POST /cluster/pgsql/deploy`

Deploy a PostgreSQL Patroni cluster. Returns immediately with a job ID.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cluster_name` | string | yes | Patroni scope name |
| `primary_ip` | string | yes | IP of the primary node |
| `standby_ips` | string[] | yes | IPs of standby nodes; empty `[]` for single-node |
| `postgres_password` | string | yes | Password for the `postgres` superuser |
| `replicator_password` | string | yes | Password for the `replicator` streaming replication user |
| `admin_username` | string | yes | Application admin user to create |
| `admin_password` | string | yes | Password for the admin user |
| `new_user` | string | no | Application user to create |
| `new_user_password` | string | no | Password for the application user |
| `new_user_ssl_required` | bool | no | Require SSL for the application user via `pg_hba.conf` (`hostssl`/`hostnossl` rules). Default `true` |
| `new_user_superuser` | bool | no | `true` = `LOGIN SUPERUSER CREATEDB CREATEROLE REPLICATION BYPASSRLS` (full superuser). `false` = `LOGIN NOSUPERUSER NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`. Default `true` |
| `new_db` | string | no | Application database to create |
| `reset_host_keys` | bool | no | Forget any previously pinned SSH host key for `primary_ip`/`standby_ips` and trust their current key. Use when a node was rebuilt/reimaged and its host key changed; otherwise the deploy fails with a host-key verification error (default `false`) |
| `ssh_port` | int | no | SSH port for Ansible (default `22`) |
| `postgres_port` | int | no | PostgreSQL port on DB nodes (default `5432`) |
| `postgres_version` | int | no | Major version: `14`, `15`, `16`, `17`, `18` (default `16`) |
| `step_timeout_seconds` | int | no | Per-step Ansible timeout (default `900`) |

```json
{
  "cluster_name": "pg-prod",
  "primary_ip": "10.0.0.1",
  "standby_ips": ["10.0.0.2"],
  "postgres_password": "PgRoot#2026",
  "replicator_password": "Repl#2026",
  "admin_username": "pgadmin",
  "admin_password": "Admin#2026",
  "new_user": "appuser",
  "new_user_password": "App#2026",
  "new_user_superuser": true,
  "new_db": "appdb",
  "postgres_version": 16,
  "step_timeout_seconds": 900
}
```

**Response `data`** — same job structure as MySQL plus a `secret` block:
```json
{
  "secret": {
    "postgres_user": "postgres",
    "postgres_password": "PgRoot#2026",
    "replicator_user": "replicator",
    "replicator_password": "Repl#2026",
    "admin_password": "Admin#2026"
  }
}
```

---

### `GET /cluster/pgsql/jobs`

List recent PostgreSQL deploy jobs. Query param: `limit` (default `20`).

---

### `GET /cluster/pgsql/jobs/{jobID}`

Get full details of a single PostgreSQL job including step results and stored secret.

---

### `POST /cluster/pgsql/jobs/{jobID}/resume`

Resume a failed PostgreSQL deploy job.

**Request:**
| Field | Type | Description |
|-------|------|-------------|
| `postgres_password` | string | postgres superuser password |
| `replicator_password` | string | Replication user password |
| `admin_password` | string | Admin user password |
| `new_user_password` | string | Application user password (if applicable) |
| `reset_host_keys` | bool | Forget any previously pinned SSH host key for this cluster's nodes and trust their current key (default `false`) |

---

### `POST /cluster/pgsql/members`

Add one or more standby nodes to an existing Patroni cluster.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `member_ips` | string[] | yes | IPs of new nodes to join |
| `reset_host_keys` | bool | no | Forget any previously pinned SSH host key for the new node(s) and trust their current key (default `false`) |

---

### `DELETE /cluster/pgsql/members`

Remove a standby node from the Patroni cluster.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `member_ip` | string | yes | IP of the standby to remove |
| `force` | bool | no | Force removal even if the node is unreachable |

---

### `POST /cluster/pgsql/metrics`

Collect live metrics from a PostgreSQL cluster. Data is sourced from **Prometheus exporters** (`postgres_exporter` on `:9187` and `node_exporter` on `:9100`) running on each DB node, plus the **Patroni REST API** for cluster and failover categories — no database credentials are required.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | no | Resolves `node_ips` and Patroni config from the stored deploy job |
| `proxy_port` | int | yes | HAProxy frontend port for this cluster (e.g. `25042`). Used only to populate `port` in the response |
| `db_metric_exporter_port` | int | no | `postgres_exporter` port on DB nodes (default `9187`) |
| `node_exporter_port` | int | no | `node_exporter` port on DB nodes (default `9100`) |
| `patroni_port` | int | no | Patroni REST port for cluster health checks (default `8008`) |
| `node_ips` | string[] | if no job_id | Cluster member IPs to scrape exporters and Patroni REST from |
| `categories` | string[] | no | Categories to collect; empty = all 9 |
| `from` | string | no | ISO 8601 lower bound for `failover` event time filter |
| `to` | string | no | ISO 8601 upper bound for `failover` event time filter |

```json
{
  "job_id": "xyz456",
  "proxy_port": 25042,
  "patroni_port": 8008,
  "categories": ["cluster", "replication", "connections"]
}
```

**Categories:**
| Category | Description |
|----------|-------------|
| `cluster` | Patroni HA state, DCS health, node roles (leader / sync_standby / replica), TTL/loop_wait |
| `uptime` | PostgreSQL process uptime (`pg_postmaster_start_time`) |
| `failover` | Patroni timeline and failover history (filterable by `from`/`to`) |
| `connections` | Active, idle, idle-in-transaction, lock-waiters, per-state breakdown, wait events |
| `replication` | Streaming replication LSN lag per standby, WAL config, replication slot lag |
| `performance` | TPS, cache hit ratio, temp files/bytes, checkpoint pressure, bgwriter stats |
| `query` | Slow query count, deadlocks, lock waits, seq-scan ratio, high-seq-scan tables |
| `maintenance` | Autovacuum health, tables needing vacuum, logical slot lag, WAL archiver stats, lock summary |
| `system` | Per-node OS metrics (CPU, memory, disk, network) from `node_exporter` |

---

### `POST /cluster/pgsql/users`

Create (or update) a PostgreSQL role. Idempotent — re-running with the same username updates the password and grants.

Connects as the **`postgres`** superuser resolved from the deploy job.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `username` | string | yes | New role name |
| `password` | string | yes | Password |
| `superuser` | bool | no | `true` = `LOGIN SUPERUSER CREATEDB CREATEROLE REPLICATION BYPASSRLS` (same as deploy-time superuser). `false` = `LOGIN NOSUPERUSER NOCREATEROLE INHERIT` scoped to `database`. Default `false` |
| `ssl_required` | bool | no | `true` = `hostssl` + `hostnossl reject` rule in `pg_hba.conf`. `false` = plain `host` rule. Patroni DCS is patched automatically. Omit to use default (`true`) |
| `database` | string | no | Database to grant scoped access on (ignored when `superuser: true`) |

**Protection rules** — the following roles cannot be deleted or renamed via this API:
- Built-in system roles: `postgres`, any role starting with `pg_`
- Replication roles (`rolreplication = true`, e.g. `replicator`)
- The cluster admin user created at deploy time

Roles created with `superuser: true` are **not** protected and can be freely deleted or renamed.

---

### `PATCH /cluster/pgsql/users`

Rename a PostgreSQL role.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `username` | string | yes | Current role name |
| `new_username` | string | yes | New role name |

---

### `PUT /cluster/pgsql/users/password`

Reset a PostgreSQL role's password.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `username` | string | yes | Role name |
| `password` | string | yes | New password |

---

### `DELETE /cluster/pgsql/users`

Drop a PostgreSQL role.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `username` | string | yes | Role to drop |

---

### `POST /cluster/pgsql/databases`

Create a PostgreSQL database.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `dbname` | string | yes | Database name to create |

---

### `PATCH /cluster/pgsql/databases`

Rename a PostgreSQL database.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `dbname` | string | yes | Current database name |
| `new_dbname` | string | yes | New database name |

---

### `DELETE /cluster/pgsql/databases`

Drop a PostgreSQL database.

**Request:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `job_id` | string | yes | Source deploy job ID |
| `dbname` | string | yes | Database to drop |
