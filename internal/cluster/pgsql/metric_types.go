package pgsql

import "time"

// Metric categories — aligned to the standard database cluster metrics reference.
// Each maps to an equivalent concept in MySQL (noted in comments).
const (
	// cluster: Patroni HA state, DCS health, node roles, TTL/loop_wait.
	// MySQL: Group Replication performance_schema.replication_group_members.
	MetricCategoryCluster = "cluster"

	// uptime: PostgreSQL process uptime (pg_postmaster_start_time).
	// MySQL: SHOW STATUS LIKE 'Uptime'.
	MetricCategoryUptime = "uptime"

	// failover: Patroni timeline/failover history with time-range filtering.
	// MySQL: Group Replication view change events.
	MetricCategoryFailover = "failover"

	// connections: Active, idle, idle-in-transaction, lock-waiters, wait-event breakdown.
	// MySQL: SHOW PROCESSLIST + performance_schema.events_waits_current.
	MetricCategoryConnections = "connections"

	// replication: Streaming replication lag (LSN pipeline), standby count, WAL config,
	// replication slots lag.
	// MySQL: performance_schema.replication_applier_status_by_worker.
	MetricCategoryReplication = "replication"

	// performance: TPS (avg since stats reset), cache hit ratio, temp files/bytes,
	// checkpoint pressure, bgwriter stats.
	// MySQL: SHOW STATUS handler/InnoDB/binlog metrics.
	MetricCategoryPerformance = "performance"

	// query: avg/p95/p99 latency (pg_stat_statements), slow queries, longest-running,
	// lock-wait & deadlock counts, seq-scan vs index-scan ratio, rows returned/fetched.
	// MySQL: performance_schema.events_statements_summary_by_digest.
	MetricCategoryQuery = "query"

	// maintenance: Running autovacuum workers, tables needing vacuum, XID wraparound age,
	// logical replication slot lag, pg_locks granted vs waiting.
	// MySQL: InnoDB purge stats, information_schema.innodb_metrics.
	MetricCategoryMaintenance = "maintenance"
)

