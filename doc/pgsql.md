# PostgreSQL HA with Patroni + etcd

This project deploys PostgreSQL HA using `Patroni` and `etcd` for distributed consensus and automatic leader election.

## Assumptions

- PostgreSQL is already installed on every node.
- `patroni[etcd]` is already installed.
- `etcd` is already installed.
- The API host already has the matching private key for the cloud-template SSH user, such as `clusterops`.
- That SSH user can run `sudo` without a password on every DB node.

Supported topologies:

- Single-node bootstrap:
  - 1 primary node
- HA cluster:
  - 1 primary node
  - 1 or more standby nodes

## What the automation writes

On every node the playbooks create:

- `/etc/etcd/etcd.conf`
- `/etc/systemd/system/etcd.service`
- `/etc/patroni/patroni.yml`
- `/etc/systemd/system/patroni.service`

The generated Patroni config follows this layout:

- `scope: <cluster_name>`
- `namespace: /db/`
- REST API on `:8008`
- etcd client endpoints on `:2379`
- PostgreSQL on `:5432` (or `postgres_port` if provided)
- PostgreSQL SSL enabled with the distro default snakeoil certificate and key:
  - `/etc/ssl/certs/ssl-cert-snakeoil.pem`
  - `/etc/ssl/private/ssl-cert-snakeoil.key`
- `synchronous_mode: true` and `synchronous_mode_strict: false` for DCS-managed synchronous replication
- `password_encryption: scram-sha-256` enforced cluster-wide
- `pg_stat_statements` loaded via `shared_preload_libraries`

## API payloads

### Deploy

Minimal (no app DB):

```json
{
  "cluster_name": "postgres-cluster",
  "primary_ip": "10.0.0.1",
  "standby_ips": ["10.0.0.2", "10.0.0.3"],
  "ssh_port": 22,
  "postgres_port": 5432,
  "postgres_version": 16,
  "step_timeout_seconds": 900
}
```

Full (with app user and DB):

```json
{
  "cluster_name": "postgres-cluster",
  "primary_ip": "10.0.0.1",
  "standby_ips": ["10.0.0.2", "10.0.0.3"],
  "new_user": "appuser",
  "new_user_password": "AppUser#2026",
  "new_user_ssl_required": true,
  "new_db": "appdb",
  "ssh_port": 22,
  "postgres_port": 5432,
  "postgres_version": 16,
  "step_timeout_seconds": 900
}
```

**`postgres_version`** controls which major version paths are used during Ansible provisioning. Supported: `14`, `15`, `16`, `17`, `18`. Default: `16`.

Single-node example — set `standby_ips` to `[]`:

```json
{
  "cluster_name": "postgres-cluster",
  "primary_ip": "10.0.0.1",
  "standby_ips": [],
  "postgres_version": 16,
  "ssh_port": 22,
  "postgres_port": 5432,
  "step_timeout_seconds": 900
}
```

### Deploy response

The `202 Accepted` response includes the job and the generated cluster credentials:

```json
{
  "status": "accepted",
  "message": "PostgreSQL cluster deployment started",
  "data": {
    "id": "abc123...",
    "status": "running",
    "secret": {
      "postgres_user": "postgres",
      "postgres_password": "<generated>",
      "replicator_user": "replicator",
      "replicator_password": "<generated>",
      "admin_password": "<generated>"
    }
  }
}
```

Save `postgres_user` and `postgres_password` — they are needed for the Collect Metrics request. All five secret fields are reused automatically on resume.

### Get Job response

`GET /cluster/pgsql/jobs/{jobID}` returns the secret alongside the job:

```json
{
  "status": "ok",
  "message": "success",
  "data": {
    "id": "abc123...",
    "status": "completed",
    "secret": {
      "postgres_user": "postgres",
      "postgres_password": "<stored password>",
      "replicator_user": "replicator",
      "replicator_password": "<stored password>",
      "admin_password": "<stored password>"
    }
  }
}
```

### Resume a failed job

`POST /cluster/pgsql/jobs/{jobID}/resume`

```json
{
  "new_user_password": "AppUser#2026"
}
```

Omit `new_user_password` if the original deploy had no `new_user`. Stored secrets (postgres, replicator, admin passwords) are reused automatically from the saved job state.

