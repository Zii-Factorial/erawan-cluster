package core

import "time"

// ---------------------------------------------------------------------------
// Shared metric response types — used by every database engine adapter.
// Field names and JSON keys must remain identical across engines so clients
// can parse responses uniformly regardless of which engine produced them.
// Engine-specific fields use omitempty and are absent when not applicable.
// ---------------------------------------------------------------------------

// MetricResponse is the top-level JSON envelope returned by every engine.
type MetricResponse struct {
	CollectedAt time.Time         `json:"collected_at"`
	Engine      string            `json:"engine"`              // "pgsql" | "mysql"
	Host        string            `json:"host"`
	Port        int               `json:"port"`
	Databases   []DatabaseInfo    `json:"databases,omitempty"` // user databases with sizes
	Users       []UserInfo        `json:"users"`               // non-system DB users (always [])
	Nodes       []NodeMetric      `json:"nodes"`               // per-node OS metrics (node_exporter)
	Categories  map[string]any    `json:"categories"`
	Errors      map[string]string `json:"errors,omitempty"`
}

// DatabaseInfo describes one user database.
type DatabaseInfo struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

// UserInfo describes one non-system database user/role.
type UserInfo struct {
	Username  string   `json:"username"`
	Host      string   `json:"host,omitempty"`      // MySQL: connection host constraint (e.g. "%")
	SuperUser bool     `json:"superuser,omitempty"`
	Databases []string `json:"databases,omitempty"` // databases this user can access
}

// ---------------------------------------------------------------------------
// system — node_exporter OS metrics (identical across all engines)
// ---------------------------------------------------------------------------

// NodeMetric holds OS-level resource metrics for one cluster node.
type NodeMetric struct {
	Host              string        `json:"host"`
	UptimeSeconds     int64         `json:"uptime_seconds"`
	CPUUsagePct       float64       `json:"cpu_usage_pct"` // 100 - idle%
	MemTotalBytes     int64         `json:"memory_total_bytes"`
	MemAvailableBytes int64         `json:"memory_available_bytes"`
	MemUsedPct        float64       `json:"memory_used_pct"`
	Load1             float64       `json:"load1"`
	Load5             float64       `json:"load5"`
	Load15            float64       `json:"load15"`
	Disks             []DiskStat    `json:"disks"`
	NetworkInterfaces []NetworkStat `json:"network_interfaces"`
}

// DiskStat describes one mounted filesystem.
type DiskStat struct {
	Mountpoint string  `json:"mountpoint"`
	SizeBytes  int64   `json:"size_bytes"`
	UsedBytes  int64   `json:"used_bytes"`
	AvailBytes int64   `json:"avail_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

// NetworkStat describes one network interface's cumulative I/O.
type NetworkStat struct {
	Interface    string `json:"interface"`
	RxBytesTotal int64  `json:"rx_bytes_total"`
	TxBytesTotal int64  `json:"tx_bytes_total"`
}

// ---------------------------------------------------------------------------
// uptime — how long the DB server process has been running
// ---------------------------------------------------------------------------

// UptimeMetric shows server process uptime.
// pgsql: derived from pg_postmaster_start_time_seconds.
// mysql: derived from mysql_global_status_uptime.
type UptimeMetric struct {
	StartTime     time.Time `json:"start_time"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	UptimeHuman   string    `json:"uptime_human"` // e.g. "3d 14h 22m 5s"
}

// ---------------------------------------------------------------------------
// connections — active threads / sessions
// ---------------------------------------------------------------------------