var allMetricCategories = []string{
	MetricCategoryCluster,
	MetricCategoryUptime,
	MetricCategoryFailover,
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
// Time range (From / To, ISO 8601) applies where data has timestamps:
//   - failover:  filters Patroni history events by occurred_at
//   - query:     filters slow-query list by query_start
//
// All snapshot categories (connections, replication, performance, …) always
// return the current point-in-time value — time range is not applicable.
type MetricRequest struct {
	// JobID resolves host, port, user, password, and node_ips from the stored deploy job.
	// When set, explicit host/port/user/password/node_ips fields are ignored.
	JobID string `json:"job_id"`

	// PostgreSQL connection — point this at HAProxy when a proxy is in use.
	Host           string `json:"host"`
	Port           int    `json:"port"`            // default 5432
	User           string `json:"user"`            // superuser recommended for full metrics
	Password       string `json:"password"`
	Database       string `json:"database"`        // for table/query queries; default "postgres"
	SSLMode        string `json:"ssl_mode"`        // "disable" | "require"; default "disable"
	ConnectTimeout int    `json:"connect_timeout"` // seconds; default 10

	// NodeIPs is the list of all cluster member IPs/hostnames.
	// Used to auto-discover the Patroni leader for REST calls (cluster, failover categories).
	// The collector calls GET /leader on each IP and uses the first that responds 200.
	// When omitted, cluster and failover categories are skipped.
	NodeIPs []string `json:"node_ips"`

	// Patroni REST
	PatroniPort int `json:"patroni_port"` // default 8008

	// Collection filters
	Categories []string   `json:"categories"` // empty = all
	Databases  []string   `json:"databases"`  // empty = all user databases; non-empty = filter per-db results to these names
	Limit      int        `json:"limit"`      // top-N for slow queries, latency; default 20
	From       *time.Time `json:"from"`       // ISO 8601; filters failover events and slow queries
	To         *time.Time `json:"to"`         // ISO 8601; upper bound for time-range filters
}

// MetricResponse is the top-level JSON envelope.
type MetricResponse struct {
	CollectedAt   time.Time         `json:"collected_at"`
	Host          string            `json:"host"`
	Port          int               `json:"port"`
	DatabaseCount int               `json:"database_count"` // total user databases on this server
	Users         []UserInfo        `json:"users"`
	Databases     []DatabaseInfo    `json:"databases"`
	From          *time.Time        `json:"from,omitempty"`
	To            *time.Time        `json:"to,omitempty"`
	Categories    map[string]any    `json:"categories"`
	Errors        map[string]string `json:"errors,omitempty"`
}

// UserInfo describes one PostgreSQL role that can log in.
type UserInfo struct {
	Name        string `json:"name"`
	IsSuperuser bool   `json:"is_superuser"`
	CanCreateDB bool   `json:"can_create_db"`
	CanLogin    bool   `json:"can_login"`
}

// DatabaseInfo describes one PostgreSQL database with its owner, size, and encoding.
type DatabaseInfo struct {
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	SizeBytes int64  `json:"size_bytes"`
	Encoding  string `json:"encoding"`
}

// ---------------------------------------------------------------------------
// cluster — Patroni HA state
// ---------------------------------------------------------------------------

// ClusterMetric holds Patroni node and cluster state including DCS health.
type ClusterMetric struct {
	Scope          string        `json:"scope"`
	Role           string        `json:"role"`    // primary | replica
	State          string        `json:"state"`   // running | stopped | …
	Timeline       int           `json:"timeline"`
	PatroniVersion string        `json:"patroni_version"`
	ServerVersion  int           `json:"server_version,omitempty"`
	DCSLastSeen    *time.Time    `json:"dcs_last_seen,omitempty"`
	TTL            int           `json:"ttl,omitempty"`
	LoopWait       int           `json:"loop_wait,omitempty"`
	RetryTimeout   int           `json:"retry_timeout,omitempty"`
	Members        []ClusterMember `json:"members"`
}

// ClusterMember describes one node in the Patroni cluster.
type ClusterMember struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Role     string `json:"role"`
	State    string `json:"state"`
	Timeline int    `json:"timeline,omitempty"`
	Lag      any    `json:"lag"`
}

// ---------------------------------------------------------------------------
// uptime
// ---------------------------------------------------------------------------

// UptimeMetric shows how long the PostgreSQL process has been running.
type UptimeMetric struct {
	StartTime     time.Time `json:"start_time"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	UptimeHuman   string    `json:"uptime_human"` // e.g. "3d 14h 22m 5s"
}

// ---------------------------------------------------------------------------
// failover — Patroni timeline history
// ---------------------------------------------------------------------------

// FailoverMetric holds Patroni failover history with optional time-range filter.
type FailoverMetric struct {
	CurrentTimeline              int             `json:"current_timeline"`
	TotalEvents                  int             `json:"total_events"`
	TimeSinceLastFailoverSeconds *int64          `json:"time_since_last_failover_seconds,omitempty"`
	Events                       []FailoverEvent `json:"events"`
}

// FailoverEvent is one entry from Patroni /history.
type FailoverEvent struct {
	Timeline   int       `json:"timeline"`
	LSN        string    `json:"lsn"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
}

// ---------------------------------------------------------------------------
// connections — pg_stat_activity
// ---------------------------------------------------------------------------

// ConnectionMetric holds connection-pool statistics and wait-event breakdown.
type ConnectionMetric struct {
	MaxConnections    int          `json:"max_connections"`
	TotalConnections  int          `json:"total_connections"`
	UtilizationPct    float64      `json:"utilization_pct"`
	Active            int          `json:"active"`
	Idle              int          `json:"idle"`
	IdleInTransaction int          `json:"idle_in_transaction"`
	WaitingForLock    int          `json:"waiting_for_lock"`
	AvgSessionAgeMs   float64      `json:"avg_session_age_ms"`
	WaitEvents        []WaitEvent  `json:"wait_events"`
	ByDatabase        []DBConnStat `json:"by_database"`
}

