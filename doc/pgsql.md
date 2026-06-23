# PostgreSQL HA with Patroni + etcd

This document covers what gets deployed on each VM, what the tech stack is, how the config files look, and how the cluster workflow operates.

See [doc/api.md](api.md) for the full API payload reference.  
**Diagrams (draw.io):** [diagrams/pgsql-cluster.drawio](diagrams/pgsql-cluster.drawio)

---

## Tech Stack

| Layer | Technology | Purpose |
|-------|-----------|---------|
| Database engine | PostgreSQL 14–18 | Data storage and SQL interface |
| HA manager | Patroni | Leader election, automatic failover, config management |
| Distributed consensus | etcd | Patroni DCS (Distributed Configuration Store) — stores cluster state |
| Health check | Patroni REST API (`:8008`) | Used by HAProxy to route to the current leader |
| Automation | Ansible playbooks | Deploy, configure, and manage the cluster lifecycle |

---

## What Is Installed on Each VM

Every cluster node (primary and standbys) runs the same stack:

```
DB Node VM
├── PostgreSQL server        (data engine; managed by Patroni, not systemd)
├── Patroni                  (HA manager; runs as patroni.service)
│   └── patroni.service      (systemd-managed; controls PostgreSQL process)
└── etcd                     (distributed key-value store for DCS)
    └── etcd.service         (systemd-managed)
```

**PostgreSQL is NOT managed by the distro `postgresql@...` systemd service after deploy.** Patroni takes over as the process manager. The distro service is stopped and disabled.

---

## Supported Topologies

| Mode | Nodes | Failover |
|------|-------|---------|
| Single-node | 1 primary | No |
| HA cluster | 1 primary + 1 or more standbys | Yes (automatic via Patroni) |

For quorum, etcd needs an odd number of nodes. Use **3 or more** for production.

---

## What Happens on Each VM After Deploy

### Phase 1 — Preflight
Each node is verified to have PostgreSQL, Patroni, and etcd binaries installed and reachable from the proxy.

### Phase 2 — Base configuration
On each node:
- `postgresql@<version>` systemd service is stopped and disabled
- etcd systemd unit and config are written
- Patroni systemd unit is written

### Phase 3 — Node configuration
On each node, the playbook writes:
- `/etc/etcd/etcd.conf` — etcd member config (unique per node)
- `/etc/patroni/patroni.yml` — Patroni config (unique per node)

PostgreSQL data directory (`/var/lib/postgresql/<major>/main`) is cleared for a fresh bootstrap.

### Phase 4 — Cluster bootstrap
1. `etcd.service` starts on all nodes simultaneously
2. etcd cluster forms (peer discovery via `initial-cluster` list)
3. `patroni.service` starts on the **primary node**
4. Patroni bootstraps PostgreSQL, initializes the data directory, creates users
5. Patroni pushes `bootstrap.dcs` config to etcd:
   - `synchronous_mode: true`
   - `synchronous_mode_strict: false`
6. The API PATCH-es Patroni REST (`/config`) to enforce sync mode — idempotent, applies to both new and existing clusters
7. `patroni.service` starts on **standby nodes**
8. Each standby clones the primary data directory via `pg_basebackup` and starts streaming

### Phase 5 — Verification
- Patroni REST `/leader` confirms primary election
- Patroni REST `/replica` confirms all standbys are streaming
- `pg_stat_replication` row count matches `len(standby_ips)`
- Patroni cluster view confirms `sync_standby` is elected (when standbys > 0)

### Phase 6 — Application DB and user
If `new_db` and `new_user` are set:

When `new_user_superuser: true` (default):
```sql
CREATE ROLE appuser WITH LOGIN SUPERUSER CREATEDB CREATEROLE REPLICATION BYPASSRLS PASSWORD 'password';
CREATE DATABASE appdb OWNER appuser;
GRANT ALL PRIVILEGES ON DATABASE appdb TO appuser;
```

When `new_user_superuser: false`:
```sql
CREATE ROLE appuser WITH LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD 'password';
CREATE DATABASE appdb OWNER appuser;
GRANT ALL PRIVILEGES ON DATABASE appdb TO appuser;
```

