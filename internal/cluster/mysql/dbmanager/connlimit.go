package dbmanager

import (
	"context"
	"fmt"

	mysql "erawan-cluster/internal/cluster/mysql"
)

/**
 * SetConnectionLimit changes max_connections on every node of the cluster owned
 * by req.JobID and persists the new value into the stored job spec, so every
 * later Ansible operation (recover, rejoin, add member) re-applies it.
 *
 * Per node it runs SET PERSIST (survives restarts via mysqld-auto.cnf) and falls
 * back to SET GLOBAL on engines without PERSIST support (MySQL 5.7). Nodes that
 * are unreachable are reported per node; the persisted spec heals them on the
 * next recover/rejoin. The spec is only updated when at least one node accepted
 * the new value, so a fully stopped cluster rejects the edit instead of silently
 * deferring it.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req SetConnectionLimitRequest - the req (SetConnectionLimitRequest)
 *
 * Returns:
 *   *ConnectionLimitStatus - per-node apply results; non-nil even on partial failure
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) SetConnectionLimit(ctx context.Context, req SetConnectionLimitRequest) (*ConnectionLimitStatus, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	nodes, port, adminUser, adminPass, err := s.resolveNodes(req.JobID)
	if err != nil {
		return nil, err
	}

	status := &ConnectionLimitStatus{JobID: req.JobID, ConnectionLimit: req.ConnectionLimit}
	applied := 0
	for i, ip := range nodes {
		node := NodeConnectionLimit{IP: ip, Role: nodeRole(i)}
		live, err := s.applyMaxConnections(ctx, ip, port, adminUser, adminPass, req.ConnectionLimit)
		if err != nil {
			node.Error = err.Error()
		} else {
			node.MaxConnections = live
			applied++
		}
		status.Nodes = append(status.Nodes, node)
	}

	if applied == 0 {
		return status, fmt.Errorf("no cluster node accepted the new connection limit; is the cluster running?")
	}
	if err := s.store.Update(req.JobID, func(j *mysql.Job) error {
		j.Request.ConnectionLimit = req.ConnectionLimit
		return nil
	}); err != nil {
		return status, fmt.Errorf("connection limit applied on %d/%d node(s) but persisting it to the job spec failed: %w", applied, len(nodes), err)
	}
	if applied < len(nodes) {
		return status, fmt.Errorf("connection limit applied on %d/%d node(s); the remaining node(s) pick it up on the next recover/rejoin", applied, len(nodes))
	}
	return status, nil
}

/**
 * GetConnectionLimit reports the configured connection limit from the stored
 * job spec plus the live max_connections value read from every cluster node.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   jobID string - the deploy job that owns the cluster
 *
 * Returns:
 *   *ConnectionLimitStatus - configured value and per-node live values
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) GetConnectionLimit(ctx context.Context, jobID string) (*ConnectionLimitStatus, error) {
	job, err := s.store.Load(jobID)
	if err != nil {
		return nil, fmt.Errorf("load job %q: %w", jobID, err)
	}
	nodes, port, adminUser, adminPass, err := s.resolveNodes(jobID)
	if err != nil {
		return nil, err
	}

	status := &ConnectionLimitStatus{JobID: jobID, ConnectionLimit: job.Request.ConnectionLimit}
	for i, ip := range nodes {
		node := NodeConnectionLimit{IP: ip, Role: nodeRole(i)}
		live, err := s.readMaxConnections(ctx, ip, port, adminUser, adminPass)
		if err != nil {
			node.Error = err.Error()
		} else {
			node.MaxConnections = live
		}
		status.Nodes = append(status.Nodes, node)
	}
	return status, nil
}

/**
 * resolveNodes loads every node IP (primary first), port, and admin credentials
 * from the stored job. StandbyIPs is kept current by the member add/remove
 * flows, so the list reflects the cluster as it exists today.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   jobID string - the jobID string
 *
 * Returns:
 *   nodes []string - all node IPs, primary first
 *   port int - the MySQL port
 *   user string - the admin user
 *   password string - the admin password
 *   err error - error value; non-nil when the operation fails
 */
func (s *Service) resolveNodes(jobID string) (nodes []string, port int, user, password string, err error) {
	job, err := s.store.Load(jobID)
	if err != nil {
		return nil, 0, "", "", fmt.Errorf("load job %q: %w", jobID, err)
	}
	secret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return nil, 0, "", "", fmt.Errorf("load job secret %q: %w", jobID, err)
	}
	p := job.Request.MySQLPort
	if p == 0 {
		p = 3306
	}
	ips := append([]string{job.Request.PrimaryIP}, job.Request.StandbyIPs...)
	return ips, p, secret.AdminUser, secret.AdminPassword, nil
}

/**
 * applyMaxConnections sets max_connections on a single node and reads back the
 * live value. SET PERSIST both applies the value immediately and records it in
 * mysqld-auto.cnf (which overrides my.cnf), so it survives node restarts; on
 * engines without PERSIST it degrades to SET GLOBAL, relying on the persisted
 * job spec for durability across restarts.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   host string - the host string
 *   port int - the port value
 *   user string - the user string
 *   password string - the password string
 *   limit int - the new max_connections value
 *
 * Returns:
 *   int - the live max_connections value after the change
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) applyMaxConnections(ctx context.Context, host string, port int, user, password string, limit int) (int, error) {
	db, err := s.connect(ctx, host, port, "", user, password)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, fmt.Sprintf("SET PERSIST max_connections = %d", limit)); err != nil {
		if _, gerr := db.ExecContext(ctx, fmt.Sprintf("SET GLOBAL max_connections = %d", limit)); gerr != nil {
			return 0, fmt.Errorf("set max_connections: %w", err)
		}
	}

	var live int
	if err := db.QueryRowContext(ctx, "SELECT @@GLOBAL.max_connections").Scan(&live); err != nil {
		return 0, fmt.Errorf("read back max_connections: %w", err)
	}
	return live, nil
}

/**
 * readMaxConnections reads the live max_connections value from a single node.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   host string - the host string
 *   port int - the port value
 *   user string - the user string
 *   password string - the password string
 *
 * Returns:
 *   int - the live max_connections value
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) readMaxConnections(ctx context.Context, host string, port int, user, password string) (int, error) {
	db, err := s.connect(ctx, host, port, "", user, password)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var live int
	if err := db.QueryRowContext(ctx, "SELECT @@GLOBAL.max_connections").Scan(&live); err != nil {
		return 0, fmt.Errorf("read max_connections: %w", err)
	}
	return live, nil
}

/**
 * nodeRole labels a node by its position in the resolved node list: the stored
 * spec always lists the primary first, then the standbys.
 *
 * Params:
 *   index int - position in the resolved node list
 *
 * Returns:
 *   string - "primary" or "standby"
 */
func nodeRole(index int) string {
	if index == 0 {
		return "primary"
	}
	return "standby"
}
