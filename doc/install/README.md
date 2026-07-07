# Production Installation Guides

This folder contains OS-specific production install guides for the API host:
- `ubuntu.md`
- `debian.md`
- `rocky.md`

PostgreSQL deployment note:

- PostgreSQL Patroni/etcd can be deployed as a single primary node or as a primary with one or more standby nodes.
- `standby_ips` may be empty for a small single-node deployment.

MySQL deployment note:

- MySQL InnoDB Cluster can be deployed as a single primary node or as a primary with one or more secondary nodes.
- This deployment does not use MySQL Router; every node runs a lightweight primary-check HTTP endpoint (`:9200` by default) that HAProxy uses to find the current Group Replication primary.

SSH access note:

- Recommended default is SSH key authentication with a dedicated sudo-capable user.
- Set `CLUSTER_SSH_USER` and `CLUSTER_SSH_PRIVATE_KEY_PATH` in `/etc/erawan-cluster/.env`.
- Place the matching private key on the API host before restarting the service.
- These guides assume the cloud image or template already trusts the corresponding public key on each DB node.

All guides use the same production layout:

```text
/usr/local/bin/erawan-cluster                 # API binary
/opt/erawan-cluster/cluster/                  # Ansible playbooks
/etc/erawan-cluster/.env                      # API config (root-owned, group-readable)
/var/lib/erawan-cluster/cluster/jobs/         # MySQL job state
/var/lib/erawan-cluster/cluster/jobs/pgsql/   # PostgreSQL job state (default)
/var/lib/erawan-cluster/haproxy/tenants/      # Generated HAProxy tenant configs
/etc/systemd/system/erawan-cluster.service    # API systemd unit
```

## Security baseline
1. Run API as non-root (`erawan` user from installer scripts).
2. Set strong `API_KEY` in `/etc/erawan-cluster/.env`.
3. Restrict API network exposure (private subnet or firewall allowlist only).
4. Keep MySQL and SSH credentials out of shell history and Postman exports.
5. Keep HAProxy reload permission minimal:
   - `erawan ALL=(root) NOPASSWD: /bin/systemctl reload haproxy`
6. Keep file permissions strict:
   - `/etc/erawan-cluster/.env` as `0640` and owned by `root:erawan`
   - `/var/lib/erawan-cluster` as `0750`
7. For internet-exposed environments, terminate TLS in front of API.

## HAProxy rollout behavior
- Installers configure HAProxy for hot reload.
- Runtime updates should use `reload` (no restart) to avoid dropping active connections.
