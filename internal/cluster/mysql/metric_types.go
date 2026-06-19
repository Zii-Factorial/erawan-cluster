package mysql

import "time"

// Metric categories — aligned to the standard database cluster metrics reference.
const (
	// cluster: InnoDB Cluster / Group Replication membership and primary election state.
	MetricCategoryCluster = "cluster"

	// uptime: MySQL server process uptime.
	MetricCategoryUptime = "uptime"

	// connections: Active/sleeping threads, utilisation, per-database breakdown.
	MetricCategoryConnections = "connections"

	// replication: Group Replication member stats and applier worker lag.
	MetricCategoryReplication = "replication"

	// performance: InnoDB buffer-pool health, QPS/TPS, temp-table and sort pressure.
	MetricCategoryPerformance = "performance"

	// query: Digest stats from performance_schema, slow queries, lock waits, full-scan tables.
	MetricCategoryQuery = "query"

	// maintenance: InnoDB purge lag, table fragmentation, metadata locks, open tables.
	MetricCategoryMaintenance = "maintenance"
)

var allMetricCategories = []string{
	MetricCategoryCluster,
	MetricCategoryUptime,
	MetricCategoryConnections,
	MetricCategoryReplication,
	MetricCategoryPerformance,
	MetricCategoryQuery,
	MetricCategoryMaintenance,
}

// ---------------------------------------------------------------------------
// Request / Response envelope
// ---------------------------------------------------------------------------

// MetricRequest defines connection parameters and collection filters.
//
// Time range (From / To, ISO 8601) applies to time-filtered categories:
//   - query: filters slow-query list by query start time
//
// All snapshot categories return the current point-in-time value.
type MetricRequest struct {
	// JobID resolves user and password from the stored deploy job.
	// host is always sourced from the server's PROXY_HOST env (never from the client).
	JobID string `json:"job_id"`

	// Host and Port are server-side only — injected from PROXY_HOST env and the
	// stored deploy job. Clients cannot set these fields; all SQL traffic routes
	// through local HAProxy on the port the cluster was deployed with.
	Host           string `json:"-"`
	Port           int    `json:"-"`               // from job (MySQLPort) or default 3306
	User           string `json:"user"`            // needs PROCESS, SELECT on performance_schema
	Password       string `json:"password"`
	Database       string `json:"database"`        // for table/query stats; default "information_schema"
	SSLMode        string `json:"ssl_mode"`        // "disable" | "require"; default "disable"
	ConnectTimeout int    `json:"connect_timeout"` // seconds; default 10

	// Collection filters
	Categories []string   `json:"categories"` // empty = all
	Databases  []string   `json:"databases"`  // empty = all user databases; non-empty = filter per-db results to these names
	Limit      int        `json:"limit"`      // top-N cap for slow queries, digest lists; default 20
	From       *time.Time `json:"from"`       // ISO 8601 lower bound for slow queries
	To         *time.Time `json:"to"`         // ISO 8601 upper bound for slow queries
}

// MetricResponse is the top-level JSON envelope.
type MetricResponse struct {
	CollectedAt   time.Time         `json:"collected_at"`
	Host          string            `json:"host"`
	Port          int               `json:"port"`
	DatabaseCount int               `json:"database_count"` // total user databases on this server
	Users         []UserInfo        `json:"users"`
	Databases     []DatabaseInfo    `json:"databases"`
	Categories    map[string]any    `json:"categories"`
	Errors        map[string]string `json:"errors,omitempty"`
}

// UserInfo describes one MySQL user account.
type UserInfo struct {
	User     string `json:"user"`
	Host     string `json:"host"`
	HasSuper bool   `json:"has_super"`
}

// DatabaseInfo describes one MySQL database with its size and character set.
type DatabaseInfo struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
}

// ---------------------------------------------------------------------------
// cluster — Group Replication membership
// ---------------------------------------------------------------------------

// ClusterMetric holds InnoDB Cluster / Group Replication membership state.
type ClusterMetric struct {
	GroupName   string          `json:"group_name"`
	GRStatus    string          `json:"gr_status"`              // ONLINE | OFFLINE
	PrimaryHost string          `json:"primary_host,omitempty"`
	PrimaryPort int             `json:"primary_port,omitempty"`
	MemberCount int             `json:"member_count"`
	Members     []ClusterMember `json:"members"`
}

