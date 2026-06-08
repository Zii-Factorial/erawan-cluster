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

	// storage: DB sizes, top tables & index sizes, table/index bloat, WAL volume,
	// OS disk usage (via SSH when configured).
	// MySQL: information_schema.tables + OS df.
	MetricCategoryStorage = "storage"

	// query: avg/p95/p99 latency (pg_stat_statements), slow queries, longest-running,
	// lock-wait & deadlock counts, seq-scan vs index-scan ratio, rows returned/fetched.
	// MySQL: performance_schema.events_statements_summary_by_digest.
	MetricCategoryQuery = "query"

	// maintenance: Running autovacuum workers, tables needing vacuum, XID wraparound age,
	// logical replication slot lag, pg_locks granted vs waiting.
	// MySQL: InnoDB purge stats, information_schema.innodb_metrics.
	MetricCategoryMaintenance = "maintenance"

	// system: OS-level metrics via SSH — CPU %, memory, swap, load average,
	// network I/O, NTP clock offset.
	// Requires SSH to be configured (gracefully omitted when not available).
	MetricCategorySystem = "system"
)

var allMetricCategories = []string{
	MetricCategoryCluster,
	MetricCategoryUptime,
	MetricCategoryFailover,
	MetricCategoryConnections,
	MetricCategoryReplication,
	MetricCategoryPerformance,
	MetricCategoryStorage,
	MetricCategoryQuery,
	MetricCategoryMaintenance,
	MetricCategorySystem,
}

// ---------------------------------------------------------------------------
// Request / Response envelope
// ---------------------------------------------------------------------------

// MetricRequest defines connection parameters, optional SSH for OS metrics,
// and collection filters.
//
// Time range (From / To, ISO 8601) applies where data has timestamps:
//   - failover:  filters Patroni history events by occurred_at
//   - query:     filters slow-query list by query_start
//
// All snapshot categories (connections, replication, performance, …) always
// return the current point-in-time value — time range is not applicable.
type MetricRequest struct {
	// PostgreSQL connection
	Host           string `json:"host"`
	Port           int    `json:"port"`            // default 5432
	User           string `json:"user"`            // superuser recommended for full metrics
	Password       string `json:"password"`
	Database       string `json:"database"`        // for table/query queries; default "postgres"
	SSLMode        string `json:"ssl_mode"`        // "disable" | "require"; default "disable"
	ConnectTimeout int    `json:"connect_timeout"` // seconds; default 10

	// Patroni REST
	PatroniPort int `json:"patroni_port"` // default 8008

	// SSH — required only for system / storage-disk categories.
	// User and key path are taken from the service-level SSH config.
	// Only the port needs to be supplied in the request.
	SSHPort int `json:"ssh_port"` // default 22

	// Collection filters
	Categories []string   `json:"categories"` // empty = all
	Limit      int        `json:"limit"`      // top-N for tables, slow queries, latency; default 20
	From       *time.Time `json:"from"`       // ISO 8601; filters failover events and slow queries
	To         *time.Time `json:"to"`         // ISO 8601; upper bound for time-range filters
}

// MetricResponse is the top-level JSON envelope.
type MetricResponse struct {
	CollectedAt time.Time         `json:"collected_at"`
	Host        string            `json:"host"`
	Port        int               `json:"port"`
	From        *time.Time        `json:"from,omitempty"`
	To          *time.Time        `json:"to,omitempty"`
	Categories  map[string]any    `json:"categories"`
	Errors      map[string]string `json:"errors,omitempty"`
}

// ---------------------------------------------------------------------------
// cluster — Patroni HA state
// ---------------------------------------------------------------------------

// ClusterMetric holds Patroni node and cluster state including DCS health.
type ClusterMetric struct {
	Scope          string          `json:"scope"`
	Role           string          `json:"role"`    // primary | replica
	State          string          `json:"state"`   // running | stopped | …
	Timeline       int             `json:"timeline"`
	PatroniVersion string          `json:"patroni_version"`
	ServerVersion  int             `json:"server_version,omitempty"`
	// DCS (etcd) connectivity
	DCSLastSeen  *time.Time `json:"dcs_last_seen,omitempty"`
	// Patroni timing config (from /config)
	TTL          int `json:"ttl,omitempty"`
	LoopWait     int `json:"loop_wait,omitempty"`
	RetryTimeout int `json:"retry_timeout,omitempty"`
	Members      []ClusterMember `json:"members"`
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
	TotalEvents                  int             `json:"total_events"` // within requested range
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
	MaxConnections    int         `json:"max_connections"`
	TotalConnections  int         `json:"total_connections"`
	UtilizationPct    float64     `json:"utilization_pct"`    // total/max*100
	Active            int         `json:"active"`
	Idle              int         `json:"idle"`
	IdleInTransaction int         `json:"idle_in_transaction"`
	WaitingForLock    int         `json:"waiting_for_lock"`
	AvgSessionAgeMs   float64     `json:"avg_session_age_ms"` // avg time connections have been open
	WaitEvents        []WaitEvent `json:"wait_events"`         // breakdown by wait_event_type
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
	Type     string `json:"type"`              // physical | logical
	Database string `json:"database,omitempty"` // logical slots only
	Active   bool   `json:"active"`
	LagBytes *int64 `json:"lag_bytes"` // nil when connected to standby
}