// ConnectionMetric holds connection-pool statistics.
// pgsql: sourced from pg_stat_activity_count + pg_settings_max_connections.
// mysql: sourced from mysql_global_status_threads_* + mysql_global_variables_max_connections.
type ConnectionMetric struct {
	MaxConnections     int     `json:"max_connections"`
	TotalConnections   int     `json:"total_connections"`
	UtilizationPct     float64 `json:"utilization_pct"`
	// Active: pgsql = state='active'; mysql = threads_running.
	Active int `json:"active"`
	// Idle: pgsql = state='idle'; mysql = threads_connected - threads_running.
	Idle              int          `json:"idle"`
	IdleInTransaction int          `json:"idle_in_transaction,omitempty"` // pgsql only
	WaitingForLock    int          `json:"waiting_for_lock"`
	MaxUsedConnections int         `json:"max_used_connections,omitempty"` // mysql
	AbortedConnects    int64       `json:"aborted_connects,omitempty"`     // mysql
	WaitEvents         []WaitEvent `json:"wait_events,omitempty"`          // pgsql
	ByDatabase         []DBConnStat `json:"by_database,omitempty"`         // pgsql
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
// replication — streaming replication state and slot lag
// ---------------------------------------------------------------------------

// ReplicationMetric combines replication topology and slot lag.
// pgsql: primary scrapes pg_stat_replication_* for all standbys.
// mysql: each node is scraped for its replica_status to get lag.
type ReplicationMetric struct {
	StandbyCount int                 `json:"standby_count"`
	Members      []ReplicationMember `json:"members"` // primary first, then standbys
	Slots        []ReplicationSlot   `json:"slots,omitempty"` // pgsql logical/physical slots
	MaxWALSenders int                `json:"max_wal_senders,omitempty"` // pgsql
}

// ReplicationMember describes one node in the replication topology.
type ReplicationMember struct {
	// Role: "primary" | "standby" (pgsql) | "secondary" (mysql GR)
	Role            string `json:"role"`
	// Host: pgsql = client_addr from pg_stat_replication; mysql = node IP.
	Host            string `json:"host"`
	ApplicationName string `json:"application_name,omitempty"`
	State           string `json:"state,omitempty"`
	SyncState       string `json:"sync_state,omitempty"` // pgsql: async | sync | quorum | potential

	// Lag (nil means not applicable or not measurable, e.g. primary self).
	WriteLagSeconds  *float64 `json:"write_lag_seconds,omitempty"`
	FlushLagSeconds  *float64 `json:"flush_lag_seconds,omitempty"` // pgsql pipeline only
	ReplayLagSeconds *float64 `json:"replay_lag_seconds,omitempty"`
	ReplayLagBytes   *int64   `json:"replay_lag_bytes,omitempty"`

	// MySQL replica applier thread status.
	IORunning  string `json:"io_running,omitempty"`
	SQLRunning string `json:"sql_running,omitempty"`
}

// ReplicationSlot describes one pg_replication_slot entry. pgsql only.
type ReplicationSlot struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Database string `json:"database,omitempty"`
	Active   bool   `json:"active"`
	LagBytes *int64 `json:"lag_bytes"`
}

// ---------------------------------------------------------------------------
// performance — throughput, cache, bgwriter, InnoDB buffer pool
// ---------------------------------------------------------------------------

// PerformanceMetric merges throughput, cache health, I/O pressure, and write stats.
// Fields not applicable to an engine are omitted (omitempty).
type PerformanceMetric struct {
	// Throughput (both engines).
	TPS float64 `json:"tps,omitempty"`
	QPS float64 `json:"qps,omitempty"` // mysql only

	// Cache — shared concept with engine-specific backing store.
	// pgsql: shared_buffers hit ratio from pg_stat_database_blks_*
	// mysql: InnoDB buffer pool hit ratio from innodb_buffer_pool_read_requests.
	CacheHitRatioPct float64 `json:"cache_hit_ratio_pct"`
	// CacheSizeBytes: pgsql = shared_buffers × 8192; mysql = innodb_buffer_pool_size.
	CacheSizeBytes  int64 `json:"cache_size_bytes,omitempty"`
	CacheFreePages  int64 `json:"cache_free_pages,omitempty"`  // mysql (InnoDB pages)
	CacheDirtyPages int64 `json:"cache_dirty_pages,omitempty"` // mysql (InnoDB pages)

	// pgsql transaction totals (cumulative since pg start).
	TotalTransactions int64 `json:"total_transactions,omitempty"`
	TotalCommits      int64 `json:"total_commits,omitempty"`
	TotalRollbacks    int64 `json:"total_rollbacks,omitempty"`

	// Row DML (both engines, cumulative since start).
	// pgsql: pg_stat_database_tup_inserted/updated/deleted.
	// mysql: mysql_global_status_com_insert/update/delete.
	RowsInserted int64 `json:"rows_inserted,omitempty"`
	RowsUpdated  int64 `json:"rows_updated,omitempty"`
	RowsDeleted  int64 `json:"rows_deleted,omitempty"`

	// I/O timing (pgsql: pg_stat_database_blk_read/write_time; track_io_timing=on required).
	BlkReadTimeMs  float64 `json:"blk_read_time_ms,omitempty"`
	BlkWriteTimeMs float64 `json:"blk_write_time_ms,omitempty"`

	// Temp pressure (pgsql: pg_stat_database_temp_files/bytes).
	TempFiles int64 `json:"temp_files,omitempty"`
	TempBytes int64 `json:"temp_bytes,omitempty"`

	// Checkpoint pressure (pgsql: pg_stat_bgwriter / pg_stat_checkpointer PG16+).
	CheckpointsTimed   int64   `json:"checkpoints_timed,omitempty"`
	CheckpointsReq     int64   `json:"checkpoints_req,omitempty"`
	CheckpointRatioPct float64 `json:"checkpoint_ratio_pct,omitempty"`
	BuffersCheckpoint  int64   `json:"buffers_checkpoint,omitempty"`
	BuffersClean       int64   `json:"buffers_clean,omitempty"`
	BuffersBackend     int64   `json:"buffers_backend,omitempty"`
	BuffersAlloc       int64   `json:"buffers_alloc,omitempty"`

	// MySQL InnoDB temp tables / sort pressure.
	TmpDiskTables    int64   `json:"tmp_disk_tables,omitempty"`
	TmpTables        int64   `json:"tmp_tables,omitempty"`
	TmpTablesDiskPct float64 `json:"tmp_tables_disk_ratio_pct,omitempty"`
	SortMergePasses  int64   `json:"sort_merge_passes,omitempty"`

	// Row locking (mysql: innodb_row_lock_waits / innodb_row_lock_time_avg).
	RowLockWaits int64   `json:"row_lock_waits,omitempty"`
	RowLockAvgMs float64 `json:"row_lock_avg_ms,omitempty"`

	// Scan efficiency (both, different unit):
	// pgsql: idx_scans / (idx_scans + seq_scans) across all user tables.
	// mysql: handler_read_key / (handler_read_key + handler_read_rnd_next).
	IdxLookupRatioPct float64 `json:"idx_lookup_ratio_pct,omitempty"`

	// MySQL network I/O (mysql_global_status_bytes_received/sent).
	BytesReceived int64 `json:"bytes_received,omitempty"`
	BytesSent     int64 `json:"bytes_sent,omitempty"`
}