The resume `202` response also returns the secret in the same format as the deploy response.

## Deployment flow

1. Preflight checks confirm `psql`, `patroni`, `etcd`, and the PostgreSQL server binaries are present on every node.
2. Base configuration stops the distro-managed PostgreSQL service, installs the `etcd` systemd unit, and installs the Patroni systemd unit.
3. Primary and standby steps write node-specific Patroni configs and reset the PostgreSQL data directories for a fresh Patroni bootstrap.
4. Cluster bootstrap starts `etcd` on all nodes, then starts Patroni on the primary, then on standby nodes when `standby_ips` is not empty.
5. Verification checks systemd state, Patroni REST API membership, and `pg_stat_replication`.

When `new_user_ssl_required` is omitted, it defaults to `true`.
SSH user and private key are configured once on the API host through `CLUSTER_SSH_USER` and `CLUSTER_SSH_PRIVATE_KEY_PATH`.

## Collect Metrics

`POST /cluster/pgsql/metrics`

Point `port` at HAProxy when a proxy is in front of the cluster. `host` is optional — when omitted the server uses the `PROXY_HOST` environment variable (typically `127.0.0.1`). Use `node_ips` for Patroni REST auto-discovery (`cluster` and `failover` categories).

**Always use the PostgreSQL superuser credentials** (`postgres_user` / `postgres_password` from the deploy response). The `postgres` superuser has full access to `pg_stat_*` views and `pg_stat_statements`. Application users (`new_user`) and the replicator user do not have these privileges.

### Request

