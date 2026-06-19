# Proxy Architecture

This document explains how the erawan-cluster proxy node works — what runs on it, how it controls cluster jobs, and how SQL traffic flows through it.

**Diagrams (draw.io):** [diagrams/proxy-architecture.drawio](diagrams/proxy-architecture.drawio) · [diagrams/deploy-job-workflow.drawio](diagrams/deploy-job-workflow.drawio)

---

## Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        PROXY NODE                               │
│                                                                 │
│   ┌──────────────────┐         ┌──────────────────────────┐    │
│   │  erawan-cluster  │         │         HAProxy           │    │
│   │    Go API        │─ SSH ──▶│   :25041  MySQL frontend  │    │
│   │    :8080         │         │   :25042  pgsql frontend  │    │
│   └────────┬─────────┘         └──────────┬───────────────┘    │
│            │ ansible-playbook             │ TCP proxy           │
└────────────┼──────────────────────────────┼────────────────────┘
             │                              │
     ┌───────┴──────────────────┐    ┌──────┴───────────────┐
     │      DB CLUSTER          │    │      DB CLUSTER       │
     │  10.0.0.1  (primary)     │    │  10.0.0.1 :3306       │
     │  10.0.0.2  (secondary)   │◀───│  10.0.0.2 :3306       │
     │  10.0.0.3  (secondary)   │    │  10.0.0.3 :3306       │
     └──────────────────────────┘    └──────────────────────┘
```

A single **proxy node** runs two services:

- **erawan-cluster API** (`/usr/local/bin/erawan-cluster`) — the Go REST API that receives requests from clients, manages cluster jobs, and runs Ansible playbooks over SSH
- **HAProxy** — the SQL traffic proxy that routes database connections from clients to the correct DB node

Client applications **never connect directly to DB node IPs**. All SQL connections go through HAProxy.

---

## How HAProxy Config Works

For each database cluster, the API writes a HAProxy tenant config file and reloads HAProxy.

### MySQL config layout

```haproxy
frontend mysql_25041
    bind *:25041
    default_backend mysql_25041_be

backend mysql_25041_be
    balance roundrobin
    option tcp-check
    server node1 10.0.0.1:3306 check
    server node2 10.0.0.2:3306 check
    server node3 10.0.0.3:3306 check
```

### PostgreSQL config layout

PostgreSQL uses a Patroni health check to route only to the **current leader**:

```haproxy
frontend pgsql_25042
    bind *:25042
    default_backend pgsql_25042_be

backend pgsql_25042_be
    balance roundrobin
    option httpchk GET /leader
    http-check expect status 200
    server node1 10.0.0.1:5432 check port 8008
    server node2 10.0.0.2:5432 check port 8008
    server node3 10.0.0.3:5432 check port 8008
```

HAProxy polls each node's Patroni REST API (`GET /leader`) on port `8008`. Only the node that returns `200` receives connections. After a failover, Patroni elects a new leader and HAProxy automatically reroutes within seconds — no config change required.

### Config storage

Tenant configs are stored as individual files in `TENANTS_DIR` (default `/var/lib/erawan-cluster/haproxy/tenants/`), one file per port:

```
/var/lib/erawan-cluster/haproxy/tenants/
  25041.cfg    ← MySQL cluster A
  25042.cfg    ← PostgreSQL cluster B
  25043.cfg    ← MySQL cluster C
```

The HAProxy main config includes this directory with `include /var/lib/erawan-cluster/haproxy/tenants/*.cfg`.

### Hot reload

Every config change calls the `HAPROXY_RELOAD_CMD` (default: `sudo /bin/systemctl reload haproxy`). HAProxy reloads gracefully — active connections are not dropped.

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

## How Metric Connections Flow

The metric endpoint connects to the database **through HAProxy**, never directly to a DB VM IP:

```
POST /cluster/mysql/metrics  { "job_id": "abc", "proxy_port": 25041 }
           │
           ▼
  Look up admin user/password from job secret
           │
           ▼
  req.Host = PROXY_HOST env (default 127.0.0.1)
  req.Port = proxy_port (25041)
           │
           ▼
  sql.Open("mysql", "clusteradmin:pass@tcp(127.0.0.1:25041)/")
           │
           ▼
       HAProxy :25041
           │
           ▼
    DB node :3306 (primary or active node)
           │
           ▼
  Collect metrics, return JSON
```

**Key rule**: the `proxy_port` in the request payload is the **HAProxy frontend port**, not the MySQL server port (3306). This ensures:
- All SQL metric connections route through HAProxy
- No direct IP connections are made to DB VMs from the API
- HAProxy's health check ensures the connection reaches the current primary

---

## Security Model

| Concern | How it is handled |
|---------|------------------|
| API authentication | `X-API-Key` header required on every request |
| Request body encryption | Optional AES-256-GCM via `ENCRYPTION_KEY` env |
| SSH to DB nodes | Dedicated `clusterops` user with key-only auth and passwordless sudo |
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
   │                             │                              ││ bootstrap router
   │── GET /cluster/mysql/jobs/{id} ─────▶                     ││
   │◀── 200 { status:completed, secret:{...} } ◀──────────────│
   │                             │                              │
   │── POST /cluster/mysql/metrics ──────▶                      │
   │   { job_id, proxy_port:25041 }                             │
   │                             │── sql connect to 127.0.0.1:25041
   │                             │                 │            │
   │                             │              HAProxy        │
   │                             │                 │──────────▶│ :3306
   │                             │                 │◀──────────│ metrics
   │◀── 200 { categories:{...} } ◀───────────────│            │
```