`new_user_ssl_required` (default `true`) controls `pg_hba.conf` rules written by Patroni:
- `true` → `hostssl all appuser 0.0.0.0/0 scram-sha-256` + `hostnossl all appuser 0.0.0.0/0 reject`
- `false` → `host all appuser 0.0.0.0/0 scram-sha-256`

---

## Config Files Written to Each VM

### `/etc/etcd/etcd.conf`

```ini
ETCD_NAME=node1
ETCD_DATA_DIR=/var/lib/etcd
ETCD_LISTEN_CLIENT_URLS=http://0.0.0.0:2379
ETCD_ADVERTISE_CLIENT_URLS=http://10.0.0.1:2379
ETCD_LISTEN_PEER_URLS=http://0.0.0.0:2380
ETCD_INITIAL_ADVERTISE_PEER_URLS=http://10.0.0.1:2380
ETCD_INITIAL_CLUSTER=node1=http://10.0.0.1:2380,node2=http://10.0.0.2:2380,node3=http://10.0.0.3:2380
ETCD_INITIAL_CLUSTER_TOKEN=etcd-cluster-<cluster_name>
ETCD_INITIAL_CLUSTER_STATE=new
```

### `/etc/patroni/patroni.yml`

```yaml
scope: pg-prod
namespace: /db/
name: node1

restapi:
  listen: 0.0.0.0:8008
  connect_address: 10.0.0.1:8008

etcd:
  hosts: 10.0.0.1:2379,10.0.0.2:2379,10.0.0.3:2379

bootstrap:
  dcs:
    ttl: 30
    loop_wait: 10
    retry_timeout: 10
    maximum_lag_on_failover: 1048576
    synchronous_mode: true
    synchronous_mode_strict: false
    postgresql:
      use_pg_rewind: true
      use_slots: true
      parameters:
        wal_level: replica
        hot_standby: "on"
        max_connections: 200
        max_wal_senders: 10
        wal_keep_size: 1024
        password_encryption: scram-sha-256
        shared_preload_libraries: pg_stat_statements

  initdb:
    - encoding: UTF8
    - data-checksums

postgresql:
  listen: 0.0.0.0:5432
  connect_address: 10.0.0.1:5432
  data_dir: /var/lib/postgresql/16/main
  bin_dir: /usr/lib/postgresql/16/bin
  config_dir: /etc/postgresql/16/main
  pgpass: /tmp/pgpass

  authentication:
    superuser:
      username: postgres
      password: <postgres_password>
    replication:
      username: replicator
      password: <replicator_password>

  parameters:
    unix_socket_directories: /var/run/postgresql
    ssl: "on"
    ssl_cert_file: /etc/ssl/certs/ssl-cert-snakeoil.pem
    ssl_key_file: /etc/ssl/private/ssl-cert-snakeoil.key

tags:
  nofailover: false
  noloadbalance: false
  clonefrom: false
  nosync: false       ← all nodes eligible for sync_standby election
```

---

## Cluster Architecture

```
            Application Clients
                    │
                    ▼
          ┌──────────────────┐
          │     HAProxy       │
          │  :25042 (TCP)    │
          │  httpchk GET /leader on :8008 │
          │  → only current leader gets traffic │
          └────────┬─────────┘
                   │
       ┌───────────┼───────────┐
       │           │           │
       ▼           ▼           ▼
  ┌─────────┐ ┌─────────┐ ┌─────────┐
  │ Node 1  │ │ Node 2  │ │ Node 3  │
  │ Patroni │ │ Patroni │ │ Patroni │
  │ :8008   │ │ :8008   │ │ :8008   │
  │ [leader]│ │[sync_sb]│ │[replica]│
  └────┬────┘ └────┬────┘ └────┬────┘
       │ streaming replication  │
  ┌─────────┐ ┌─────────┐ ┌─────────┐
  │Postgres │ │Postgres │ │Postgres │
  │:5432    │◀│:5432    │ │:5432    │
  │(primary)│  streaming│  streaming│
  └─────────┘ └─────────┘ └─────────┘
       │           │           │
       └─────┬─────┘           │
             │    etcd DCS     │
       ┌─────▼───────────────────┐
       │  etcd cluster (3 nodes) │
       │  stores Patroni leader  │
       │  lock + cluster config  │
       └─────────────────────────┘
```

