#!/usr/bin/env bash
set -euo pipefail

# Production installer for Ubuntu 24.04+
# Usage:
#   sudo bash scripts/install-ubuntu.sh
# Optional environment overrides:
#   BIN_SRC=./bin/erawan-cluster   (auto-detected from snap if not set)
#   CLUSTER_SRC=./cluster          (auto-detected from snap if not set)
#   APP_ROOT=/opt/erawan-cluster

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "Run as root (sudo)." >&2
  exit 1
fi

APP_USER="${APP_USER:-erawan}"
APP_GROUP="${APP_GROUP:-erawan}"
APP_NAME="${APP_NAME:-erawan-cluster}"
APP_ROOT="${APP_ROOT:-/opt/erawan-cluster}"
APP_BIN="${APP_BIN:-/usr/local/bin/erawan-cluster}"
APP_ENV_DIR="${APP_ENV_DIR:-/etc/erawan-cluster}"
APP_ENV_FILE="${APP_ENV_FILE:-${APP_ENV_DIR}/.env}"
APP_STATE_DIR="${APP_STATE_DIR:-/var/lib/erawan-cluster}"
JOBS_DIR="${APP_STATE_DIR}/cluster/jobs"
KEYS_DIR="${APP_STATE_DIR}/keys"
TENANTS_DIR="${APP_STATE_DIR}/haproxy/tenants"
SUDOERS_FILE="/etc/sudoers.d/${APP_USER}-haproxy-reload"
UNIT_FILE="/etc/systemd/system/${APP_NAME}.service"
HAPROXY_OVERRIDE_DIR="/etc/systemd/system/haproxy.service.d"
HAPROXY_OVERRIDE_FILE="${HAPROXY_OVERRIDE_DIR}/override.conf"
APP_ROOT_PARENT="$(dirname "${APP_ROOT}")"
CLUSTER_INSTALL_DIR="${APP_ROOT}/cluster"
TMP_CLUSTER_STAGE=""
TMP_CLUSTER_DIR=""
BACKUP_CLUSTER_DIR=""

