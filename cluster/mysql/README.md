# MySQL Cluster Ansible

This folder contains the MySQL InnoDB Cluster automation used by the API.

Supported topologies:

- 1 MySQL primary node only
- 1 MySQL primary node plus 1 or more secondary nodes

Implemented workflow:

- MySQL and MySQL Shell preflight checks
- Instance preparation for InnoDB Cluster
- Cluster creation on the requested primary node
- Secondary-node addition when `standby_ips` is provided
- Group Replication auto-rejoin and boot-recovery setup on all nodes
- Cluster verification and optional application database bootstrap
- Primary-check HTTP endpoint on every node so HAProxy can route writes to
  the current Group Replication primary without MySQL Router
- Rollback playbook for primary-check cleanup and cluster dissolve

Architecture overview:

```text
      API / Ansible
           |
           v
   +----------------+
   | mysqlsh        |
   | cluster tasks  |
   +----------------+
           |
           v
   +---------------------+
   | InnoDB Cluster      |
   | primary + secondary |
   +---------------------+
           |
           v
   +---------------------+
   | primary-check :9200 |
   | on every node       |
   +---------------------+
```

Entry points:

- `cluster/mysql/playbooks/deploy.yml`
- `cluster/mysql/playbooks/rollback.yml`
