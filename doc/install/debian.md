# Debian Production Install (12+)

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
sudo bash scripts/install-debian.sh
```

Custom artifact paths:
```bash
sudo BIN_SRC=/path/to/erawan-cluster CLUSTER_SRC=/path/to/cluster bash scripts/install-debian.sh
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
TENANTS_DIR=/var/lib/erawan-cluster/haproxy/tenants
HAPROXY_RELOAD_CMD=sudo /bin/systemctl reload haproxy
CLUSTER_STATE_DIR=/var/lib/erawan-cluster/cluster/jobs
# Optional PostgreSQL-backed job store:
# DB_CONNECTION=postgres://user:pass@127.0.0.1:5432/erawan?sslmode=disable
MYSQL_DEPLOY_PLAYBOOK=/opt/erawan-cluster/cluster/mysql/playbooks/deploy.yml
MYSQL_ROLLBACK_PLAYBOOK=/opt/erawan-cluster/cluster/mysql/playbooks/rollback.yml
PGSQL_DEPLOY_PLAYBOOK=/opt/erawan-cluster/cluster/pgsql/playbooks/deploy.yml
CLUSTER_SSH_USER=clusterops
CLUSTER_SSH_PRIVATE_KEY_PATH=/var/lib/erawan-cluster/keys/clusterops_ed25519
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

## 5) Reload services
```bash
sudo systemctl daemon-reload
sudo systemctl reload haproxy || sudo systemctl start haproxy
sudo systemctl restart erawan-cluster
```

## 6) Verify
```bash
sudo systemctl status erawan-cluster --no-pager
sudo systemctl status haproxy --no-pager
curl -s http://127.0.0.1:8080/health
sudo ss -lntp | grep -E ':8080|:25000|:6446' || true
```

## 7) Live logs
```bash
sudo journalctl -u erawan-cluster -f
sudo journalctl -u haproxy -f
```

## PostgreSQL deployment note
If you use the PostgreSQL Patroni/etcd cluster API, the target topology can be either 1 primary node only or 1 primary node with 1 or more standby nodes. Use `standby_ips: []` for a small single-node deployment.

## MySQL deployment note
If you use the MySQL InnoDB Cluster API, the target topology can be either 1 primary node only or 1 primary node with 1 or more secondary nodes. MySQL Router bootstrap is optional.

## Debian HAProxy notes
1. Ensure `/etc/haproxy/haproxy.cfg` has:
   - `stats socket /run/haproxy/admin.sock mode 660 level admin expose-fd listeners`
2. Installer writes systemd override:
   - `/etc/systemd/system/haproxy.service.d/override.conf`
3. Verify running HAProxy includes both config paths:
   - `/etc/haproxy/haproxy.cfg`
   - `/var/lib/erawan-cluster/haproxy/tenants`
