# Security & Hardening Reference

This document covers the control plane's security posture, the secure-by-default
settings introduced in the hardening overhaul, known security risks and their
accepted trade-offs, and the deployment hardening checklist.

---

## Authentication

- The API is gated by a single shared key in the `X-API-Key` header
  (constant-time compared). Set it with `API_KEY`.
- **Fail-closed:** the process refuses to start with an empty `API_KEY` unless
  `ENV=dev`. Never run a non-dev deployment without a key.
- Optional AES-256-GCM request/response body encryption via `ENCRYPTION_KEY`
  (64 hex chars / 32 bytes).

### No per-caller authorization (accepted trade-off)

There is no tenant/ownership model: any holder of the API key can read or act on
**any** job, including retrieving a cluster's stored admin DB password via
`GET /cluster/{engine}/jobs/{id}`. Treat the API key as a fully privileged
operator credential and restrict network exposure accordingly.

Introducing tenant-scoped jobs and per-tenant auth is a prerequisite to closing
this gap and is intentionally **out of scope** for the current work.

### No rate limiting (known risk)

No per-IP or per-key request throttling is implemented. An adversary with the API
key can trigger many concurrent Ansible deployments, exhausting background-job
slots (`CLUSTER_MAX_CONCURRENT_JOBS`, default 4) or database connection budgets.
Mitigations:

- Place the API behind a reverse proxy (Nginx, HAProxy, Caddy) and configure
  rate limits there.
- Set `CLUSTER_MAX_CONCURRENT_JOBS` to an appropriate ceiling for the host.

---

## Control-plane → node SSH (anti-MITM)

Ansible verifies node SSH host keys **by default**
(`StrictHostKeyChecking=yes` + `ANSIBLE_HOST_KEY_CHECKING=True`).

| Env | Default | Effect |
|---|---|---|
| `CLUSTER_SSH_KNOWN_HOSTS` | _(unset)_ | Pin a `known_hosts` file (`UserKnownHostsFile`). |
| `CLUSTER_SSH_INSECURE_HOST_KEY` | `false` | `true` disables host-key verification (greenfield bootstrap only). |

> When `CLUSTER_SSH_KNOWN_HOSTS` is set, each deploy/add-member step pins any
> node not already present in that file via a trust-on-first-use `ssh-keyscan`
> before Ansible connects (see `SSHPolicy.EnsureKnownHosts` in
> `internal/cluster/core/ssh.go`), so brand-new nodes no longer need a manual
> pre-seed step. `StrictHostKeyChecking=yes` still applies afterwards, so a
> real key change on a previously-provisioned node (MITM, host reimage) still
> fails the step instead of being silently trusted.

---

## Control-plane → database TLS (anti-MITM)

Database connections verify the server certificate **by default**.

| Connection | Secure default | Opt-out |
|---|---|---|
| MySQL admin (dbmanager) | verified TLS | `CLUSTER_DB_TLS_MODE=skip-verify` \| `false` |
| MySQL metrics | `ssl_mode=require` ⇒ verified TLS | `ssl_mode=skip-verify` |
| PostgreSQL admin (dbmanager) | `sslmode=verify-full` | `CLUSTER_DB_SSL_MODE=require` \| `verify-ca` \| `disable` |
| PostgreSQL metrics | `ssl_mode=require` ⇒ `verify-full` | `ssl_mode=skip-verify` (⇒ pq `require`) |

> Clusters using self-signed certificates or certificates whose SAN does not
> include the connection host/IP will fail verification. Either issue
> certificates that match, or opt out with the variables above.

**Risk:** Setting `CLUSTER_DB_SSL_MODE=disable` (PostgreSQL) or
`CLUSTER_DB_TLS_MODE=false` (MySQL) exposes admin credentials and queries to
MITM on the network between the control plane and DB nodes. Only use these
opt-outs in isolated networks where you control all traffic.

---

## Resource-exhaustion controls

| Env | Default | Effect |
|---|---|---|
| `CLUSTER_MAX_CONCURRENT_JOBS` | `4` | Caps concurrent background jobs (each spawns an `ansible-playbook` process). Excess jobs queue cheaply; handlers still return 202 immediately. |
| request body limit | 2 MB | Oversized bodies rejected before read/decrypt. |
| `ENABLE_PPROF` | `false` | When `true`, serves `net/http/pprof` on `127.0.0.1:6060` (loopback only) for leak/CPU profiling. No authentication — do not expose outside the host. |

