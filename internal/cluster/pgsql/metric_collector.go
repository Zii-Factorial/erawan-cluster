package pgsql

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"erawan-cluster/internal/cluster/core"
)

// Collector gathers live metrics from a PostgreSQL / Patroni cluster.
// Data is sourced from postgres_exporter (:9187), node_exporter (:9100),
// and the Patroni REST API (:8008). No direct database connection is required.
type Collector struct {
	httpClient *http.Client
}

func NewCollector() *Collector {
	return &Collector{
		httpClient: core.NewScrapeClient(),
	}
}

// Collect gathers every requested category and returns a MetricResponse.
func (c *Collector) Collect(ctx context.Context, req MetricRequest) MetricResponse {
	resp := MetricResponse{
		CollectedAt: time.Now().UTC(),
		Engine:      "pgsql",
		Host:        req.Host,
		Port:        resolvePort(req.Port, 5432),
		Users:       []UserInfo{},
		Categories:  make(map[string]any),
		Errors:      make(map[string]string),
	}

	pgPort := resolvePort(req.DBMetricExporterPort, 9187)
	nodePort := resolvePort(req.NodeExporterPort, 9100)

	// Discover the actual current primary via Patroni /leader — survives failover.
	// Done exactly once per request and shared with every category collector;
	// re-probing in each collector multiplied the cost when nodes were down.
	// Falls back to nodeIPs[0] when Patroni is unreachable (single-node, non-Patroni setup).
	leaderIP := ""
	leaderErr := fmt.Errorf("node_ips required for cluster/failover metrics")
	primaryIP := ""
	if len(req.NodeIPs) > 0 {
		leaderIP, leaderErr = c.discoverLeader(ctx, req)
		if leaderErr == nil {
			primaryIP = leaderIP
		} else {
			primaryIP = req.NodeIPs[0]
		}
	} else if req.Host != "" {
		primaryIP = req.Host
	}
	req.PrimaryIP = primaryIP

	// Scrape postgres_exporter on the current primary — used by most DB categories.
	var pgMetrics core.MetricFamily
	if primaryIP != "" {
		url := fmt.Sprintf("http://%s:%d/metrics", primaryIP, pgPort)
		var err error
		pgMetrics, err = core.ScrapeMetrics(ctx, c.httpClient, url)
		if err != nil {
			// Record the scrape error for all DB-dependent categories.
			for _, cat := range resolveCategories(req.Categories) {
				if !isPatroniCategory(cat) && cat != MetricCategorySystem {
					resp.Errors[cat] = "pgsql exporter scrape: " + err.Error()
				}
			}
		}
	}

	// Scrape databases list from pg_database_size_bytes.
	if pgMetrics != nil {
		resp.Databases = normalizeDatabases(pgMetrics)
		resp.Users = collectPgsqlUsersFromExporter(pgMetrics)
	}

	// Scrape node_exporter on every node — collected into resp.Nodes in parallel.
	nodeMetrics := c.scrapeAllNodes(ctx, req.NodeIPs, nodePort)
	for _, nm := range nodeMetrics {
		resp.Nodes = append(resp.Nodes, nm)
	}

	// Primary node uptime from node_exporter — used as fallback for collectUptime.
	// Match by IP so failover to a different primary is handled correctly.
	var primaryNodeUptimeSec int64
	for _, nm := range nodeMetrics {
		if nm.Host == primaryIP {
			primaryNodeUptimeSec = nm.UptimeSeconds
			break
		}
	}
	if primaryNodeUptimeSec == 0 && len(nodeMetrics) > 0 {
		primaryNodeUptimeSec = nodeMetrics[0].UptimeSeconds
	}

	// Collect each category concurrently.
	categories := resolveCategories(req.Categories)
	type result struct {
		data any
		err  error
	}
	results := make(map[string]result, len(categories))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, cat := range categories {
		// Skip DB categories if the scrape failed.
		if pgMetrics == nil && !isPatroniCategory(cat) && cat != MetricCategorySystem {
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
				data, err = c.collectCluster(ctx, req, leaderIP, leaderErr)
			case MetricCategoryFailover:
				data, err = c.collectFailover(ctx, req, leaderIP, leaderErr)
			case MetricCategoryUptime:
				data, err = collectUptime(pgMetrics, primaryNodeUptimeSec)
			case MetricCategoryConnections:
				data, err = collectConnections(pgMetrics)
			case MetricCategoryReplication:
				data, err = c.collectReplication(ctx, pgMetrics, req, leaderIP, leaderErr)
			case MetricCategoryPerformance:
				data, err = collectPerformance(pgMetrics)
			case MetricCategoryQuery:
				data, err = collectQuery(pgMetrics)
			case MetricCategoryMaintenance:
				data, err = collectMaintenance(pgMetrics)
			case MetricCategorySystem:
				// Already in resp.Nodes; nothing to add to categories map.
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
		return fmt.Errorf("proxy_port is required (HAProxy frontend port, e.g. 25041)")
	}
	if req.ProxyPort < 1 || req.ProxyPort > 65535 {
		return fmt.Errorf("proxy_port must be between 1 and 65535")
	}
	if len(req.NodeIPs) == 0 && req.JobID == "" {
		return fmt.Errorf("job_id or node_ips is required to locate cluster exporters")
	}
	if req.From != nil && req.To != nil && req.From.After(*req.To) {
		return fmt.Errorf("from must be before to")
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
// Node exporter — system metrics
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

	// CPU: compute average since boot using idle vs total seconds.
	idleTotal := f.SumWhere("node_cpu_seconds_total", "mode", "idle")
	allTotal := f.Sum("node_cpu_seconds_total")
	if allTotal > 0 {
		nm.CPUUsagePct = math.Round((1-idleTotal/allTotal)*10000) / 100
	}

	// Disks: include only real filesystems, exclude pseudo/temp mounts.
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

	// Network: exclude loopback and virtual interfaces.
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
// Databases — from pg_database_size_bytes
// =============================================================================

func normalizeDatabases(f core.MetricFamily) []DatabaseInfo {
	excludeDB := map[string]bool{"template0": true, "template1": true}
	var dbs []DatabaseInfo
	for _, s := range f["pg_database_size_bytes"] {
		name := s.Labels["datname"]
		if name == "" || excludeDB[name] {
			continue
		}
		dbs = append(dbs, DatabaseInfo{Name: name, SizeBytes: int64(s.Value)})
	}
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].Name < dbs[j].Name })
	return dbs
}