```json
{
  "port": 25005,
  "node_ips": ["10.0.0.1", "10.0.0.2", "10.0.0.3"],
  "user": "postgres",
  "password": "<postgres_password from deploy response>",
  "ssl_mode": "require",
  "connect_timeout": 10,
  "patroni_port": 8008,
  "categories": [],
  "databases": [],
  "limit": 20
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `host` | `PROXY_HOST` env | HAProxy or primary IP. Omit to use server default. |
| `port` | `5432` | HAProxy listen port or direct PostgreSQL port |
| `node_ips` | — | All cluster member IPs. The collector calls `GET /leader` on each to auto-discover the Patroni leader. Required for `cluster` and `failover` categories. |
| `user` | — | PostgreSQL superuser (`postgres`) |
| `password` | — | Superuser password from deploy response |
| `database` | `postgres` | Connection default database |
| `ssl_mode` | `disable` | `disable` or `require` |
| `connect_timeout` | `10` | Seconds |
| `patroni_port` | `8008` | Patroni REST port on each node |
| `categories` | all | Leave empty for all 8, or specify a subset |
| `databases` | all | Optional list of database names to filter per-database results. Empty = all databases. |
| `limit` | `20` | Top-N cap for slow queries, stale tables, and seq-scan tables. Max 500. |
| `from` | — | ISO 8601 lower bound for failover events and slow queries |
| `to` | — | ISO 8601 upper bound for failover events and slow queries |

### Response envelope

Every response includes `database_count` — the total number of non-template databases on the server (includes `postgres`):

```json
{
  "collected_at": "...",
  "host": "127.0.0.1",
  "port": 25005,
  "database_count": 3,
  "categories": { ... },
  "errors": { ... }
}
```

### Available categories

| Category | Source | Description |
|----------|--------|-------------|
| `cluster` | Patroni REST `/` + `/cluster` + `/config` | HA state, node roles (leader/sync_standby/replica), DCS health, TTL, loop_wait, retry_timeout |
| `uptime` | `pg_postmaster_start_time()` | Server start time, uptime in seconds and human-readable form |
| `failover` | Patroni REST `/history` | Timeline change events, time since last failover, supports `from`/`to` filter |
| `connections` | `pg_database` + `pg_stat_activity` | Active, idle, idle-in-transaction, lock-waiters, wait-event breakdown, per-database counts (all databases shown including those with zero connections) |
| `replication` | `pg_stat_replication` + `pg_replication_slots` + `pg_settings` | Streaming lag (LSN pipeline), sync state, WAL level, max_wal_senders, slot lag bytes |
| `performance` | `pg_stat_bgwriter` + `pg_stat_database` | Avg TPS since stats reset, cache hit ratio, temp files/bytes, checkpoint pressure, bgwriter stats |
| `query` | `pg_stat_activity` + `pg_stat_statements` | Avg/P95/P99 latency (when `pg_stat_statements` is available), slow queries, lock/deadlock counts, seq-scan vs index-scan ratio, high seq-scan tables |
| `maintenance` | `pg_stat_progress_vacuum` + `pg_stat_user_tables` + `pg_replication_slots` + `pg_locks` | Running autovacuum workers, stale tables (high dead tuple count), tables needing vacuum, XID wraparound age and risk level, logical slot lag, lock grant/wait counts |

### `databases` filter scope

When `databases` is non-empty, only matching database names appear in these fields:

- `connections.by_database` — per-database connection counts
- `query.slow_queries` — filtered by the query's active database (`datname`)
- `maintenance.workers` — autovacuum workers filtered by database name
- `maintenance.logical_slot_lag` — logical replication slots filtered by slot database

`database_count` always reflects the total on the server regardless of the filter.

### Failover detection

The `failover` category returns Patroni timeline change history from `/history`:

```json
"failover": {
  "current_timeline": 3,
  "total_events": 2,
  "time_since_last_failover_seconds": 3600,
  "events": [
    { "timeline": 2, "lsn": "...", "reason": "...", "occurred_at": "..." }
  ]
}
```

The `cluster` category shows the current leader and all member roles, which confirms which node holds primary after a failover:

```json
"cluster": {
  "role": "primary",
  "members": [
    { "name": "node1", "role": "leader",       "state": "running" },
    { "name": "node2", "role": "sync_standby", "state": "streaming" },
    { "name": "node3", "role": "replica",      "state": "streaming" }
  ]
}
```

### `limit` scope

`limit` applies to list-type fields only:
- `query.slow_queries`
- `query.high_seq_scan_tables`
- `query.top_by_mean_exec_ms`
- `query.top_by_total_exec_ms`
- `maintenance.stale_tables`

All snapshot categories (`connections`, `replication`, `performance`, `uptime`, `cluster`, `failover`) return full results regardless of `limit`.

`from` / `to` filters `failover.events` (by `occurred_at`) and `query.slow_queries` (by query start time).

## Optional modes

Single-node mode:

- Set `standby_ips` to `[]`.
- Only the primary node is configured and bootstrapped.

HA mode:

- Set `standby_ips` to one or more standby node IPs.
- Patroni bootstraps the primary first then joins standby nodes.

## Architecture Overview

```text
                    App Clients / Applications
                               |
                               v
                   +------------------------+
                   |        HAProxy         |
                   | TCP proxy, port 25005  |
                   | httpchk GET /leader    |
                   | routes to primary only |
                   +------------------------+
                               |
            +------------------+------------------+
            |                  |                  |
            v                  v                  v
   +----------------+  +----------------+  +----------------+
   | Patroni Leader |  | Patroni Sync   |  | Patroni Async  |
   | PostgreSQL     |  | Standby        |  | Replica        |
   | (write)        |  | (streaming)    |  | (streaming)    |
   +----------------+  +----------------+  +----------------+
            |                  |                  |
            +------------------+------------------+
                               |
          +--------------------+--------------------+
          |                    |                    |
   +-------------+    +-------------+    +-------------+
   | etcd node 1 |    | etcd node 2 |    | etcd node 3 |
   | (DCS)       |    | (DCS)       |    | (DCS)       |
   +-------------+    +-------------+    +-------------+
```

HAProxy uses `httpchk GET /leader` on Patroni port `8008` — only the node returning `200 OK` receives connections. Patroni auto-manages `synchronous_standby_names` via `synchronous_mode: true`.

## Important behavior

- The automation is intended for a fresh cluster bootstrap.
- It clears `/var/lib/etcd` and the PostgreSQL data directory under `/var/lib/postgresql/<major>/main` once per deployment job.
- Resume retries for the same job do not wipe the PostgreSQL or etcd data again.
- Patroni becomes the process manager for PostgreSQL; the distro `postgresql@...` service is stopped and disabled.
- Credentials (postgres, replicator, admin) are generated internally if not provided and stored per-job. They are returned in the deploy `202` response and reused automatically on resume.