// ---------------------------------------------------------------------------
// query — deadlocks, locks, slow queries, row throughput, scan breakdown
// ---------------------------------------------------------------------------

// QueryMetric consolidates query health statistics.
// pgsql: from pg_locks_count, pg_stat_database_deadlocks, pg_stat_user_tables.
// mysql: from mysql_global_status_innodb_deadlocks, slow_queries, handler_read_*.
type QueryMetric struct {
	DeadlockCount   int64 `json:"deadlock_count"`
	LockWaitCount   int   `json:"lock_wait_count"`
	SlowQueryCount  int64 `json:"slow_query_count,omitempty"` // mysql + pgsql (if pg_stat_statements)

	// Row throughput (pgsql: pg_stat_database_tup_returned/fetched).
	RowsReturned int64 `json:"rows_returned,omitempty"`
	RowsFetched  int64 `json:"rows_fetched,omitempty"`

	// Table scan breakdown (pgsql: pg_stat_user_tables; mysql: handler counts).
	SeqScansTotal   int64   `json:"seq_scans_total,omitempty"`
	IdxScansTotal   int64   `json:"idx_scans_total,omitempty"`
	IdxScanRatioPct float64 `json:"idx_scan_ratio_pct,omitempty"`

	// Top tables with high sequential scans (pgsql only).
	HighSeqScanTables []ScanEfficiency `json:"high_seq_scan_tables,omitempty"`
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
// maintenance — vacuum, InnoDB purge, slots, locks, archiver
// ---------------------------------------------------------------------------

// MaintenanceMetric consolidates housekeeping health indicators.
type MaintenanceMetric struct {
	// Vacuum health (pgsql: pg_stat_user_tables dead tuple counts).
	TablesNeedingVacuum int          `json:"tables_needing_vacuum,omitempty"`
	StaleTables         []StaleTable `json:"stale_tables,omitempty"`

	// WAL archiver (pgsql: pg_stat_archiver).
	ArchiverArchived int64 `json:"archiver_archived,omitempty"`
	ArchiverFailed   int64 `json:"archiver_failed,omitempty"`

	// Logical replication slot lag (pgsql: pg_replication_slots).
	LogicalSlotLag []LogicalSlotStat `json:"logical_slot_lag,omitempty"`

	// Lock summary (pgsql: pg_locks_count).
	TotalLocks   int `json:"total_locks,omitempty"`
	GrantedLocks int `json:"granted_locks,omitempty"`
	WaitingLocks int `json:"waiting_locks,omitempty"`

	// InnoDB purge lag (mysql: innodb_history_list_length).
	PurgeLagTransactions int64  `json:"purge_lag_transactions,omitempty"`
	PurgeLagRiskLevel    string `json:"purge_lag_risk_level,omitempty"` // ok | warning | danger

	// Open table cache (mysql).
	OpenTables   int64 `json:"open_tables,omitempty"`
	OpenedTables int64 `json:"opened_tables,omitempty"`

	// InnoDB log waits (mysql: innodb_log_waits — non-zero = redo log flush bottleneck).
	InnodbLogWaits   int64 `json:"innodb_log_waits,omitempty"`
	TableLocksWaited int64 `json:"table_locks_waited,omitempty"`
}

// StaleTable identifies a table with a high dead-tuple count.
type StaleTable struct {
	Schema         string     `json:"schema"`
	Table          string     `json:"table"`
	LastAutovacuum *time.Time `json:"last_autovacuum"`
	DeadTuples     int64      `json:"dead_tuples"`
}

// LogicalSlotStat describes one logical replication slot and its WAL lag.
type LogicalSlotStat struct {
	Name     string `json:"name"`
	Database string `json:"database"`
	Active   bool   `json:"active"`
	LagBytes int64  `json:"lag_bytes"`
}
