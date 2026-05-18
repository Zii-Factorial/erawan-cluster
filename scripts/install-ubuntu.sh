#!/usr/bin/env bash
set -euo pipefail

# Production installer for Ubuntu 24.04+
# Usage (run from the repo root):
#   sudo bash scripts/install-ubuntu.sh
#
# Optional environment overrides:
#   APP_ROOT=/snap/erawan-cluster   — where the repo lives (default)
#   BIN_SRC=./bin/erawan-cluster    — pre-built binary path
#   CLUSTER_SRC=./cluster           — cluster playbooks source

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "Run as root (sudo)." >&2
  exit 1
fi

APP_USER="${APP_USER:-erawan}"
APP_GROUP="${APP_GROUP:-erawan}"
APP_NAME="${APP_NAME:-erawan-cluster}"
APP_ROOT="${APP_ROOT:-/snap/erawan-cluster}"
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
CLUSTER_INSTALL_DIR="${APP_ROOT}/cluster"
TMP_CLUSTER_STAGE=""

cleanup() {
  [[ -n "${TMP_CLUSTER_STAGE:-}" ]] && rm -rf "${TMP_CLUSTER_STAGE}"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Resolve binary source — prefer explicit override, then local build output
# ---------------------------------------------------------------------------
if [[ -z "${BIN_SRC:-}" ]]; then
  if [[ -f "./bin/${APP_NAME}" ]]; then
    BIN_SRC="./bin/${APP_NAME}"
  elif [[ -f "${APP_ROOT}/bin/${APP_NAME}" ]]; then
    BIN_SRC="${APP_ROOT}/bin/${APP_NAME}"
  else
    BIN_SRC="./bin/${APP_NAME}"
  fi
fi

# ---------------------------------------------------------------------------
# Resolve cluster source
# ---------------------------------------------------------------------------
if [[ -z "${CLUSTER_SRC:-}" ]]; then
  if [[ -d "./cluster" ]]; then
    CLUSTER_SRC="./cluster"
  elif [[ -d "${APP_ROOT}/cluster" ]]; then
    CLUSTER_SRC="${APP_ROOT}/cluster"
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
  local root="$1" missing=0 rel
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
[[ -f "${BIN_SRC}" ]] || {
  echo "Binary not found: ${BIN_SRC}" >&2
  echo "Build it first:  go build -o bin/${APP_NAME} ." >&2
  exit 1
}
[[ -d "${CLUSTER_SRC}" ]] || {
  echo "Cluster dir not found: ${CLUSTER_SRC}" >&2
  exit 1
}
validate_cluster_tree "${CLUSTER_SRC}" || {
  echo "Cluster source tree is incomplete; aborting install." >&2
  exit 1
}

echo "==> Creating user and directories"
id -u "${APP_USER}" >/dev/null 2>&1 \
  || useradd -r -m -d "${APP_STATE_DIR}" -s /usr/sbin/nologin "${APP_USER}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0750 "${APP_STATE_DIR}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0750 "${JOBS_DIR}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0700 "${KEYS_DIR}"
install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0755 "${TENANTS_DIR}"
install -d -o root -g root -m 0755 "${APP_ROOT}"
install -d -o root -g "${APP_GROUP}" -m 0750 "${APP_ENV_DIR}"

echo "==> Installing binary"
install -m 0755 "${BIN_SRC}" "${APP_BIN}"

# ---------------------------------------------------------------------------
# Install cluster playbooks.
# If CLUSTER_SRC already resolves to CLUSTER_INSTALL_DIR (i.e. the git repo
# IS the install location), skip the copy — git pull already updated the files.
# ---------------------------------------------------------------------------
_src_real="$(realpath "${CLUSTER_SRC}")"
_dst_real="$(realpath "${CLUSTER_INSTALL_DIR}" 2>/dev/null || echo "${CLUSTER_INSTALL_DIR}")"

if [[ "${_src_real}" == "${_dst_real}" ]]; then
  echo "==> Cluster already at ${CLUSTER_INSTALL_DIR} (git pull keeps it up to date)"
  validate_cluster_tree "${CLUSTER_INSTALL_DIR}" || {
    echo "Cluster tree at ${CLUSTER_INSTALL_DIR} is incomplete; aborting." >&2
    exit 1
  }
else
  echo "==> Installing cluster playbooks"
  TMP_CLUSTER_STAGE="$(mktemp -d /tmp/.erawan-cluster-stage.XXXXXX)"
  cp -a "${CLUSTER_SRC}" "${TMP_CLUSTER_STAGE}/cluster"
  validate_cluster_tree "${TMP_CLUSTER_STAGE}/cluster" || {
    echo "Staged cluster tree is incomplete; aborting install." >&2
    exit 1
  }
  rm -rf "${CLUSTER_INSTALL_DIR}"
  mv "${TMP_CLUSTER_STAGE}/cluster" "${CLUSTER_INSTALL_DIR}"
fi

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
MYSQL_DEPLOY_PLAYBOOK=${CLUSTER_INSTALL_DIR}/mysql/playbooks/deploy.yml
MYSQL_ROLLBACK_PLAYBOOK=${CLUSTER_INSTALL_DIR}/mysql/playbooks/rollback.yml
PGSQL_DEPLOY_PLAYBOOK=${CLUSTER_INSTALL_DIR}/pgsql/playbooks/deploy.yml
CLUSTER_SSH_USER=
CLUSTER_SSH_PRIVATE_KEY_PATH=

CLUSTER_ANSIBLE_DEBUG=false
CLUSTER_ANSIBLE_VERBOSITY=0
CLUSTER_STEP_OUTPUT_MAX_CHARS=8000
EOF
fi
chown root:"${APP_GROUP}" "${APP_ENV_FILE}"
chmod 0640 "${APP_ENV_FILE}"

# Always sync path-sensitive keys so re-installs and APP_ROOT changes take effect.
upsert_env() {
  local key="$1" val="$2" file="$3"
  if grep -q "^${key}=" "${file}" 2>/dev/null; then
    sed -i "s|^${key}=.*|${key}=${val}|" "${file}"
  else
    echo "${key}=${val}" >>"${file}"
  fi
}

upsert_env "MYSQL_DEPLOY_PLAYBOOK"   "${CLUSTER_INSTALL_DIR}/mysql/playbooks/deploy.yml"   "${APP_ENV_FILE}"
upsert_env "MYSQL_ROLLBACK_PLAYBOOK" "${CLUSTER_INSTALL_DIR}/mysql/playbooks/rollback.yml" "${APP_ENV_FILE}"
upsert_env "PGSQL_DEPLOY_PLAYBOOK"   "${CLUSTER_INSTALL_DIR}/pgsql/playbooks/deploy.yml"   "${APP_ENV_FILE}"
upsert_env "CLUSTER_STATE_DIR"       "${JOBS_DIR}"                                          "${APP_ENV_FILE}"
upsert_env "TENANTS_DIR"             "${TENANTS_DIR}"                                       "${APP_ENV_FILE}"

echo "==> Configuring HAProxy global socket"
if grep -qE '^\s*stats socket /run/haproxy/admin\.sock' /etc/haproxy/haproxy.cfg; then
  sed -i -E 's|^\s*stats socket /run/haproxy/admin\.sock.*|    stats socket /run/haproxy/admin.sock mode 660 level admin expose-fd listeners|' /etc/haproxy/haproxy.cfg
fi

echo "==> Configuring HAProxy tenant loading"
if [[ -f /etc/default/haproxy ]]; then
  if grep -q '^CONFIG=' /etc/default/haproxy; then
    sed -i "s|^CONFIG=.*|CONFIG=\"/etc/haproxy/haproxy.cfg -f ${TENANTS_DIR}\"|" /etc/default/haproxy
  else
    echo "CONFIG=\"/etc/haproxy/haproxy.cfg -f ${TENANTS_DIR}\"" >>/etc/default/haproxy
  fi
fi

echo "==> Configuring HAProxy systemd override"
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
  systemctl reload haproxy
else
  systemctl start haproxy
fi
systemctl restart "${APP_NAME}"

echo ""
echo "==> Done"
echo "  Binary:   ${APP_BIN}"
echo "  Cluster:  ${CLUSTER_INSTALL_DIR}"
echo "  Env file: ${APP_ENV_FILE}"
echo ""
echo "Edit ${APP_ENV_FILE} and set:"
echo "  API_KEY                      — strong random key"
echo "  CLUSTER_SSH_USER             — SSH user for cluster nodes"
echo "  CLUSTER_SSH_PRIVATE_KEY_PATH — path to the SSH private key"
echo ""
echo "Check status:"
echo "  systemctl status ${APP_NAME} --no-pager"
echo "  systemctl status haproxy --no-pager"
