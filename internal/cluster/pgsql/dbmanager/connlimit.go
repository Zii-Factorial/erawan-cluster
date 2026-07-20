package dbmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	pgsql "erawan-cluster/internal/cluster/pgsql"
)

// configAckTimeout bounds how long we wait for Patroni to acknowledge a DCS
// config change on a node (its run loop applies DCS state every loop_wait,
// 10s by default) before giving up on that node.
const configAckTimeout = 45 * time.Second

/**
 * SetConnectionLimit changes max_connections cluster-wide for the Patroni
 * cluster owned by req.JobID. The value is PATCHed into the Patroni DCS via
 * the current primary (so every node — including ones that are down right now
 * or added later — converges on it), persisted into the stored job spec for
 * future Ansible operations, and then applied with a rolling restart because
 * PostgreSQL only picks up max_connections at server start.
 *
 * Restart order follows the hot-standby rule (replica max_connections must be
 * >= the primary's): increases restart standbys first and the primary last,
 * decreases restart the primary first.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req SetConnectionLimitRequest - the req (SetConnectionLimitRequest)
 *
 * Returns:
 *   *ConnectionLimitStatus - per-node results; non-nil even on partial failure
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) SetConnectionLimit(ctx context.Context, req SetConnectionLimitRequest) (*ConnectionLimitStatus, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	job, err := s.store.Load(req.JobID)
	if err != nil {
		return nil, fmt.Errorf("load job %q: %w", req.JobID, err)
	}
	secret, err := s.store.LoadSecret(req.JobID)
	if err != nil {
		return nil, fmt.Errorf("load job secret %q: %w", req.JobID, err)
	}
	port := job.Request.PostgresPort
	if port == 0 {
		port = 5432
	}
	candidates := append([]string{job.Request.PrimaryIP}, job.Request.StandbyIPs...)
	primary, err := s.findPrimary(ctx, candidates)
	if err != nil {
		return nil, fmt.Errorf("discover primary: %w; is the cluster running?", err)
	}

	// The live value on the primary decides the rolling-restart order below.
	oldLimit, oldErr := s.readMaxConnections(ctx, primary, port, secret.PostgresUser, secret.PostgresPassword)

	if err := s.patchPatroniMaxConnections(ctx, primary, job.Request.AdminUsername, secret.AdminPassword, req.ConnectionLimit); err != nil {
		return nil, err
	}

	// The DCS now owns the new value cluster-wide, so persist it in the job
	// spec before the restarts: even if a node fails below, the cluster and
	// every future Ansible run agree on the new limit.
	if err := s.store.Update(req.JobID, func(j *pgsql.Job) error {
		j.Request.ConnectionLimit = req.ConnectionLimit
		return nil
	}); err != nil {
		return nil, fmt.Errorf("patroni config updated but persisting the limit to the job spec failed: %w", err)
	}

	standbys := without(candidates, primary)
	ordered := append(append([]string{}, standbys...), primary)
	if oldErr == nil && req.ConnectionLimit < oldLimit {
		ordered = append([]string{primary}, standbys...)
	}

	restartErr := make(map[string]string, len(ordered))
	for _, ip := range ordered {
		if err := s.applyPendingRestart(ctx, ip, port, job.Request.AdminUsername, secret.AdminPassword,
			secret.PostgresUser, secret.PostgresPassword, req.ConnectionLimit); err != nil {
			restartErr[ip] = err.Error()
		}
	}

	status := &ConnectionLimitStatus{JobID: req.JobID, ConnectionLimit: req.ConnectionLimit}
	converged := 0
	for _, ip := range candidates {
		node := s.nodeStatus(ctx, ip, port, primary, secret.PostgresUser, secret.PostgresPassword)
		if node.Error == "" {
			if msg, ok := restartErr[ip]; ok {
				node.Error = msg
			} else if node.MaxConnections == req.ConnectionLimit {
				converged++
			} else {
				node.Error = fmt.Sprintf("live max_connections is still %d; node applies the new value on its next restart", node.MaxConnections)
			}
		}
		status.Nodes = append(status.Nodes, node)
	}

	if converged < len(candidates) {
		return status, fmt.Errorf("connection limit stored cluster-wide, but only %d/%d node(s) are running with it; see per-node errors", converged, len(candidates))
	}
	return status, nil
}

/**
 * GetConnectionLimit reports the configured connection limit from the stored
 * job spec plus the live max_connections and pending-restart state of every
 * cluster node.
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
	secret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return nil, fmt.Errorf("load job secret %q: %w", jobID, err)
	}
	port := job.Request.PostgresPort
	if port == 0 {
		port = 5432
	}
	candidates := append([]string{job.Request.PrimaryIP}, job.Request.StandbyIPs...)
	// Best-effort role labels: when no primary answers (cluster stopped) fall
	// back to the deploy-time layout so the response still lists every node.
	primary, err := s.findPrimary(ctx, candidates)
	if err != nil {
		primary = job.Request.PrimaryIP
	}

	status := &ConnectionLimitStatus{JobID: jobID, ConnectionLimit: job.Request.ConnectionLimit}
	for _, ip := range candidates {
		status.Nodes = append(status.Nodes, s.nodeStatus(ctx, ip, port, primary, secret.PostgresUser, secret.PostgresPassword))
	}
	return status, nil
}

/**
 * nodeStatus reads one node's live max_connections and Patroni pending-restart
 * flag, labeling its role from the discovered primary.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   ip string - the node IP
 *   port int - the PostgreSQL port
 *   primary string - the discovered primary IP, for role labeling
 *   user string - the postgres superuser
 *   password string - the postgres superuser password
 *
 * Returns:
 *   NodeConnectionLimit - the node's live state, with Error set when unreachable
 */
