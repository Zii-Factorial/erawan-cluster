<p >
  <img src="doc/assets/A5172f582418f41729f3c587f6a5f95e6w.png" alt="erawan-cluster  logo" width="180"/>
</p>

# erawan-cluster

**Version 1.01** — REST API for automated database cluster lifecycle management and HAProxy configuration.

---

## Tech Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.22+ |
| HTTP Router | [go-chi/chi](https://github.com/go-chi/chi) |
| Build | Makefile |
| Automation | Ansible |
| Proxy | HAProxy (optional) |
| MySQL Cluster | MySQL InnoDB Cluster + MySQL Shell + MySQL Router |
| PostgreSQL Cluster | PostgreSQL + Patroni + etcd |

---

## Features

### MySQL Cluster
- Automated MySQL InnoDB Cluster deployment via Ansible
- Supports single-node bootstrap or primary-plus-secondary topologies
- Auto-failover using MySQL InnoDB Cluster native HA
- Explicit Group Replication auto-rejoin and restart rejoin settings for multi-node clusters
- MySQL Router bootstrap and service configuration on DB nodes
- MySQL Shell (`mysqlsh`) for cluster operations (`dba.configure_instance`, `dba.createCluster`, `dba.addInstance`)
- Optional prepared-node mode via `assume_prepared`
- Application database and user provisioning
- Job-based async deployment with resume and rollback support
- Optional router bootstrap via `bootstrap_router`

### PostgreSQL Cluster
- Automated Patroni-based PostgreSQL cluster deployment
- Embedded `etcd` distributed consensus across database nodes
- Supports single-node bootstrap or primary-plus-standby topologies
- Automatic leader election and replica bootstrap
- `pg_rewind`-based recovery support for diverged replicas
- Job-based rollout with verification via Patroni REST API

### HAProxy (Optional)
- Tenant-based HAProxy config generation and hot reload
- Multi-tenant frontend/backend config per port
- No HAProxy restart required — live reload only

---

## Requirements

### API Host
- Go 1.22+
- `ansible-playbook` installed
- SSH client available on the API host
- SSH access to all target DB nodes
- HAProxy installed (if using proxy features)
- `sudo` permission for HAProxy reload command

### MySQL Target Nodes
- MySQL installed and running
- `mysqlsh` (MySQL Shell) installed
- Local MySQL administration as OS `root` available through a Unix socket
- Supported topology is either 1 primary only or 1 primary plus 1 or more secondary nodes
- Nodes reachable from API host via SSH
- Nodes can reach each other on MySQL port (default 3306)

### PostgreSQL Target Nodes
- PostgreSQL installed on all target nodes
- `patroni[etcd]` installed
- `etcd` installed
- Supported topology is either 1 primary only or 1 primary plus 1 or more standby nodes
- Nodes reachable on ports 2379, 2380, 5432, and 8008

---

## Quick Start

This is the recommended setup for a fresh Ubuntu 24.04 proxy node that will run:

- the `erawan-cluster` API
- Ansible playbooks
- optional HAProxy tenant configs

### 1. Prepare the proxy node
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

This installs:

- `haproxy`
- `ansible`
- `openssh-client`
- the API binary at `/usr/local/bin/erawan-cluster`
- playbooks at `/opt/erawan-cluster/cluster`
- the systemd service `erawan-cluster`

### 4. Generate the SSH key on the proxy node

Generate a dedicated RSA 4096 key for the cluster automation user:

```bash
sudo install -d -o erawan -g erawan -m 0700 /var/lib/erawan-cluster/keys
sudo -u erawan ssh-keygen -t rsa -b 4096 -N '' -C 'clusterops@proxy-node' -f /var/lib/erawan-cluster/keys/clusterops_rsa
```

Show the public key:

```bash
sudo cat /var/lib/erawan-cluster/keys/clusterops_rsa.pub
```

### 5. Trust the public key on every DB node

On each DB node, make sure the SSH user exists:

```bash
sudo useradd -m -s /bin/bash clusterops || true
sudo install -d -o clusterops -g clusterops -m 700 /home/clusterops/.ssh
```

Append the proxy node public key:

```bash
echo 'PASTE_PROXY_PUBLIC_KEY_HERE' | sudo tee -a /home/clusterops/.ssh/authorized_keys
sudo chown clusterops:clusterops /home/clusterops/.ssh/authorized_keys
sudo chmod 600 /home/clusterops/.ssh/authorized_keys
```

Allow passwordless sudo for automation:

```bash
echo 'clusterops ALL=(ALL) NOPASSWD:ALL' | sudo tee /etc/sudoers.d/clusterops
sudo chmod 440 /etc/sudoers.d/clusterops
```

### 6. Configure the API service

Edit the env file:

```bash
sudo nano /etc/erawan-cluster/.env
```

Set at minimum:

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

### 7. Restart and verify the proxy node
```bash
sudo systemctl daemon-reload
sudo systemctl reload haproxy || sudo systemctl start haproxy
sudo systemctl restart erawan-cluster
sudo systemctl status erawan-cluster --no-pager
sudo systemctl status haproxy --no-pager
curl http://127.0.0.1:8080/health
```

### 8. Test SSH from the proxy node to a DB node
```bash
sudo -u erawan ssh -i /var/lib/erawan-cluster/keys/clusterops_rsa clusterops@<db-node-ip> 'whoami'
sudo -u erawan ssh -i /var/lib/erawan-cluster/keys/clusterops_rsa clusterops@<db-node-ip> 'sudo -n whoami'
```

Expected results:

- first command returns `clusterops`
- second command returns `root`

After that, the proxy node is ready to call the MySQL and PostgreSQL cluster APIs.

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `API_ADDR` | `:8080` | Listen address |
| `TENANTS_DIR` | `/var/lib/erawan-cluster/haproxy/tenants` | HAProxy tenant config directory |
| `HAPROXY_RELOAD_CMD` | `sudo /bin/systemctl reload haproxy` | HAProxy reload command |
| `CLUSTER_STATE_DIR` | `/var/lib/erawan-cluster/cluster/jobs` | Job state directory |
| `PGSQL_CLUSTER_STATE_DIR` | `<CLUSTER_STATE_DIR>/pgsql` | PostgreSQL job state directory |
| `ANSIBLE_PLAYBOOK_BIN` | `ansible-playbook` | Ansible binary path |
| `MYSQL_DEPLOY_PLAYBOOK` | `<project>/cluster/mysql/playbooks/deploy.yml` | MySQL deploy playbook path |
| `MYSQL_ROLLBACK_PLAYBOOK` | `<project>/cluster/mysql/playbooks/rollback.yml` | MySQL rollback playbook path |
| `PGSQL_DEPLOY_PLAYBOOK` | `<project>/cluster/pgsql/playbooks/deploy.yml` | PostgreSQL deploy playbook path |
| `CLUSTER_SSH_USER` | empty | Required SSH login user for MySQL and PostgreSQL jobs |
| `CLUSTER_SSH_PRIVATE_KEY_PATH` | empty | Required SSH private key path on the API host |
| `MYSQL_ANSIBLE_DEBUG` | `false` | Stream live Ansible logs to journal |
| `MYSQL_ANSIBLE_VERBOSITY` | `0` | Ansible verbosity level (1–4) |
| `PGSQL_ANSIBLE_DEBUG` | `false` | Stream live PostgreSQL Ansible logs to journal |
| `PGSQL_ANSIBLE_VERBOSITY` | `0` | PostgreSQL Ansible verbosity level (1–4) |

---

## Recommended SSH Setup

- Prefer a dedicated SSH user such as `clusterops` instead of logging in as `root`.
- Grant that user passwordless `sudo` on the DB nodes so Ansible can `become: true`.
- Generate or place the matching private key on the API host and set `CLUSTER_SSH_USER` and `CLUSTER_SSH_PRIVATE_KEY_PATH` before starting the service.
- If you generate the key on the proxy node, copy the `.pub` key to every DB node or bake it into your cloud template.
- This version is SSH-key only for cluster operations; request payloads no longer accept SSH credentials.

## Make Commands

```bash
make tidy    # go mod tidy
make fmt     # format source
make test    # run tests
make build   # build binary to ./bin
make run     # run API directly
```

---

## Security

- Request body capped at 1 MiB
- Unknown JSON fields rejected
- IP, port, and username input validation
- Job files stored with restrictive permissions (`0700` dir, `0600` files)
- User input never shell-interpolated
