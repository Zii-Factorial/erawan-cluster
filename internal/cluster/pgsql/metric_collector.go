package pgsql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// Collector gathers live metrics from a PostgreSQL / Patroni cluster.
// Each category is collected independently — a failure in one never suppresses the others.
type Collector struct {
	httpClient *http.Client
}

// NewCollector returns a ready-to-use Collector.
func NewCollector() *Collector {
	return &Collector{
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Collect gathers every requested category and returns a MetricResponse.
func (c *Collector) Collect(ctx context.Context, req MetricRequest) MetricResponse {
	resp := MetricResponse{
		CollectedAt: time.Now().UTC(),
		Host:        req.Host,
		Port:        resolvePort(req.Port, 5432),
		From:        req.From,
		To:          req.To,
		Categories:  make(map[string]any),
		Errors:      make(map[string]string),
	}

	categories := resolveCategories(req.Categories)

	var db *sql.DB
	if categoriesNeedDB(categories) {
		var err error
		db, err = openDB(req)
		if err != nil {
			for _, cat := range categories {
				if !requiresNoDB(cat) {
					resp.Errors[cat] = "db connect: " + err.Error()
				}
			}
		} else {
			defer db.Close()
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}

	type catResult struct {
		data any
		err  error
	}
	results := make(map[string]catResult, len(categories))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, cat := range categories {
		if !requiresNoDB(cat) && db == nil {
			continue // error already recorded above
		}
		cat := cat
		wg.Add(1)
		go func() {
			defer wg.Done()
			var data any
			var err error
			switch cat {
			case MetricCategoryCluster:
				data, err = c.collectCluster(ctx, req)
			case MetricCategoryUptime:
				data, err = collectUptime(ctx, db)
			case MetricCategoryFailover:
				data, err = c.collectFailover(ctx, req)
			case MetricCategoryConnections:
				data, err = collectConnections(ctx, db)
			case MetricCategoryReplication:
				data, err = collectReplication(ctx, db)
			case MetricCategoryPerformance:
				data, err = collectPerformance(ctx, db)
			case MetricCategoryQuery:
				data, err = collectQuery(ctx, db, limit, req.From, req.To)
			case MetricCategoryMaintenance:
				data, err = collectMaintenance(ctx, db, limit)
			}
			mu.Lock()
			results[cat] = catResult{data, err}
			mu.Unlock()
		}()
	}
	wg.Wait()

	for cat, r := range results {
		if r.err != nil {
			resp.Errors[cat] = r.err.Error()
		} else {
			resp.Categories[cat] = r.data
		}
	}

	if len(resp.Errors) == 0 {
		resp.Errors = nil
	}
	return resp
}

// ValidateMetricRequest applies defaults and validates required fields.
func ValidateMetricRequest(req *MetricRequest) error {
	req.Host = strings.TrimSpace(req.Host)
	if req.Host == "" {
		return fmt.Errorf("host is required")
	}
	if req.Port == 0 {
		req.Port = 5432
	}
	if req.Port < 1 || req.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	req.User = strings.TrimSpace(req.User)
	if req.User == "" {
		return fmt.Errorf("user is required")
	}
	if req.SSLMode == "" {
		req.SSLMode = "disable"
	}
	if req.SSLMode != "disable" && req.SSLMode != "require" {
		return fmt.Errorf("ssl_mode must be 'disable' or 'require'")
	}
	if req.From != nil && req.To != nil && req.From.After(*req.To) {
		return fmt.Errorf("from must be before to")
	}
	valid := make(map[string]bool, len(allMetricCategories))
	for _, c := range allMetricCategories {
		valid[c] = true
	}
	for _, cat := range req.Categories {
		if !valid[strings.ToLower(strings.TrimSpace(cat))] {
			return fmt.Errorf("unknown category %q; valid: %s",
				cat, strings.Join(allMetricCategories, ", "))
		}
	}
	return nil
}

// =============================================================================
// helpers
// =============================================================================

func resolvePort(port, def int) int {
	if port <= 0 {
		return def
	}
	return port
}

// discoverLeader probes each node in req.NodeIPs and returns the IP of the
// node that responds 200 to GET /leader (i.e. the current Patroni primary).
// Returns an error if no node responds as leader.
func (c *Collector) discoverLeader(ctx context.Context, req MetricRequest) (string, error) {
	patroniPort := resolvePort(req.PatroniPort, 8008)
	if len(req.NodeIPs) == 0 {
		return "", fmt.Errorf("node_ips is required for cluster/failover metrics — provide the IPs of all cluster members")
	}
	for _, ip := range req.NodeIPs {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		url := fmt.Sprintf("http://%s:%d/leader", ip, patroniPort)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no Patroni leader found among node_ips %v (port %d) — cluster may be unhealthy", req.NodeIPs, patroniPort)
}

// requiresNoDB returns true for categories that use Patroni REST only (no DB connection needed).
func requiresNoDB(cat string) bool {
	return cat == MetricCategoryCluster || cat == MetricCategoryFailover
}

func categoriesNeedDB(cats []string) bool {
	for _, c := range cats {
		if !requiresNoDB(c) {
			return true
		}
	}
	return false
}

func resolveCategories(requested []string) []string {
	if len(requested) == 0 {
		return allMetricCategories
	}
	valid := make(map[string]bool, len(allMetricCategories))
	for _, c := range allMetricCategories {
		valid[c] = true
	}
	out := make([]string, 0, len(requested))
	for _, r := range requested {
		lc := strings.ToLower(strings.TrimSpace(r))
		if valid[lc] {
			out = append(out, lc)
		}
	}
	return out
}

func openDB(req MetricRequest) (*sql.DB, error) {
	port := resolvePort(req.Port, 5432)
	dbName := req.Database
	if dbName == "" {
		dbName = "postgres"
	}
	timeout := req.ConnectTimeout
	if timeout <= 0 {
		timeout = 10
	}
	sslMode := req.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(req.User, req.Password),
		Host:   fmt.Sprintf("%s:%d", req.Host, port),
		Path:   "/" + dbName,
	}
	q := url.Values{}
	q.Set("sslmode", sslMode)
	q.Set("connect_timeout", strconv.Itoa(timeout))
	u.RawQuery = q.Encode()

	db, err := sql.Open("postgres", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(len(allMetricCategories))
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Second)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// nullTime converts *time.Time to an interface{} suitable for database/sql params
// (nil → SQL NULL, non-nil → time value).
func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

// =============================================================================
// Patroni HTTP helpers
// =============================================================================

const patroniBodyLimit = 1 << 20 // 1 MB — sufficient for any Patroni response

func (c *Collector) patroniRequest(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("patroni %s: HTTP %d", rawURL, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, patroniBodyLimit)).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", rawURL, err)
	}
	return nil
}

func (c *Collector) patroniGET(ctx context.Context, rawURL string) (map[string]any, error) {
	var out map[string]any
	return out, c.patroniRequest(ctx, rawURL, &out)
}

func (c *Collector) patroniGETArray(ctx context.Context, rawURL string) ([]any, error) {
	var out []any
	return out, c.patroniRequest(ctx, rawURL, &out)
}

// =============================================================================
// cluster — Patroni /, /cluster, /config
// =============================================================================

func (c *Collector) collectCluster(ctx context.Context, req MetricRequest) (*ClusterMetric, error) {
	leaderIP, err := c.discoverLeader(ctx, req)
	if err != nil {
		return nil, err
	}
	patroniPort := resolvePort(req.PatroniPort, 8008)
	base := fmt.Sprintf("http://%s:%d", leaderIP, patroniPort)

	nodeState, err := c.patroniGET(ctx, base+"/")
	if err != nil {
		return nil, fmt.Errorf("patroni node state: %w", err)
	}
	clusterState, err := c.patroniGET(ctx, base+"/cluster")
	if err != nil {
		return nil, fmt.Errorf("patroni cluster state: %w", err)
	}

	m := &ClusterMetric{
		Role:     getString(nodeState, "role"),
		State:    getString(nodeState, "state"),
		Timeline: getInt(nodeState, "timeline"),
		Members:  []ClusterMember{},
	}
	if p, ok := nodeState["patroni"].(map[string]any); ok {
		m.PatroniVersion = getString(p, "version")
		m.Scope = getString(p, "scope")
	}
	if sv, ok := nodeState["server_version"].(float64); ok {
		m.ServerVersion = int(sv)
	}
	// dcs_last_seen is a Unix timestamp in the Patroni node state.
	if dcs, ok := nodeState["dcs_last_seen"].(float64); ok && dcs > 0 {
		t := time.Unix(int64(dcs), 0).UTC()
		m.DCSLastSeen = &t
	}

	// Fetch TTL/loop_wait/retry_timeout from /config.
	if cfg, err := c.patroniGET(ctx, base+"/config"); err == nil {
		m.TTL = getInt(cfg, "ttl")
		m.LoopWait = getInt(cfg, "loop_wait")
		m.RetryTimeout = getInt(cfg, "retry_timeout")
	}

	if rawMembers, ok := clusterState["members"].([]any); ok {
		for _, raw := range rawMembers {
			mm, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			host, port := splitHostPort(getString(mm, "host"), 5432)
			m.Members = append(m.Members, ClusterMember{
				Name:     getString(mm, "name"),
				Host:     host,
				Port:     port,
				Role:     getString(mm, "role"),
				State:    getString(mm, "state"),
				Timeline: getInt(mm, "timeline"),
				Lag:      mm["lag"],
			})
		}
	}
	return m, nil
}

// =============================================================================
// uptime — pg_postmaster_start_time()
// =============================================================================

func collectUptime(ctx context.Context, db *sql.DB) (*UptimeMetric, error) {
	const q = `
		SELECT
		    pg_postmaster_start_time()                                     AS start_time,
		    extract(epoch from now() - pg_postmaster_start_time())::bigint AS uptime_seconds`

	var m UptimeMetric
	if err := db.QueryRowContext(ctx, q).Scan(&m.StartTime, &m.UptimeSeconds); err != nil {
		return nil, fmt.Errorf("scan uptime: %w", err)
	}
	m.StartTime = m.StartTime.UTC()
	m.UptimeHuman = formatDuration(m.UptimeSeconds)
	return &m, nil
}

// =============================================================================
// failover — Patroni /history (time-range aware)
// =============================================================================

func (c *Collector) collectFailover(ctx context.Context, req MetricRequest) (*FailoverMetric, error) {
	leaderIP, err := c.discoverLeader(ctx, req)
	if err != nil {
		return nil, err
	}
	patroniPort := resolvePort(req.PatroniPort, 8008)
	base := fmt.Sprintf("http://%s:%d", leaderIP, patroniPort)

	nodeState, err := c.patroniGET(ctx, base+"/")
	if err != nil {
		return nil, fmt.Errorf("patroni node state: %w", err)
	}
	rawHistory, err := c.patroniGETArray(ctx, base+"/history")
	if err != nil {
		return nil, fmt.Errorf("patroni history: %w", err)
	}

	m := &FailoverMetric{
		CurrentTimeline: getInt(nodeState, "timeline"),
		Events:          []FailoverEvent{},
	}

	var lastEventTime *time.Time
	// Patroni /history format: [[timeline, lsn, reason, timestamp], ...]
	for _, raw := range rawHistory {
		entry, ok := raw.([]any)
		if !ok || len(entry) < 4 {
			continue
		}
		timeline, _ := entry[0].(float64)
		lsn, _ := entry[1].(string)
		reason, _ := entry[2].(string)
		tsStr, _ := entry[3].(string)
		ts := parsePatroniTime(tsStr)
		if ts.IsZero() {
			continue // unrecognised timestamp format — skip to avoid year-0001 garbage
		}

		if req.From != nil && ts.Before(*req.From) {
			continue
		}
		if req.To != nil && ts.After(*req.To) {
			continue
		}

		t := ts.UTC()
		lastEventTime = &t
		m.Events = append(m.Events, FailoverEvent{
			Timeline:   int(timeline),
			LSN:        lsn,
			Reason:     reason,
			OccurredAt: t,
		})
	}
	m.TotalEvents = len(m.Events)
	if lastEventTime != nil {
		sec := int64(time.Since(*lastEventTime).Seconds())
		m.TimeSinceLastFailoverSeconds = &sec
	}
	return m, nil
}

// =============================================================================
// connections — pg_stat_activity
// =============================================================================

func collectConnections(ctx context.Context, db *sql.DB) (*ConnectionMetric, error) {
	const qSummary = `
		SELECT
		    count(*)                                                          AS total,
		    count(*) FILTER (WHERE state = 'active')                         AS active,
		    count(*) FILTER (WHERE state = 'idle')                           AS idle,
		    count(*) FILTER (WHERE state = 'idle in transaction')            AS idle_in_tx,
		    count(*) FILTER (WHERE wait_event_type = 'Lock')                 AS waiting_lock,
		    coalesce(
		        round(avg(extract(epoch from now()-backend_start))*1000, 2),
		        0
		    )                                                                 AS avg_session_age_ms
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()`

	const qWaitEvents = `
		SELECT coalesce(wait_event_type,'None') AS evt, count(*) AS cnt
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		GROUP BY wait_event_type
		ORDER BY cnt DESC`

	const qPerDB = `
		SELECT
		    datname,
		    count(*)                                  AS total,
		    count(*) FILTER (WHERE state='active')   AS active,
		    count(*) FILTER (WHERE state='idle')     AS idle
		FROM pg_stat_activity
		WHERE datname IS NOT NULL AND pid <> pg_backend_pid()
		GROUP BY datname
		ORDER BY total DESC`

	m := &ConnectionMetric{ByDatabase: []DBConnStat{}, WaitEvents: []WaitEvent{}}

	if err := db.QueryRowContext(ctx, qSummary).Scan(
		&m.TotalConnections, &m.Active, &m.Idle,
		&m.IdleInTransaction, &m.WaitingForLock, &m.AvgSessionAgeMs,
	); err != nil {
		return nil, fmt.Errorf("scan connection summary: %w", err)
	}

	var maxStr string
	if err := db.QueryRowContext(ctx, `SHOW max_connections`).Scan(&maxStr); err == nil {
		fmt.Sscanf(maxStr, "%d", &m.MaxConnections)
	}
	if m.MaxConnections > 0 {
		m.UtilizationPct = math.Round(float64(m.TotalConnections)/float64(m.MaxConnections)*10000) / 100
	}

	rows, err := db.QueryContext(ctx, qWaitEvents)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var w WaitEvent
			if rows.Scan(&w.Type, &w.Count) == nil {
				m.WaitEvents = append(m.WaitEvents, w)
			}
		}
		_ = rows.Err()
	}

	rows2, err := db.QueryContext(ctx, qPerDB)
	if err != nil {
		return nil, fmt.Errorf("query connections per db: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var s DBConnStat
		if err := rows2.Scan(&s.Database, &s.Total, &s.Active, &s.Idle); err != nil {
			continue
		}
		m.ByDatabase = append(m.ByDatabase, s)
	}
	return m, rows2.Err()
}

// =============================================================================
// replication — pg_stat_replication + pg_replication_slots + pg_settings
// =============================================================================

func collectReplication(ctx context.Context, db *sql.DB) (*ReplicationMetric, error) {
	m := &ReplicationMetric{Members: []ReplicationMember{}, Slots: []ReplicationSlot{}}

	// WAL config.
	const qCfg = `
		SELECT
		    max(CASE WHEN name='wal_level'      THEN setting END) AS wal_level,
		    max(CASE WHEN name='max_wal_senders' THEN setting END) AS max_wal_senders
		FROM pg_settings
		WHERE name IN ('wal_level','max_wal_senders')`
	var maxSendersStr string
	_ = db.QueryRowContext(ctx, qCfg).Scan(&m.WALLevel, &maxSendersStr)
	fmt.Sscanf(maxSendersStr, "%d", &m.MaxWALSenders)

	// Streaming members.
	const qMembers = `
		SELECT
		    coalesce(client_addr::text,''),
		    application_name,
		    state,
		    sync_state,
		    sent_lsn::text,
		    write_lsn::text,
		    flush_lsn::text,
		    replay_lsn::text,
		    extract(epoch from write_lag)::float,
		    extract(epoch from flush_lag)::float,
		    extract(epoch from replay_lag)::float
		FROM pg_stat_replication
		ORDER BY application_name`

	rows, err := db.QueryContext(ctx, qMembers)
	if err != nil {
		return nil, fmt.Errorf("query replication: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r ReplicationMember
		var wl, fl, rl sql.NullFloat64
		if err := rows.Scan(
			&r.ClientAddr, &r.ApplicationName, &r.State, &r.SyncState,
			&r.SentLSN, &r.WriteLSN, &r.FlushLSN, &r.ReplayLSN,
			&wl, &fl, &rl,
		); err != nil {
			continue
		}
		if wl.Valid {
			v := wl.Float64
			r.WriteLagSeconds = &v
		}
		if fl.Valid {
			v := fl.Float64
			r.FlushLagSeconds = &v
		}
		if rl.Valid {
			v := rl.Float64
			r.ReplayLagSeconds = &v
		}
		m.Members = append(m.Members, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m.StandbyCount = len(m.Members)

	// Replication slots.
	const qSlots = `
		SELECT
		    slot_name,
		    slot_type,
		    coalesce(database,''),
		    active,
		    CASE WHEN pg_is_in_recovery() THEN NULL
		         ELSE pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)::bigint
		    END AS lag_bytes
		FROM pg_replication_slots
		ORDER BY slot_name`

	rows2, err := db.QueryContext(ctx, qSlots)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var s ReplicationSlot
			var lagBytes sql.NullInt64
			if err := rows2.Scan(&s.Name, &s.Type, &s.Database, &s.Active, &lagBytes); err != nil {
				continue
			}
			if lagBytes.Valid {
				v := lagBytes.Int64
				s.LagBytes = &v
			}
			m.Slots = append(m.Slots, s)
		}
		_ = rows2.Err()
	}
	return m, nil
}

// =============================================================================
// performance — throughput, cache hit, checkpoints, bgwriter
// =============================================================================

func collectPerformance(ctx context.Context, db *sql.DB) (*PerformanceMetric, error) {
	const qDB = `
		SELECT
		    sum(xact_commit)                                   AS commits,
		    sum(xact_rollback)                                 AS rollbacks,
		    sum(xact_commit) + sum(xact_rollback)              AS total_tx,
		    CASE WHEN sum(blks_hit + blks_read) = 0 THEN 0::numeric
		         ELSE round(sum(blks_hit)::numeric / sum(blks_hit + blks_read) * 100, 2)
		    END                                                AS cache_hit_ratio,
		    sum(temp_files)                                    AS temp_files,
		    sum(temp_bytes)                                    AS temp_bytes,
		    min(stats_reset)                                   AS stats_reset
		FROM pg_stat_database
		WHERE datname NOT IN ('template0', 'template1')`

	const qBGW = `
		SELECT
		    checkpoints_timed,
		    checkpoints_req,
		    buffers_checkpoint,
		    buffers_clean,
		    buffers_backend,
		    buffers_alloc,
		    checkpoint_write_time,
		    checkpoint_sync_time
		FROM pg_stat_bgwriter`

	m := &PerformanceMetric{}

	var statsReset sql.NullTime
	if err := db.QueryRowContext(ctx, qDB).Scan(
		&m.TotalCommits, &m.TotalRollbacks, &m.TotalTransactions,
		&m.CacheHitRatioPct, &m.TempFiles, &m.TempBytes, &statsReset,
	); err != nil {
		return nil, fmt.Errorf("scan performance db stats: %w", err)
	}

	// Calculate avg TPS since stats reset (fall back to server start time).
	if statsReset.Valid {
		t := statsReset.Time
		m.StatsResetAt = &t
		var secs int64
		_ = db.QueryRowContext(ctx, `SELECT extract(epoch from now() - $1)::bigint`, t).Scan(&secs)
		if secs > 0 {
			m.AvgTPS = math.Round(float64(m.TotalTransactions)/float64(secs)*1000) / 1000
		}
	} else {
		var secs int64
		_ = db.QueryRowContext(ctx,
			`SELECT extract(epoch from now() - pg_postmaster_start_time())::bigint`).Scan(&secs)
		if secs > 0 {
			m.AvgTPS = math.Round(float64(m.TotalTransactions)/float64(secs)*1000) / 1000
		}
	}

	if err := db.QueryRowContext(ctx, qBGW).Scan(
		&m.CheckpointsTimed, &m.CheckpointsReq,
		&m.BuffersCheckpoint, &m.BuffersClean, &m.BuffersBackend, &m.BuffersAlloc,
		&m.CheckpointWriteMs, &m.CheckpointSyncMs,
	); err != nil {
		return nil, fmt.Errorf("scan bgwriter: %w", err)
	}
	total := m.CheckpointsTimed + m.CheckpointsReq
	if total > 0 {
		m.CheckpointRatioPct = math.Round(float64(m.CheckpointsTimed)/float64(total)*10000) / 100
	}
	return m, nil
}

// =============================================================================
// query — latency, slow queries, locks, scans, row throughput
// =============================================================================

func collectQuery(ctx context.Context, db *sql.DB, limit int, from, to *time.Time) (*QueryMetric, error) {
	m := &QueryMetric{
		SlowQueryThresholdMs: 1000,
		SlowQueries:          []SlowQuery{},
		HighSeqScanTables:    []ScanEfficiency{},
	}

	// --- pg_stat_statements (optional extension) ---
	const qPercentiles = `
		SELECT
		    coalesce(avg(mean_exec_time), 0),
		    coalesce(percentile_disc(0.95) WITHIN GROUP (ORDER BY mean_exec_time), 0),
		    coalesce(percentile_disc(0.99) WITHIN GROUP (ORDER BY mean_exec_time), 0)
		FROM pg_stat_statements
		WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND calls > 0`

	const qTopMean = `
		SELECT left(query,300),calls,mean_exec_time,max_exec_time,min_exec_time,
		       stddev_exec_time,total_exec_time,rows
		FROM pg_stat_statements
		WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database())
		ORDER BY mean_exec_time DESC LIMIT $1`

	const qTopTotal = `
		SELECT left(query,300),calls,mean_exec_time,max_exec_time,min_exec_time,
		       stddev_exec_time,total_exec_time,rows
		FROM pg_stat_statements
		WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database())
		ORDER BY total_exec_time DESC LIMIT $1`

	if err := db.QueryRowContext(ctx, qPercentiles).Scan(
		&m.AvgExecMs, &m.P95ExecMs, &m.P99ExecMs,
	); err == nil {
		m.StatementsAvailable = true
		m.TopByMeanExecMs, _ = scanQueryStats(ctx, db, qTopMean, limit)
		m.TopByTotalExecMs, _ = scanQueryStats(ctx, db, qTopTotal, limit)
	}

	// --- Slow queries from pg_stat_activity (time-range aware) ---
	// $1=from (nullable), $2=to (nullable), $3=limit, $4=threshold_ms
	const qSlow = `
		SELECT
		    pid,
		    coalesce(datname,''),
		    coalesce(usename,''),
		    coalesce(state,''),
		    left(coalesce(query,''),300),
		    query_start,
		    extract(epoch from now()-query_start)*1000 AS dur_ms
		FROM pg_stat_activity
		WHERE state = 'active'
		  AND query_start IS NOT NULL
		  AND pid <> pg_backend_pid()
		  AND extract(epoch from now()-query_start)*1000 > $4
		  AND ($1::timestamptz IS NULL OR query_start >= $1)
		  AND ($2::timestamptz IS NULL OR query_start <= $2)
		ORDER BY dur_ms DESC
		LIMIT $3`

	rows, err := db.QueryContext(ctx, qSlow, nullTime(from), nullTime(to), limit, m.SlowQueryThresholdMs)
	if err != nil {
		return nil, fmt.Errorf("query slow queries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s SlowQuery
		if err := rows.Scan(
			&s.PID, &s.Database, &s.User, &s.State, &s.Query, &s.StartedAt, &s.DurationMs,
		); err != nil {
			continue
		}
		s.StartedAt = s.StartedAt.UTC()
		if s.DurationMs > m.LongestRunningMs {
			m.LongestRunningMs = s.DurationMs
		}
		m.SlowQueries = append(m.SlowQueries, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m.SlowQueryCount = len(m.SlowQueries)

	// --- Lock / deadlock counts ---
	_ = db.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_stat_activity
		WHERE wait_event_type='Lock' AND pid<>pg_backend_pid()`).Scan(&m.LockWaitCount)

	_ = db.QueryRowContext(ctx, `
		SELECT coalesce(sum(deadlocks),0) FROM pg_stat_database WHERE datname NOT IN ('template0', 'template1')`).Scan(&m.DeadlockCount)

	// --- Row throughput ---
	_ = db.QueryRowContext(ctx, `
		SELECT coalesce(sum(tup_returned),0), coalesce(sum(tup_fetched),0)
		FROM pg_stat_database WHERE datname NOT IN ('template0', 'template1')`).
		Scan(&m.RowsReturned, &m.RowsFetched)

	// --- Scan efficiency ---
	_ = db.QueryRowContext(ctx, `
		SELECT coalesce(sum(seq_scan),0), coalesce(sum(idx_scan),0)
		FROM pg_stat_user_tables`).Scan(&m.SeqScansTotal, &m.IdxScansTotal)

	total := m.SeqScansTotal + m.IdxScansTotal
	if total > 0 {
		m.IdxScanRatioPct = math.Round(float64(m.IdxScansTotal)/float64(total)*10000) / 100
	}

	const qSeq = `
		SELECT schemaname, relname,
		    coalesce(seq_scan,0), coalesce(idx_scan,0),
		    CASE WHEN coalesce(seq_scan,0)+coalesce(idx_scan,0)=0 THEN 0::numeric
		         ELSE round(coalesce(idx_scan,0)::numeric /
		                    (coalesce(seq_scan,0)+coalesce(idx_scan,0))*100,2)
		    END
		FROM pg_stat_user_tables
		WHERE coalesce(seq_scan,0) > 50
		ORDER BY seq_scan DESC LIMIT $1`

	rows2, err := db.QueryContext(ctx, qSeq, limit)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var s ScanEfficiency
			if rows2.Scan(&s.Schema, &s.Table, &s.SeqScans, &s.IdxScans, &s.IdxScanRatioPct) == nil {
				m.HighSeqScanTables = append(m.HighSeqScanTables, s)
			}
		}
		_ = rows2.Err()
	}
	return m, nil
}

func scanQueryStats(ctx context.Context, db *sql.DB, q string, limit int) ([]QueryStat, error) {
	rows, err := db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueryStat
	for rows.Next() {
		var s QueryStat
		if err := rows.Scan(
			&s.Query, &s.Calls,
			&s.MeanExecMs, &s.MaxExecMs, &s.MinExecMs, &s.StddevExecMs,
			&s.TotalExecMs, &s.Rows,
		); err != nil {
			continue
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// =============================================================================
// maintenance — autovacuum, XID age, logical slots, lock grants
// =============================================================================

func collectMaintenance(ctx context.Context, db *sql.DB, limit int) (*MaintenanceMetric, error) {
	m := &MaintenanceMetric{
		Workers:        []VacuumWorker{},
		StaleTables:    []StaleTable{},
		LogicalSlotLag: []LogicalSlotStat{},
	}

	// Running autovacuum workers.
	const qVacuum = `
		SELECT p.pid, p.datname, p.phase, n.nspname, c.relname,
		       to_char(now()-a.query_start,'HH24:MI:SS')
		FROM pg_stat_progress_vacuum p
		JOIN pg_class         c ON c.oid = p.relid
		JOIN pg_namespace     n ON n.oid = c.relnamespace
		JOIN pg_stat_activity a ON a.pid = p.pid
		ORDER BY a.query_start`

	rows, err := db.QueryContext(ctx, qVacuum)
	if err != nil {
		return nil, fmt.Errorf("query vacuum progress: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var w VacuumWorker
		if rows.Scan(&w.PID, &w.Database, &w.Phase, &w.Schema, &w.Table, &w.Duration) == nil {
			m.Workers = append(m.Workers, w)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vacuum workers: %w", err)
	}
	m.AutovacuumRunning = len(m.Workers)

	// Stale tables (highest dead tuple count, top-N).
	const qStale = `
		SELECT schemaname, relname, last_autovacuum, last_autoanalyze, n_dead_tup
		FROM pg_stat_user_tables
		WHERE n_dead_tup > 0
		ORDER BY n_dead_tup DESC
		LIMIT $1`

	rows2, err := db.QueryContext(ctx, qStale, limit)
	if err != nil {
		return nil, fmt.Errorf("query stale tables: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var s StaleTable
		var lv, la sql.NullTime
		if rows2.Scan(&s.Schema, &s.Table, &lv, &la, &s.DeadTuples) == nil {
			if lv.Valid {
				t := lv.Time
				s.LastAutovacuum = &t
			}
			if la.Valid {
				t := la.Time
				s.LastAutoanalyze = &t
			}
			m.StaleTables = append(m.StaleTables, s)
		}
	}
	if err := rows2.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale tables: %w", err)
	}

	// Tables needing vacuum (dead ratio > 20 %).
	_ = db.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_stat_user_tables
		WHERE n_live_tup+n_dead_tup>0
		  AND n_dead_tup::float/(n_live_tup+n_dead_tup) > 0.20`).Scan(&m.TablesNeedingVacuum)

	// XID wraparound risk — worst-case database.
	const qXID = `
		SELECT max(age(datfrozenxid)) FROM pg_database WHERE datistemplate=false`
	_ = db.QueryRowContext(ctx, qXID).Scan(&m.OldestXIDAge)
	switch {
	case m.OldestXIDAge > 1_500_000_000:
		m.XIDRiskLevel = "danger"
	case m.OldestXIDAge > 1_000_000_000:
		m.XIDRiskLevel = "warning"
	default:
		m.XIDRiskLevel = "ok"
	}

	// Logical replication slot lag.
	const qSlots = `
		SELECT slot_name, coalesce(database,''), active,
		       CASE WHEN pg_is_in_recovery() THEN 0
		            ELSE pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)::bigint
		       END
		FROM pg_replication_slots
		WHERE slot_type = 'logical'
		ORDER BY 4 DESC`

	rows3, err := db.QueryContext(ctx, qSlots)
	if err != nil {
		return nil, fmt.Errorf("query logical slots: %w", err)
	}
	defer rows3.Close()
	for rows3.Next() {
		var s LogicalSlotStat
		if rows3.Scan(&s.Name, &s.Database, &s.Active, &s.LagBytes) == nil {
			m.LogicalSlotLag = append(m.LogicalSlotLag, s)
		}
	}
	if err := rows3.Err(); err != nil {
		return nil, fmt.Errorf("iterate logical slots: %w", err)
	}

	// Lock grants vs waits.
	const qLocks = `
		SELECT
		    count(*)                             AS total,
		    count(*) FILTER (WHERE granted)      AS granted,
		    count(*) FILTER (WHERE NOT granted)  AS waiting
		FROM pg_locks`
	_ = db.QueryRowContext(ctx, qLocks).Scan(&m.TotalLocks, &m.GrantedLocks, &m.WaitingLocks)

	return m, nil
}

// =============================================================================
// string / map helpers
// =============================================================================

func getString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func getInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(math.Round(v))
	case int:
		return v
	}
	return 0
}

func splitHostPort(hostport string, defaultPort int) (string, int) {
	idx := strings.LastIndex(hostport, ":")
	if idx < 0 {
		return hostport, defaultPort
	}
	port := defaultPort
	fmt.Sscanf(hostport[idx+1:], "%d", &port)
	return hostport[:idx], port
}

// formatDuration converts seconds to a human-readable string like "3d 14h 22m 5s".
func formatDuration(seconds int64) string {
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	mins := (seconds % 3600) / 60
	secs := seconds % 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	parts = append(parts, fmt.Sprintf("%ds", secs))
	return strings.Join(parts, " ")
}

// parsePatroniTime handles the several time formats Patroni uses in /history.
func parsePatroniTime(s string) time.Time {
	for _, f := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999+00:00",
		"2006-01-02T15:04:05+00:00",
	} {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