cleanup() {
  rm -rf "${TMP_CLUSTER_DIR}" "${TMP_CLUSTER_STAGE}" "${BACKUP_CLUSTER_DIR}"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Auto-detect sources: prefer explicit env override, then local build output,
# then snap installation, then fail with a clear message.
# ---------------------------------------------------------------------------
SNAP_CURRENT="/snap/${APP_NAME}/current"

if [[ -z "${BIN_SRC:-}" ]]; then
  if [[ -f "./bin/${APP_NAME}" ]]; then
    BIN_SRC="./bin/${APP_NAME}"
  elif [[ -f "${SNAP_CURRENT}/bin/${APP_NAME}" ]]; then
    BIN_SRC="${SNAP_CURRENT}/bin/${APP_NAME}"
    echo "==> Snap binary detected: ${BIN_SRC}"
  elif [[ -f "${SNAP_CURRENT}/${APP_NAME}" ]]; then
    BIN_SRC="${SNAP_CURRENT}/${APP_NAME}"
    echo "==> Snap binary detected: ${BIN_SRC}"
  else
    BIN_SRC="./bin/${APP_NAME}"
  fi
fi

if [[ -z "${CLUSTER_SRC:-}" ]]; then
  if [[ -d "./cluster" ]]; then
    CLUSTER_SRC="./cluster"
  elif [[ -d "${SNAP_CURRENT}/cluster" ]]; then
    CLUSTER_SRC="${SNAP_CURRENT}/cluster"
    echo "==> Snap cluster detected: ${CLUSTER_SRC}"
  else
    CLUSTER_SRC="./cluster"
  fi
fi

required_cluster_files=(
  "mysql/playbooks/deploy.yml"
  "mysql/playbooks/rollback.yml"
  "mysql/playbooks/tasks/01_preflight.yml"
  "mysql/playbooks/tasks/02_configure_instances.yml"
  "mysql/playbooks/tasks/03_create_cluster.yml"
  "mysql/playbooks/tasks/04_add_instances.yml"
  "mysql/playbooks/tasks/05_bootstrap_router.yml"
  "mysql/playbooks/tasks/06_verify_cluster.yml"
  "mysql/playbooks/tasks/07_enable_auto_rejoin.yml"
  "mysql/playbooks/tasks/07_init_app_db.yml"
  "pgsql/playbooks/deploy.yml"
)

validate_cluster_tree() {
  local root="$1"
  local missing=0
  local rel
  for rel in "${required_cluster_files[@]}"; do
    if [[ ! -f "${root}/${rel}" ]]; then
      echo "Missing required cluster file: ${root}/${rel}" >&2
      missing=1
    fi
  done
  [[ "${missing}" -eq 0 ]]
}

echo "==> Installing packages"
apt-get update -qq
apt-get install -y haproxy ansible ca-certificates openssh-client

echo "==> Validating sources"
[[ -f "${BIN_SRC}" ]] || { echo "Binary not found: ${BIN_SRC}" >&2; echo "Set BIN_SRC= or install the snap first." >&2; exit 1; }
[[ -d "${CLUSTER_SRC}" ]] || { echo "Cluster dir not found: ${CLUSTER_SRC}" >&2; echo "Set CLUSTER_SRC= or install the snap first." >&2; exit 1; }
validate_cluster_tree "${CLUSTER_SRC}" || {
  echo "Cluster source tree is incomplete; aborting install." >&2
  exit 1
}

echo "==> Creating user and directories"
id -u "${APP_USER}" >/dev/null 2>&1 || useradd -r -m -d "${APP_STATE_DIR}" -s /usr/sbin/nologin "${APP_USER}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0750 "${APP_STATE_DIR}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0750 "${JOBS_DIR}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0700 "${KEYS_DIR}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0755 "${TENANTS_DIR}"
install -d -o root -g root -m 0755 "${APP_ROOT_PARENT}"
install -d -o root -g root -m 0755 "${APP_ROOT}"
install -d -o root -g "${APP_GROUP}" -m 0750 "${APP_ENV_DIR}"

echo "==> Installing binary and playbooks"
install -m 0755 "${BIN_SRC}" "${APP_BIN}"
TMP_CLUSTER_STAGE="$(mktemp -d "${APP_ROOT_PARENT}/.erawan-cluster-cluster.XXXXXX")"
TMP_CLUSTER_DIR="${TMP_CLUSTER_STAGE}/cluster"
BACKUP_CLUSTER_DIR="${APP_ROOT_PARENT}/.erawan-cluster-cluster.backup.$$"
cp -a "${CLUSTER_SRC}" "${TMP_CLUSTER_DIR}"
validate_cluster_tree "${TMP_CLUSTER_DIR}" || {
  echo "Staged cluster tree is incomplete; aborting install." >&2
  exit 1
}
if [[ -d "${CLUSTER_INSTALL_DIR}" ]]; then
  mv "${CLUSTER_INSTALL_DIR}" "${BACKUP_CLUSTER_DIR}"
fi
if ! mv "${TMP_CLUSTER_DIR}" "${CLUSTER_INSTALL_DIR}"; then
  if [[ -d "${BACKUP_CLUSTER_DIR}" ]]; then
    mv "${BACKUP_CLUSTER_DIR}" "${CLUSTER_INSTALL_DIR}" || true
  fi
  echo "Failed to install cluster tree." >&2
  exit 1
fi
rm -rf "${BACKUP_CLUSTER_DIR}"

echo "==> Writing env file"
if [[ ! -f "${APP_ENV_FILE}" ]]; then
  cat >"${APP_ENV_FILE}" <<EOF
API_HOST=127.0.0.1
API_PORT=8080
ENV=prod
API_KEY=CHANGE_TO_STRONG_RANDOM_KEY

TENANTS_DIR=${TENANTS_DIR}
HAPROXY_RELOAD_CMD=sudo /bin/systemctl reload haproxy
HAPROXY_RELOAD_TIMEOUT_SECONDS=15

CLUSTER_STATE_DIR=${JOBS_DIR}

ANSIBLE_PLAYBOOK_BIN=/usr/bin/ansible-playbook
MYSQL_DEPLOY_PLAYBOOK=${APP_ROOT}/cluster/mysql/playbooks/deploy.yml
MYSQL_ROLLBACK_PLAYBOOK=${APP_ROOT}/cluster/mysql/playbooks/rollback.yml
PGSQL_DEPLOY_PLAYBOOK=${APP_ROOT}/cluster/pgsql/playbooks/deploy.yml
CLUSTER_SSH_USER=
CLUSTER_SSH_PRIVATE_KEY_PATH=

CLUSTER_ANSIBLE_DEBUG=false
CLUSTER_ANSIBLE_VERBOSITY=0
CLUSTER_STEP_OUTPUT_MAX_CHARS=8000
EOF
fi
chown root:"${APP_GROUP}" "${APP_ENV_FILE}"
chmod 0640 "${APP_ENV_FILE}"

# Always ensure path-sensitive keys point to the current install location.
# This fixes stale paths on re-install and snap-based installs where the env
# file was written by a previous run with different paths.
upsert_env() {
  local key="$1" val="$2" file="$3"
  if grep -q "^${key}=" "${file}" 2>/dev/null; then
    sed -i "s|^${key}=.*|${key}=${val}|" "${file}"
  else
    echo "${key}=${val}" >> "${file}"
  fi
}

upsert_env "MYSQL_DEPLOY_PLAYBOOK"   "${APP_ROOT}/cluster/mysql/playbooks/deploy.yml"   "${APP_ENV_FILE}"
upsert_env "MYSQL_ROLLBACK_PLAYBOOK" "${APP_ROOT}/cluster/mysql/playbooks/rollback.yml" "${APP_ENV_FILE}"
upsert_env "PGSQL_DEPLOY_PLAYBOOK"   "${APP_ROOT}/cluster/pgsql/playbooks/deploy.yml"   "${APP_ENV_FILE}"
upsert_env "CLUSTER_STATE_DIR"       "${JOBS_DIR}"                                       "${APP_ENV_FILE}"
upsert_env "TENANTS_DIR"             "${TENANTS_DIR}"                                    "${APP_ENV_FILE}"

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

echo ""
echo "==> Done"
echo "Binary:   ${APP_BIN}"
echo "Cluster:  ${CLUSTER_INSTALL_DIR}"
echo "Env file: ${APP_ENV_FILE}"
echo ""
echo "Before running cluster jobs, edit ${APP_ENV_FILE} and set:"
echo "  API_KEY                  — strong random key"
echo "  CLUSTER_SSH_USER         — SSH user for cluster nodes"
echo "  CLUSTER_SSH_PRIVATE_KEY_PATH — path to the SSH private key"
echo ""
echo "Check status:"
echo "  systemctl status ${APP_NAME} --no-pager"
echo "  systemctl status haproxy --no-pager"