Graceful shutdown drains in-flight jobs (their Ansible runs are cancelled via
the root context) before exit.

---

## Secrets handling

### Ansible vars file

Cluster secrets (admin password, new-user password) are written to a temporary
`vars.json` file on the control-plane host during each Ansible run:

- The file is created with mode `0600` inside a `os.MkdirTemp` directory.
- The directory is removed with `defer os.RemoveAll(workspace)` after the run.
- **Risk:** any process running as the same OS user can read the file during the
  Ansible run window. On shared hosts this is a real exposure.
- **Mitigation:** run the control plane as a dedicated service account
  (`erawan` or similar) with no other interactive users on the host. Mount the
  temp directory from a `tmpfs` volume where possible.

### Deploy response credentials

The `POST /cluster/{engine}/deploy` response body includes the cluster admin
credentials in a `secret` block. This is intentional — it is the only time
the generated credentials are returned — but it means:

- Store the credentials immediately and securely (password manager, vault).
- Ensure the API is **not** behind a proxy that logs request/response bodies.
- Add `Cache-Control: no-store` at the reverse-proxy layer for the deploy
  endpoint.

### State-directory secrets

Job admin passwords are stored under the state dir with `0600` file permissions
inside a `0700` directory. Restrict host-level access to that path. Back up
the directory only through encrypted channels.

### Encryption key

`ENCRYPTION_KEY` is read from an environment variable. Environment variables are
visible in `/proc/<pid>/environ` to same-UID processes. Where possible, inject
the key from a secrets manager at startup (Vault agent, AWS Secrets Manager
sidecar, systemd `LoadCredential`) rather than from the process environment.

---

## Input validation & SQL safety

### Identifier escaping

- PostgreSQL role/database names use `pq.QuoteIdentifier()` throughout the
  dbmanager.
- MySQL identifiers use a local `mysqlID()` function that wraps the name in
  backticks with backtick characters escaped. Passwords use `mysqlLit()` with
  backslash and single-quote escaping.
- **Risk:** `mysqlLit()` uses naive `strings.ReplaceAll` rather than a
  battle-tested library. A carefully crafted password containing multi-byte
  sequences or NO_BACKSLASH_ESCAPES MySQL mode changes could theoretically
  bypass the escaping. The practical risk is low given the controlled input
  path, but it is a known limitation.
- **Mitigation (future):** migrate MySQL password changes to prepared statements
  with `?` placeholders.

### ALTER DEFAULT PRIVILEGES (PostgreSQL)

`ALTER DEFAULT PRIVILEGES FOR ROLE … IN SCHEMA public GRANT … TO …` statements
construct role/target names via string concatenation. While role names are
validated against `^[a-zA-Z_][a-zA-Z0-9_-]{1,31}$` before reaching this code,
the raw string is still interpolated without `pq.QuoteIdentifier`.

**Risk:** a crafted role name that passes the pattern but contains SQL-significant
characters could inject SQL. The pattern currently prevents this, but defense in
depth requires identifier quoting.

**Status:** tracked for a follow-up fix — switch to `pq.QuoteIdentifier` on both
the role and target parameters in the ALTER DEFAULT PRIVILEGES statements.

### Ansible shell tasks — Jinja2 quoting

Some Ansible shell tasks (e.g., `openssl req -subj "/CN={{ ansible_host }}/O=…"`)
interpolate variables without the `| quote` filter. If a node IP were somehow
set to a non-IP value containing shell metacharacters this could cause unexpected
behavior.

**Mitigation:** all node IPs are validated as IPv4/IPv6 addresses before being
written to the job spec. The risk is accepted for now; future hardening should
add `| quote` to all Jinja2 variables in `shell:` tasks.

---

## Concurrent job isolation

There is no per-cluster mutex. Two simultaneous deploys or a simultaneous
deploy + rollback targeting the same `cluster_name` can race on shared state
files.

**Mitigation:** the operator must ensure only one job per cluster runs at a time.
This is a known limitation and a prerequisite for multi-tenant use.

---

## Error message policy

Internal errors (SQL errors, file-system errors, Ansible exit codes) are wrapped
with `fmt.Errorf` and propagated up to HTTP handlers, which return them to
clients as `"message"` strings. This leaks internal paths, cluster names, and
node IPs.

**Current behaviour:** detailed errors aid debugging during the early operational
phase and are accepted.

**Future hardening:** return opaque correlation IDs to clients (`"operation
failed [req-id]"`), log full details server-side only.

