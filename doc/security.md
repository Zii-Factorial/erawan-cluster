# Security & DB-cluster hardening

This document covers the control plane's security posture, the secure-by-default
settings introduced in the hardening overhaul, and how to relax them when an
environment requires it.

## Authentication

- The API is gated by a single shared key in the `X-API-Key` header
  (constant-time compared). Set it with `API_KEY`.
- **Fail-closed:** the process refuses to start with an empty `API_KEY` unless
  `ENV=dev`. Never run a non-dev deployment without a key.
- Optional AES-256-GCM request/response body encryption via `ENCRYPTION_KEY`
  (64 hex chars / 32 bytes).

### Known limitation — no per-caller authorization (IDOR)

There is no tenant/ownership model: any holder of the API key can read or act on
**any** job, including retrieving a cluster's stored admin DB password via
`GET /cluster/{engine}/jobs/{id}`. Treat the API key as a fully privileged
operator credential and restrict network exposure accordingly. Introducing
tenant-scoped jobs and per-tenant auth is the prerequisite to closing this and
is intentionally **out of scope** for the current work.

## Control-plane → node SSH (anti-MITM)

Ansible verifies node SSH host keys **by default**
(`StrictHostKeyChecking=yes` + `ANSIBLE_HOST_KEY_CHECKING=True`).

| Env | Default | Effect |
|---|---|---|
| `CLUSTER_SSH_KNOWN_HOSTS` | _(unset)_ | Pin a `known_hosts` file (`UserKnownHostsFile`). |
| `CLUSTER_SSH_INSECURE_HOST_KEY` | `false` | `true` disables host-key verification (greenfield bootstrap only). |

> Provisioning brand-new nodes whose host keys are not yet known will fail until
> you either pre-seed `known_hosts` or temporarily set
> `CLUSTER_SSH_INSECURE_HOST_KEY=true`.

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

## Resource-exhaustion controls

| Env | Default | Effect |
|---|---|---|
| `CLUSTER_MAX_CONCURRENT_JOBS` | `4` | Caps concurrent background jobs (each spawns an `ansible-playbook` process). Excess jobs queue cheaply; handlers still return 202 immediately. |
| request body limit | 2 MB | Oversized bodies rejected before read/decrypt. |
| `ENABLE_PPROF` | `false` | When `true`, serves `net/http/pprof` on `127.0.0.1:6060` (loopback only) for leak/CPU profiling. |

Graceful shutdown drains in-flight jobs (their Ansible runs are cancelled via
the root context) before exit.

## Dependency & toolchain hygiene

- Built with Go ≥ 1.25.11 (`go.mod` `go` directive); `make vulncheck`
  (`govulncheck`) must report no findings.
- `make check` runs fmt/vet/staticcheck/vulncheck/test as the pre-commit gate.

## DB-cluster best practices

- **TLS everywhere** for admin and replication traffic (see above); prefer
  CA-issued certs whose SAN matches node addresses so `verify-full` passes.
- **Least privilege:** the dbmanager grants application users a broad DDL set
  (`SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES`).
  Narrow these grants per workload where possible; system users/databases are
  already protected from modification/deletion.
- **Secrets at rest:** job admin passwords are stored under the state dir with
  0600 file permissions and a 0700 directory; restrict host access to that path.