### Node roles

| Role | Description |
|------|-------------|
| `leader` | Current primary — accepts writes, holds the Patroni DCS lock |
| `sync_standby` | Synchronous standby — commits only confirmed by this node; RPO = 0 |
| `replica` | Asynchronous standby — small lag; does not block primary commits |

### Synchronous replication

`synchronous_mode: true` (DCS-managed) means:
- Patroni sets PostgreSQL `synchronous_standby_names` to the current `sync_standby` node
- Primary commits only after the `sync_standby` acknowledges the WAL write
- After a failover, Patroni automatically elects the remaining standby as the new `sync_standby`

`synchronous_mode_strict: false` means: if no sync_standby is available, the primary falls back to async mode (accepts writes) rather than blocking.

### HAProxy health check

HAProxy checks `GET http://<node>:8008/leader` on each backend. Patroni returns:
- `200 OK` — this node is the current leader (primary)
- `503` — this node is a standby or not ready

Only the leader receives client connections. After a failover, Patroni elects a new leader within seconds, and HAProxy reroutes automatically without any config change.

---

## Failover Flow

```
1. Primary node (Node 1) dies or loses etcd lock
           │
           ▼
2. Patroni on Node 2 (sync_standby) detects leader loss
   → tries to acquire etcd lock
           │
           ▼
3. Node 2 wins election → becomes leader
   → promotes PostgreSQL to read-write
   → Node 2 REST /leader now returns 200
           │
           ▼
4. HAProxy health check fires (≤10s interval)
   → Node 1: /leader returns 503 (or no response)
   → Node 2: /leader returns 200
   → HAProxy reroutes all new connections to Node 2
           │
           ▼
5. Node 3 (was replica) detects new leader via etcd
   → Patroni elects Node 3 as sync_standby
   → pg_rewind used if Node 3 diverged during transition
           │
           ▼
6. Cluster stable: Node 2=leader, Node 3=sync_standby
   (Node 1 rejoins as replica when it recovers)
```

---

## Add Member Workflow

```
POST /cluster/pgsql/members
  { "job_id": "abc", "member_ips": ["10.0.0.4"] }
           │
           ▼
  1. Register new node as etcd learner
  2. Configure etcd on new node (joins existing cluster)
  3. Promote etcd learner to full voting member
  4. Write /etc/patroni/patroni.yml on new node
  5. Start patroni.service on new node
  6. Patroni clones data from primary via pg_basebackup
  7. New node starts streaming replication
  8. Patroni verifies /replica API returns 200
  9. If no sync_standby exists, new node may be elected sync_standby
```

---

## Metrics Collection

Metrics connect through HAProxy — **not directly to DB node IPs**:

```
POST /cluster/pgsql/metrics
  { "job_id": "abc", "proxy_port": 25042 }
           │
           ▼
  API → 127.0.0.1:25042 → HAProxy → Patroni leader check → PostgreSQL primary :5432
```

Use the **postgres superuser credentials** (`postgres_user` / `postgres_password` from the deploy response). The `postgres` user has full access to `pg_stat_*` views and `pg_stat_statements`. Application users do not have these privileges.

For the `cluster` and `failover` categories, the collector also calls Patroni REST directly on each `node_ips` entry to get HA state and timeline history.

---

## Auto-Rejoin and Node Recovery

Patroni handles all recovery scenarios automatically. No manual intervention is needed for typical single-node failures.

### Key recovery parameters (DCS-managed, apply to all nodes)

| Parameter | Value | Effect |
|-----------|-------|--------|
| `ttl` | 30 s | Leader lock expires after 30 s without renewal; triggers failover |
| `loop_wait` | 10 s | Patroni health-check interval |
| `retry_timeout` | 10 s | Timeout for etcd operations |
| `maximum_lag_on_failover` | 1 048 576 bytes (1 MB) | Standby is eligible for promotion only if lag < 1 MB |
| `use_pg_rewind` | true | Prefer fast WAL diff over full re-clone when a node has diverged |
| `remove_data_directory_on_rewind_failure` | true | If pg_rewind fails, wipe data dir and re-clone via pg_basebackup |
| `remove_data_directory_on_diverged_timelines` | true | If timelines diverge unresolvably, wipe and re-clone |

