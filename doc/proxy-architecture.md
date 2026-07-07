# Proxy Architecture

This document explains how the erawan-cluster Cluster Management API and proxy node work together — what runs on each, how cluster jobs are controlled, and how SQL traffic flows through HAProxy.

**Diagrams (draw.io):** [diagrams/proxy-architecture.drawio](diagrams/proxy-architecture.drawio) · [diagrams/deploy-job-workflow.drawio](diagrams/deploy-job-workflow.drawio)

---

## Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                            PROXY NODE                           │
│                                                                 │
│  ┌──────────────────────────────┐  ┌─────────────────────────┐  │
│  │       erawan-cluster         │  │        HAProxy          │  │
│  │  Cluster Management API      │─▶│  :25041  MySQL          │  │
│  │  :8080                       │  │  :25042  PostgreSQL     │  │
│  └──────────────┬───────────────┘  └────────────┬────────────┘  │
│                 │  SSH + Ansible                │  TCP proxy    │
└─────────────────┼───────────────────────────────┼───────────────┘
                  │                               │
                  ▼                               ▼
      ┌───────────────────────┐     ┌──────────────────────────┐
      │     MySQL Cluster     │     │   PostgreSQL Cluster     │
      │  node1 :3306 primary  │     │  node1 :5432 leader      │
      │  node2 :3306 secondary│     │  node2 :5432 sync_sb     │
      │  node3 :3306 secondary│     │  node3 :5432 replica     │
      └───────────────────────┘     └──────────────────────────┘
```

A single **proxy node** runs two services:

- **erawan-cluster** (`/usr/local/bin/erawan-cluster`) — the Cluster Management API that receives requests from clients, manages cluster jobs, and runs Ansible playbooks over SSH
- **HAProxy** — the SQL traffic proxy that routes database connections from clients to the correct DB node

Client applications **never connect directly to DB node IPs**. All SQL connections go through HAProxy.

---

## How HAProxy Config Works

For each database cluster, the API writes a HAProxy tenant config file and reloads HAProxy. Every write is validated with `haproxy -c` before touching disk — a bad config for one cluster never blocks other clusters.

### MySQL config layout

This deployment does not run MySQL Router. Instead, every DB node runs a
lightweight primary-check HTTP endpoint (`:9200` by default — see
[doc/mysql.md](mysql.md#primary-check-endpoint)) that reports `200` when the
node is the current Group Replication primary and `503` otherwise. MySQL
uses **active/passive** routing just like PostgreSQL: HAProxy `httpchk`s the
primary-check endpoint on each node, and only the one currently answering
`200` receives traffic; the others sit as `backup` servers.

```haproxy
listen node_25041
    bind *:25041
    mode tcp

    balance first

    option clitcpka
    option srvtcpka
    option httpchk GET /
    http-check expect status 200

    timeout connect  500ms
    timeout check    200ms
    timeout queue    5s
    timeout client   10m
    timeout server   10m
    timeout client-fin  2s
    timeout server-fin  2s

    option redispatch 1
    retries 2

    default-server inter 500ms fastinter 100ms downinter 200ms fall 2 rise 2 on-marked-down shutdown-sessions on-marked-up shutdown-backup-sessions check port 9200

    # Backend port 3306 (MySQL)
    # Use first server as primary, others as backup
    server db1 10.0.0.1:3306 check
    server db2 10.0.0.2:3306 check backup
    server db3 10.0.0.3:3306 check backup