func (s *Service) nodeStatus(ctx context.Context, ip string, port int, primary, user, password string) NodeConnectionLimit {
	node := NodeConnectionLimit{IP: ip, Role: "standby"}
	if ip == primary {
		node.Role = "primary"
	}
	live, err := s.readMaxConnections(ctx, ip, port, user, password)
	if err != nil {
		node.Error = err.Error()
		return node
	}
	node.MaxConnections = live
	if pending, err := s.patroniPendingRestart(ctx, ip); err == nil {
		node.PendingRestart = pending
	}
	return node
}

/**
 * readMaxConnections reads the live max_connections value from a single node.
 * Standbys answer too: hot standby accepts read-only connections.
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
	db, err := s.connect(ctx, host, port, "postgres", user, password)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var live int
	if err := db.QueryRowContext(ctx, "SELECT current_setting('max_connections')::int").Scan(&live); err != nil {
		return 0, fmt.Errorf("read max_connections: %w", err)
	}
	return live, nil
}

/**
 * patchPatroniMaxConnections PATCHes the cluster-wide max_connections into the
 * Patroni DCS through the primary's REST API. Patroni merges the patch into
 * the existing config, so other parameters are untouched.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   primary string - the current primary IP
 *   user string - the Patroni REST API user
 *   password string - the Patroni REST API password
 *   limit int - the new max_connections value
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) patchPatroniMaxConnections(ctx context.Context, primary, user, password string, limit int) error {
	patch := map[string]interface{}{
		"postgresql": map[string]interface{}{
			"parameters": map[string]interface{}{
				"max_connections": limit,
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal patroni patch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		fmt.Sprintf("http://%s:8008/config", primary), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build patroni PATCH: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(user, password)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH patroni config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PATCH patroni config: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

/**
 * patroniPendingRestart reports whether a node's Patroni flags it as needing a
 * restart to apply a config change.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   ip string - the node IP
 *
 * Returns:
 *   bool - true when the node has a restart pending
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) patroniPendingRestart(ctx context.Context, ip string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s:8008/patroni", ip), nil)
	if err != nil {
		return false, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("GET patroni status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		return false, fmt.Errorf("GET patroni status: status %d", resp.StatusCode)
	}
	var state struct {
		PendingRestart bool `json:"pending_restart"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return false, fmt.Errorf("parse patroni status: %w", err)
	}
	return state.PendingRestart, nil
}

/**
 * applyPendingRestart waits for a node's Patroni to acknowledge the DCS config
 * change (its run loop applies DCS state every loop_wait seconds) and, when the
 * node flags a pending restart, triggers a conditional restart through the
 * Patroni REST API. Nodes already running the wanted value are left untouched.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   ip string - the node IP
 *   port int - the PostgreSQL port
 *   apiUser string - the Patroni REST API user
 *   apiPassword string - the Patroni REST API password
 *   pgUser string - the postgres superuser
 *   pgPassword string - the postgres superuser password
 *   want int - the max_connections value the node must converge on
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) applyPendingRestart(ctx context.Context, ip string, port int, apiUser, apiPassword, pgUser, pgPassword string, want int) error {
	deadline := time.Now().Add(configAckTimeout)
	for {
		live, liveErr := s.readMaxConnections(ctx, ip, port, pgUser, pgPassword)
		pending, pendErr := s.patroniPendingRestart(ctx, ip)
		switch {
		case liveErr == nil && live == want && pendErr == nil && !pending:
			return nil
		case pendErr == nil && pending:
			return s.patroniRestart(ctx, ip, apiUser, apiPassword)
		}
		if time.Now().After(deadline) {
			if liveErr != nil {
				return fmt.Errorf("node unreachable: %v", liveErr)
			}
			return fmt.Errorf("node did not acknowledge the config change within %s", configAckTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

/**
 * patroniRestart POSTs a conditional restart ({"restart_pending": true}) to a
 * node's Patroni REST API: the node restarts PostgreSQL in place only when a
 * restart is actually pending, and the call blocks until the restart finished.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   ip string - the node IP
 *   user string - the Patroni REST API user
 *   password string - the Patroni REST API password
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) patroniRestart(ctx context.Context, ip, user, password string) error {
	body, err := json.Marshal(map[string]interface{}{"restart_pending": true})
	if err != nil {
		return fmt.Errorf("marshal restart request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s:8008/restart", ip), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build patroni restart: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(user, password)

	// The default service client times out in seconds; a restart legitimately
	// takes longer, so this call gets its own generous window.
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST patroni restart: %w", err)
	}
	defer resp.Body.Close()
	// 503 means the restart condition is no longer satisfied (the pending flag
	// cleared between our check and the call) — nothing left to do.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusServiceUnavailable {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("POST patroni restart: status %d: %s", resp.StatusCode, b)
}
