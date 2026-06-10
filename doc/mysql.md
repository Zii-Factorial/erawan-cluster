# MySQL InnoDB Cluster with MySQL Router

This project deploys MySQL HA with MySQL InnoDB Cluster, MySQL Shell, and optional MySQL Router bootstrap.

## Assumptions

- MySQL is already installed and running on every target node.
- `mysqlsh` is already installed on every target node.
- Local MySQL administration as OS `root` works through a Unix socket on every target node.
- MySQL nodes can reach each other on the MySQL port.
- The API host can SSH to every target node.
- The API host already has the matching private key for the cloud-template SSH user, such as `clusterops`.
- That SSH user can run `sudo` without a password on every DB node.

Supported topologies:

- Single-node bootstrap:
  - 1 primary node
- HA cluster:
  - 1 primary node
  - 1 or more secondary nodes

For production automatic failover, use at least 3 database nodes so Group Replication can keep quorum after a single node loss.

## API payloads

### Deploy

Full example (with app user and DB):

```json
{
  "cluster_name": "prodCluster",
  "primary_ip": "192.168.122.154",
  "standby_ips": ["192.168.122.111"],
  "new_user": "appuser",
  "new_user_password": "AppUser#2026",
  "new_user_ssl_required": true,
  "new_db": "appdb",
  "assume_prepared": false,
  "bootstrap_router": true,
  "ssh_port": 22,
  "mysql_port": 3306,
  "mysql_version": 8,
  "step_timeout_seconds": 900
}
```

Single-node example — set `standby_ips` to `[]`:

```json
{
  "cluster_name": "prodCluster",
  "primary_ip": "192.168.122.154",
  "standby_ips": [],
  "bootstrap_router": false,
  "ssh_port": 22,
  "mysql_port": 3306,
  "mysql_version": 8,
  "step_timeout_seconds": 900
}
```

### Deploy response

The `202 Accepted` response includes the job and the generated cluster credentials:

```json
{
  "status": "accepted",
  "message": "MySQL cluster deployment started",
  "data": {
    "id": "abc123...",
    "status": "running",
    "secret": {
      "admin_user": "clusteradmin",
      "admin_password": "<generated>"
    }
  }
}
```

Save `admin_user` and `admin_password` — they are required for the Collect Metrics request.

### Get Job response

`GET /cluster/mysql/jobs/{jobID}` returns the secret alongside the job:

```json
{
  "status": "ok",
  "message": "success",
  "data": {
    "id": "abc123...",
    "status": "completed",
    "secret": {
      "admin_user": "clusteradmin",
      "admin_password": "<stored password>"
    }
  }
}
```

### Resume a failed job

`POST /cluster/mysql/jobs/{jobID}/resume`

```json
{
  "new_user_password": "AppUser#2026"
}
```

Omit `new_user_password` if the original deploy had no `new_user`. The cluster-admin password is reused automatically from stored job state.

The resume `202` response also returns the secret in the same format as the deploy response.

### Rollback

`POST /cluster/mysql/jobs/{jobID}/rollback`

```json
{}
```

## Field behavior

- `primary_ip`: node used to create the initial InnoDB Cluster.
- `standby_ips`: optional list of replica nodes to add after cluster creation. Set to `[]` for single-node.
- `cluster_admin_username`: optional override for the internally managed cluster admin account. Defaults to `clusteradmin`.
- `bootstrap_router`: when `true`, bootstraps MySQL Router on all DB nodes.
- `mysql_version`: target MySQL major version. Supported: `8`. Defaults to `8`.
- `mysql_port`: MySQL port on each target node. Defaults to `3306`.
- `mysql_recovery_method`: optional Ansible variable override for how standbys join the cluster. Defaults to `auto`, which prefers faster incremental recovery and falls back to clone when necessary.
- `ssh_port`: SSH port for the target nodes. Defaults to `22`.
- `assume_prepared`: when `true`, skips preflight and instance-configuration steps.
- `new_user`, `new_user_password`, `new_db`: optional application database bootstrap.
- `new_user_ssl_required`: controls whether the created MySQL user requires SSL.

