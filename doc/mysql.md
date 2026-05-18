# MySQL InnoDB Cluster with MySQL Router

This project deploys MySQL HA with MySQL InnoDB Cluster, MySQL Shell, and optional MySQL Router bootstrap.

## Assumptions

- MySQL is already installed and running on every target node.
- `mysqlsh` is already installed on every target node.
- Local MySQL administration as OS `root` works through a Unix socket on every target node.
- MySQL nodes can reach each other on the MySQL port.
- The API host can SSH to every target node.
- The API host already has the matching private key for the cloud-template SSH user, such as `clusterops`.
- That SSH user can run `sudo` without a password on every DB node.

Supported topologies:

- Single-node bootstrap:
  - 1 primary node
- HA cluster:
  - 1 primary node
  - 1 or more secondary nodes

For production automatic failover, use at least 3 database nodes so Group Replication can keep quorum after a single node loss.

## API payload

Use a MySQL deploy body like this:

```json
{
  "cluster_name": "prodCluster",
  "primary_ip": "192.168.122.154",
  "standby_ips": ["192.168.122.111"],
  "new_user": "appuser",
  "new_user_password": "AppUser#2026",
  "new_user_ssl_required": true,
  "new_db": "appdb",
  "assume_prepared": false,
  "bootstrap_router": true,
  "ssh_port": 22,
  "mysql_port": 3306,
  "step_timeout_seconds": 900
}
```

To resume a failed job:

```json
{
  "new_user_password": "AppUser#2026"
}
```

To roll back a MySQL job:

```json
{}
```

## Field behavior

- `primary_ip`: node used to create the initial InnoDB Cluster.
- `standby_ips`: optional list of replica nodes to add after cluster creation.
- `cluster_admin_username`: optional override for the internally managed cluster admin account. Defaults to `clusteradmin`.
- `bootstrap_router`: when `true`, bootstraps MySQL Router on all DB nodes.
- `mysql_recovery_method`: optional Ansible variable override for how standbys join the cluster. Defaults to `auto`, which prefers faster incremental recovery and falls back to clone when necessary.
- `ssh_port`: SSH port for the target nodes. Defaults to `22`.
- `assume_prepared`: when `true`, skips preflight and instance-configuration steps.
- `new_user`, `new_user_password`, `new_db`: optional application database bootstrap.
- `new_user_ssl_required`: controls whether the created MySQL user requires SSL.

SSH user and private key are configured once on the API host through `CLUSTER_SSH_USER` and `CLUSTER_SSH_PRIVATE_KEY_PATH`.

The generated MySQL instance config also points to MySQL's default auto-generated TLS files in the data directory:

- `/var/lib/mysql/ca.pem`
- `/var/lib/mysql/server-cert.pem`
- `/var/lib/mysql/server-key.pem`

## Deployment flow

1. Preflight checks confirm MySQL, MySQL Shell, and connectivity prerequisites are present.
2. Instance configuration prepares each node for InnoDB Cluster and creates or updates the cluster admin account.
3. Cluster creation runs on the requested primary node.
4. Secondary nodes are added when `standby_ips` is not empty. By default, MySQL Shell uses `recoveryMethod=auto`, which prefers faster incremental recovery when possible and falls back to clone when needed.
5. Group Replication auto-start and auto-rejoin settings are enabled on all members.
6. MySQL Router is bootstrapped on all nodes when `bootstrap_router` is enabled.
7. Verification checks cluster health and router state.
8. Optional application database and user creation runs on the primary.

## Architecture Overview

```text
                         App Clients
                              |
                              v
                        +------------+
                        |  HAProxy    |
                        |  optional   |
                        +------------+
                              |
                              v
                  +-------------------------+
                  | MySQL Router on DB nodes|
                  | optional bootstrap      |
                  +-------------------------+
                     |            |            |
                     v            v            v
                +---------+  +---------+  +---------+
                | Primary |  |Secondary|  |Secondary|
                | MySQL   |  | MySQL   |  | MySQL   |
                +---------+  +---------+  +---------+
                     \            |            /
                      \           |           /
                       +---------------------+
                       | InnoDB Cluster GR   |
                       | managed by mysqlsh  |
                       +---------------------+
```

## Optional modes

Single-node mode:

- Leave `standby_ips` empty.
- The `add_instances` step is skipped automatically.

Prepared-node mode:

- Set `assume_prepared` to `true` if the nodes were already prepared earlier.
- The `preflight` and `configure_instances` steps are skipped.

No-router mode:

- Set `bootstrap_router` to `false`.
- The router bootstrap step is skipped.

## What the automation manages

The MySQL playbooks manage:

- InnoDB Cluster lifecycle through `mysqlsh`
- Cluster member addition on secondary nodes
- MySQL Router bootstrap and systemd service installation
- Optional application database and user creation
- Rollback for router services and cluster dissolve

## Important behavior

- MySQL supports both single-node and multi-node deployments in this project.
- Rollback support exists for MySQL jobs through the rollback API.
- If `bootstrap_router` is enabled, router services are created on all target DB nodes.
- Auto-rejoin behavior is explicitly configured after cluster formation:
  - `group_replication_start_on_boot = ON`
  - `group_replication_autorejoin_tries = 3`
  - `group_replication_unreachable_majority_timeout = 30`
  - `group_replication_exit_state_action = READ_ONLY`
