#!/usr/bin/env bash
set -euo pipefail

# Production installer for Ubuntu 24.04+
# Usage:
#   sudo bash scripts/install-ubuntu.sh
#
# Optional environment overrides:
#   APP_ROOT=/snap/erawan-cluster                         — runtime project root
#   BIN_SRC=/snap/erawan-cluster/bin/erawan-cluster      — pre-built binary path
#   CLUSTER_SRC=/snap/erawan-cluster/cluster             — cluster playbooks source

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "Run as root (sudo)." >&2
  exit 1
fi

APP_USER="${APP_USER:-erawan}"
APP_GROUP="${APP_GROUP:-erawan}"
APP_NAME="${APP_NAME:-erawan-cluster}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
PROJECT_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd -P)"
APP_ROOT="${APP_ROOT:-${PROJECT_ROOT}}"
APP_BIN="${APP_BIN:-/usr/local/bin/erawan-cluster}"
APP_ENV_DIR="${APP_ENV_DIR:-/etc/erawan-cluster}"
APP_ENV_FILE="${APP_ENV_FILE:-${APP_ENV_DIR}/.env}"
APP_STATE_DIR="${APP_STATE_DIR:-/var/lib/erawan-cluster}"
JOBS_DIR="${APP_STATE_DIR}/cluster/jobs"
KEYS_DIR="${APP_STATE_DIR}/keys"
TENANTS_DIR="${APP_STATE_DIR}/haproxy/tenants"
HAPROXY_DEFAULTS_FILE="/etc/default/haproxy"
HAPROXY_CONFIG_FILE="/etc/haproxy/haproxy.cfg"
HAPROXY_SOCKET_LINE='    stats socket /run/haproxy/admin.sock mode 660 level admin expose-fd listeners'
SUDOERS_FILE="/etc/sudoers.d/${APP_USER}-haproxy-reload"
UNIT_FILE="/etc/systemd/system/${APP_NAME}.service"
HAPROXY_OVERRIDE_DIR="/etc/systemd/system/haproxy.service.d"
HAPROXY_OVERRIDE_FILE="${HAPROXY_OVERRIDE_DIR}/override.conf"
CLUSTER_INSTALL_DIR="${APP_ROOT}/cluster"
LOG_DIR="${LOG_DIR:-/var/erawan-cluster}"
LOG_FILE="${LOG_DIR}/erawan-cluster.log"
TMP_CLUSTER_STAGE=""

cleanup() {
  [[ -n "${TMP_CLUSTER_STAGE:-}" ]] && rm -rf "${TMP_CLUSTER_STAGE}"
}
trap cleanup EXIT

log_step() {
  echo "==> $*"
}

die() {
  echo "$*" >&2
  exit 1
}

if [[ -z "${BIN_SRC:-}" ]]; then
  if [[ -f "${PROJECT_ROOT}/bin/${APP_NAME}" ]]; then
    BIN_SRC="${PROJECT_ROOT}/bin/${APP_NAME}"
  elif [[ -f "${APP_ROOT}/bin/${APP_NAME}" ]]; then
    BIN_SRC="${APP_ROOT}/bin/${APP_NAME}"
  else
    BIN_SRC="${PROJECT_ROOT}/bin/${APP_NAME}"
  fi
fi

if [[ -z "${CLUSTER_SRC:-}" ]]; then
  if [[ -d "${PROJECT_ROOT}/cluster" ]]; then
    CLUSTER_SRC="${PROJECT_ROOT}/cluster"
  elif [[ -d "${APP_ROOT}/cluster" ]]; then
    CLUSTER_SRC="${APP_ROOT}/cluster"
  else
    CLUSTER_SRC="${PROJECT_ROOT}/cluster"
  fi
fi