// WaitEvent summarises backends grouped by wait_event_type.
type WaitEvent struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// DBConnStat is a per-database connection breakdown.
type DBConnStat struct {
	Database string `json:"database"`
	Total    int    `json:"total"`
	Active   int    `json:"active"`
	Idle     int    `json:"idle"`
}

// ---------------------------------------------------------------------------
// replication — pg_stat_replication + pg_replication_slots + pg_settings
// ---------------------------------------------------------------------------

// ReplicationMetric combines streaming replication state, configuration, and slot lag.
type ReplicationMetric struct {
	WALLevel      string              `json:"wal_level"`
	MaxWALSenders int                 `json:"max_wal_senders"`
	StandbyCount  int                 `json:"standby_count"`
	Members       []ReplicationMember `json:"members"`
	Slots         []ReplicationSlot   `json:"slots"`
}

// ReplicationMember describes one standby in pg_stat_replication.
type ReplicationMember struct {
	ClientAddr       string   `json:"client_addr"`
	ApplicationName  string   `json:"application_name"`
	State            string   `json:"state"`
	SyncState        string   `json:"sync_state"` // sync | async | potential | quorum
	SentLSN          string   `json:"sent_lsn"`
	WriteLSN         string   `json:"write_lsn"`
	FlushLSN         string   `json:"flush_lsn"`
	ReplayLSN        string   `json:"replay_lsn"`
	WriteLagSeconds  *float64 `json:"write_lag_seconds"`
	FlushLagSeconds  *float64 `json:"flush_lag_seconds"`
	ReplayLagSeconds *float64 `json:"replay_lag_seconds"`
}

// ReplicationSlot describes one pg_replication_slot entry.
type ReplicationSlot struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Database string `json:"database,omitempty"` // logical slots only
	Active   bool   `json:"active"`
	LagBytes *int64 `json:"lag_bytes"`
}

// ---------------------------------------------------------------------------
// performance — throughput, cache, checkpoint, bgwriter
// ---------------------------------------------------------------------------

// PerformanceMetric merges database throughput, buffer cache health,
// checkpoint pressure, and background-writer statistics.
type PerformanceMetric struct {
	StatsResetAt      *time.Time `json:"stats_reset_at"`
	TotalTransactions int64      `json:"total_transactions"`
	TotalCommits      int64      `json:"total_commits"`
	TotalRollbacks    int64      `json:"total_rollbacks"`
	AvgTPS            float64    `json:"avg_tps"`

	CacheHitRatioPct float64 `json:"cache_hit_ratio_pct"`

	TempFiles int64 `json:"temp_files"`
	TempBytes int64 `json:"temp_bytes"`

	CheckpointsTimed   int64   `json:"checkpoints_timed"`
	CheckpointsReq     int64   `json:"checkpoints_req"`
	CheckpointRatioPct float64 `json:"checkpoint_ratio_pct"`
	CheckpointWriteMs  float64 `json:"checkpoint_write_time_ms"`
	CheckpointSyncMs   float64 `json:"checkpoint_sync_time_ms"`

	BuffersCheckpoint int64 `json:"buffers_checkpoint"`
	BuffersClean      int64 `json:"buffers_clean"`
	BuffersBackend    int64 `json:"buffers_backend"`
	BuffersAlloc      int64 `json:"buffers_alloc"`
}

// ---------------------------------------------------------------------------
// query — latency, slow queries, locks, scan efficiency, row throughput
// ---------------------------------------------------------------------------