// collectPgsqlUsersFromExporter extracts non-system roles from
// pg_roles_connection_limit exposed by postgres_exporter.
// Excluded: all pg_* built-in roles, superuser accounts, and the exporter account.
func collectPgsqlUsersFromExporter(f core.MetricFamily) []UserInfo {
	skip := map[string]bool{
		"exporter":    true,
		"postgres":    true,
		"replicator":  true,
	}
	var users []UserInfo
	for _, s := range f["pg_roles_connection_limit"] {
		role := s.Labels["rolname"]
		if role == "" || strings.HasPrefix(role, "pg_") || skip[role] {
			continue
		}
		users = append(users, UserInfo{Username: role})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	return users
}

// =============================================================================
// uptime — pg_postmaster_start_time_seconds
// =============================================================================

func collectUptime(f core.MetricFamily, nodeUptimeSec int64) (*UptimeMetric, error) {
	startUnix := f.First("pg_postmaster_start_time_seconds", 0)
	if startUnix == 0 {
		// Fallback: derive uptime from node_boot_time_seconds (node_exporter :9100).
		if nodeUptimeSec > 0 {
			startTime := time.Now().UTC().Add(-time.Duration(nodeUptimeSec) * time.Second)
			return &UptimeMetric{
				StartTime:     startTime,
				UptimeSeconds: nodeUptimeSec,
				UptimeHuman:   formatDuration(nodeUptimeSec),
			}, nil
		}
		return nil, fmt.Errorf("pg_postmaster_start_time_seconds not found in exporter output")
	}
	startTime := time.Unix(int64(startUnix), 0).UTC()
	uptimeSec := int64(time.Since(startTime).Seconds())
	return &UptimeMetric{
		StartTime:     startTime,
		UptimeSeconds: uptimeSec,
		UptimeHuman:   formatDuration(uptimeSec),
	}, nil
}

// =============================================================================
// connections — pg_stat_activity_count + pg_settings_max_connections
// =============================================================================

func collectConnections(f core.MetricFamily) (*ConnectionMetric, error) {
	m := &ConnectionMetric{
		WaitEvents: []WaitEvent{},
		ByDatabase: []DBConnStat{},
	}

	m.MaxConnections = int(f.First("pg_settings_max_connections", 0))

	// Sum across all datname/wait_event labels by state.
	for _, s := range f["pg_stat_activity_count"] {
		state := s.Labels["state"]
		m.TotalConnections += int(s.Value)
		switch state {
		case "active":
			m.Active += int(s.Value)
		case "idle":
			m.Idle += int(s.Value)
		case "idle in transaction", "idle_in_transaction":
			m.IdleInTransaction += int(s.Value)
		}
		if s.Labels["wait_event_type"] == "Lock" {
			m.WaitingForLock += int(s.Value)
		}
	}

	if m.MaxConnections > 0 {
		m.UtilizationPct = math.Round(float64(m.TotalConnections)/float64(m.MaxConnections)*10000) / 100
	}

	// Wait events grouped by wait_event_type.
	waitTotals := map[string]int{}
	for _, s := range f["pg_stat_activity_count"] {
		wt := s.Labels["wait_event_type"]
		if wt == "" {
			wt = "None"
		}
		waitTotals[wt] += int(s.Value)
	}
	for wt, cnt := range waitTotals {
		m.WaitEvents = append(m.WaitEvents, WaitEvent{Type: wt, Count: cnt})
	}
	sort.Slice(m.WaitEvents, func(i, j int) bool {
		return m.WaitEvents[i].Count > m.WaitEvents[j].Count
	})

	// Per-database breakdown.
	dbTotals := map[string]*DBConnStat{}
	for _, s := range f["pg_stat_activity_count"] {
		dn := s.Labels["datname"]
		if dn == "" {
			continue
		}
		if dbTotals[dn] == nil {
			dbTotals[dn] = &DBConnStat{Database: dn}
		}
		st := s.Labels["state"]
		dbTotals[dn].Total += int(s.Value)
		switch st {
		case "active":
			dbTotals[dn].Active += int(s.Value)
		case "idle":
			dbTotals[dn].Idle += int(s.Value)
		}
	}
	for _, stat := range dbTotals {
		m.ByDatabase = append(m.ByDatabase, *stat)
	}
	sort.Slice(m.ByDatabase, func(i, j int) bool {
		return m.ByDatabase[i].Total > m.ByDatabase[j].Total
	})

	return m, nil
}

// =============================================================================
// replication — pg_stat_replication_* + pg_replication_slots_*
// =============================================================================

func (c *Collector) collectReplication(ctx context.Context, f core.MetricFamily, req MetricRequest, leaderIP string, leaderErr error) (*ReplicationMetric, error) {
	m := &ReplicationMetric{
		Members: []ReplicationMember{},
		Slots:   []ReplicationSlot{},
	}

	m.MaxWALSenders = int(f.First("pg_settings_max_wal_senders", 0))

	// Build per-standby lag index from pg_stat_replication for supplemental seconds data.
	type lagInfo struct {
		writeSec, flushSec, replaySec float64
		replayBytes                   int64
		appName, syncState, state     string
	}
	lagByAddr := map[string]lagInfo{}
	for _, s := range f["pg_stat_replication_write_lag_seconds"] {
		addr := s.Labels["client_addr"]
		if addr == "" {
			continue
		}
		lm := map[string]string{"application_name": s.Labels["application_name"], "client_addr": addr}
		rb := f.FirstWhere("pg_stat_replication_pg_wal_lsn_diff", lm, 0)
		lagByAddr[addr] = lagInfo{
			writeSec:    f.FirstWhere("pg_stat_replication_write_lag_seconds", lm, 0),
			flushSec:    f.FirstWhere("pg_stat_replication_flush_lag_seconds", lm, 0),
			replaySec:   f.FirstWhere("pg_stat_replication_replay_lag_seconds", lm, 0),
			replayBytes: int64(rb),
			appName:     s.Labels["application_name"],
			syncState:   s.Labels["sync_state"],
			state:       s.Labels["state"],
		}
	}

	// Primary: Patroni /cluster is the authoritative source for all members.
	// It includes standbys that may not appear in pg_stat_replication when
	// the connection is lagging or in startup/catchup state.
	if patroniMembers, err := c.patroniClusterMembers(ctx, req, leaderIP, leaderErr); err == nil && len(patroniMembers) > 0 {
		zero := float64(0)
		zeroBytes := int64(0)
		for _, pm := range patroniMembers {
			role := "secondary"
			if pm.role == "leader" || pm.role == "master" {
				role = "primary"
			}
			state := normalizePgsqlMemberState(pm.state)
			mem := ReplicationMember{
				Role:  role,
				Host:  pm.host,
				State: state,
			}
			if role == "primary" {
				mem.WriteLagSeconds = &zero
				mem.ReplayLagSeconds = &zero
				mem.ReplayLagBytes = &zeroBytes
			} else {
				lag, ok := lagByAddr[pm.host]
				if ok {
					mem.ApplicationName = lag.appName
					mem.SyncState = lag.syncState
					mem.WriteLagSeconds = &lag.writeSec
					if lag.flushSec != 0 {
						mem.FlushLagSeconds = &lag.flushSec
					}
					mem.ReplayLagSeconds = &lag.replaySec
					mem.ReplayLagBytes = &lag.replayBytes
				} else {
					lagBytes := int64(pm.lagBytes)
					lagF := float64(0)
					mem.ReplayLagSeconds = &lagF
					mem.ReplayLagBytes = &lagBytes
				}
			}
			m.Members = append(m.Members, mem)
		}
		// Primary first, then secondaries sorted by host.
		sort.Slice(m.Members, func(i, j int) bool {
			if m.Members[i].Role != m.Members[j].Role {
				return m.Members[i].Role == "primary"
			}
			return m.Members[i].Host < m.Members[j].Host
		})
		m.StandbyCount = len(m.Members) - 1
	} else {
		// Fallback: build from pg_stat_replication only.
		zero := float64(0)
		zeroBytes := int64(0)
		m.Members = append(m.Members, ReplicationMember{
			Role:             "primary",
			Host:             req.PrimaryIP,
			State:            "online",
			WriteLagSeconds:  &zero,
			ReplayLagSeconds: &zero,
			ReplayLagBytes:   &zeroBytes,
		})
		for addr, lag := range lagByAddr {
			wl, fl, rl := lag.writeSec, lag.flushSec, lag.replaySec
			rb := lag.replayBytes
			mem := ReplicationMember{
				Role:             "secondary",
				Host:             addr,
				ApplicationName:  lag.appName,
				State:            normalizePgsqlMemberState(lag.state),
				SyncState:        lag.syncState,
				WriteLagSeconds:  &wl,
				ReplayLagSeconds: &rl,
				ReplayLagBytes:   &rb,
			}
			if fl != 0 {
				mem.FlushLagSeconds = &fl
			}
			m.Members = append(m.Members, mem)
		}
		sort.Slice(m.Members, func(i, j int) bool {
			if m.Members[i].Role != m.Members[j].Role {
				return m.Members[i].Role == "primary"
			}
			return m.Members[i].Host < m.Members[j].Host
		})
		m.StandbyCount = len(m.Members) - 1
	}

	// Replication slots — actual metric names from postgres_exporter 0.19+:
	//   pg_replication_slot_slot_is_active{slot_name, slot_type, ...}
	//   pg_replication_slot_slot_current_wal_lsn{slot_name, slot_type, ...}
	slotNames := f.LabelValues("pg_replication_slot_slot_is_active", "slot_name")
	for _, name := range slotNames {
		lm := map[string]string{"slot_name": name}
		active := f.FirstWhere("pg_replication_slot_slot_is_active", lm, 0) == 1
		slotType := ""
		database := ""
		for _, s := range f["pg_replication_slot_slot_is_active"] {
			if s.Labels["slot_name"] == name {
				slotType = s.Labels["slot_type"]
				database = s.Labels["database"]
				break
			}
		}
		m.Slots = append(m.Slots, ReplicationSlot{
			Name:     name,
			Type:     slotType,
			Database: database,
			Active:   active,
		})
	}

	return m, nil
}

// patroniClusterMember is a minimal Patroni /cluster member used by collectReplication.
type patroniClusterMember struct {
	host, role, state string
	lagBytes          float64
}

// patroniClusterMembers fetches /cluster from the current Patroni leader and returns
// all members with their host IPs, roles, states, and WAL lag. The leader is
// discovered once per Collect and passed in.
func (c *Collector) patroniClusterMembers(ctx context.Context, req MetricRequest, leaderIP string, leaderErr error) ([]patroniClusterMember, error) {
	if leaderErr != nil {
		return nil, leaderErr
	}
	patroniPort := resolvePort(req.PatroniPort, 8008)
	clusterState, err := c.patroniGET(ctx, fmt.Sprintf("http://%s:%d/cluster", leaderIP, patroniPort))
	if err != nil {
		return nil, err
	}
	rawMembers, ok := clusterState["members"].([]any)
	if !ok {
		return nil, fmt.Errorf("no members in patroni /cluster")
	}
	var out []patroniClusterMember
	for _, raw := range rawMembers {
		mm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		host, _ := splitHostPort(getString(mm, "host"), 5432)
		role := strings.ToLower(getString(mm, "role"))
		state := strings.ToLower(getString(mm, "state"))
		var lagBytes float64
		if v, ok := mm["lag"].(float64); ok {
			lagBytes = v
		}
		out = append(out, patroniClusterMember{host: host, role: role, state: state, lagBytes: lagBytes})
	}
	return out, nil
}

// normalizePgsqlMemberState maps pg_stat_replication state values to the
// common "online" / "offline" / "unknown" format shared with MySQL.
func normalizePgsqlMemberState(s string) string {
	switch strings.ToLower(s) {
	case "streaming", "startup", "catchup", "backup":
		return "online"
	case "stopped":
		return "offline"
	default:
		if s == "" {
			return "unknown"
		}
		return "online"
	}
}

// =============================================================================
// performance — pg_stat_database_* + pg_stat_bgwriter_*
// =============================================================================

func collectPerformance(f core.MetricFamily) (*PerformanceMetric, error) {
	m := &PerformanceMetric{}

	// Aggregate across all user databases (excluding templates).
	excludeDB := map[string]bool{"template0": true, "template1": true}
	var blksHit, blksRead float64
	for _, s := range f["pg_stat_database_blks_hit"] {
		if !excludeDB[s.Labels["datname"]] {
			blksHit += s.Value
		}
	}
	// Handle _total suffix variant (counter naming).
	if blksHit == 0 {
		for _, s := range f["pg_stat_database_blks_hit_total"] {
			if !excludeDB[s.Labels["datname"]] {
				blksHit += s.Value
			}
		}
	}
	for _, s := range f["pg_stat_database_blks_read"] {
		if !excludeDB[s.Labels["datname"]] {
			blksRead += s.Value
		}
	}
	if blksRead == 0 {
		for _, s := range f["pg_stat_database_blks_read_total"] {
			if !excludeDB[s.Labels["datname"]] {
				blksRead += s.Value
			}
		}
	}
	if blksHit+blksRead > 0 {
		m.CacheHitRatioPct = math.Round(blksHit/(blksHit+blksRead)*10000) / 100
	}

	for _, s := range f["pg_stat_database_xact_commit"] {
		if !excludeDB[s.Labels["datname"]] {
			m.TotalCommits += int64(s.Value)
		}
	}
	if m.TotalCommits == 0 {
		for _, s := range f["pg_stat_database_xact_commit_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.TotalCommits += int64(s.Value)
			}
		}
	}
	for _, s := range f["pg_stat_database_xact_rollback"] {
		if !excludeDB[s.Labels["datname"]] {
			m.TotalRollbacks += int64(s.Value)
		}
	}
	if m.TotalRollbacks == 0 {
		for _, s := range f["pg_stat_database_xact_rollback_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.TotalRollbacks += int64(s.Value)
			}
		}
	}
	m.TotalTransactions = m.TotalCommits + m.TotalRollbacks

	for _, s := range f["pg_stat_database_temp_files"] {
		if !excludeDB[s.Labels["datname"]] {
			m.TempFiles += int64(s.Value)
		}
	}
	if m.TempFiles == 0 {
		for _, s := range f["pg_stat_database_temp_files_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.TempFiles += int64(s.Value)
			}
		}
	}
	for _, s := range f["pg_stat_database_temp_bytes"] {
		if !excludeDB[s.Labels["datname"]] {
			m.TempBytes += int64(s.Value)
		}
	}
	if m.TempBytes == 0 {
		for _, s := range f["pg_stat_database_temp_bytes_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.TempBytes += int64(s.Value)
			}
		}
	}

	// Bgwriter / checkpointer (PG16+ split bgwriter into bgwriter + checkpointer).
	m.CheckpointsTimed = int64(f.SumAlt("pg_stat_bgwriter_checkpoints_timed_total", "pg_stat_checkpointer_num_timed_total"))
	if m.CheckpointsTimed == 0 {
		m.CheckpointsTimed = int64(f.SumAlt("pg_stat_bgwriter_checkpoints_timed", "pg_stat_checkpointer_num_timed"))
	}
	m.CheckpointsReq = int64(f.SumAlt("pg_stat_bgwriter_checkpoints_req_total", "pg_stat_checkpointer_num_requested_total"))
	if m.CheckpointsReq == 0 {
		m.CheckpointsReq = int64(f.SumAlt("pg_stat_bgwriter_checkpoints_req", "pg_stat_checkpointer_num_requested"))
	}
	total := m.CheckpointsTimed + m.CheckpointsReq
	if total > 0 {
		m.CheckpointRatioPct = math.Round(float64(m.CheckpointsTimed)/float64(total)*10000) / 100
	}

	m.BuffersCheckpoint = int64(f.SumAlt("pg_stat_bgwriter_buffers_checkpoint_total", "pg_stat_bgwriter_buffers_checkpoint"))
	m.BuffersClean = int64(f.SumAlt("pg_stat_bgwriter_buffers_clean_total", "pg_stat_bgwriter_buffers_clean"))
	m.BuffersBackend = int64(f.SumAlt("pg_stat_bgwriter_buffers_backend_total", "pg_stat_bgwriter_buffers_backend"))
	m.BuffersAlloc = int64(f.SumAlt("pg_stat_bgwriter_buffers_alloc_total", "pg_stat_bgwriter_buffers_alloc"))

	// TPS derived from transaction counters and server uptime.
	startUnix := f.First("pg_postmaster_start_time_seconds", 0)
	if startUnix > 0 {
		uptimeSec := time.Since(time.Unix(int64(startUnix), 0)).Seconds()
		if uptimeSec > 0 {
			m.TPS = math.Round(float64(m.TotalCommits+m.TotalRollbacks)/uptimeSec*1000) / 1000
		}
	}

	// Row DML totals from pg_stat_database.
	for _, s := range f["pg_stat_database_tup_inserted"] {
		if !excludeDB[s.Labels["datname"]] {
			m.RowsInserted += int64(s.Value)
		}
	}
	if m.RowsInserted == 0 {
		for _, s := range f["pg_stat_database_tup_inserted_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.RowsInserted += int64(s.Value)
			}
		}
	}
	for _, s := range f["pg_stat_database_tup_updated"] {
		if !excludeDB[s.Labels["datname"]] {
			m.RowsUpdated += int64(s.Value)
		}
	}
	if m.RowsUpdated == 0 {
		for _, s := range f["pg_stat_database_tup_updated_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.RowsUpdated += int64(s.Value)
			}
		}
	}
	for _, s := range f["pg_stat_database_tup_deleted"] {
		if !excludeDB[s.Labels["datname"]] {
			m.RowsDeleted += int64(s.Value)
		}
	}
	if m.RowsDeleted == 0 {
		for _, s := range f["pg_stat_database_tup_deleted_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.RowsDeleted += int64(s.Value)
			}
		}
	}

	// I/O timing (requires track_io_timing = on; will be 0 if disabled).
	for _, s := range f["pg_stat_database_blk_read_time"] {
		if !excludeDB[s.Labels["datname"]] {
			m.BlkReadTimeMs += s.Value
		}
	}
	for _, s := range f["pg_stat_database_blk_write_time"] {
		if !excludeDB[s.Labels["datname"]] {
			m.BlkWriteTimeMs += s.Value
		}
	}

	// Cache size from shared_buffers setting (each page = 8 KiB).
	sharedBufs := f.First("pg_settings_shared_buffers", 0)
	if sharedBufs > 0 {
		m.CacheSizeBytes = int64(sharedBufs) * 8192
	}

	// Index lookup ratio from pg_stat_user_tables — same concept as MySQL's handler counters.
	var idxScan, seqScan float64
	for _, s := range f["pg_stat_user_tables_idx_scan"] {
		idxScan += s.Value
	}
	for _, s := range f["pg_stat_user_tables_idx_scan_total"] {
		idxScan += s.Value
	}
	for _, s := range f["pg_stat_user_tables_seq_scan"] {
		seqScan += s.Value
	}
	for _, s := range f["pg_stat_user_tables_seq_scan_total"] {
		seqScan += s.Value
	}
	if idxScan+seqScan > 0 {
		m.IdxLookupRatioPct = math.Round(idxScan/(idxScan+seqScan)*10000) / 100
	}

	return m, nil
}