required_cluster_files=(
  "requirements.yml"
  "mysql/playbooks/deploy.yml"
  "mysql/playbooks/rollback.yml"
  "mysql/playbooks/group_vars/all.yml"
  "mysql/playbooks/roles/preflight/tasks/main.yml"
  "mysql/playbooks/roles/configure_instances/tasks/main.yml"
  "mysql/playbooks/roles/configure_instances/defaults/main.yml"
  "mysql/playbooks/roles/configure_instances/handlers/main.yml"
  "mysql/playbooks/roles/configure_instances/templates/innodb_cluster.cnf.j2"
  "mysql/playbooks/roles/create_cluster/tasks/main.yml"
  "mysql/playbooks/roles/create_cluster/defaults/main.yml"
  "mysql/playbooks/roles/add_instances/tasks/main.yml"
  "mysql/playbooks/roles/add_instances/defaults/main.yml"
  "mysql/playbooks/roles/auto_rejoin/tasks/main.yml"
  "mysql/playbooks/roles/auto_rejoin/defaults/main.yml"
  "mysql/playbooks/roles/auto_rejoin/templates/gr_watchdog.sh.j2"
  "mysql/playbooks/roles/auto_rejoin/templates/gr_watchdog.service.j2"
  "mysql/playbooks/roles/auto_rejoin/templates/gr_watchdog.timer.j2"
  "mysql/playbooks/roles/verify_cluster/tasks/main.yml"
  "mysql/playbooks/roles/verify_cluster/defaults/main.yml"
  "mysql/playbooks/roles/init_app_db/tasks/main.yml"
  "mysql/playbooks/roles/init_app_db/defaults/main.yml"
  "mysql/playbooks/roles/boot_recovery/tasks/main.yml"
  "mysql/playbooks/roles/boot_recovery/defaults/main.yml"
  "mysql/playbooks/roles/boot_recovery/templates/mysql_boot_recovery.sh.j2"
  "mysql/playbooks/roles/boot_recovery/templates/mysql_boot_recovery.service.j2"
  "mysql/playbooks/roles/bootstrap_router/tasks/main.yml"
  "mysql/playbooks/roles/bootstrap_router/defaults/main.yml"
  "mysql/playbooks/roles/bootstrap_router/templates/mysqlrouter.service.j2"
  "mysql/playbooks/roles/dissolve_cluster/tasks/main.yml"
  "mysql/playbooks/roles/dissolve_cluster/defaults/main.yml"
  "mysql/playbooks/roles/reset_gr_state/tasks/main.yml"
  "mysql/playbooks/roles/reset_gr_state/defaults/main.yml"
  "mysql/playbooks/roles/rollback/tasks/main.yml"
  "mysql/playbooks/roles/rollback/handlers/main.yml"
  "pgsql/playbooks/deploy.yml"
  "pgsql/playbooks/group_vars/all.yml"
  "pgsql/playbooks/roles/preflight/tasks/main.yml"
  "pgsql/playbooks/roles/runtime_facts/tasks/main.yml"
  "pgsql/playbooks/roles/base_config/tasks/main.yml"
  "pgsql/playbooks/roles/base_config/defaults/main.yml"
  "pgsql/playbooks/roles/base_config/handlers/main.yml"
  "pgsql/playbooks/roles/base_config/templates/etcd.conf.j2"
  "pgsql/playbooks/roles/base_config/templates/etcd.service.j2"
  "pgsql/playbooks/roles/base_config/templates/patroni.service.j2"
  "pgsql/playbooks/roles/configure_node/tasks/main.yml"
  "pgsql/playbooks/roles/configure_node/defaults/main.yml"
  "pgsql/playbooks/roles/configure_node/templates/patroni.yml.j2"
  "pgsql/playbooks/roles/cluster_bootstrap/tasks/main.yml"
  "pgsql/playbooks/roles/cluster_bootstrap/defaults/main.yml"
  "pgsql/playbooks/roles/verify_cluster/tasks/main.yml"
  "pgsql/playbooks/roles/verify_cluster/defaults/main.yml"
  "pgsql/playbooks/roles/init_app_db/tasks/main.yml"
  "pgsql/playbooks/roles/init_app_db/defaults/main.yml"
)

