# Ubuntu Production Install (22.04/24.04)

## 1) Prepare host
```bash
sudo apt update
sudo apt install -y git curl
```

## 2) Get source and build binary
```bash
git clone <repo-url> erawan-cluster
cd erawan-cluster
make build
```

## 3) Run installer
```bash
sudo bash scripts/install-ubuntu.sh
```

If your binary or cluster folder is in custom path:
```bash
sudo BIN_SRC=/path/to/erawan-cluster CLUSTER_SRC=/path/to/cluster bash scripts/install-ubuntu.sh
```

## 4) Configure API env
```bash
sudo nano /etc/erawan-cluster/.env
```

Set at minimum:
```env
ENV=prod
API_HOST=127.0.0.1
API_PORT=8080
API_KEY=<long-random-key>
PROXY_HOST=127.0.0.1

TENANTS_DIR=/var/lib/erawan-cluster/haproxy/tenants
HAPROXY_RELOAD_CMD=sudo /bin/systemctl reload haproxy
HAPROXY_RELOAD_TIMEOUT_SECONDS=15
# Comma-separated base config files that tenant operations must never touch.
HAPROXY_MAIN_CONFIGS=/etc/haproxy/haproxy.cfg

# PostgreSQL-backed job store (required for Active/Passive HA).
# When set, jobs and HAProxy tenant configs survive VIP failover — the new
# active node reconciles its local HAProxy state from the database on startup.
DB_CONNECTION=postgres://erawan:secret@127.0.0.1:5432/erawan?sslmode=disable

# DB connection-pool sizing — raise proportionally when scaling vertically.
# Rule of thumb: DB_MAX_OPEN_CONNS = (num_cpu × 2) + headroom
DB_MAX_OPEN_CONNS=25
DB_MAX_IDLE_CONNS=10
DB_CONN_MAX_LIFETIME_SECONDS=300
DB_CONN_MAX_IDLE_TIME_SECONDS=60

CLUSTER_STATE_DIR=/var/lib/erawan-cluster/cluster/jobs
CLUSTER_MAX_CONCURRENT_JOBS=4

# Seconds to wait for in-flight Ansible jobs on SIGTERM (raise if steps > 5 min).
SHUTDOWN_DRAIN_SECONDS=300

MYSQL_DEPLOY_PLAYBOOK=/opt/erawan-cluster/cluster/mysql/playbooks/deploy.yml
MYSQL_ROLLBACK_PLAYBOOK=/opt/erawan-cluster/cluster/mysql/playbooks/rollback.yml
PGSQL_DEPLOY_PLAYBOOK=/opt/erawan-cluster/cluster/pgsql/playbooks/deploy.yml
CLUSTER_SSH_USER=clusterops
CLUSTER_SSH_PRIVATE_KEY_PATH=/var/lib/erawan-cluster/keys/clusterops_ed25519
CLUSTER_SSH_INSECURE_HOST_KEY=false
CLUSTER_ANSIBLE_DEBUG=false
CLUSTER_ANSIBLE_VERBOSITY=0
CLUSTER_STEP_OUTPUT_MAX_CHARS=8000
```

Recommended SSH preparation:
```bash
sudo install -d -o erawan -g erawan -m 0700 /var/lib/erawan-cluster/keys
sudo install -o erawan -g erawan -m 0600 ./clusterops_ed25519 /var/lib/erawan-cluster/keys/clusterops_ed25519
```

Ensure the matching public key is already present for `clusterops` on every DB node and that `clusterops` can run `sudo` without a password.

## 5) Run database migrations

If `DB_CONNECTION` is set, apply all pending schema migrations before starting the service:
```bash
cd erawan-cluster
export $(grep -v '^#' /etc/erawan-cluster/.env | xargs)
make migration
```

Create a new migration:
```bash
make migration TABLE=my_table_name
```

Roll back the latest migration:
```bash
make migration ROLLBACK=my_table_name
```

## 6) Reload services
```bash
sudo systemctl daemon-reload
sudo systemctl reload haproxy || sudo systemctl start haproxy
sudo systemctl restart erawan-cluster
```

## 7) Verify
```bash
sudo systemctl status erawan-cluster --no-pager
sudo systemctl status haproxy --no-pager
curl -s http://127.0.0.1:8080/health
sudo ss -lntp | grep -E ':8080|:25000|:9200' || true
```

## 8) Live logs
```bash
sudo journalctl -u erawan-cluster -f
sudo journalctl -u haproxy -f
```

## PostgreSQL deployment note
If you use the PostgreSQL Patroni/etcd cluster API, the target topology can be either 1 primary node only or 1 primary node with 1 or more standby nodes. Use `standby_ips: []` for a small single-node deployment.

## MySQL deployment note
If you use the MySQL InnoDB Cluster API, the target topology can be either 1 primary node only or 1 primary node with 1 or more secondary nodes. This deployment does not use MySQL Router; every node runs a lightweight primary-check HTTP endpoint (`:9200` by default) that HAProxy uses to find the current Group Replication primary.

## Ubuntu HAProxy notes
1. Ensure `/etc/haproxy/haproxy.cfg` has:
   - `stats socket /run/haproxy/admin.sock mode 660 level admin expose-fd listeners`
2. Installer writes systemd override:
   - `/etc/systemd/system/haproxy.service.d/override.conf`
3. Verify running HAProxy includes both config paths:
   - `/etc/haproxy/haproxy.cfg`
   - `/var/lib/erawan-cluster/haproxy/tenants`