// =============================================================================
// query — lock/deadlock counts, row throughput, scan efficiency
// =============================================================================

func collectQuery(f core.MetricFamily) (*QueryMetric, error) {
	m := &QueryMetric{HighSeqScanTables: []ScanEfficiency{}}

	excludeDB := map[string]bool{"template0": true, "template1": true}

	// Deadlocks from pg_stat_database.
	for _, s := range f["pg_stat_database_deadlocks"] {
		if !excludeDB[s.Labels["datname"]] {
			m.DeadlockCount += int64(s.Value)
		}
	}
	if m.DeadlockCount == 0 {
		for _, s := range f["pg_stat_database_deadlocks_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.DeadlockCount += int64(s.Value)
			}
		}
	}

	// Lock waits from pg_locks_count.
	for _, s := range f["pg_locks_count"] {
		if s.Labels["granted"] == "false" {
			m.LockWaitCount += int(s.Value)
		}
	}

	// Row throughput.
	for _, s := range f["pg_stat_database_tup_returned"] {
		if !excludeDB[s.Labels["datname"]] {
			m.RowsReturned += int64(s.Value)
		}
	}
	if m.RowsReturned == 0 {
		for _, s := range f["pg_stat_database_tup_returned_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.RowsReturned += int64(s.Value)
			}
		}
	}
	for _, s := range f["pg_stat_database_tup_fetched"] {
		if !excludeDB[s.Labels["datname"]] {
			m.RowsFetched += int64(s.Value)
		}
	}
	if m.RowsFetched == 0 {
		for _, s := range f["pg_stat_database_tup_fetched_total"] {
			if !excludeDB[s.Labels["datname"]] {
				m.RowsFetched += int64(s.Value)
			}
		}
	}

	// Scan efficiency from pg_stat_user_tables.
	type tableKey struct{ schema, table string }
	seqScanByTable := map[tableKey]int64{}
	idxScanByTable := map[tableKey]int64{}

	for _, s := range f["pg_stat_user_tables_seq_scan"] {
		k := tableKey{s.Labels["schemaname"], s.Labels["relname"]}
		seqScanByTable[k] += int64(s.Value)
	}
	for _, s := range f["pg_stat_user_tables_seq_scan_total"] {
		k := tableKey{s.Labels["schemaname"], s.Labels["relname"]}
		seqScanByTable[k] += int64(s.Value)
	}
	for _, s := range f["pg_stat_user_tables_idx_scan"] {
		k := tableKey{s.Labels["schemaname"], s.Labels["relname"]}
		idxScanByTable[k] += int64(s.Value)
	}
	for _, s := range f["pg_stat_user_tables_idx_scan_total"] {
		k := tableKey{s.Labels["schemaname"], s.Labels["relname"]}
		idxScanByTable[k] += int64(s.Value)
	}

	for _, seq := range seqScanByTable {
		m.SeqScansTotal += seq
	}
	for _, idx := range idxScanByTable {
		m.IdxScansTotal += idx
	}
	total := m.SeqScansTotal + m.IdxScansTotal
	if total > 0 {
		m.IdxScanRatioPct = math.Round(float64(m.IdxScansTotal)/float64(total)*10000) / 100
	}

	// Top tables by sequential scans (those with > 50 seq scans, sorted desc).
	type scanEntry struct {
		key tableKey
		seq int64
		idx int64
	}
	var highSeq []scanEntry
	for k, seq := range seqScanByTable {
		if seq > 50 {
			highSeq = append(highSeq, scanEntry{k, seq, idxScanByTable[k]})
		}
	}
	sort.Slice(highSeq, func(i, j int) bool { return highSeq[i].seq > highSeq[j].seq })
	if len(highSeq) > 20 {
		highSeq = highSeq[:20]
	}
	for _, e := range highSeq {
		tot := e.seq + e.idx
		var idxPct float64
		if tot > 0 {
			idxPct = math.Round(float64(e.idx)/float64(tot)*10000) / 100
		}
		m.HighSeqScanTables = append(m.HighSeqScanTables, ScanEfficiency{
			Schema:          e.key.schema,
			Table:           e.key.table,
			SeqScans:        e.seq,
			IdxScans:        e.idx,
			IdxScanRatioPct: idxPct,
		})
	}

	return m, nil
}

