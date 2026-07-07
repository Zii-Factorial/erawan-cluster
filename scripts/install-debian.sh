#!/usr/bin/env bash
set -euo pipefail

# Production installer for Debian 12+
# Usage:
#   sudo bash scripts/install-debian.sh
# Optional environment overrides:
#   BIN_SRC=./bin/erawan-cluster
#   CLUSTER_SRC=./cluster

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "Run as root (sudo)." >&2
  exit 1
fi

BIN_SRC="${BIN_SRC:-./bin/erawan-cluster}"
CLUSTER_SRC="${CLUSTER_SRC:-./cluster}"

APP_USER="erawan"
APP_GROUP="erawan"
APP_NAME="erawan-cluster"
APP_ROOT="/opt/erawan-cluster"
APP_BIN="/usr/local/bin/erawan-cluster"
APP_ENV_DIR="/etc/erawan-cluster"
APP_ENV_FILE="${APP_ENV_DIR}/.env"
APP_STATE_DIR="/var/lib/erawan-cluster"
JOBS_DIR="${APP_STATE_DIR}/cluster/jobs"
KEYS_DIR="${APP_STATE_DIR}/keys"
TENANTS_DIR="${APP_STATE_DIR}/haproxy/tenants"
SUDOERS_FILE="/etc/sudoers.d/${APP_USER}-haproxy-reload"
UNIT_FILE="/etc/systemd/system/${APP_NAME}.service"
HAPROXY_OVERRIDE_DIR="/etc/systemd/system/haproxy.service.d"
HAPROXY_OVERRIDE_FILE="${HAPROXY_OVERRIDE_DIR}/override.conf"

echo "==> Installing packages"
apt update
apt install -y haproxy ansible ca-certificates openssh-client

echo "==> Validating sources"
[[ -f "${BIN_SRC}" ]] || { echo "Missing binary: ${BIN_SRC}" >&2; exit 1; }
[[ -d "${CLUSTER_SRC}" ]] || { echo "Missing cluster dir: ${CLUSTER_SRC}" >&2; exit 1; }

echo "==> Creating user and directories"
id -u "${APP_USER}" >/dev/null 2>&1 || useradd -r -m -d "${APP_STATE_DIR}" -s /usr/sbin/nologin "${APP_USER}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0750 "${APP_STATE_DIR}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0750 "${JOBS_DIR}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0700 "${KEYS_DIR}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0755 "${TENANTS_DIR}"
install -d -o root -g root -m 0755 "${APP_ROOT}"
install -d -o root -g "${APP_GROUP}" -m 0750 "${APP_ENV_DIR}"

echo "==> Installing binary and playbooks"
install -m 0755 "${BIN_SRC}" "${APP_BIN}"
rm -rf "${APP_ROOT}/cluster"
cp -a "${CLUSTER_SRC}" "${APP_ROOT}/cluster"

echo "==> Writing env file template (only if missing)"
if [[ ! -f "${APP_ENV_FILE}" ]]; then
  cat >"${APP_ENV_FILE}" <<EOF
API_HOST=127.0.0.1
API_PORT=8080
ENV=prod
API_KEY=CHANGE_TO_STRONG_RANDOM_KEY
# ENCRYPTION_KEY: 64-char hex (AES-256-GCM payload encryption). Generate: openssl rand -hex 32
ENCRYPTION_KEY=
PROXY_HOST=127.0.0.1

TENANTS_DIR=${TENANTS_DIR}
HAPROXY_RELOAD_CMD=sudo /bin/systemctl reload haproxy
HAPROXY_RELOAD_TIMEOUT_SECONDS=15
# Comma-separated list of base HAProxy config files that tenant operations must
# never touch. Add the operator-managed haproxy.cfg here.
# HAPROXY_MAIN_CONFIGS=/etc/haproxy/haproxy.cfg

# PostgreSQL-backed job store + HAProxy config persistence (Active/Passive HA).
# When set, jobs and HAProxy tenant configs are persisted in the database so a
# standby node can take over the VIP and serve requests without operator action.
# DB_CONNECTION=postgres://erawan:secret@127.0.0.1:5432/erawan?sslmode=disable

# DB connection-pool sizing — raise proportionally when scaling vertically.
# Rule of thumb: DB_MAX_OPEN_CONNS = (num_cpu * 2) + headroom
DB_MAX_OPEN_CONNS=25
DB_MAX_IDLE_CONNS=10
DB_CONN_MAX_LIFETIME_SECONDS=300
DB_CONN_MAX_IDLE_TIME_SECONDS=60

CLUSTER_STATE_DIR=${JOBS_DIR}
CLUSTER_MAX_CONCURRENT_JOBS=4

# Seconds to wait for in-flight Ansible jobs to write their final status before
# the process exits on SIGTERM. Raise if deploy steps exceed 5 minutes.
SHUTDOWN_DRAIN_SECONDS=300