SSH user and private key are configured once on the API host through `CLUSTER_SSH_USER` and `CLUSTER_SSH_PRIVATE_KEY_PATH`.

The generated MySQL instance config also points to MySQL's default auto-generated TLS files in the data directory:

- `/var/lib/mysql/ca.pem`
- `/var/lib/mysql/server-cert.pem`
- `/var/lib/mysql/server-key.pem`

## Collect Metrics

`POST /cluster/mysql/metrics`

Point `port` at HAProxy or MySQL Router when a proxy is in front of the cluster. `host` is optional — when omitted the server uses the `PROXY_HOST` environment variable (typically `127.0.0.1`).

**Always use the cluster admin credentials** (`admin_user` / `admin_password` from the deploy response). The cluster admin requires `PROCESS` privilege and access to `performance_schema`. Application users (`new_user`) do not have these privileges.

### Request

```json
{
  "port": 25010,
  "user": "clusteradmin",
  "password": "<admin_password from deploy response>",
  "ssl_mode": "disable",
  "connect_timeout": 10,
  "categories": [],
  "databases": [],
  "limit": 20
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `host` | `PROXY_HOST` env | HAProxy, MySQL Router, or primary IP. Omit to use server default. |
| `port` | `3306` | HAProxy listen port or direct MySQL port |
| `user` | — | Cluster admin user (`clusteradmin`). Must have `PROCESS` + `performance_schema` access. |
| `password` | — | Cluster admin password from deploy response |
| `database` | `information_schema` | Connection default database |
| `ssl_mode` | `disable` | `disable` or `require` |
| `connect_timeout` | `10` | Seconds |
| `categories` | all | Leave empty for all 7, or specify a subset |
| `databases` | all | Optional list of database names to filter per-database results. Empty = all user databases. |
| `limit` | `20` | Top-N cap for slow queries and digest lists. Max 500. |
| `from` | — | ISO 8601 lower bound for slow query filter |
| `to` | — | ISO 8601 upper bound for slow query filter |

### Response envelope

Every response includes `database_count` — the total number of user databases on the server (excludes `information_schema`, `mysql`, `performance_schema`, `sys`):

```json
{
  "collected_at": "...",
  "host": "127.0.0.1",
  "port": 25010,
  "database_count": 3,
  "categories": { ... },
  "errors": { ... }
}
```

### Available categories

| Category | Source | Description |
|----------|--------|-------------|
| `cluster` | `performance_schema.replication_group_members` | InnoDB Cluster / GR membership, current primary host and port, all member states and roles |
| `uptime` | `performance_schema.global_status` | MySQL server process uptime in seconds and human-readable form |
| `connections` | `information_schema.PROCESSLIST` + `performance_schema.global_status` | Active/sleeping threads, utilization %, max-used, aborted connects, per-database breakdown (all user databases shown, including those with zero connections) |
| `replication` | `performance_schema.replication_group_member_stats` + `replication_applier_status_by_worker` | GR member certification queue, conflicts detected, applier worker lag per channel |
| `performance` | `performance_schema.global_status` | QPS/TPS, InnoDB buffer pool hit ratio and dirty/free pages, temp-table disk ratio, sort pressure, row-lock waits |
| `query` | `performance_schema.events_statements_summary_by_digest` + `information_schema.PROCESSLIST` | Avg/P95/P99 digest latency, top queries by mean and total time, slow queries, lock waits, deadlocks, full-scan digest entries |
| `maintenance` | `information_schema.TABLES` + `performance_schema.metadata_locks` + `performance_schema.global_status` | InnoDB purge lag (history list length), fragmented tables, metadata lock contention, open/opened table counts |

### `databases` filter scope

When `databases` is non-empty, only matching database names appear in these fields:

- `connections.by_database` — per-database connection counts
- `query.slow_queries` — filtered by the query's active database
- `query.high_full_scan_tables` — filtered by digest schema name
- `maintenance.fragmented_tables` — filtered by table schema name

`database_count` always reflects the total on the server regardless of the filter.

### Detecting failover

MySQL InnoDB Cluster has no failover history API. Use these fields to detect that a primary change occurred:

- `cluster.primary_host` / `cluster.primary_port` — the current elected primary
- `cluster.member_count` — if lower than expected, a node left the cluster
- `cluster.members[].state` — `ERROR` or `UNREACHABLE` indicates a troubled node
- `uptime.uptime_seconds` — a short uptime means the server recently restarted

### `limit` scope

`limit` applies to list-type fields only:
- `query.slow_queries`
- `query.top_by_mean_exec_ms`
- `query.top_by_total_exec_ms`
- `query.high_full_scan_tables`
- `maintenance.fragmented_tables`

All snapshot categories (`cluster`, `uptime`, `connections`, `replication`, `performance`) return full results regardless of `limit`.

`from` / `to` filters `query.slow_queries` by query start time only.

## Deployment flow

1. Preflight checks confirm MySQL, MySQL Shell, and connectivity prerequisites are present.
2. Instance configuration prepares each node for InnoDB Cluster and creates or updates the cluster admin account.
3. Cluster creation runs on the requested primary node.
4. Secondary nodes are added when `standby_ips` is not empty. By default, MySQL Shell uses `recoveryMethod=auto`, which prefers faster incremental recovery when possible and falls back to clone when needed.
5. Group Replication auto-start and auto-rejoin settings are enabled on all members.
6. MySQL Router is bootstrapped on all nodes when `bootstrap_router` is enabled.
7. Verification checks cluster health and router state.
8. Optional application database and user creation runs on the primary.

## Architecture Overview

```text
                         App Clients
                              |
                              v
                        +------------+
                        |  HAProxy    |
                        |  optional   |
                        +------------+
                              |
                              v
                  +-------------------------+
                  | MySQL Router on DB nodes|
                  | optional bootstrap      |
                  +-------------------------+
                     |            |            |
                     v            v            v
                +---------+  +---------+  +---------+
                | Primary |  |Secondary|  |Secondary|
                | MySQL   |  | MySQL   |  | MySQL   |
                +---------+  +---------+  +---------+
                     \            |            /
                      \           |           /
                       +---------------------+
                       | InnoDB Cluster GR   |
                       | managed by mysqlsh  |
                       +---------------------+