// =============================================================================
// maintenance — stale tables, logical slots, lock grants
// =============================================================================

func collectMaintenance(f core.MetricFamily) (*MaintenanceMetric, error) {
	m := &MaintenanceMetric{
		StaleTables:    []StaleTable{},
		LogicalSlotLag: []LogicalSlotStat{},
	}

	// Stale tables by dead tuple count.
	type tableKey struct{ schema, table string }
	deadByTable := map[tableKey]int64{}
	liveByTable := map[tableKey]int64{}
	vacByTable := map[tableKey]float64{}

	for _, s := range f["pg_stat_user_tables_n_dead_tup"] {
		k := tableKey{s.Labels["schemaname"], s.Labels["relname"]}
		deadByTable[k] += int64(s.Value)
	}
	for _, s := range f["pg_stat_user_tables_n_live_tup"] {
		k := tableKey{s.Labels["schemaname"], s.Labels["relname"]}
		liveByTable[k] += int64(s.Value)
	}
	for _, s := range f["pg_stat_user_tables_last_autovacuum"] {
		k := tableKey{s.Labels["schemaname"], s.Labels["relname"]}
		vacByTable[k] = s.Value
	}

	type staleEntry struct {
		key  tableKey
		dead int64
		live int64
		vac  float64
	}
	var staleList []staleEntry
	for k, dead := range deadByTable {
		if dead > 0 {
			staleList = append(staleList, staleEntry{k, dead, liveByTable[k], vacByTable[k]})
		}
		live := liveByTable[k]
		if live+dead > 0 && float64(dead)/float64(live+dead) > 0.20 {
			m.TablesNeedingVacuum++
		}
	}
	sort.Slice(staleList, func(i, j int) bool { return staleList[i].dead > staleList[j].dead })
	if len(staleList) > 20 {
		staleList = staleList[:20]
	}
	for _, e := range staleList {
		st := StaleTable{
			Schema:     e.key.schema,
			Table:      e.key.table,
			DeadTuples: e.dead,
		}
		if e.vac > 0 {
			t := time.Unix(int64(e.vac), 0).UTC()
			st.LastAutovacuum = &t
		}
		m.StaleTables = append(m.StaleTables, st)
	}

	// Logical slot lag.
	slotNames := f.LabelValues("pg_replication_slots_active", "slot_name")
	for _, name := range slotNames {
		lm := map[string]string{"slot_name": name}
		slotType := ""
		database := ""
		for _, s := range f["pg_replication_slots_active"] {
			if s.Labels["slot_name"] == name {
				slotType = s.Labels["slot_type"]
				database = s.Labels["database"]
				break
			}
		}
		if slotType != "logical" {
			continue
		}
		active := f.FirstWhere("pg_replication_slots_active", lm, 0) == 1
		lag := int64(f.FirstWhere("pg_replication_slots_pg_wal_lsn_diff", lm, 0))
		m.LogicalSlotLag = append(m.LogicalSlotLag, LogicalSlotStat{
			Name:     name,
			Database: database,
			Active:   active,
			LagBytes: lag,
		})
	}

	// Lock grants vs waits.
	for _, s := range f["pg_locks_count"] {
		m.TotalLocks += int(s.Value)
		if s.Labels["granted"] == "true" {
			m.GrantedLocks += int(s.Value)
		} else {
			m.WaitingLocks += int(s.Value)
		}
	}

	// WAL archiver health (pg_stat_archiver).
	m.ArchiverArchived = int64(f.SumAlt("pg_stat_archiver_archived_count_total", "pg_stat_archiver_archived_count"))
	m.ArchiverFailed = int64(f.SumAlt("pg_stat_archiver_failed_count_total", "pg_stat_archiver_failed_count"))

	return m, nil
}