ANSIBLE_PLAYBOOK_BIN=/usr/bin/ansible-playbook
MYSQL_DEPLOY_PLAYBOOK=${APP_ROOT}/cluster/mysql/playbooks/deploy.yml
MYSQL_ROLLBACK_PLAYBOOK=${APP_ROOT}/cluster/mysql/playbooks/rollback.yml
PGSQL_DEPLOY_PLAYBOOK=${APP_ROOT}/cluster/pgsql/playbooks/deploy.yml

CLUSTER_SSH_USER=
CLUSTER_SSH_PRIVATE_KEY_PATH=
# New node host keys are auto-pinned to CLUSTER_SSH_KNOWN_HOSTS via ssh-keyscan
# before each connection (trust-on-first-use), so this should stay false even
# for greenfield bootstrap. Only set true as a manual escape hatch if
# ssh-keyscan can't reach nodes from this host (e.g. firewalled) or
# CLUSTER_SSH_KNOWN_HOSTS is unset.
CLUSTER_SSH_INSECURE_HOST_KEY=false
CLUSTER_SSH_KNOWN_HOSTS=${KEYS_DIR}/known_hosts

CLUSTER_ANSIBLE_DEBUG=false
CLUSTER_ANSIBLE_VERBOSITY=0
CLUSTER_STEP_OUTPUT_MAX_CHARS=8000

ENABLE_PPROF=false
EOF
fi
chown root:"${APP_GROUP}" "${APP_ENV_FILE}"
chmod 0640 "${APP_ENV_FILE}"

echo "==> Configuring HAProxy global socket"
if grep -qE '^\s*stats socket /run/haproxy/admin\.sock' /etc/haproxy/haproxy.cfg; then
  sed -i -E 's|^\s*stats socket /run/haproxy/admin\.sock.*|    stats socket /run/haproxy/admin.sock mode 660 level admin expose-fd listeners|' /etc/haproxy/haproxy.cfg
fi

echo "==> Configuring HAProxy tenant loading"
if [[ -f /etc/default/haproxy ]]; then
  if grep -q '^CONFIG=' /etc/default/haproxy; then
    sed -i "s|^CONFIG=.*|CONFIG=\"/etc/haproxy/haproxy.cfg -f ${TENANTS_DIR}\"|" /etc/default/haproxy
  else
    echo "CONFIG=\"/etc/haproxy/haproxy.cfg -f ${TENANTS_DIR}\"" >> /etc/default/haproxy
  fi
fi

echo "==> Configuring HAProxy systemd override for hot reload with tenant directory"
install -d -o root -g root -m 0755 "${HAPROXY_OVERRIDE_DIR}"
cat >"${HAPROXY_OVERRIDE_FILE}" <<EOF
[Service]
ExecStart=
ExecStart=/usr/sbin/haproxy -Ws -f /etc/haproxy/haproxy.cfg -f ${TENANTS_DIR} -p /run/haproxy.pid -S /run/haproxy-master.sock
ExecReload=
ExecReload=/usr/sbin/haproxy -c -q -f /etc/haproxy/haproxy.cfg -f ${TENANTS_DIR}
ExecReload=/bin/kill -USR2 \$MAINPID
EOF

echo "==> Writing sudoers rule"
cat >"${SUDOERS_FILE}" <<EOF
${APP_USER} ALL=(root) NOPASSWD: /bin/systemctl reload haproxy
EOF
chmod 0440 "${SUDOERS_FILE}"

echo "==> Writing systemd unit"
cat >"${UNIT_FILE}" <<EOF
[Unit]
Description=Erawan Cluster API
After=network.target

[Service]
Type=simple
User=${APP_USER}
Group=${APP_GROUP}
WorkingDirectory=${APP_ROOT}
EnvironmentFile=${APP_ENV_FILE}
ExecStart=${APP_BIN}
Restart=always
RestartSec=5
LimitNOFILE=65535
PrivateTmp=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths=${APP_STATE_DIR}

[Install]
WantedBy=multi-user.target
EOF

echo "==> Validating HAProxy config"
haproxy -c -f /etc/haproxy/haproxy.cfg -f "${TENANTS_DIR}"

echo "==> Starting services"
systemctl daemon-reload
systemctl enable haproxy "${APP_NAME}"
if systemctl is-active --quiet haproxy; then
  echo "==> Reloading HAProxy (no restart)"
  systemctl reload haproxy
else
  echo "==> Starting HAProxy (first install)"
  systemctl start haproxy
fi
systemctl restart "${APP_NAME}"

echo "==> Done"
echo "Edit ${APP_ENV_FILE} and set API_KEY, ENCRYPTION_KEY, CLUSTER_SSH_USER, and CLUSTER_SSH_PRIVATE_KEY_PATH before running cluster jobs."
echo "Check status:"
echo "  systemctl status ${APP_NAME} --no-pager"
echo "  systemctl status haproxy --no-pager"