missing_cluster_files=()

collect_missing_cluster_files() {
  local root="$1" rel
  missing_cluster_files=()
  for rel in "${required_cluster_files[@]}"; do
    if [[ ! -f "${root}/${rel}" ]]; then
      missing_cluster_files+=("${rel}")
    fi
  done
}

validate_cluster_tree() {
  local root="$1" rel
  collect_missing_cluster_files "${root}"
  if [[ "${#missing_cluster_files[@]}" -eq 0 ]]; then
    return 0
  fi
  for rel in "${missing_cluster_files[@]}"; do
    echo "Missing required cluster file: ${root}/${rel}" >&2
  done
  return 1
}

restore_missing_cluster_files_from_git() {
  local root="$1" rel restored=0
  if ! command -v git >/dev/null 2>&1; then
    return 1
  fi
  if [[ ! -d "${PROJECT_ROOT}/.git" ]]; then
    return 1
  fi
  collect_missing_cluster_files "${root}"
  if [[ "${#missing_cluster_files[@]}" -eq 0 ]]; then
    return 0
  fi
  echo "==> Restoring missing tracked cluster files from git"
  for rel in "${missing_cluster_files[@]}"; do
    if git -C "${PROJECT_ROOT}" ls-files --error-unmatch "cluster/${rel}" >/dev/null 2>&1; then
      git -C "${PROJECT_ROOT}" checkout -- "cluster/${rel}"
      restored=1
    fi
  done
  (( restored == 1 ))
}

ensure_binary_present() {
  if [[ -f "${BIN_SRC}" ]]; then
    return 0
  fi
  if command -v make >/dev/null 2>&1 && [[ -f "${PROJECT_ROOT}/Makefile" ]]; then
    log_step "Building ${APP_NAME}"
    make -C "${PROJECT_ROOT}" build
  fi
  [[ -f "${BIN_SRC}" ]]
}

upsert_env() {
  local key="$1" val="$2" file="$3"
  if grep -q "^${key}=" "${file}" 2>/dev/null; then
    sed -i "s|^${key}=.*|${key}=${val}|" "${file}"
  else
    echo "${key}=${val}" >>"${file}"
  fi
}

install_packages() {
  log_step "Installing packages"
  apt-get update -qq
  apt-get install -y haproxy ansible ca-certificates openssh-client
}

validate_sources() {
  log_step "Validating sources"
  ensure_binary_present || die "Binary not found: ${BIN_SRC}
Unable to build it automatically; check Go and make availability."
  [[ -d "${CLUSTER_SRC}" ]] || die "Cluster dir not found: ${CLUSTER_SRC}"
  if ! validate_cluster_tree "${CLUSTER_SRC}"; then
    restore_missing_cluster_files_from_git "${CLUSTER_SRC}" || true
  fi
  validate_cluster_tree "${CLUSTER_SRC}" || die "Cluster source tree is incomplete; aborting install."
}

create_user_and_directories() {
  log_step "Creating user and directories"
  id -u "${APP_USER}" >/dev/null 2>&1 \
    || useradd -r -m -d "${APP_STATE_DIR}" -s /usr/sbin/nologin "${APP_USER}"
  install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0750 "${APP_STATE_DIR}" "${JOBS_DIR}"
  install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0700 "${KEYS_DIR}"
  install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0755 "${TENANTS_DIR}"
  install -d -o root -g root -m 0755 "${APP_ROOT}"
  install -d -o root -g "${APP_GROUP}" -m 0750 "${APP_ENV_DIR}"
  install -d -o "${APP_USER}" -g "${APP_GROUP}" -m 0750 "${LOG_DIR}"
  touch "${LOG_FILE}"
  chown "${APP_USER}:${APP_GROUP}" "${LOG_FILE}"
  chmod 0640 "${LOG_FILE}"
}

