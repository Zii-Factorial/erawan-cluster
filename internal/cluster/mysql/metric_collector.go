package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

// Collector gathers live metrics from a MySQL InnoDB Cluster.
// Each category is collected independently — a failure in one never suppresses the others.
type Collector struct{}

// NewCollector returns a ready-to-use Collector.
func NewCollector() *Collector {
	return &Collector{}
}

// Collect gathers every requested category and returns a MetricResponse.
func (c *Collector) Collect(ctx context.Context, req MetricRequest) MetricResponse {
	resp := MetricResponse{
		CollectedAt: time.Now().UTC(),
		Host:        req.Host,
		Port:        resolvePort(req.Port, 3306),
		Categories:  make(map[string]any),
		Errors:      make(map[string]string),
	}

	categories := resolveCategories(req.Categories)

	db, err := openDB(req)
	if err != nil {
		for _, cat := range categories {
			resp.Errors[cat] = "db connect: " + err.Error()
		}
		return resp
	}
	defer db.Close()

	resp.DatabaseCount, _ = collectDatabaseCount(ctx, db)

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
		cat := cat
		wg.Add(1)
		go func() {
			defer wg.Done()
			var data any
			var err error
			switch cat {
			case MetricCategoryCluster:
				data, err = collectCluster(ctx, db)
			case MetricCategoryUptime:
				data, err = collectUptime(ctx, db)
			case MetricCategoryConnections:
				data, err = collectConnections(ctx, db, req.Databases)
			case MetricCategoryReplication:
				data, err = collectReplication(ctx, db)
			case MetricCategoryPerformance:
				data, err = collectPerformance(ctx, db)
			case MetricCategoryQuery:
				data, err = collectQuery(ctx, db, limit, req.From, req.To, req.Databases)
			case MetricCategoryMaintenance:
				data, err = collectMaintenance(ctx, db, limit, req.Databases)
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
	if req.Port == 0 {
		req.Port = 3306
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
	port := resolvePort(req.Port, 3306)
	dbName := req.Database
	if dbName == "" {
		dbName = "information_schema"
	}
	timeout := req.ConnectTimeout
	if timeout <= 0 {
		timeout = 10
	}

	cfg := gomysql.NewConfig()
	cfg.User = req.User
	cfg.Passwd = req.Password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", req.Host, port)
	cfg.DBName = dbName
	cfg.Timeout = time.Duration(timeout) * time.Second
	cfg.ReadTimeout = 30 * time.Second
	cfg.WriteTimeout = 30 * time.Second
	cfg.ParseTime = true
	cfg.AllowNativePasswords = true
	if req.SSLMode == "require" {
		cfg.TLSConfig = "skip-verify"
	}

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(len(allMetricCategories))
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Second)
	if err := db.PingContext(ctx()); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// ctx returns a background context; openDB ping does not need the request ctx.
func ctx() context.Context { return context.Background() }

// collectDatabaseCount returns the number of user databases (excludes system schemas).
func collectDatabaseCount(ctx context.Context, db *sql.DB) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys')`).Scan(&count)
	return count, err
}

// dbSet builds a lookup set from a list of database names.
// Returns nil when the list is empty, meaning "no filter — include all".
func dbSet(databases []string) map[string]bool {
	if len(databases) == 0 {
		return nil
	}
	s := make(map[string]bool, len(databases))
	for _, d := range databases {
		s[d] = true
	}
	return s
}

// inDBSet reports whether name passes the filter.
// A nil set means no filter (always passes).
func inDBSet(set map[string]bool, name string) bool {
	if set == nil {
		return true
	}
	return set[name]
}

// formatDuration converts seconds to "3d 14h 22m 5s".
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

// =============================================================================
// cluster — Group Replication membership
// =============================================================================

func collectCluster(ctx context.Context, db *sql.DB) (*ClusterMetric, error) {
	m := &ClusterMetric{Members: []ClusterMember{}}

	_ = db.QueryRowContext(ctx, `
		SELECT VARIABLE_VALUE FROM performance_schema.global_variables
		WHERE VARIABLE_NAME = 'group_replication_group_name'`).Scan(&m.GroupName)

	rows, err := db.QueryContext(ctx, `
		SELECT MEMBER_ID, MEMBER_HOST, MEMBER_PORT, MEMBER_STATE, MEMBER_ROLE,
		       COALESCE(MEMBER_VERSION, '')
		FROM performance_schema.replication_group_members
		ORDER BY MEMBER_ROLE DESC, MEMBER_HOST`)
	if err != nil {
		return nil, fmt.Errorf("query group members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mem ClusterMember
		if rows.Scan(&mem.MemberID, &mem.Host, &mem.Port, &mem.State, &mem.Role, &mem.Version) == nil {
			if mem.Role == "PRIMARY" && m.PrimaryHost == "" {
				m.PrimaryHost = mem.Host
				m.PrimaryPort = mem.Port
			}
			m.Members = append(m.Members, mem)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m.MemberCount = len(m.Members)
	if m.MemberCount > 0 {
		m.GRStatus = "ONLINE"
	} else {
		m.GRStatus = "OFFLINE"
	}
	return m, nil
}

// =============================================================================
// uptime
// =============================================================================

func collectUptime(ctx context.Context, db *sql.DB) (*UptimeMetric, error) {
	var uptime int64
	err := db.QueryRowContext(ctx, `
		SELECT VARIABLE_VALUE FROM performance_schema.global_status
		WHERE VARIABLE_NAME = 'Uptime'`).Scan(&uptime)
	if err != nil {
		return nil, fmt.Errorf("scan uptime: %w", err)
	}
	return &UptimeMetric{
		UptimeSeconds: uptime,
		UptimeHuman:   formatDuration(uptime),
	}, nil
}

// =============================================================================
// connections — threads + processlist
// =============================================================================

func collectConnections(ctx context.Context, db *sql.DB, databases []string) (*ConnectionMetric, error) {
	m := &ConnectionMetric{ByDatabase: []DBConnStat{}}

	_ = db.QueryRowContext(ctx, `SELECT @@max_connections`).Scan(&m.MaxConnections)

	const qStatus = `
		SELECT
		    MAX(CASE WHEN VARIABLE_NAME = 'Threads_connected'    THEN CAST(VARIABLE_VALUE AS UNSIGNED) END),
		    MAX(CASE WHEN VARIABLE_NAME = 'Threads_running'      THEN CAST(VARIABLE_VALUE AS UNSIGNED) END),
		    MAX(CASE WHEN VARIABLE_NAME = 'Max_used_connections' THEN CAST(VARIABLE_VALUE AS UNSIGNED) END),
		    MAX(CASE WHEN VARIABLE_NAME = 'Aborted_connects'     THEN CAST(VARIABLE_VALUE AS UNSIGNED) END)
		FROM performance_schema.global_status
		WHERE VARIABLE_NAME IN ('Threads_connected','Threads_running','Max_used_connections','Aborted_connects')`

	if err := db.QueryRowContext(ctx, qStatus).Scan(
		&m.TotalConnections, &m.Running, &m.MaxUsedConnections, &m.AbortedConnects,
	); err != nil {
		return nil, fmt.Errorf("scan connection status: %w", err)
	}

	m.Sleeping = m.TotalConnections - m.Running
	if m.Sleeping < 0 {
		m.Sleeping = 0
	}
	if m.MaxConnections > 0 {
		m.UtilizationPct = math.Round(float64(m.TotalConnections)/float64(m.MaxConnections)*10000) / 100
	}

	_ = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.PROCESSLIST
		WHERE STATE LIKE '%Waiting for%lock%' OR STATE LIKE '%lock wait%'`).Scan(&m.WaitingForLock)

	filter := dbSet(databases)
	rows, err := db.QueryContext(ctx, `
		SELECT
		    s.schema_name,
		    COUNT(p.id),
		    SUM(CASE WHEN p.COMMAND = 'Query' THEN 1 ELSE 0 END),
		    SUM(CASE WHEN p.COMMAND = 'Sleep'  THEN 1 ELSE 0 END)
		FROM information_schema.schemata s
		LEFT JOIN information_schema.PROCESSLIST p ON p.db = s.schema_name
		WHERE s.schema_name NOT IN ('information_schema','mysql','performance_schema','sys')
		GROUP BY s.schema_name
		ORDER BY COUNT(p.id) DESC, s.schema_name`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var s DBConnStat
			if rows.Scan(&s.Database, &s.Total, &s.Running, &s.Sleeping) == nil {
				if inDBSet(filter, s.Database) {
					m.ByDatabase = append(m.ByDatabase, s)
				}
			}
		}
		_ = rows.Err()
	}
	return m, nil
}

// =============================================================================
// replication — GR member stats + applier workers
// =============================================================================

func collectReplication(ctx context.Context, db *sql.DB) (*ReplicationMetric, error) {
	m := &ReplicationMetric{Members: []GRMemberStat{}, Appliers: []ApplierStat{}}

	rows, err := db.QueryContext(ctx, `
		SELECT
		    s.MEMBER_ID,
		    m.MEMBER_HOST,
		    m.MEMBER_PORT,
		    m.MEMBER_ROLE,
		    s.COUNT_TRANSACTIONS_IN_QUEUE,
		    s.COUNT_TRANSACTIONS_CHECKED,
		    s.COUNT_CONFLICTS_DETECTED,
		    COALESCE(s.TRANSACTIONS_COMMITTED_ALL_MEMBERS, '')
		FROM performance_schema.replication_group_member_stats s
		JOIN performance_schema.replication_group_members m USING (MEMBER_ID)
		ORDER BY m.MEMBER_ROLE DESC, m.MEMBER_HOST`)
	if err != nil {
		return nil, fmt.Errorf("query GR member stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s GRMemberStat
		if rows.Scan(
			&s.MemberID, &s.Host, &s.Port, &s.Role,
			&s.TransactionsInQueue, &s.TransactionsChecked, &s.ConflictsDetected,
			&s.TransactionsCommittedAllMembers,
		) == nil {
			m.Members = append(m.Members, s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m.GREnabled = len(m.Members) > 0

	rows2, err := db.QueryContext(ctx, `
		SELECT
		    CHANNEL_NAME,
		    WORKER_ID,
		    SERVICE_STATE,
		    COALESCE(APPLYING_TRANSACTION, ''),
		    LAST_APPLIED_TRANSACTION_ORIGINAL_COMMIT_TIMESTAMP,
		    LAST_APPLIED_TRANSACTION_END_APPLY_TIMESTAMP
		FROM performance_schema.replication_applier_status_by_worker
		WHERE CHANNEL_NAME = 'group_replication_applier'
		ORDER BY WORKER_ID`)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var s ApplierStat
			var commitTs, applyTs sql.NullTime
			if rows2.Scan(
				&s.Channel, &s.WorkerID, &s.State,
				&s.ApplyingTransaction, &commitTs, &applyTs,
			) == nil {
				if applyTs.Valid {
					t := applyTs.Time.UTC()
					s.LastAppliedAt = &t
				}
				// lag = time between original commit and when this node applied it.
				// Clamp to 0 — negative values indicate clock skew between nodes.
				if commitTs.Valid && applyTs.Valid && !commitTs.Time.IsZero() && !applyTs.Time.IsZero() {
					lag := applyTs.Time.Sub(commitTs.Time).Seconds()
					if lag < 0 {
						lag = 0
					}
					s.LagSeconds = math.Round(lag*1000) / 1000
				}
				m.Appliers = append(m.Appliers, s)
			}
		}
		_ = rows2.Err()
	}
	return m, nil
}

// =============================================================================
// performance — InnoDB buffer pool, QPS/TPS, temp tables, sort
// =============================================================================

func collectPerformance(ctx context.Context, db *sql.DB) (*PerformanceMetric, error) {
	m := &PerformanceMetric{}

	_ = db.QueryRowContext(ctx, `SELECT @@innodb_buffer_pool_size`).Scan(&m.BufferPoolSizeBytes)

	const qStatus = `
		SELECT VARIABLE_NAME, CAST(VARIABLE_VALUE AS UNSIGNED)
		FROM performance_schema.global_status
		WHERE VARIABLE_NAME IN (
		    'Innodb_buffer_pool_read_requests',
		    'Innodb_buffer_pool_reads',
		    'Innodb_buffer_pool_pages_free',
		    'Innodb_buffer_pool_pages_dirty',
		    'Created_tmp_disk_tables',
		    'Created_tmp_tables',
		    'Sort_merge_passes',
		    'Queries',
		    'Com_commit',
		    'Com_rollback',
		    'Innodb_row_lock_time_avg',
		    'Innodb_row_lock_waits',
		    'Handler_read_rnd_next',
		    'Handler_read_key',
		    'Uptime'
		)`

	rows, err := db.QueryContext(ctx, qStatus)
	if err != nil {
		return nil, fmt.Errorf("query performance status: %w", err)
	}
	defer rows.Close()

	var bufReadReq, bufReads, uptime int64
	var queries, commits, rollbacks int64

	for rows.Next() {
		var name string
		var val int64
		if rows.Scan(&name, &val) != nil {
			continue
		}
		switch name {
		case "Innodb_buffer_pool_read_requests":
			bufReadReq = val
		case "Innodb_buffer_pool_reads":
			bufReads = val
		case "Innodb_buffer_pool_pages_free":
			m.BufferPoolFreePages = val
		case "Innodb_buffer_pool_pages_dirty":
			m.BufferPoolDirtyPages = val
		case "Created_tmp_disk_tables":
			m.CreatedTmpDiskTables = val
		case "Created_tmp_tables":
			m.CreatedTmpTables = val
		case "Sort_merge_passes":
			m.SortMergePasses = val
		case "Queries":
			queries = val
		case "Com_commit":
			commits = val
		case "Com_rollback":
			rollbacks = val
		case "Innodb_row_lock_time_avg":
			m.InnodbRowLockAvgMs = float64(val)
		case "Innodb_row_lock_waits":
			m.InnodbRowLockWaits = val
		case "Handler_read_rnd_next":
			m.HandlerReadRndNext = val
		case "Handler_read_key":
			m.HandlerReadKey = val
		case "Uptime":
			uptime = val
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if bufReadReq > 0 {
		m.BufferPoolHitPct = math.Round(float64(bufReadReq-bufReads)/float64(bufReadReq)*10000) / 100
	}
	if uptime > 0 {
		m.QPS = math.Round(float64(queries)/float64(uptime)*1000) / 1000
		m.TPS = math.Round(float64(commits+rollbacks)/float64(uptime)*1000) / 1000
	}
	if m.CreatedTmpTables > 0 {
		m.TmpTablesDiskRatioPct = math.Round(float64(m.CreatedTmpDiskTables)/float64(m.CreatedTmpTables)*10000) / 100
	}
	total := m.HandlerReadRndNext + m.HandlerReadKey
	if total > 0 {
		m.IdxLookupRatioPct = math.Round(float64(m.HandlerReadKey)/float64(total)*10000) / 100
	}
	return m, nil
}

// =============================================================================
// query — digest stats, slow queries, lock waits, full-scan digests
// =============================================================================

// psToPicoMs converts performance_schema picosecond timer values to milliseconds.
// performance_schema TIMER_WAIT is in picoseconds: 1 ms = 1,000,000,000 ps.
const psPerMs = 1_000_000_000.0

func collectQuery(ctx context.Context, db *sql.DB, limit int, from, to *time.Time, databases []string) (*QueryMetric, error) {
	m := &QueryMetric{
		SlowQueryThresholdMs: 1000,
		SlowQueries:          []SlowQuery{},
		HighFullScanTables:   []ScanDigest{},
	}

	// Check if events_statements_summary_by_digest has data.
	var stmtCount int64
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM performance_schema.events_statements_summary_by_digest
		WHERE COUNT_STAR > 0`).Scan(&stmtCount); err == nil && stmtCount > 0 {
		m.StatementsAvailable = true

		_ = db.QueryRowContext(ctx, `
			SELECT COALESCE(AVG(AVG_TIMER_WAIT), 0) / ?
			FROM performance_schema.events_statements_summary_by_digest
			WHERE COUNT_STAR > 0`, psPerMs).Scan(&m.AvgExecMs)

		// P95 via window function (MySQL 8.0+).
		_ = db.QueryRowContext(ctx, `
			SELECT COALESCE(MIN(t.v), 0) FROM (
			    SELECT AVG_TIMER_WAIT / ? AS v,
			           NTILE(100) OVER (ORDER BY AVG_TIMER_WAIT) AS bucket
			    FROM performance_schema.events_statements_summary_by_digest
			    WHERE COUNT_STAR > 0
			) t WHERE t.bucket >= 95`, psPerMs).Scan(&m.P95ExecMs)

		_ = db.QueryRowContext(ctx, `
			SELECT COALESCE(MIN(t.v), 0) FROM (
			    SELECT AVG_TIMER_WAIT / ? AS v,
			           NTILE(100) OVER (ORDER BY AVG_TIMER_WAIT) AS bucket
			    FROM performance_schema.events_statements_summary_by_digest
			    WHERE COUNT_STAR > 0
			) t WHERE t.bucket >= 99`, psPerMs).Scan(&m.P99ExecMs)

		m.TopByMeanExecMs, _ = scanQueryStats(ctx, db, `
			SELECT LEFT(COALESCE(DIGEST_TEXT,''), 300), COUNT_STAR,
			    AVG_TIMER_WAIT / ?, MAX_TIMER_WAIT / ?,
			    SUM_TIMER_WAIT / ?, SUM_ROWS_EXAMINED, SUM_ERRORS
			FROM performance_schema.events_statements_summary_by_digest
			WHERE COUNT_STAR > 0
			ORDER BY AVG_TIMER_WAIT DESC LIMIT ?`, psPerMs, psPerMs, psPerMs, limit)

		m.TopByTotalExecMs, _ = scanQueryStats(ctx, db, `
			SELECT LEFT(COALESCE(DIGEST_TEXT,''), 300), COUNT_STAR,
			    AVG_TIMER_WAIT / ?, MAX_TIMER_WAIT / ?,
			    SUM_TIMER_WAIT / ?, SUM_ROWS_EXAMINED, SUM_ERRORS
			FROM performance_schema.events_statements_summary_by_digest
			WHERE COUNT_STAR > 0
			ORDER BY SUM_TIMER_WAIT DESC LIMIT ?`, psPerMs, psPerMs, psPerMs, limit)
	}

	// Slow queries from information_schema.PROCESSLIST (TIME is in seconds).
	// Filter to active Query commands exceeding the threshold.
	// TIME * 1000 converts to ms so we compare directly against SlowQueryThresholdMs.

	const qSlow = `
		SELECT ID, COALESCE(DB,''), USER, HOST, COALESCE(STATE,''),
		    LEFT(COALESCE(INFO,''), 300),
		    TIME * 1000
		FROM information_schema.PROCESSLIST
		WHERE COMMAND = 'Query'
		    AND TIME * 1000 > ?
		    AND ID != CONNECTION_ID()
		    AND USER != 'system user'
		    AND INFO != ''
		    AND (? IS NULL OR FROM_UNIXTIME(UNIX_TIMESTAMP() - TIME) >= ?)
		    AND (? IS NULL OR FROM_UNIXTIME(UNIX_TIMESTAMP() - TIME) <= ?)
		ORDER BY TIME DESC
		LIMIT ?`

	dbFilter := dbSet(databases)
	rows, err := db.QueryContext(ctx, qSlow,
		m.SlowQueryThresholdMs,
		nullTimeStr(from), nullTimeStr(from),
		nullTimeStr(to), nullTimeStr(to),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query slow queries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s SlowQuery
		if rows.Scan(&s.ID, &s.Database, &s.User, &s.Host, &s.State, &s.Query, &s.DurationMs) == nil {
			if !inDBSet(dbFilter, s.Database) {
				continue
			}
			if s.DurationMs > m.LongestRunningMs {
				m.LongestRunningMs = s.DurationMs
			}
			m.SlowQueries = append(m.SlowQueries, s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m.SlowQueryCount = len(m.SlowQueries)

	// Lock wait count.
	_ = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.PROCESSLIST
		WHERE STATE LIKE '%Waiting for%lock%' OR STATE LIKE '%lock wait%' OR STATE = 'Waiting for table metadata lock'`).Scan(&m.LockWaitCount)

	// Deadlock count from InnoDB status variable (available MySQL 8.0+).
	_ = db.QueryRowContext(ctx, `
		SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status
		WHERE VARIABLE_NAME = 'Innodb_deadlocks'`).Scan(&m.DeadlockCount)

	// Digests that required full-table scans.
	rows2, err := db.QueryContext(ctx, `
		SELECT
		    COALESCE(SCHEMA_NAME, ''),
		    LEFT(COALESCE(DIGEST_TEXT, ''), 200),
		    SUM_NO_INDEX_USED + SUM_NO_GOOD_INDEX_USED,
		    COUNT_STAR,
		    ROUND((SUM_NO_INDEX_USED + SUM_NO_GOOD_INDEX_USED) / COUNT_STAR * 100, 2)
		FROM performance_schema.events_statements_summary_by_digest
		WHERE COUNT_STAR > 0
		    AND (SUM_NO_INDEX_USED + SUM_NO_GOOD_INDEX_USED) > 0
		    AND SCHEMA_NAME NOT IN ('information_schema','performance_schema','mysql','sys')
		    AND SCHEMA_NAME IS NOT NULL
		    AND DIGEST_TEXT NOT LIKE 'SHOW%'
		ORDER BY (SUM_NO_INDEX_USED + SUM_NO_GOOD_INDEX_USED) DESC
		LIMIT ?`, limit)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var s ScanDigest
			if rows2.Scan(&s.Schema, &s.Query, &s.FullScans, &s.TotalCalls, &s.FullScanPct) == nil {
				if inDBSet(dbFilter, s.Schema) {
					m.HighFullScanTables = append(m.HighFullScanTables, s)
				}
			}
		}
		_ = rows2.Err()
	}
	return m, nil
}

func scanQueryStats(ctx context.Context, db *sql.DB, q string, args ...any) ([]QueryStat, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueryStat
	for rows.Next() {
		var s QueryStat
		if rows.Scan(&s.Query, &s.Calls, &s.MeanExecMs, &s.MaxExecMs, &s.TotalExecMs, &s.RowsExamined, &s.Errors) == nil {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

// nullTimeStr returns nil when t is nil (for IS NULL checks in MySQL).
func nullTimeStr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format("2006-01-02 15:04:05")
}

// =============================================================================
// maintenance — InnoDB purge lag, fragmentation, metadata locks
// =============================================================================

func collectMaintenance(ctx context.Context, db *sql.DB, limit int, databases []string) (*MaintenanceMetric, error) {
	m := &MaintenanceMetric{
		FragmentedTables: []FragmentedTable{},
	}

	// InnoDB history list length (purge lag).
	_ = db.QueryRowContext(ctx, `
		SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status
		WHERE VARIABLE_NAME = 'Innodb_history_list_length'`).Scan(&m.PurgeLagTransactions)
	switch {
	case m.PurgeLagTransactions > 10_000_000:
		m.PurgeLagRiskLevel = "danger"
	case m.PurgeLagTransactions > 1_000_000:
		m.PurgeLagRiskLevel = "warning"
	default:
		m.PurgeLagRiskLevel = "ok"
	}

	// Fragmented tables (DATA_FREE > 0, non-system schemas).
	dbFilter := dbSet(databases)
	rows, err := db.QueryContext(ctx, `
		SELECT TABLE_SCHEMA, TABLE_NAME,
		    COALESCE(ROW_FORMAT, ''),
		    DATA_LENGTH, DATA_FREE,
		    ROUND(DATA_FREE / (DATA_LENGTH + DATA_FREE) * 100, 2) AS fragment_pct
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA NOT IN ('information_schema','mysql','performance_schema','sys')
		    AND TABLE_TYPE = 'BASE TABLE'
		    AND DATA_FREE > 0
		    AND (DATA_LENGTH + DATA_FREE) > 0
		ORDER BY DATA_FREE DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query fragmented tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t FragmentedTable
		if rows.Scan(&t.Schema, &t.Table, &t.RowFormat, &t.DataLength, &t.DataFree, &t.FragmentPct) == nil {
			if inDBSet(dbFilter, t.Schema) {
				m.FragmentedTables = append(m.FragmentedTables, t)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fragmented tables: %w", err)
	}

	// Open / opened tables.
	const qOpen = `
		SELECT
		    MAX(CASE WHEN VARIABLE_NAME = 'Open_tables'   THEN CAST(VARIABLE_VALUE AS UNSIGNED) END),
		    MAX(CASE WHEN VARIABLE_NAME = 'Opened_tables' THEN CAST(VARIABLE_VALUE AS UNSIGNED) END)
		FROM performance_schema.global_status
		WHERE VARIABLE_NAME IN ('Open_tables','Opened_tables')`
	_ = db.QueryRowContext(ctx, qOpen).Scan(&m.OpenTables, &m.OpenedTables)

	// Metadata locks (requires performance_schema.setup_instruments 'wait/lock/metadata/sql/mdl' enabled).
	const qMDL = `
		SELECT
		    COUNT(*),
		    SUM(CASE WHEN LOCK_STATUS = 'PENDING' THEN 1 ELSE 0 END)
		FROM performance_schema.metadata_locks
		WHERE OBJECT_TYPE NOT IN ('GLOBAL','COMMIT','BACKUP LOCK','ACL CACHE','LOCKING SERVICE')`
	_ = db.QueryRowContext(ctx, qMDL).Scan(&m.TotalMetaLocks, &m.BlockedMetaLocks)

	return m, nil
}