// ---------------------------------------------------------------------------
// performance — throughput, cache, checkpoint, bgwriter
// ---------------------------------------------------------------------------

// PerformanceMetric merges database throughput, buffer cache health,
// checkpoint pressure, and background-writer statistics.
type PerformanceMetric struct {
	// Throughput — cumulative since stats_reset (or server start if never reset).
	StatsResetAt      *time.Time `json:"stats_reset_at"`
	TotalTransactions int64      `json:"total_transactions"`
	TotalCommits      int64      `json:"total_commits"`
	TotalRollbacks    int64      `json:"total_rollbacks"`
	AvgTPS            float64    `json:"avg_tps"` // transactions / seconds_since_reset

	// Buffer cache — target ≥ 99 %.
	CacheHitRatioPct float64 `json:"cache_hit_ratio_pct"`

	// Temp space — high values indicate sort/hash spills to disk.
	TempFiles int64 `json:"temp_files"`
	TempBytes int64 `json:"temp_bytes"`

	// Checkpoint health.
	// checkpoints_req >> checkpoints_timed signals checkpoint pressure.
	CheckpointsTimed   int64   `json:"checkpoints_timed"`
	CheckpointsReq     int64   `json:"checkpoints_req"`
	CheckpointRatioPct float64 `json:"checkpoint_ratio_pct"` // timed/(timed+req)*100; target >90%
	CheckpointWriteMs  float64 `json:"checkpoint_write_time_ms"`
	CheckpointSyncMs   float64 `json:"checkpoint_sync_time_ms"`

	// Background writer.
	// buffers_backend high = bgwriter can't keep up with demand.
	BuffersCheckpoint int64 `json:"buffers_checkpoint"`
	BuffersClean      int64 `json:"buffers_clean"`
	BuffersBackend    int64 `json:"buffers_backend"`
	BuffersAlloc      int64 `json:"buffers_alloc"`
}

// ---------------------------------------------------------------------------
// storage — sizes, WAL, bloat, disk
// ---------------------------------------------------------------------------

// StorageMetric consolidates database sizes, top tables, WAL info, bloat summary,
// and optional OS-level disk usage (requires SSH).
type StorageMetric struct {
	// Cluster-wide totals
	TotalClusterBytes int64        `json:"total_cluster_bytes"`
	Databases         []DBSizeStat `json:"databases"`

	// Top tables in the connected database (by total_size including indexes)
	TopTables []TableStat `json:"top_tables"`

	// WAL
	CurrentWALLSN   string `json:"current_wal_lsn"`
	WALSegmentBytes int64  `json:"wal_segment_bytes"`
	WALWrittenBytes int64 `json:"wal_written_bytes,omitempty"` // PG 14+ — total WAL bytes generated
	WALSyncCount    int64 `json:"wal_sync_count,omitempty"`    // PG 14+ — number of WAL file syncs

	// Bloat summary (all user tables in connected database)
	DeadTupleRatioPct   float64 `json:"dead_tuple_ratio_pct"`
	TablesNeedingVacuum int     `json:"tables_needing_vacuum"` // dead ratio > 20 %

	// OS disk — nil when SSH is not configured or unreachable
	Disk *DiskStat `json:"disk,omitempty"`
}

// DBSizeStat is a per-database size entry.
type DBSizeStat struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

// TableStat holds per-table size and autovacuum timestamps.
type TableStat struct {
	Schema          string     `json:"schema"`
	Table           string     `json:"table"`
	SizeBytes       int64      `json:"size_bytes"`
	IndexSizeBytes  int64      `json:"index_size_bytes"`
	TotalSizeBytes  int64      `json:"total_size_bytes"`
	LiveTuples      int64      `json:"live_tuples"`
	DeadTuples      int64      `json:"dead_tuples"`
	LastAutovacuum  *time.Time `json:"last_autovacuum"`
	LastAutoanalyze *time.Time `json:"last_autoanalyze"`
}