// ClusterMember describes one node in the Group Replication cluster.
type ClusterMember struct {
	MemberID string `json:"member_id"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	State    string `json:"state"` // ONLINE | RECOVERING | OFFLINE | ERROR | UNREACHABLE
	Role     string `json:"role"`  // PRIMARY | SECONDARY
	Version  string `json:"version,omitempty"`
}

// ---------------------------------------------------------------------------
// uptime
// ---------------------------------------------------------------------------

// UptimeMetric shows how long the MySQL server process has been running.
type UptimeMetric struct {
	UptimeSeconds int64  `json:"uptime_seconds"`
	UptimeHuman   string `json:"uptime_human"`
}

// ---------------------------------------------------------------------------
// connections — threads + processlist
// ---------------------------------------------------------------------------

// ConnectionMetric holds thread and connection statistics.
type ConnectionMetric struct {
	MaxConnections     int          `json:"max_connections"`
	TotalConnections   int          `json:"total_connections"`
	UtilizationPct     float64      `json:"utilization_pct"`
	Running            int          `json:"running"`
	Sleeping           int          `json:"sleeping"`
	WaitingForLock     int          `json:"waiting_for_lock"`
	MaxUsedConnections int          `json:"max_used_connections"`
	AbortedConnects    int64        `json:"aborted_connects"`
	ByDatabase         []DBConnStat `json:"by_database"`
}

// DBConnStat is a per-database connection breakdown.
type DBConnStat struct {
	Database string `json:"database"`
	Total    int    `json:"total"`
	Running  int    `json:"running"`
	Sleeping int    `json:"sleeping"`
}

// ---------------------------------------------------------------------------
// replication — Group Replication member stats + applier workers
// ---------------------------------------------------------------------------

// ReplicationMetric consolidates GR member certification stats and applier lag.
type ReplicationMetric struct {
	GREnabled bool            `json:"gr_enabled"`
	Members   []GRMemberStat  `json:"members"`
	Appliers  []ApplierStat   `json:"appliers"`
}

// GRMemberStat describes per-member Group Replication certification stats.
type GRMemberStat struct {
	MemberID                        string `json:"member_id"`
	Host                            string `json:"host"`
	Port                            int    `json:"port"`
	Role                            string `json:"role"`
	TransactionsInQueue             int64  `json:"transactions_in_queue"`
	TransactionsChecked             int64  `json:"transactions_checked"`
	ConflictsDetected               int64  `json:"conflicts_detected"`
	TransactionsCommittedAllMembers string `json:"transactions_committed_all_members,omitempty"`
}

// ApplierStat describes one Group Replication applier worker.
type ApplierStat struct {
	Channel             string     `json:"channel"`
	WorkerID            int        `json:"worker_id"`
	State               string     `json:"state"`
	ApplyingTransaction string     `json:"applying_transaction,omitempty"`
	LastAppliedAt       *time.Time `json:"last_applied_at,omitempty"`
	LagSeconds          float64    `json:"lag_seconds"`
}

// ---------------------------------------------------------------------------
// performance — InnoDB buffer pool, QPS/TPS, temp tables, sort
// ---------------------------------------------------------------------------

// PerformanceMetric merges InnoDB buffer-pool health, query throughput,
// temp-table pressure, sort pressure, and row-lock contention.
type PerformanceMetric struct {
	QPS float64 `json:"qps"`
	TPS float64 `json:"tps"`

	BufferPoolSizeBytes  int64   `json:"buffer_pool_size_bytes"`
	BufferPoolHitPct     float64 `json:"buffer_pool_hit_pct"`
	BufferPoolFreePages  int64   `json:"buffer_pool_free_pages"`
	BufferPoolDirtyPages int64   `json:"buffer_pool_dirty_pages"`

	CreatedTmpDiskTables  int64   `json:"created_tmp_disk_tables"`
	CreatedTmpTables      int64   `json:"created_tmp_tables"`
	TmpTablesDiskRatioPct float64 `json:"tmp_tables_disk_ratio_pct"`

	SortMergePasses    int64   `json:"sort_merge_passes"`
	InnodbRowLockWaits int64   `json:"innodb_row_lock_waits"`
	InnodbRowLockAvgMs float64 `json:"innodb_row_lock_avg_ms"`

	// IdxLookupRatioPct = handler_read_key / (handler_read_key + handler_read_rnd_next) * 100
	// Low value means many full-row scans — tables may be missing indexes.
	HandlerReadRndNext int64   `json:"handler_read_rnd_next"`
	HandlerReadKey     int64   `json:"handler_read_key"`
	IdxLookupRatioPct  float64 `json:"idx_lookup_ratio_pct"`
}

// ---------------------------------------------------------------------------
// query — digest stats, slow queries, locks, full-scan tables
// ---------------------------------------------------------------------------

// QueryMetric consolidates query latency (from events_statements_summary_by_digest),
// active slow queries, lock waits, and full-table-scan digest stats.
type QueryMetric struct {
	StatementsAvailable  bool           `json:"performance_schema_available"`
	AvgExecMs            float64        `json:"avg_exec_ms"`
	P95ExecMs            float64        `json:"p95_exec_ms"`
	P99ExecMs            float64        `json:"p99_exec_ms"`
	TopByMeanExecMs      []QueryStat    `json:"top_by_mean_exec_ms,omitempty"`
	TopByTotalExecMs     []QueryStat    `json:"top_by_total_exec_ms,omitempty"`
	SlowQueryThresholdMs float64        `json:"slow_query_threshold_ms"`
	SlowQueryCount       int            `json:"slow_query_count"`
	LongestRunningMs     float64        `json:"longest_running_ms"`
	SlowQueries          []SlowQuery    `json:"slow_queries"`
	LockWaitCount        int            `json:"lock_wait_count"`
	DeadlockCount        int64          `json:"deadlock_count"`
	HighFullScanTables   []ScanDigest   `json:"high_full_scan_tables"`
}

// QueryStat is one row from events_statements_summary_by_digest.
type QueryStat struct {
	Query       string  `json:"query"`
	Calls       int64   `json:"calls"`
	MeanExecMs  float64 `json:"mean_exec_ms"`
	MaxExecMs   float64 `json:"max_exec_ms"`
	TotalExecMs float64 `json:"total_exec_ms"`
	RowsExamined int64  `json:"rows_examined"`
	Errors      int64   `json:"errors"`
}

// SlowQuery is a currently-running query exceeding the slow threshold.
type SlowQuery struct {
	ID         int64   `json:"id"`
	Database   string  `json:"database"`
	User       string  `json:"user"`
	Host       string  `json:"host"`
	State      string  `json:"state"`
	Query      string  `json:"query"`
	DurationMs float64 `json:"duration_ms"`
}

// ScanDigest is a digest entry that required full-table scans.
type ScanDigest struct {
	Schema       string  `json:"schema"`
	Query        string  `json:"query"`
	FullScans    int64   `json:"full_scans"`   // SUM_NO_INDEX_USED + SUM_NO_GOOD_INDEX_USED
	TotalCalls   int64   `json:"total_calls"`
	FullScanPct  float64 `json:"full_scan_pct"`
}

// ---------------------------------------------------------------------------
// maintenance — InnoDB purge, fragmentation, metadata locks
// ---------------------------------------------------------------------------

// MaintenanceMetric consolidates InnoDB purge lag, table fragmentation,
// metadata lock contention, and open-table counts.
type MaintenanceMetric struct {
	PurgeLagTransactions int64            `json:"purge_lag_transactions"` // InnoDB history list length
	PurgeLagRiskLevel    string           `json:"purge_lag_risk_level"`   // ok | warning | danger
	FragmentedTables     []FragmentedTable `json:"fragmented_tables"`
	OpenTables           int64            `json:"open_tables"`
	OpenedTables         int64            `json:"opened_tables"`
	TotalMetaLocks       int              `json:"total_metadata_locks"`
	BlockedMetaLocks     int              `json:"blocked_metadata_locks"`
}

// FragmentedTable is a table with significant free-space fragmentation.
type FragmentedTable struct {
	Schema      string  `json:"schema"`
	Table       string  `json:"table"`
	RowFormat   string  `json:"row_format"`
	DataLength  int64   `json:"data_length"`
	DataFree    int64   `json:"data_free"`
	FragmentPct float64 `json:"fragment_pct"`
}
