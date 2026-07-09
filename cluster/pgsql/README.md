# PostgreSQL Cluster Ansible

This folder contains the PostgreSQL HA cluster automation used by the API.

Supported topologies:

- 1 PostgreSQL primary node only
- 1 PostgreSQL primary node plus 1 or more standby nodes

Implemented workflow:

- PostgreSQL + `patroni` + `etcd` preflight checks
- Shared `etcd` cluster configuration on all PostgreSQL nodes
- Patroni leader bootstrap on the requested primary node
- Patroni replica bootstrap on standby nodes when `standby_ips` is provided
- Retry-safe bootstrap resets data only once per deployment job
- Cluster verification through systemd state, Patroni REST API, and replication checks
- Optional application database/user bootstrap
- Add member: etcd learner registration → join → promote to voter → Patroni
  standby bootstrap (one node at a time); stale etcd registrations from
  destroyed nodes are cleaned up first, with guarded `force-new-cluster`
  recovery when they have cost the primary its quorum
- Remove member: graceful Patroni + etcd removal of a standby
- Stop: data-preserving ordered shutdown (standbys → primary → etcd)
- Start / recover: restart a stopped or outage-hit cluster from existing data
  (`cluster_bootstrap` + `verify_cluster`; data directories untouched)

Architecture overview:

```text
      API / Ansible
           |
           v
   +------------------+
   | Patroni services |
   | on all PG nodes  |
   +------------------+
           |
           v
   +------------------+
   | PostgreSQL       |
   | leader + optional|
   | replicas         |
   +------------------+
           ^
           |
   +------------------+
   | etcd cluster     |
   | shared DCS state |
   +------------------+
```

Entry points:

- `cluster/pgsql/playbooks/deploy.yml`
- `cluster/pgsql/playbooks/add_member.yml`
- `cluster/pgsql/playbooks/remove_member.yml`
- `cluster/pgsql/playbooks/stop.yml`
