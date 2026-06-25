package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"erawan-cluster/internal/cluster/core"
	gomysql "github.com/go-sql-driver/mysql"
)

// Collector gathers live metrics from a MySQL InnoDB Cluster.
// Data is sourced from mysqld_exporter (:9104) and node_exporter (:9100).
// No direct database connection is required.
type Collector struct {
	httpClient *http.Client
}

func NewCollector() *Collector {
	return &Collector{
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Collect gathers every requested category and returns a MetricResponse.
func (c *Collector) Collect(ctx context.Context, req MetricRequest) MetricResponse {
	resp := MetricResponse{
		CollectedAt: time.Now().UTC(),
		Engine:      "mysql",
		Host:        req.Host,
		Port:        resolvePort(req.Port, 3306),
		Categories:  make(map[string]any),
		Errors:      make(map[string]string),
	}

	mysqlPort := resolvePort(req.MysqlExporterPort, 9104)
	nodePort := resolvePort(req.NodeExporterPort, 9100)

	// Primary is the first node IP (set by ConnectionInfo from stored job).
	primaryIP := ""
	if len(req.NodeIPs) > 0 {
		primaryIP = req.NodeIPs[0]
	} else if req.Host != "" {
		primaryIP = req.Host
	}

	// Scrape mysqld_exporter on the primary node for all DB-level categories.
	var primaryMetrics core.MetricFamily
	if primaryIP != "" {
		url := fmt.Sprintf("http://%s:%d/metrics", primaryIP, mysqlPort)
		var err error
		primaryMetrics, err = core.ScrapeMetrics(ctx, c.httpClient, url)
		if err != nil {
			for _, cat := range resolveCategories(req.Categories) {
				if cat != MetricCategorySystem {
					resp.Errors[cat] = "mysql exporter scrape: " + err.Error()
				}
			}
		}
	}

	// Standby IPs (everything after the primary).
	var standbyIPs []string
	if len(req.NodeIPs) > 1 {
		standbyIPs = req.NodeIPs[1:]
	}

	// Scrape mysqld_exporter on each standby to collect per-node replica lag.
	standbyMetrics := c.scrapeAllMysqlExporters(ctx, standbyIPs, mysqlPort)

	// Scrape node_exporter on every node for OS metrics.
	for _, nm := range c.scrapeAllNodes(ctx, req.NodeIPs, nodePort) {
		resp.Nodes = append(resp.Nodes, nm)
	}

	// Database sizes from info_schema tables collector (if enabled on exporter).
	if primaryMetrics != nil {
		if dbs := normalizeDatabases(primaryMetrics); len(dbs) > 0 {
			resp.Databases = dbs
		}
	}

	// Collect DB users via direct connection to node IPs (mysql.user is replicated,
	// so any healthy node can answer this query).
	if req.DBUser != "" && req.DBPort > 0 {
		nodes := req.NodeIPs
		if len(nodes) == 0 && req.DBHost != "" {
			nodes = []string{req.DBHost}
		}
		var firstErr error
		for _, ip := range nodes {
			users, err := collectMysqlUsers(ctx, ip, req.DBPort, req.DBUser, req.DBPassword, req.TLSMode)
			if err == nil {
				resp.Users = users
				break
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if resp.Users == nil && firstErr != nil {
			resp.Errors["users"] = firstErr.Error()
		}
	}

	categories := resolveCategories(req.Categories)
	type result struct {
		data any
		err  error
	}
	results := make(map[string]result, len(categories))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, cat := range categories {
		if primaryMetrics == nil && cat != MetricCategorySystem {
			continue
		}
		cat := cat
		wg.Add(1)
		go func() {
			defer wg.Done()
			var data any
			var err error
			switch cat {
			case MetricCategoryCluster:
				data, err = collectCluster(primaryMetrics)
			case MetricCategoryUptime:
				data, err = collectUptime(primaryMetrics)
			case MetricCategoryConnections:
				data, err = collectConnections(primaryMetrics)
			case MetricCategoryReplication:
				data, err = collectReplication(primaryIP, standbyIPs, primaryMetrics, standbyMetrics)
			case MetricCategoryPerformance:
				data, err = collectPerformance(primaryMetrics)
			case MetricCategoryQuery:
				data, err = collectQuery(primaryMetrics)
			case MetricCategoryMaintenance:
				data, err = collectMaintenance(primaryMetrics)
			case MetricCategorySystem:
				// Already in resp.Nodes.
			}
			mu.Lock()
			results[cat] = result{data, err}
			mu.Unlock()
		}()
	}
	wg.Wait()

	for cat, r := range results {
		if cat == MetricCategorySystem {
			continue
		}
		if r.err != nil {
			if _, already := resp.Errors[cat]; !already {
				resp.Errors[cat] = r.err.Error()
			}
		} else if r.data != nil {
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
	if req.ProxyPort == 0 {
		return fmt.Errorf("proxy_port is required (HAProxy frontend port, e.g. 23306)")
	}
	if req.ProxyPort < 1 || req.ProxyPort > 65535 {
		return fmt.Errorf("proxy_port must be between 1 and 65535")
	}
	if len(req.NodeIPs) == 0 && req.JobID == "" {
		return fmt.Errorf("job_id or node_ips is required to locate cluster exporters")
	}
	valid := map[string]bool{}
	for _, c := range allMetricCategories {
		valid[c] = true
	}
	for _, cat := range req.Categories {
		if !valid[strings.ToLower(strings.TrimSpace(cat))] {
			return fmt.Errorf("unknown category %q; valid: %s", cat, strings.Join(allMetricCategories, ", "))
		}
	}
	return nil
}

// =============================================================================
// Multi-node mysqld_exporter scraping
// =============================================================================

// scrapeAllMysqlExporters scrapes mysqld_exporter on each standby node.
// Returns a map from node IP to its metric family.
func (c *Collector) scrapeAllMysqlExporters(ctx context.Context, standbyIPs []string, port int) map[string]core.MetricFamily {
	if len(standbyIPs) == 0 {
		return nil
	}
	type scrapeResult struct {
		ip      string
		metrics core.MetricFamily
	}
	results := make([]scrapeResult, len(standbyIPs))
	var wg sync.WaitGroup
	for i, ip := range standbyIPs {
		i, ip := i, ip
		wg.Add(1)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("http://%s:%d/metrics", ip, port)
			m, err := core.ScrapeMetrics(ctx, c.httpClient, url)
			if err == nil {
				results[i] = scrapeResult{ip: ip, metrics: m}
			}
		}()
	}
	wg.Wait()
	out := make(map[string]core.MetricFamily, len(standbyIPs))
	for _, r := range results {
		if r.metrics != nil {
			out[r.ip] = r.metrics
		}
	}
	return out
}

// =============================================================================
// Node exporter — system metrics (identical logic to pgsql)
// =============================================================================

type nodeResult struct {
	metric NodeMetric
	err    error
}

func (c *Collector) scrapeAllNodes(ctx context.Context, nodeIPs []string, port int) []NodeMetric {
	if len(nodeIPs) == 0 {
		return nil
	}
	results := make([]nodeResult, len(nodeIPs))
	var wg sync.WaitGroup
	for i, ip := range nodeIPs {
		i, ip := i, ip
		wg.Add(1)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("http://%s:%d/metrics", ip, port)
			m, err := core.ScrapeMetrics(ctx, c.httpClient, url)
			if err != nil {
				results[i] = nodeResult{err: err}
				return
			}
			results[i] = nodeResult{metric: normalizeNodeMetrics(ip, m)}
		}()
	}
	wg.Wait()
	var out []NodeMetric
	for _, r := range results {
		if r.err == nil {
			out = append(out, r.metric)
		}
	}
	return out
}

func normalizeNodeMetrics(host string, f core.MetricFamily) NodeMetric {
	nm := NodeMetric{Host: host}

	bootTime := f.First("node_boot_time_seconds", 0)
	if bootTime > 0 {
		nm.UptimeSeconds = int64(time.Now().Unix()) - int64(bootTime)
	}

	nm.Load1 = f.First("node_load1", 0)
	nm.Load5 = f.First("node_load5", 0)
	nm.Load15 = f.First("node_load15", 0)

	nm.MemTotalBytes = int64(f.First("node_memory_MemTotal_bytes", 0))
	nm.MemAvailableBytes = int64(f.First("node_memory_MemAvailable_bytes", 0))
	if nm.MemTotalBytes > 0 {
		used := nm.MemTotalBytes - nm.MemAvailableBytes
		nm.MemUsedPct = math.Round(float64(used)/float64(nm.MemTotalBytes)*10000) / 100
	}

	idleTotal := f.SumWhere("node_cpu_seconds_total", "mode", "idle")
	allTotal := f.Sum("node_cpu_seconds_total")
	if allTotal > 0 {
		nm.CPUUsagePct = math.Round((1-idleTotal/allTotal)*10000) / 100
	}

	excludedFSTypes := map[string]bool{
		"tmpfs": true, "devtmpfs": true, "devfs": true,
		"overlay": true, "squashfs": true, "nsfs": true,
	}
	diskSeen := map[string]bool{}
	for _, s := range f["node_filesystem_size_bytes"] {
		mp := s.Labels["mountpoint"]
		fstype := s.Labels["fstype"]
		if excludedFSTypes[fstype] || diskSeen[mp] {
			continue
		}
		diskSeen[mp] = true
		avail := f.FirstWhere("node_filesystem_avail_bytes", s.Labels, 0)
		total := s.Value
		used := total - avail
		var usedPct float64
		if total > 0 {
			usedPct = math.Round(used/total*10000) / 100
		}
		nm.Disks = append(nm.Disks, DiskStat{
			Mountpoint: mp,
			SizeBytes:  int64(total),
			UsedBytes:  int64(used),
			AvailBytes: int64(avail),
			UsedPct:    usedPct,
		})
	}
	sort.Slice(nm.Disks, func(i, j int) bool {
		return nm.Disks[i].Mountpoint < nm.Disks[j].Mountpoint
	})

	excludedDevPrefixes := []string{"lo", "veth", "docker", "br-", "virbr"}
	netSeen := map[string]bool{}
	for _, s := range f["node_network_receive_bytes_total"] {
		dev := s.Labels["device"]
		if netSeen[dev] {
			continue
		}
		excluded := false
		for _, pfx := range excludedDevPrefixes {
			if strings.HasPrefix(dev, pfx) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		netSeen[dev] = true
		tx := f.FirstWhere("node_network_transmit_bytes_total", map[string]string{"device": dev}, 0)
		nm.NetworkInterfaces = append(nm.NetworkInterfaces, NetworkStat{
			Interface:    dev,
			RxBytesTotal: int64(s.Value),
			TxBytesTotal: int64(tx),
		})
	}
	sort.Slice(nm.NetworkInterfaces, func(i, j int) bool {
		return nm.NetworkInterfaces[i].Interface < nm.NetworkInterfaces[j].Interface
	})

	return nm
}

// =============================================================================
// Databases — from info_schema tables collector (--collector.info_schema.tables)
// =============================================================================

func normalizeDatabases(f core.MetricFamily) []DatabaseInfo {
	excludeDB := map[string]bool{
		"information_schema": true,
		"performance_schema": true,
		"mysql":              true,
		"sys":                true,
	}
	dbSizes := map[string]int64{}
	for _, s := range f["mysql_info_schema_table_size_bytes"] {
		schema := s.Labels["schema"]
		comp := s.Labels["component"]
		if excludeDB[schema] {
			continue
		}
		if comp == "data_length" || comp == "index_length" {
			dbSizes[schema] += int64(s.Value)
		}
	}
	var dbs []DatabaseInfo
	for name, size := range dbSizes {
		dbs = append(dbs, DatabaseInfo{Name: name, SizeBytes: size})
	}
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].Name < dbs[j].Name })
	return dbs
}

// =============================================================================
// cluster — Group Replication membership
// =============================================================================

func collectCluster(f core.MetricFamily) (*ClusterMetric, error) {
	m := &ClusterMetric{Members: []ClusterMember{}}

	// Detect GR by presence of any GR-related metric.
	grEnabled := false
	for k := range f {
		if strings.HasPrefix(k, "mysql_global_status_group_replication") {
			grEnabled = true
			break
		}
	}
	m.GREnabled = grEnabled

	// Parse member details if the exporter exposes them via custom queries.
	for _, s := range f["mysql_gr_member_state"] {
		host := s.Labels["member_host"]
		port := 0
		fmt.Sscanf(s.Labels["member_port"], "%d", &port)
		role := s.Labels["member_role"]
		state := s.Labels["member_state"]
		if role == "PRIMARY" {
			m.PrimaryHost = host
		}
		m.Members = append(m.Members, ClusterMember{
			Host:  host,
			Port:  port,
			State: state,
			Role:  role,
		})
	}
	m.MemberCount = len(m.Members)

	return m, nil
}

// =============================================================================
// uptime — mysql_global_status_uptime
// =============================================================================

func collectUptime(f core.MetricFamily) (*UptimeMetric, error) {
	uptimeSec := int64(f.First("mysql_global_status_uptime", 0))
	if uptimeSec == 0 {
		return nil, fmt.Errorf("mysql_global_status_uptime not found in exporter output")
	}
	startTime := time.Now().UTC().Add(-time.Duration(uptimeSec) * time.Second)
	return &UptimeMetric{
		StartTime:     startTime,
		UptimeSeconds: uptimeSec,
		UptimeHuman:   formatDuration(uptimeSec),
	}, nil
}

// =============================================================================
// connections — mysql_global_status_threads_*
// =============================================================================

func collectConnections(f core.MetricFamily) (*ConnectionMetric, error) {
	m := &ConnectionMetric{
		WaitEvents: []WaitEvent{},
		ByDatabase: []DBConnStat{},
	}

	m.MaxConnections = int(f.First("mysql_global_variables_max_connections", 0))
	m.TotalConnections = int(f.First("mysql_global_status_threads_connected", 0))
	m.Active = int(f.First("mysql_global_status_threads_running", 0))
	m.Idle = m.TotalConnections - m.Active
	if m.Idle < 0 {
		m.Idle = 0
	}
	m.MaxUsedConnections = int(f.First("mysql_global_status_max_used_connections", 0))
	m.AbortedConnects = int64(f.SumAlt(
		"mysql_global_status_aborted_connects_total",
		"mysql_global_status_aborted_connects",
	))
	// WaitingForLock: use innodb_row_lock_current_waits as the best proxy.
	m.WaitingForLock = int(f.First("mysql_global_status_innodb_row_lock_current_waits", 0))

	if m.MaxConnections > 0 {
		m.UtilizationPct = math.Round(float64(m.TotalConnections)/float64(m.MaxConnections)*10000) / 100
	}
	return m, nil
}

// =============================================================================
// replication — multi-node: primary + each standby's replica status
// =============================================================================

func collectReplication(primaryIP string, standbyIPs []string, primary core.MetricFamily, standbys map[string]core.MetricFamily) (*ReplicationMetric, error) {
	m := &ReplicationMetric{
		Members: []ReplicationMember{},
		Slots:   []ReplicationSlot{},
	}

	// Primary member — always listed first with zero lag.
	zero := float64(0)
	zeroBytes := int64(0)
	m.Members = append(m.Members, ReplicationMember{
		Role:             "primary",
		Host:             primaryIP,
		State:            "online",
		WriteLagSeconds:  &zero,
		ReplayLagSeconds: &zero,
		ReplayLagBytes:   &zeroBytes,
	})

	// Each standby: scrape its own mysqld_exporter for replica applier status.
	for _, ip := range standbyIPs {
		f, ok := standbys[ip]
		if !ok {
			m.Members = append(m.Members, ReplicationMember{
				Role:  "secondary",
				Host:  ip,
				State: "unknown",
			})
			continue
		}

		// Support both pre-8.0.22 (slave) and post-8.0.22 (replica) naming.
		lagSec := f.FirstAlt(
			"mysql_slave_status_seconds_behind_master",
			"mysql_replica_status_seconds_behind_source",
			-1,
		)
		ioRunning := replicaIORunning(f)
		sqlRunning := replicaSQLRunning(f)

		state := "offline"
		if ioRunning == "Yes" && sqlRunning == "Yes" {
			state = "online"
		}

		mem := ReplicationMember{
			Role:       "secondary",
			Host:       ip,
			State:      state,
			IORunning:  ioRunning,
			SQLRunning: sqlRunning,
		}
		if lagSec >= 0 {
			lagRef := lagSec
			mem.ReplayLagSeconds = &lagRef
		}
		m.Members = append(m.Members, mem)
	}

	m.StandbyCount = len(m.Members) - 1
	return m, nil
}

func replicaIORunning(f core.MetricFamily) string {
	for _, name := range []string{
		"mysql_slave_status_slave_io_running",
		"mysql_replica_status_replica_io_running",
	} {
		for _, s := range f[name] {
			if s.Value == 1 {
				return "Yes"
			}
			return "No"
		}
	}
	return ""
}

func replicaSQLRunning(f core.MetricFamily) string {
	for _, name := range []string{
		"mysql_slave_status_slave_sql_running",
		"mysql_replica_status_replica_sql_running",
	} {
		for _, s := range f[name] {
			if s.Value == 1 {
				return "Yes"
			}
			return "No"
		}
	}
	return ""
}

// =============================================================================
// performance — InnoDB buffer pool, QPS/TPS, row DML, temp tables, sort, network
// =============================================================================

func collectPerformance(f core.MetricFamily) (*PerformanceMetric, error) {
	m := &PerformanceMetric{}

	uptime := f.First("mysql_global_status_uptime", 0)

	// QPS and TPS.
	queries := f.SumAlt("mysql_global_status_queries", "mysql_global_status_questions")
	if uptime > 0 {
		m.QPS = math.Round(queries/uptime*1000) / 1000
	}
	commits := f.SumAlt("mysql_global_status_com_commit_total", "mysql_global_status_com_commit")
	rollbacks := f.SumAlt("mysql_global_status_com_rollback_total", "mysql_global_status_com_rollback")
	m.TotalCommits = int64(commits)
	m.TotalRollbacks = int64(rollbacks)
	m.TotalTransactions = m.TotalCommits + m.TotalRollbacks
	if uptime > 0 {
		m.TPS = math.Round((commits+rollbacks)/uptime*1000) / 1000
	}

	// InnoDB buffer pool — size from variables, hit ratio from read counters.
	m.CacheSizeBytes = int64(f.First("mysql_global_variables_innodb_buffer_pool_size", 0))
	readReqs := f.SumAlt(
		"mysql_global_status_innodb_buffer_pool_read_requests_total",
		"mysql_global_status_innodb_buffer_pool_read_requests",
	)
	reads := f.SumAlt(
		"mysql_global_status_innodb_buffer_pool_reads_total",
		"mysql_global_status_innodb_buffer_pool_reads",
	)
	if readReqs > 0 {
		m.CacheHitRatioPct = math.Round((1-reads/readReqs)*10000) / 100
	}
	m.CacheFreePages = int64(f.First("mysql_global_status_innodb_buffer_pool_pages_free", 0))
	m.CacheDirtyPages = int64(f.First("mysql_global_status_innodb_buffer_pool_pages_dirty", 0))

	// Row DML statement counts (cumulative since server start).
	m.RowsInserted = int64(f.SumAlt("mysql_global_status_com_insert_total", "mysql_global_status_com_insert"))
	m.RowsUpdated = int64(f.SumAlt("mysql_global_status_com_update_total", "mysql_global_status_com_update"))
	m.RowsDeleted = int64(f.SumAlt("mysql_global_status_com_delete_total", "mysql_global_status_com_delete"))

	// Temp tables and sort pressure.
	m.TmpDiskTables = int64(f.SumAlt(
		"mysql_global_status_created_tmp_disk_tables_total",
		"mysql_global_status_created_tmp_disk_tables",
	))
	m.TmpTables = int64(f.SumAlt(
		"mysql_global_status_created_tmp_tables_total",
		"mysql_global_status_created_tmp_tables",
	))
	if m.TmpTables > 0 {
		m.TmpTablesDiskPct = math.Round(float64(m.TmpDiskTables)/float64(m.TmpTables)*10000) / 100
	}
	m.SortMergePasses = int64(f.SumAlt(
		"mysql_global_status_sort_merge_passes_total",
		"mysql_global_status_sort_merge_passes",
	))

	// InnoDB row lock contention.
	m.RowLockWaits = int64(f.SumAlt(
		"mysql_global_status_innodb_row_lock_waits_total",
		"mysql_global_status_innodb_row_lock_waits",
	))
	m.RowLockAvgMs = f.First("mysql_global_status_innodb_row_lock_time_avg", 0)

	// Scan efficiency from handler counters:
	// handler_read_rnd_next ≈ full-table-scan row reads; handler_read_key ≈ index lookups.
	rndNext := f.SumAlt("mysql_global_status_handler_read_rnd_next_total", "mysql_global_status_handler_read_rnd_next")
	readKey := f.SumAlt("mysql_global_status_handler_read_key_total", "mysql_global_status_handler_read_key")
	total := rndNext + readKey
	if total > 0 {
		m.IdxLookupRatioPct = math.Round(readKey/total*10000) / 100
	}

	// Network I/O.
	m.BytesReceived = int64(f.SumAlt("mysql_global_status_bytes_received_total", "mysql_global_status_bytes_received"))
	m.BytesSent = int64(f.SumAlt("mysql_global_status_bytes_sent_total", "mysql_global_status_bytes_sent"))

	return m, nil
}

// =============================================================================
// query — deadlocks, lock waits, slow queries, row throughput, scan efficiency
// =============================================================================

func collectQuery(f core.MetricFamily) (*QueryMetric, error) {
	m := &QueryMetric{HighSeqScanTables: []ScanEfficiency{}}

	// InnoDB deadlocks (cumulative).
	m.DeadlockCount = int64(f.SumAlt(
		"mysql_global_status_innodb_deadlocks_total",
		"mysql_global_status_innodb_deadlocks",
	))

	// Current row lock waiters.
	m.LockWaitCount = int(f.First("mysql_global_status_innodb_row_lock_current_waits", 0))

	// Slow queries (requires slow_query_log = ON in MySQL config).
	m.SlowQueryCount = int64(f.SumAlt(
		"mysql_global_status_slow_queries_total",
		"mysql_global_status_slow_queries",
	))

	// Row throughput — com_select is the closest proxy for rows returned.
	m.RowsReturned = int64(f.SumAlt("mysql_global_status_com_select_total", "mysql_global_status_com_select"))

	// Scan efficiency via handler counters (same values as performance).
	rndNext := f.SumAlt("mysql_global_status_handler_read_rnd_next_total", "mysql_global_status_handler_read_rnd_next")
	readKey := f.SumAlt("mysql_global_status_handler_read_key_total", "mysql_global_status_handler_read_key")
	m.SeqScansTotal = int64(rndNext)
	m.IdxScansTotal = int64(readKey)
	total := rndNext + readKey
	if total > 0 {
		m.IdxScanRatioPct = math.Round(readKey/total*10000) / 100
	}

	return m, nil
}

// =============================================================================
// maintenance — InnoDB purge lag, open tables, log waits, table lock contention
// =============================================================================

func collectMaintenance(f core.MetricFamily) (*MaintenanceMetric, error) {
	m := &MaintenanceMetric{
		StaleTables:    []StaleTable{},
		LogicalSlotLag: []LogicalSlotStat{},
	}

	// InnoDB purge lag (history list length = number of uncommitted old row versions).
	m.PurgeLagTransactions = int64(f.First("mysql_global_status_innodb_history_list_length", 0))
	switch {
	case m.PurgeLagTransactions > 1_000_000:
		m.PurgeLagRiskLevel = "danger"
	case m.PurgeLagTransactions > 100_000:
		m.PurgeLagRiskLevel = "warning"
	default:
		m.PurgeLagRiskLevel = "ok"
	}

	// Open table cache utilization.
	m.OpenTables = int64(f.First("mysql_global_status_open_tables", 0))
	m.OpenedTables = int64(f.SumAlt(
		"mysql_global_status_opened_tables_total",
		"mysql_global_status_opened_tables",
	))

	// InnoDB redo log waits — non-zero indicates log flush is a bottleneck.
	m.InnodbLogWaits = int64(f.SumAlt(
		"mysql_global_status_innodb_log_waits_total",
		"mysql_global_status_innodb_log_waits",
	))

	// Table-level lock contention (MyISAM + metadata lock waits).
	m.TableLocksWaited = int64(f.SumAlt(
		"mysql_global_status_table_locks_waited_total",
		"mysql_global_status_table_locks_waited",
	))

	return m, nil
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
	valid := map[string]bool{}
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

// collectMysqlUsers connects directly to the primary and returns all non-system
// users with their host constraint, superuser flag, and accessible databases.
func collectMysqlUsers(ctx context.Context, host string, port int, user, password, tlsModeOverride string) ([]UserInfo, error) {
	cfg := gomysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", host, port)
	cfg.DBName = "mysql"
	cfg.Timeout = 10 * time.Second
	cfg.ParseTime = true
	cfg.AllowNativePasswords = true
	cfg.TLSConfig = mysqlMetricTLSMode(tlsModeOverride)

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect %s:%d: %w", host, port, err)
	}

	// Fetch all non-system users.
	rows, err := db.QueryContext(ctx, `
		SELECT User, Host,
		       IF(Super_priv = 'Y', TRUE, FALSE) AS superuser
		FROM mysql.user
		WHERE User NOT IN ('root','mysql.sys','mysql.session','mysql.infoschema')
		  AND User <> ''
		ORDER BY User, Host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	userMap := map[string]*UserInfo{}
	var order []string
	for rows.Next() {
		var username, host string
		var superuser bool
		if err := rows.Scan(&username, &host, &superuser); err != nil {
			return nil, err
		}
		if _, exists := userMap[username]; !exists {
			userMap[username] = &UserInfo{Username: username, Host: host, SuperUser: superuser}
			order = append(order, username)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fetch per-user database grants from mysql.db.
	dbRows, err := db.QueryContext(ctx, `
		SELECT User, Db
		FROM mysql.db
		WHERE User NOT IN ('root','mysql.sys','mysql.session','mysql.infoschema')
		  AND User <> ''
		ORDER BY User, Db`)
	if err == nil {
		defer dbRows.Close()
		for dbRows.Next() {
			var u, d string
			if err := dbRows.Scan(&u, &d); err == nil {
				if info, ok := userMap[u]; ok {
					info.Databases = append(info.Databases, d)
				}
			}
		}
	}

	out := make([]UserInfo, 0, len(order))
	for _, name := range order {
		out = append(out, *userMap[name])
	}
	return out, nil
}

// mysqlMetricTLSMode resolves the TLS mode for metric collector direct connections.
func mysqlMetricTLSMode(override string) string {
	// Payload value takes priority over env var.
	for _, m := range []string{override, os.Getenv("CLUSTER_DB_TLS_MODE")} {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "true":
			return "true"
		case "skip-verify":
			return "skip-verify"
		case "false", "disable", "off":
			return "false"
		}
	}
	// Default: skip-verify encrypts without hostname verification, compatible with
	// HAProxy TCP passthrough where the cert CN is the node IP, not the proxy.
	return "skip-verify"
}
