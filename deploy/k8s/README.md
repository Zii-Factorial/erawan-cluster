# Deploying erawan-cluster on CloudStack Kubernetes (CKS)

This deploys the embedded proxy node (Go API + HAProxy + Ansible in one image) onto
a CloudStack-managed Kubernetes cluster. See [../../doc/ha-architecture.md](../../doc/ha-architecture.md)
for the target architecture.

## Phase status — read this first

| | Phase 1 (these manifests) | Phase 2 (HA target) |
|---|---|---|
| Replicas | **1** (StatefulSet + PVC) | N, active-active |
| State | local PVC | shared control DB (jobs, secrets, tenant config) |
| Config apply | API writes local files | reconciler converges every node from DB |
| Job runs | inline | leased leader, exactly-once |
| On pod restart | **brief downtime** | zero downtime |

Phase 1 gets the app **running on k8s with CI/CD today**. It is **not** yet
zero-downtime or horizontally scalable — that needs code build steps 1–3 from the
architecture doc (DB store → reconciler → leader election). **Do not raise `replicas`
above 1** until those land, or every replica will keep its own local state and run
Ansible twice.

## Prerequisites

- A CKS cluster with the CloudStack cloud-controller-manager (for `type: LoadBalancer`).
- A default StorageClass (set `storageClassName` in `statefulset.yaml` if needed).
- Network path from the pods to your DB node VM IPs (SSH + DB/Patroni ports).

## Install

```sh
# 1. image — set your GHCR owner in kustomization.yaml + statefulset.yaml,
#    or override at apply time:
#    (the Release workflow pushes ghcr.io/<owner>/erawan-cluster)

# 2. create the secret out-of-band (never commit it):
kubectl create namespace erawan
kubectl -n erawan create secret generic erawan-secrets \
  --from-literal=API_KEY="$(openssl rand -hex 24)" \
  --from-literal=ENCRYPTION_KEY="$(openssl rand -hex 16)" \
  --from-file=ssh-privatekey=./clusterops_id_rsa

# 3. apply everything else:
kubectl apply -k deploy/k8s
```

`ENCRYPTION_KEY` must match what your clients use for AES-256-GCM payloads (or omit it
to disable payload encryption). `API_KEY` is required on every request via `X-API-Key`.

## Notes / gotchas

- **SQL ports are dynamic.** A k8s Service must list every port, but HAProxy binds a
  new port per tenant. Keep `service-sql.yaml` in sync with the tenant ports you
  create (the Phase 2 operator automates this). Pre-publish the band you plan to use.
- **Probes are TCP**, because `/health` requires the API key.
- **Reload** is `SIGUSR2` to the HAProxy master (no systemd in a pod) — see
  `../docker/haproxy-reload.sh`.
- **`externalTrafficPolicy: Local`** on the SQL Service preserves client source IPs;
  drop it if your LB setup needs SNAT.

## Security posture

Applied in these manifests / image:

- **Non-root** (uid/gid 10001), `runAsNonRoot`, `allowPrivilegeEscalation: false`,
  **all capabilities dropped**, `seccompProfile: RuntimeDefault`.
- **Read-only root filesystem** — only `/tmp`, `/run`, and the state PVC are writable.
  ⚠️ This is the one setting to verify end-to-end: if Ansible misbehaves under it,
  flip `readOnlyRootFilesystem` to `false` and re-test (temp dirs are already pointed
  at `/tmp`).
- **SA token not mounted** in Phase 1 (no k8s API use).
- **Secrets** never committed: `secret.yaml`/`*.secret.yaml`/keys are gitignored;
  create the Secret out-of-band (SealedSecrets/External Secrets/SOPS recommended).
  SSH key mounted read-only at mode `0440`.
- **NetworkPolicy** restricts ingress to the API + published SQL ports (needs a
  policy-enforcing CNI). Egress is open for Ansible → DB VMs; narrow it to your DB
  subnets in production.
- **CI** runs `govulncheck` (advisory). It currently flags **stdlib** CVEs
  (e.g. `GO-2025-4175`) fixed by a Go patch bump — keep the toolchain current
  (`setup-go: "1.24"` pulls the latest 1.24.x; bump local dev toolchains too).

Still on the operator to provide: TLS/mTLS termination for the API (today it's
plain HTTP behind the LB + `X-API-Key`), and image signing/scanning in the registry.