// =============================================================================
// Patroni REST — cluster + failover (unchanged from previous implementation)
// =============================================================================

const patroniBodyLimit = 1 << 20

func (c *Collector) collectCluster(ctx context.Context, req MetricRequest, leaderIP string, leaderErr error) (*ClusterMetric, error) {
	if leaderErr != nil {
		return nil, leaderErr
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
	if dcs, ok := nodeState["dcs_last_seen"].(float64); ok && dcs > 0 {
		t := time.Unix(int64(dcs), 0).UTC()
		m.DCSLastSeen = &t
	}
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
			role := getString(mm, "role")
			mem := ClusterMember{
				Name:     getString(mm, "name"),
				Host:     host,
				Port:     port,
				Role:     role,
				State:    getString(mm, "state"),
				Timeline: getInt(mm, "timeline"),
			}
			if role == "leader" || role == "master" {
				zero := int64(0)
				mem.LagBytes = &zero
			} else if v, ok := mm["lag"].(float64); ok {
				n := int64(v)
				mem.LagBytes = &n
			}
			m.Members = append(m.Members, mem)
		}
	}
	return m, nil
}

func (c *Collector) collectFailover(ctx context.Context, req MetricRequest, leaderIP string, leaderErr error) (*FailoverMetric, error) {
	if leaderErr != nil {
		return nil, leaderErr
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
			continue
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

func (c *Collector) discoverLeader(ctx context.Context, req MetricRequest) (string, error) {
	patroniPort := resolvePort(req.PatroniPort, 8008)
	if len(req.NodeIPs) == 0 {
		return "", fmt.Errorf("node_ips required for cluster/failover metrics")
	}
	// Probe every node in parallel: exactly one node (the leader) answers
	// /leader with 200, replicas answer 503. During failover the dead old
	// primary is often first in node_ips — probing sequentially made every
	// metric request wait out its full connect timeout before reaching the
	// new leader.
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	found := make(chan string, len(req.NodeIPs))
	var wg sync.WaitGroup
	for _, ip := range req.NodeIPs {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			url := fmt.Sprintf("http://%s:%d/leader", ip, patroniPort)
			httpReq, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
			if err != nil {
				return
			}
			resp, err := c.httpClient.Do(httpReq)
			if err != nil {
				return
			}
			status := resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if status == http.StatusOK {
				found <- ip
			}
		}(ip)
	}
	go func() {
		wg.Wait()
		close(found)
	}()
	if ip, ok := <-found; ok {
		return ip, nil
	}
	return "", fmt.Errorf("no Patroni leader found among node_ips %v", req.NodeIPs)
}

func (c *Collector) patroniGET(ctx context.Context, rawURL string) (map[string]any, error) {
	var out map[string]any
	return out, c.patroniRequest(ctx, rawURL, &out)
}

func (c *Collector) patroniGETArray(ctx context.Context, rawURL string) ([]any, error) {
	var out []any
	return out, c.patroniRequest(ctx, rawURL, &out)
}

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

// =============================================================================
// helpers
// =============================================================================

func isPatroniCategory(cat string) bool {
	return cat == MetricCategoryCluster || cat == MetricCategoryFailover
}

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