```

`db_port` in the create request should be the **raw MySQL port** (`3306`).
`primary_check_port` (default `9200`) is the port HAProxy health-checks to
find the current primary.

### PostgreSQL config layout

PostgreSQL also uses **active/passive** routing. HAProxy uses the Patroni REST API to health-check each node — only the Patroni leader (`GET /leader` → 200) receives connections. After a failover, Patroni elects a new leader and HAProxy reroutes within seconds.

```haproxy
listen node_25042
    bind *:25042
    mode tcp

    balance first

    option clitcpka
    option srvtcpka
    option httpchk GET /leader
    http-check expect status 200

    timeout connect  500ms
    timeout check    200ms
    timeout queue    5s
    timeout client   10m
    timeout server   10m
    timeout client-fin  2s
    timeout server-fin  2s

    option redispatch 1
    retries 2

    default-server inter 500ms fastinter 100ms downinter 200ms fall 2 rise 2 on-marked-down shutdown-sessions on-marked-up shutdown-backup-sessions check port 8008

    # Backend port 5432 (PostgreSQL)
    # Use first server as primary, others as backup
    server db1 10.0.0.1:5432 check
    server db2 10.0.0.2:5432 check backup
    server db3 10.0.0.3:5432 check backup
```

### Config storage

Tenant configs are stored as individual files in `TENANTS_DIR` (default `/var/lib/erawan-cluster/haproxy/tenants/`), one file per port:

```
/var/lib/erawan-cluster/haproxy/tenants/
  25041.cfg      ← MySQL cluster A
  25042.cfg      ← PostgreSQL cluster B
  25043.cfg      ← MySQL cluster C