---

## Network exposure

The HTTP server does **not** terminate TLS itself. It must sit behind a
TLS-terminating reverse proxy (Nginx, Caddy, HAProxy) in any production
deployment. Do not expose the plain-HTTP port on a public or shared network
interface.

See the [install guides](install/) for example reverse-proxy configurations.

---

## Audit logging

The `middleware.Logger` records HTTP method, path, status code, and latency.
It does **not** record which API key was used, the body contents, or the
business-level operation (e.g., "user X was created on cluster Y").

Structured audit logging (operation, actor/key-hash, result, scrubbed
parameters) is a planned feature and is not yet implemented.

---

## Dependency & toolchain hygiene

- Built with Go ≥ 1.25.11 (`go.mod` `go` directive); `make vulncheck`
  (`govulncheck`) must report no findings.
- `make check` runs fmt/vet/staticcheck/vulncheck/test as the pre-commit gate.

---

## DB-cluster best practices

- **TLS everywhere** for admin and replication traffic (see above); prefer
  CA-issued certs whose SAN matches node addresses so `verify-full` passes.
- **Least privilege:** the dbmanager grants application users a broad DDL set
  (`SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES`).
  Narrow these grants per workload where possible; system users/databases are
  already protected from modification/deletion.
- **Secrets at rest:** job admin passwords are stored under the state dir with
  `0600` file permissions and a `0700` directory; restrict host access to that
  path.
- **SSL by default:** `ssl_required` defaults to `true` for both MySQL and
  PostgreSQL users created via the API (deploy-time and dbmanager). Explicitly
  set `"ssl_required": false` only when you understand the risk.

---

## Deployment hardening checklist

| # | Check | Notes |
|---|-------|-------|
| 1 | `API_KEY` set to a long random value | `openssl rand -hex 32` |
| 2 | `ENV` is not `dev` in production | Prevents no-auth bypass |
| 3 | API behind TLS-terminating proxy | Nginx / Caddy / HAProxy |
| 4 | Rate limiting at the proxy layer | e.g., Nginx `limit_req_zone` |
| 5 | API port not exposed on public interfaces | Bind to internal IP only |
| 6 | `CLUSTER_SSH_INSECURE_HOST_KEY=false` | Default; only `true` during initial bootstrap |
| 7 | `CLUSTER_DB_SSL_MODE` not set to `disable` | Default is `verify-full` |
| 8 | `ENABLE_PPROF=false` in production | Default; enable only for debugging sessions |
| 9 | State directory on a dedicated, permission-restricted path | `chmod 0700 <state_dir>` |
| 10 | Control plane runs as a dedicated service account | No shared users on the host |
| 11 | Temp directory on `tmpfs` or in-memory mount | Reduces plaintext-secret window |
| 12 | Deploy response `secret` block saved to a vault immediately | Not returned again by default |
| 13 | Proxy logs do not record response bodies | Prevents credential leakage in access logs |
| 14 | `ENCRYPTION_KEY` injected from vault, not shell profile | Reduces `/proc` exposure |
| 15 | `make vulncheck` clean before each release | No known Go CVEs in dependencies |

---

## Known risks summary

| Severity | Risk | Status |
|----------|------|--------|
| Critical | MySQL password escaping uses naive `strings.ReplaceAll` instead of prepared statements | Open — tracked for fix |
| Critical | PostgreSQL ALTER DEFAULT PRIVILEGES uses string concat instead of `pq.QuoteIdentifier` | Open — tracked for fix |
| Critical | Ansible `vars.json` writes plaintext secrets to disk for run duration | Accepted — mitigate with dedicated service account + tmpfs |
| High | No rate limiting on API | Accepted — delegate to reverse proxy |
| High | No structured audit log | Planned feature |
| High | Error messages return internal details to clients | Accepted during early operational phase |
| High | Deploy response includes plaintext credentials | Accepted — by design, store immediately |
| High | No per-cluster concurrent job lock | Accepted — operator must serialize |
| High | `CLUSTER_DB_SSL_MODE=disable` opt-out exists | Accepted — documented risk |
| Medium | Jinja2 variables unquoted in Ansible shell tasks | Low actual risk (IPs validated); tracked for fix |
| Medium | pprof endpoint has no authentication | Accepted — loopback only, disabled by default |
| Medium | No native TLS on HTTP server | Accepted — delegated to reverse proxy |
| Low | `ENCRYPTION_KEY` from environment variable | Mitigate with vault injection |