install_binary() {
  log_step "Installing binary"
  install -m 0755 "${BIN_SRC}" "${APP_BIN}"
}

install_ansible_collections() {
  local req="${CLUSTER_INSTALL_DIR}/requirements.yml"
  [[ -f "${req}" ]] || return 0
  log_step "Installing Ansible Galaxy collections"
  ansible-galaxy collection install -r "${req}"
}

install_cluster_tree() {
  local src_real dst_real
  src_real="$(realpath "${CLUSTER_SRC}")"
  dst_real="$(realpath "${CLUSTER_INSTALL_DIR}" 2>/dev/null || echo "${CLUSTER_INSTALL_DIR}")"

  if [[ "${src_real}" == "${dst_real}" ]]; then
    log_step "Cluster already at ${CLUSTER_INSTALL_DIR} (git pull keeps it up to date)"
    validate_cluster_tree "${CLUSTER_INSTALL_DIR}" || die "Cluster tree at ${CLUSTER_INSTALL_DIR} is incomplete; aborting."
    return 0
  fi

  log_step "Installing cluster playbooks"
  TMP_CLUSTER_STAGE="$(mktemp -d /tmp/.erawan-cluster-stage.XXXXXX)"
  cp -a "${CLUSTER_SRC}" "${TMP_CLUSTER_STAGE}/cluster"
  validate_cluster_tree "${TMP_CLUSTER_STAGE}/cluster" || die "Staged cluster tree is incomplete; aborting install."
  rm -rf "${CLUSTER_INSTALL_DIR}"
  mv "${TMP_CLUSTER_STAGE}/cluster" "${CLUSTER_INSTALL_DIR}"
}

write_env_file() {
  log_step "Writing env file"
  if [[ ! -f "${APP_ENV_FILE}" ]]; then
    cat >"${APP_ENV_FILE}" <<EOF
API_HOST=127.0.0.1
API_PORT=8080
ENV=prod
API_KEY=CHANGE_TO_STRONG_RANDOM_KEY
# ENCRYPTION_KEY: 64-char hex (AES-256-GCM payload encryption). Generate: openssl rand -hex 32
# ENCRYPTION_KEY=

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
CLUSTER_SSH_KNOWN_HOSTS=${KEYS_DIR}/known_hosts
# CLUSTER_SSH_INSECURE_HOST_KEY=true  # uncomment only for first-time bootstrap before host keys are scanned

CLUSTER_ANSIBLE_DEBUG=false
CLUSTER_ANSIBLE_VERBOSITY=0
CLUSTER_STEP_OUTPUT_MAX_CHARS=8000
EOF
  fi
  chown root:"${APP_GROUP}" "${APP_ENV_FILE}"
  chmod 0640 "${APP_ENV_FILE}"

  upsert_env "MYSQL_DEPLOY_PLAYBOOK"     "${CLUSTER_INSTALL_DIR}/mysql/playbooks/deploy.yml"   "${APP_ENV_FILE}"
  upsert_env "MYSQL_ROLLBACK_PLAYBOOK"  "${CLUSTER_INSTALL_DIR}/mysql/playbooks/rollback.yml" "${APP_ENV_FILE}"
  upsert_env "PGSQL_DEPLOY_PLAYBOOK"    "${CLUSTER_INSTALL_DIR}/pgsql/playbooks/deploy.yml"   "${APP_ENV_FILE}"
  upsert_env "CLUSTER_STATE_DIR"        "${JOBS_DIR}"                                          "${APP_ENV_FILE}"
  upsert_env "TENANTS_DIR"              "${TENANTS_DIR}"                                       "${APP_ENV_FILE}"
  upsert_env "CLUSTER_SSH_KNOWN_HOSTS"  "${KEYS_DIR}/known_hosts"                             "${APP_ENV_FILE}"
}