// DiskStat holds OS-level disk usage for the PostgreSQL data directory.
type DiskStat struct {
	Path       string  `json:"path"`
	TotalBytes int64   `json:"total_bytes"`
	UsedBytes  int64   `json:"used_bytes"`
	FreeBytes  int64   `json:"free_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

// ---------------------------------------------------------------------------
// query — latency, slow queries, locks, scan efficiency, row throughput
// ---------------------------------------------------------------------------

// QueryMetric consolidates query latency (percentiles from pg_stat_statements),
// active slow queries, lock/deadlock counts, scan efficiency, and row throughput.
type QueryMetric struct {
	// Latency percentiles (requires pg_stat_statements extension).
	StatementsAvailable bool        `json:"pg_stat_statements_available"`
	AvgExecMs           float64     `json:"avg_exec_ms"`
	P95ExecMs           float64     `json:"p95_exec_ms"`
	P99ExecMs           float64     `json:"p99_exec_ms"`
	TopByMeanExecMs     []QueryStat `json:"top_by_mean_exec_ms,omitempty"`  // slowest mean
	TopByTotalExecMs    []QueryStat `json:"top_by_total_exec_ms,omitempty"` // most cumulative time

	// Currently running slow queries (time-range filter applies to query_start).
	SlowQueryThresholdMs float64    `json:"slow_query_threshold_ms"`
	SlowQueryCount       int        `json:"slow_query_count"`
	LongestRunningMs     float64    `json:"longest_running_ms"`
	SlowQueries          []SlowQuery `json:"slow_queries"`

	// Lock / deadlock counts.
	LockWaitCount int   `json:"lock_wait_count"` // current backends waiting on locks
	DeadlockCount int64 `json:"deadlock_count"`  // cumulative since stats reset

	// Row throughput (cumulative from pg_stat_database since stats reset).
	RowsReturned int64 `json:"rows_returned"`
	RowsFetched  int64 `json:"rows_fetched"`

	// Scan efficiency — tables with high sequential-scan counts.
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
	IdxScanRatioPct float64 `json:"idx_scan_ratio_pct"` // higher = better index utilisation
}

// ---------------------------------------------------------------------------
// maintenance — autovacuum, XID wraparound, logical slots, lock grants
// ---------------------------------------------------------------------------

// MaintenanceMetric consolidates autovacuum activity, XID wraparound risk,
// logical replication slot lag, and pg_locks grant/wait counts.
type MaintenanceMetric struct {
	// Autovacuum workers currently running.
	AutovacuumRunning int            `json:"autovacuum_running"`
	Workers           []VacuumWorker `json:"workers"`

	// Tables with stale vacuum/analyze (top-N by dead tuple count).
	TablesNeedingVacuum int          `json:"tables_needing_vacuum"` // dead ratio > 20 %
	StaleTables         []StaleTable `json:"stale_tables"`

	// XID wraparound risk (worst-case database in the cluster).
	OldestXIDAge int64  `json:"oldest_xid_age"`
	XIDRiskLevel string `json:"xid_risk_level"` // ok | warning | danger

	// Logical replication slot lag — unmanaged slots can cause WAL accumulation.
	LogicalSlotLag []LogicalSlotStat `json:"logical_slot_lag"`

	// pg_locks snapshot.
	TotalLocks   int `json:"total_locks"`
	GrantedLocks int `json:"granted_locks"`
	WaitingLocks int `json:"waiting_locks"`
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

// ---------------------------------------------------------------------------
// system — OS-level metrics (via SSH)
// ---------------------------------------------------------------------------

// SystemMetric holds OS-level host metrics collected via SSH.
// When SSH is not configured the struct is returned with SSHAvailable=false
// and all numeric fields at zero — no error is raised.
type SystemMetric struct {
	SSHAvailable bool `json:"ssh_available"`

	// CPU — percentage non-idle (sampled over ~100 ms via /proc/stat).
	CPUUsagePct float64 `json:"cpu_usage_pct"`

	// Memory (/proc/meminfo), bytes.
	MemoryTotalBytes int64   `json:"memory_total_bytes"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes"`
	MemoryFreePct    float64 `json:"memory_free_pct"`

	// Swap (/proc/meminfo), bytes.
	SwapTotalBytes int64 `json:"swap_total_bytes"`
	SwapUsedBytes  int64 `json:"swap_used_bytes"`

	// Load average (/proc/loadavg).
	LoadAvg1m  float64 `json:"load_avg_1m"`
	LoadAvg5m  float64 `json:"load_avg_5m"`
	LoadAvg15m float64 `json:"load_avg_15m"`

	// Network I/O — cumulative bytes since boot (/proc/net/dev, primary interface).
	NetInterface string `json:"net_interface,omitempty"`
	NetRxBytes   int64  `json:"net_rx_bytes"`
	NetTxBytes   int64  `json:"net_tx_bytes"`

	// NTP clock offset in milliseconds (chronyc or ntpq); nil if unavailable.
	NTPOffsetMs *float64 `json:"ntp_offset_ms"`
}