```

## Optional modes

Single-node mode:

- Leave `standby_ips` empty.
- The `add_instances` step is skipped automatically.

Prepared-node mode:

- Set `assume_prepared` to `true` if the nodes were already prepared earlier.
- The `preflight` and `configure_instances` steps are skipped.

No-router mode:

- Set `bootstrap_router` to `false`.
- The router bootstrap step is skipped.

## What the automation manages

The MySQL playbooks manage:

- InnoDB Cluster lifecycle through `mysqlsh`
- Cluster member addition on secondary nodes
- MySQL Router bootstrap and systemd service installation
- Optional application database and user creation
- Rollback for router services and cluster dissolve

## Important behavior

- MySQL supports both single-node and multi-node deployments in this project.
- Rollback support exists for MySQL jobs through the rollback API.
- If `bootstrap_router` is enabled, router services are created on all target DB nodes.
- Auto-rejoin behavior is explicitly configured after cluster formation:
  - `group_replication_start_on_boot = ON`
  - `group_replication_autorejoin_tries = 3`
  - `group_replication_unreachable_majority_timeout = 30`
  - `group_replication_exit_state_action = READ_ONLY`
- The cluster admin password is generated internally if not supplied and stored per-job. It is returned in the deploy `202` response and reused automatically on resume.