// QueryMetric consolidates query latency (percentiles from pg_stat_statements),
// active slow queries, lock/deadlock counts, scan efficiency, and row throughput.
type QueryMetric struct {
	StatementsAvailable bool        `json:"pg_stat_statements_available"`
	AvgExecMs           float64     `json:"avg_exec_ms"`
	P95ExecMs           float64     `json:"p95_exec_ms"`
	P99ExecMs           float64     `json:"p99_exec_ms"`
	TopByMeanExecMs     []QueryStat `json:"top_by_mean_exec_ms,omitempty"`
	TopByTotalExecMs    []QueryStat `json:"top_by_total_exec_ms,omitempty"`

	SlowQueryThresholdMs float64     `json:"slow_query_threshold_ms"`
	SlowQueryCount       int         `json:"slow_query_count"`
	LongestRunningMs     float64     `json:"longest_running_ms"`
	SlowQueries          []SlowQuery `json:"slow_queries"`

	LockWaitCount int   `json:"lock_wait_count"`
	DeadlockCount int64 `json:"deadlock_count"`

	RowsReturned int64 `json:"rows_returned"`
	RowsFetched  int64 `json:"rows_fetched"`

	SeqScansTotal     int64            `json:"seq_scans_total"`
	IdxScansTotal     int64            `json:"idx_scans_total"`
	IdxScanRatioPct   float64          `json:"idx_scan_ratio_pct"`
	HighSeqScanTables []ScanEfficiency `json:"high_seq_scan_tables"`
}

// QueryStat is one row from pg_stat_statements.
type QueryStat struct {
	Query        string  `json:"query"`
	Calls        int64   `json:"calls"`
	MeanExecMs   float64 `json:"mean_exec_ms"`
	MaxExecMs    float64 `json:"max_exec_ms"`
	MinExecMs    float64 `json:"min_exec_ms"`
	StddevExecMs float64 `json:"stddev_exec_ms"`
	TotalExecMs  float64 `json:"total_exec_ms"`
	Rows         int64   `json:"rows"`
}

// SlowQuery is a currently-running query from pg_stat_activity.
type SlowQuery struct {
	PID        int       `json:"pid"`
	Database   string    `json:"database"`
	User       string    `json:"user"`
	State      string    `json:"state"`
	Query      string    `json:"query"`
	StartedAt  time.Time `json:"started_at"`
	DurationMs float64   `json:"duration_ms"`
}

// ScanEfficiency compares sequential vs index scans for one table.
type ScanEfficiency struct {
	Schema          string  `json:"schema"`
	Table           string  `json:"table"`
	SeqScans        int64   `json:"seq_scans"`
	IdxScans        int64   `json:"idx_scans"`
	IdxScanRatioPct float64 `json:"idx_scan_ratio_pct"`
}

// ---------------------------------------------------------------------------
// maintenance — autovacuum, XID wraparound, logical slots, lock grants
// ---------------------------------------------------------------------------

// MaintenanceMetric consolidates autovacuum activity, XID wraparound risk,
// logical replication slot lag, and pg_locks grant/wait counts.
type MaintenanceMetric struct {
	AutovacuumRunning   int               `json:"autovacuum_running"`
	Workers             []VacuumWorker    `json:"workers"`
	TablesNeedingVacuum int               `json:"tables_needing_vacuum"`
	StaleTables         []StaleTable      `json:"stale_tables"`
	OldestXIDAge        int64             `json:"oldest_xid_age"`
	XIDRiskLevel        string            `json:"xid_risk_level"` // ok | warning | danger
	LogicalSlotLag      []LogicalSlotStat `json:"logical_slot_lag"`
	TotalLocks          int               `json:"total_locks"`
	GrantedLocks        int               `json:"granted_locks"`
	WaitingLocks        int               `json:"waiting_locks"`
}

// VacuumWorker describes one running autovacuum/vacuum backend.
type VacuumWorker struct {
	PID      int    `json:"pid"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Phase    string `json:"phase"`
	Duration string `json:"duration"`
}

// StaleTable identifies a table with a high dead-tuple count or stale vacuum.
type StaleTable struct {
	Schema          string     `json:"schema"`
	Table           string     `json:"table"`
	LastAutovacuum  *time.Time `json:"last_autovacuum"`
	LastAutoanalyze *time.Time `json:"last_autoanalyze"`
	DeadTuples      int64      `json:"dead_tuples"`
}

// LogicalSlotStat describes one logical replication slot and its WAL lag.
type LogicalSlotStat struct {
	Name     string `json:"name"`
	Database string `json:"database"`
	Active   bool   `json:"active"`
	LagBytes int64  `json:"lag_bytes"`
}