### Scenario 1 — Standby goes down and comes back

```
1. Standby loses connectivity or is rebooted
2. Patroni on the standby detects it is no longer streaming
3. Patroni attempts pg_rewind to fast-sync the diverged WAL from current primary
   → Success: streaming replication resumes (seconds)
   → Failure: data dir is wiped; pg_basebackup re-clones from primary (minutes)
4. Standby rejoins as replica; if no sync_standby exists, Patroni elects it as sync_standby
```

`pg_rewind` works by shipping only the blocks that changed since the last common checkpoint — much faster than a full clone for short outages.

### Scenario 2 — Primary goes down (automatic failover)

```
1. Primary (Node 1) dies or loses network
2. Patroni on Node 1 fails to renew the etcd leader key
3. After ttl = 30 s, the leader key expires
4. Patroni on Node 2 (sync_standby) wins the election:
   → acquires etcd lock
   → promotes PostgreSQL from standby to read-write (pg_ctl promote)
   → REST /leader on Node 2 now returns 200
5. HAProxy health check (≤10 s interval):
   → Node 1: /leader → 503 (or no response) → removed from pool
   → Node 2: /leader → 200 → receives all new connections
6. Node 3 (replica) detects new leader via etcd, re-attaches streaming replication to Node 2
   Patroni may promote Node 3 to sync_standby
```

**Client impact:** connections in flight to Node 1 at the moment of failure are dropped. New connections route to Node 2 within ≤40 s (ttl + HAProxy interval).

### Scenario 3 — Old primary comes back after failover

```
1. Node 1 recovers and patroni.service starts
2. Patroni on Node 1 detects an active leader key in etcd (Node 2 is now leader)
3. Patroni demotes Node 1:
   → pg_rewind runs to walk back Node 1's timeline to Node 2's divergence point
   → If rewind fails: data directory is wiped, pg_basebackup re-clones from Node 2
4. Node 1 restarts as a streaming replica of Node 2
5. Patroni REST /replica on Node 1 returns 200; HAProxy does NOT route writes to it
```

Node 1 never "fights" for leadership. Patroni reads the etcd lock and immediately yields.

### Scenario 4 — etcd node goes down

etcd uses Raft consensus. A 3-node etcd cluster tolerates **1 node failure** and continues serving reads and writes. Patroni keeps running as long as etcd has quorum.

When the etcd node recovers:
```
1. etcd on that node starts, discovers peers via initial-cluster
2. etcd syncs Raft log from the remaining members (automatic)
3. etcd rejoins the cluster as a voting member — no Patroni or PostgreSQL restart needed
```

### Recovery scenario matrix

| Scenario | Mechanism | Recovery time |
|----------|-----------|---------------|
| Standby rebooted, primary still running | pg_rewind → resume streaming | Seconds (fast WAL sync) |
| Standby diverged, rewind fails | Full pg_basebackup re-clone | Minutes (data size dependent) |
| Primary dies, standby takes over | Patroni leader election via etcd TTL | ≤30 s (failover) + ≤10 s (HAProxy) |
| Old primary returns after failover | pg_rewind to new leader's timeline | Seconds to minutes |
| etcd single node dies | Raft quorum maintained (3-node: tolerates 1) | Immediate (other 2 nodes serve) |
| etcd node returns | Raft log sync | Seconds |

---

## Important Behaviors

- PostgreSQL data directory and etcd data are cleared **once per deployment job**. Resuming a failed job does not clear data again.
- `pg_rewind` is enabled — this allows a former primary that diverged to rejoin as a standby without a full data copy.
- `pg_stat_statements` is loaded via `shared_preload_libraries` and available immediately after deploy.
- `password_encryption: scram-sha-256` is enforced cluster-wide — MD5 auth is not supported.
- SSL is enabled using the distro default snakeoil certificate. Replace with a real cert for production.
- Single-node mode: `standby_ips: []` — only primary is bootstrapped. No HA.
- The deploy response credentials (`postgres_password`, `replicator_password`, `admin_password`) are stored per-job and used automatically when `job_id` is supplied to the metrics endpoint.