configure_haproxy() {
  log_step "Configuring HAProxy global socket"
  if grep -qE '^\s*stats socket /run/haproxy/admin\.sock' "${HAPROXY_CONFIG_FILE}"; then
    sed -i -E "s|^\s*stats socket /run/haproxy/admin\.sock.*|${HAPROXY_SOCKET_LINE}|" "${HAPROXY_CONFIG_FILE}"
  fi

  log_step "Configuring HAProxy tenant loading"
  if [[ -f "${HAPROXY_DEFAULTS_FILE}" ]]; then
    upsert_env "CONFIG" "\"${HAPROXY_CONFIG_FILE} -f ${TENANTS_DIR}\"" "${HAPROXY_DEFAULTS_FILE}"
  fi

  log_step "Configuring HAProxy systemd override"
  install -d -o root -g root -m 0755 "${HAPROXY_OVERRIDE_DIR}"
  cat >"${HAPROXY_OVERRIDE_FILE}" <<EOF
[Service]
ExecStart=
ExecStart=/usr/sbin/haproxy -Ws -f ${HAPROXY_CONFIG_FILE} -f ${TENANTS_DIR} -p /run/haproxy.pid -S /run/haproxy-master.sock
ExecReload=
ExecReload=/usr/sbin/haproxy -c -q -f ${HAPROXY_CONFIG_FILE} -f ${TENANTS_DIR}
ExecReload=/bin/kill -USR2 \$MAINPID
EOF
}

write_sudoers_rule() {
  log_step "Writing sudoers rule"
  cat >"${SUDOERS_FILE}" <<EOF
${APP_USER} ALL=(root) NOPASSWD: /bin/systemctl reload haproxy
EOF
  chmod 0440 "${SUDOERS_FILE}"
}

write_systemd_unit() {
  log_step "Writing systemd unit"
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
ReadWritePaths=${APP_STATE_DIR} ${LOG_DIR}
StandardOutput=append:${LOG_FILE}
StandardError=append:${LOG_FILE}

[Install]
WantedBy=multi-user.target
EOF
}

start_services() {
  log_step "Validating HAProxy config"
  haproxy -c -f "${HAPROXY_CONFIG_FILE}" -f "${TENANTS_DIR}"

  log_step "Starting services"
  systemctl daemon-reload
  systemctl enable haproxy "${APP_NAME}"
  if systemctl is-active --quiet haproxy; then
    systemctl reload haproxy
  else
    systemctl start haproxy
  fi
  systemctl restart "${APP_NAME}"
}

print_summary() {
  echo ""
  log_step "Done"
  echo "  Binary:   ${APP_BIN}"
  echo "  Cluster:  ${CLUSTER_INSTALL_DIR}"
  echo "  Env file: ${APP_ENV_FILE}"
  echo ""
  echo "Edit ${APP_ENV_FILE} and set:"
  echo "  API_KEY                      — strong random key"
  echo "  ENCRYPTION_KEY               — 64-char hex for AES-256-GCM payload encryption (openssl rand -hex 32)"
  echo "  CLUSTER_SSH_USER             — SSH user for cluster nodes"
  echo "  CLUSTER_SSH_PRIVATE_KEY_PATH — path to the SSH private key"
  echo ""
  echo "Before first deploy, scan cluster node SSH host keys:"
  echo "  ssh-keyscan -H <node-ip> [<node-ip> ...] >> ${KEYS_DIR}/known_hosts"
  echo "  (or set CLUSTER_SSH_INSECURE_HOST_KEY=true in ${APP_ENV_FILE} for bootstrap only)"
  echo ""
  echo "Logs: ${LOG_FILE}"
  echo "  tail -f ${LOG_FILE}"
  echo ""
  echo "Check status:"
  echo "  systemctl status ${APP_NAME} --no-pager"
  echo "  systemctl status haproxy --no-pager"
}

main() {
  install_packages
  validate_sources
  create_user_and_directories
  install_binary
  install_cluster_tree
  install_ansible_collections
  write_env_file
  configure_haproxy
  write_sudoers_rule
  write_systemd_unit
  start_services
  print_summary
}

main "$@"
