<p >
  <img src="doc/assets/A5172f582418f41729f3c587f6a5f95e6w.png" alt="erawan-cluster logo" width="180"/>
</p>

# erawan-cluster

**Version 1.02** — REST API for automated database cluster lifecycle management, live metrics collection, and HAProxy configuration.

---

## Tech Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.22+ |
| HTTP Router | [go-chi/chi](https://github.com/go-chi/chi) |
| Build | Makefile |
| Automation | Ansible |
| Proxy | HAProxy |
| MySQL Cluster | MySQL InnoDB Cluster + MySQL Shell + MySQL Router |
| PostgreSQL Cluster | PostgreSQL + Patroni + etcd |

---

## Features

- **MySQL cluster lifecycle** — deploy, resume, rollback, add/remove members, user and database management
- **PostgreSQL cluster lifecycle** — deploy, resume, add/remove members, user and database management
- **Live metrics** — 7 MySQL categories and 8 PostgreSQL categories collected via HAProxy
- **HAProxy management** — tenant config generation, member addition, hot reload (no restart)
- **Job-based async execution** — all cluster operations run as tracked background jobs with step-level progress
- **Encryption** — optional AES-256-GCM request/response body encryption

See [doc/api.md](doc/api.md) for the full API reference.  
See [doc/proxy-architecture.md](doc/proxy-architecture.md) for the system design.  
See [doc/mysql.md](doc/mysql.md) and [doc/pgsql.md](doc/pgsql.md) for cluster detail.

---

## Requirements

### API (proxy) host
- Go 1.22+
- `ansible-playbook` installed
- SSH client available
- SSH access to all target DB nodes
- HAProxy installed

### MySQL target nodes
- MySQL installed and running
- `mysqlsh` (MySQL Shell) installed
- Local MySQL administration as OS `root` via Unix socket
- Nodes reachable from the proxy via SSH
- Nodes reachable from each other on MySQL port (default 3306)

### PostgreSQL target nodes
- PostgreSQL installed on all nodes
- `patroni[etcd]` installed
- `etcd` installed
- Ports 2379, 2380, 5432, 8008 reachable between nodes

---

## Quick Start

Recommended setup for a fresh Ubuntu 24.04 proxy node.

### 1. Install system packages
```bash
sudo apt update
sudo apt install -y git curl make golang-go
```

### 2. Clone and build
```bash
git clone https://github.com/Zii-Factorial/erawan-cluster.git
cd erawan-cluster
make tidy
make build
```

### 3. Run the Ubuntu installer
```bash
sudo bash scripts/install-ubuntu.sh
```

Installs: `haproxy`, `ansible`, `openssh-client`, the API binary at `/usr/local/bin/erawan-cluster`, playbooks at `/opt/erawan-cluster/cluster`, and the systemd service `erawan-cluster`.

### 4. Generate the SSH key on the proxy node
```bash
sudo install -d -o erawan -g erawan -m 0700 /var/lib/erawan-cluster/keys
sudo -u erawan ssh-keygen -t rsa -b 4096 -N '' -C 'clusterops@proxy-node' \
  -f /var/lib/erawan-cluster/keys/clusterops_rsa
sudo cat /var/lib/erawan-cluster/keys/clusterops_rsa.pub
```

### 5. Trust the public key on every DB node
```bash
# On each DB node:
sudo useradd -m -s /bin/bash clusterops || true
sudo install -d -o clusterops -g clusterops -m 700 /home/clusterops/.ssh
echo 'PASTE_PROXY_PUBLIC_KEY_HERE' | sudo tee -a /home/clusterops/.ssh/authorized_keys
sudo chown clusterops:clusterops /home/clusterops/.ssh/authorized_keys
sudo chmod 600 /home/clusterops/.ssh/authorized_keys
echo 'clusterops ALL=(ALL) NOPASSWD:ALL' | sudo tee /etc/sudoers.d/clusterops
sudo chmod 440 /etc/sudoers.d/clusterops
```

### 6. Configure the API service
```bash
sudo nano /etc/erawan-cluster/.env
```

Minimum required:
```env
ENV=prod
API_HOST=127.0.0.1
API_PORT=8080
API_KEY=CHANGE_THIS_TO_A_LONG_RANDOM_SECRET
TENANTS_DIR=/var/lib/erawan-cluster/haproxy/tenants
HAPROXY_RELOAD_CMD=sudo /bin/systemctl reload haproxy
CLUSTER_STATE_DIR=/var/lib/erawan-cluster/cluster/jobs
MYSQL_DEPLOY_PLAYBOOK=/opt/erawan-cluster/cluster/mysql/playbooks/deploy.yml
MYSQL_ROLLBACK_PLAYBOOK=/opt/erawan-cluster/cluster/mysql/playbooks/rollback.yml
PGSQL_DEPLOY_PLAYBOOK=/opt/erawan-cluster/cluster/pgsql/playbooks/deploy.yml
CLUSTER_SSH_USER=clusterops
CLUSTER_SSH_PRIVATE_KEY_PATH=/var/lib/erawan-cluster/keys/clusterops_rsa
```

### 7. Start and verify
```bash
sudo systemctl daemon-reload
sudo systemctl reload haproxy || sudo systemctl start haproxy
sudo systemctl restart erawan-cluster
sudo systemctl status erawan-cluster --no-pager
curl http://127.0.0.1:8080/health
```

### 8. Test SSH connectivity
```bash
sudo -u erawan ssh -i /var/lib/erawan-cluster/keys/clusterops_rsa clusterops@<db-ip> 'whoami'
sudo -u erawan ssh -i /var/lib/erawan-cluster/keys/clusterops_rsa clusterops@<db-ip> 'sudo -n whoami'
```

Both should succeed (`clusterops`, then `root`).

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `API_ADDR` | `:8080` | Listen address (overrides API_HOST + API_PORT) |
| `API_HOST` | `0.0.0.0` | Bind host |
| `API_PORT` | `8080` | Bind port |
| `API_KEY` | — | Required API key sent in `X-API-Key` header |
| `ENCRYPTION_KEY` | — | Optional AES-256-GCM key for body encryption |
| `PROXY_HOST` | `127.0.0.1` | HAProxy address used for metric SQL connections |
| `TENANTS_DIR` | `/var/lib/erawan-cluster/haproxy/tenants` | HAProxy tenant config directory |
| `HAPROXY_RELOAD_CMD` | `sudo /bin/systemctl reload haproxy` | HAProxy reload command |
| `HAPROXY_RELOAD_TIMEOUT_SECONDS` | `15` | Timeout for reload command |
| `HAPROXY_MAIN_CONFIGS` | — | Comma-separated extra HAProxy config files to include |
| `CLUSTER_STATE_DIR` | `/var/lib/erawan-cluster/cluster/jobs` | Root job state directory |
| `MYSQL_CLUSTER_STATE_DIR` | `<CLUSTER_STATE_DIR>/mysql` | MySQL job state directory |
| `PGSQL_CLUSTER_STATE_DIR` | `<CLUSTER_STATE_DIR>/pgsql` | PostgreSQL job state directory |
| `ANSIBLE_PLAYBOOK_BIN` | `ansible-playbook` | Ansible binary path |
| `CLUSTER_SSH_USER` | — | Required SSH login user for all cluster jobs |
| `CLUSTER_SSH_PRIVATE_KEY_PATH` | — | Required SSH private key path |
| `MYSQL_DEPLOY_PLAYBOOK` | `<project>/cluster/mysql/playbooks/deploy.yml` | MySQL deploy playbook |
| `MYSQL_ROLLBACK_PLAYBOOK` | `<project>/cluster/mysql/playbooks/rollback.yml` | MySQL rollback playbook |
| `MYSQL_ADD_MEMBER_PLAYBOOK` | `<project>/cluster/mysql/playbooks/add_member.yml` | MySQL add-member playbook |
| `MYSQL_REMOVE_MEMBER_PLAYBOOK` | `<project>/cluster/mysql/playbooks/remove_member.yml` | MySQL remove-member playbook |
| `PGSQL_DEPLOY_PLAYBOOK` | `<project>/cluster/pgsql/playbooks/deploy.yml` | PostgreSQL deploy playbook |
| `PGSQL_ADD_MEMBER_PLAYBOOK` | `<project>/cluster/pgsql/playbooks/add_member.yml` | PostgreSQL add-member playbook |
| `PGSQL_REMOVE_MEMBER_PLAYBOOK` | `<project>/cluster/pgsql/playbooks/remove_member.yml` | PostgreSQL remove-member playbook |
| `MYSQL_ANSIBLE_DEBUG` | `false` | Stream live Ansible logs to journal |
| `MYSQL_ANSIBLE_VERBOSITY` | `0` | Ansible verbosity level (1–4) |
| `PGSQL_ANSIBLE_DEBUG` | `false` | Stream live PostgreSQL Ansible logs to journal |
| `PGSQL_ANSIBLE_VERBOSITY` | `0` | PostgreSQL Ansible verbosity level (1–4) |
| `CLUSTER_ANSIBLE_DEBUG` | `false` | Debug flag for both engines |
| `CLUSTER_ANSIBLE_VERBOSITY` | `0` | Verbosity for both engines |

---

## Make Commands

```bash
make tidy    # go mod tidy
make fmt     # format source
make test    # run tests
make build   # build binary to ./bin
make run     # run API directly
```

---

## Code Structure

```
cmd/api/
  main.go          entry point, service wiring
  api.go           application struct, route registration
  health.go        health check handler
  json.go          response helpers (package main)
  version.go       version constant
  mysql/
    handler.go     MySQL cluster + DB manager HTTP handlers
  pgsql/
    handler.go     PostgreSQL cluster + DB manager HTTP handlers
  haproxy/
    handler.go     HAProxy management HTTP handlers

internal/
  cluster/mysql/   MySQL cluster service + metrics
  cluster/pgsql/   PostgreSQL cluster service + metrics
  haproxy/         HAProxy config service
  render/          Shared JSON response helpers
  security/        API key middleware, AES-GCM cipher
  env/             Environment variable helpers
```

---

## Security

- All requests require `X-API-Key` header
- Request body capped at 2 MiB
- Unknown JSON fields rejected
- IP, port, and username input validated
- Job state files stored with restrictive permissions (`0700` dir, `0600` files)
- User input never shell-interpolated into Ansible commands
- Optional AES-256-GCM body encryption via `ENCRYPTION_KEY`
- SQL metric connections always route through HAProxy (`PROXY_HOST`), never directly to DB VM IPs