```

The HAProxy main config includes this directory with `include /var/lib/erawan-cluster/haproxy/tenants/*.cfg`.

`.bak` files are created automatically before every write or delete. If HAProxy reload fails after writing, the backup is restored immediately — no manual rollback needed.

### Hot reload

Every config change calls the `HAPROXY_RELOAD_CMD` (default: `sudo /bin/systemctl reload haproxy`). HAProxy reloads gracefully — active connections are not dropped. After reload, the API verifies the port is listening (or gone) before returning success.

---

## How Cluster Jobs Work

All cluster operations (deploy, add member, remove member) run asynchronously as **jobs**.

### Job lifecycle

```
POST /cluster/mysql/deploy
        │
        ▼
  Validate request
        │
        ▼
  Create job file (status=pending)
        │
        ▼
  Return 202 with job ID  ◀─── client polls GET /jobs/{id}
        │
        ▼  (background goroutine)
  Build Ansible inventory
        │
        ▼
  Run ansible-playbook
        │
        ├── step 1 complete → update job file
        ├── step 2 complete → update job file
        │   ...
        ├── step N complete → status=completed
        │
        └── step X fails   → status=failed, error=<message>
```

### Job state storage

Each job is stored as a JSON file on disk:

```
/var/lib/erawan-cluster/cluster/jobs/
  mysql/
    <jobID>.json         ← job state + step results
    <jobID>.secret       ← cluster credentials (root/admin passwords)
  pgsql/
    <jobID>.json
    <jobID>.secret
```

The `.secret` file is encrypted at rest and holds passwords returned in the deploy response. These are used later by `add-member`, `dbmanager`, and `metrics` endpoints to look up credentials by `job_id`.

### Resume and rollback

If a job fails mid-way:
- `POST /jobs/{id}/resume` — re-runs Ansible starting from `last_completed_step + 1`. All previously completed steps are skipped. Secrets (passwords) must be re-supplied in the request body since they are not stored in plaintext.
- `POST /jobs/{id}/rollback` — runs the rollback playbook to undo the cluster configuration.

### What Ansible does

The API builds a temporary Ansible inventory from the job's `primary_ip` and `standby_ips`, then calls `ansible-playbook` with:
- `--inventory <tmpfile>`
- `-e` for all cluster variables (passwords, ports, cluster name)
- `--private-key <CLUSTER_SSH_PRIVATE_KEY_PATH>`
- `-u <CLUSTER_SSH_USER>`

Ansible SSHes into each DB node and applies the roles. The API parses Ansible's output line-by-line to track step progress.

---

## How Metric Collection Works

The metric endpoint scrapes **Prometheus exporters** running on each DB node — it does not make direct SQL connections. No database credentials are required.

```
POST /cluster/mysql/metrics  { "job_id": "abc", "proxy_port": 25041 }
           │
           ▼
  Resolve node_ips from stored job (or accept directly)
           │
           ▼
  Scrape mysqld_exporter on each node :9104 (parallel)
  Scrape node_exporter on each node :9100 (parallel)
           │
           ▼
  Discover current primary from GR member info in exporter data
           │
           ▼
  Aggregate per-category metrics, return JSON

POST /cluster/pgsql/metrics  { "job_id": "abc", "proxy_port": 25042 }
           │
           ▼
  Resolve node_ips from stored job (or accept directly)
           │
           ▼
  Scrape postgres_exporter on each node :9187 (parallel)
  Scrape node_exporter on each node :9100 (parallel)
  Call Patroni REST API on each node :8008 (for cluster/failover categories)
           │
           ▼
  Discover current primary from Patroni leader API
           │
           ▼
  Aggregate per-category metrics, return JSON
```

`proxy_port` is recorded in the response `port` field for reference. The exporters are contacted directly on their node IPs — HAProxy is not in the metric collection path.

---

## Security Model

| Concern | How it is handled |
|---------|------------------|
| API authentication | `X-API-Key` header required on every request |
| Request body encryption | Optional AES-256-GCM via `ENCRYPTION_KEY` env |
| SSH to DB nodes | Dedicated `clusterops` user with key-only auth and passwordless sudo |
| SSH host key verification | Trust-on-first-use: a node's key is pinned to `known_hosts` the first time it's contacted, then `StrictHostKeyChecking=yes` on every later connection. A real key change (MITM, or a rebuilt/reimaged node) fails loudly instead of being silently trusted. Pass `reset_host_keys: true` on deploy/resume/add-member to explicitly re-trust a node's current key (e.g. after rebuilding it) |
| Cluster passwords | Stored in `.secret` files with `0600` permissions; never logged |
| SQL connections | Always via HAProxy, never direct DB VM IPs |
| Input validation | IP, port, username validated; unknown JSON fields rejected |
| Body size limit | 2 MiB cap to prevent oversized payloads |

---

## PROXY_HOST Environment Variable

The `PROXY_HOST` env var controls which IP the API uses to reach HAProxy for SQL metric connections. Default: `127.0.0.1`.

If HAProxy listens on a different interface, set:
```env
PROXY_HOST=10.0.0.100
```

This value is injected server-side — clients never specify the SQL host. Clients only specify `proxy_port` (the HAProxy frontend port).

---

## Workflow: Deploying a Cluster End-to-End

```
1. Client                     2. Proxy Node                  3. DB Nodes
   │                             │                              │
   │── POST /haproxy/config/mysql ──────▶                       │
   │   { port:25041, node_ips, db_port }                        │
   │                             │── write 25041.cfg             │
   │                             │── reload HAProxy              │
   │◀── 200 OK ─────────────────│                              │
   │                             │                              │
   │── POST /cluster/mysql/deploy ──────▶                       │
   │   { cluster_name, primary_ip, ... }                        │
   │                             │── create job (pending)        │
   │◀── 202 { job_id } ─────────│                              │
   │                             │── ansible-playbook ──────────▶│
   │                             │                              ││ configure instances
   │── GET /cluster/mysql/jobs/{id} ─────▶                     ││ create cluster
   │◀── 200 { status:running, progress_percent:40 } ──────────││ add secondaries
   │                             │                              ││ install primary-check
   │── GET /cluster/mysql/jobs/{id} ─────▶                     ││
   │◀── 200 { status:completed, secret:{...} } ◀──────────────│
   │                             │                              │
   │── POST /cluster/mysql/metrics ──────▶                      │
   │   { job_id, proxy_port:25041 }                             │
   │                             │── scrape :9104 mysqld_exporter ──▶│
   │                             │── scrape :9100 node_exporter  ──▶│
   │                             │◀── exporter data ◀──────────────│
   │◀── 200 { categories:{...} } ◀──────────────│              │
```
