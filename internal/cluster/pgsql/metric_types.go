package pgsql

import (
	"time"

	"erawan-cluster/internal/cluster/core"
)

// Metric categories.
const (
	MetricCategoryCluster     = "cluster"
	MetricCategoryUptime      = "uptime"
	MetricCategoryFailover    = "failover"
	MetricCategoryConnections = "connections"
	MetricCategoryReplication = "replication"
	MetricCategoryPerformance = "performance"
	MetricCategoryQuery       = "query"
	MetricCategoryMaintenance = "maintenance"
	MetricCategorySystem      = "system"
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
	MetricCategorySystem,
}

// Type aliases — these are the same types as core; the alias lets existing
// collector code and tests reference pgsql.NodeMetric etc. without change.
type (
	MetricResponse    = core.MetricResponse
	DatabaseInfo      = core.DatabaseInfo
	UserInfo          = core.UserInfo
	NodeMetric        = core.NodeMetric
	DiskStat          = core.DiskStat
	NetworkStat       = core.NetworkStat
	UptimeMetric      = core.UptimeMetric
	ConnectionMetric  = core.ConnectionMetric
	WaitEvent         = core.WaitEvent
	DBConnStat        = core.DBConnStat
	ReplicationMetric = core.ReplicationMetric
	ReplicationMember = core.ReplicationMember
	ReplicationSlot   = core.ReplicationSlot
	PerformanceMetric = core.PerformanceMetric
	QueryMetric       = core.QueryMetric
	ScanEfficiency    = core.ScanEfficiency
	MaintenanceMetric = core.MaintenanceMetric
	StaleTable        = core.StaleTable
	LogicalSlotStat   = core.LogicalSlotStat
)

// ---------------------------------------------------------------------------
// Request — pgsql-specific (PatroniPort, PgsqlExporterPort, time filters)
// ---------------------------------------------------------------------------

// MetricRequest defines collection parameters.
// No database credentials are accepted from the client — they are not needed
// since all data comes from Prometheus exporters and the Patroni REST API.
type MetricRequest struct {
	// JobID resolves node IPs and Patroni config from the stored deploy job.
	JobID string `json:"job_id"`

	// ProxyPort is the HAProxy frontend port for this cluster (e.g. 25041).
	ProxyPort int `json:"proxy_port"`

	// Exporter port overrides — omit to use standard defaults.
	PgsqlExporterPort int `json:"pgsql_exporter_port"` // default 9187
	NodeExporterPort  int `json:"node_exporter_port"`  // default 9100
	PatroniPort       int `json:"patroni_port"`         // default 8008

	// NodeIPs lists all cluster member IPs, used to scrape per-node exporters.
	// Resolved from the stored job when job_id is provided; may be provided directly.
	NodeIPs []string `json:"node_ips,omitempty"`

	// Collection filters.
	Categories []string   `json:"categories"` // empty = all
	From       *time.Time `json:"from"`       // filters failover events
	To         *time.Time `json:"to"`

	// Server-side only — never accepted from the client.
	Host string `json:"-"`
	Port int    `json:"-"`
}

// ---------------------------------------------------------------------------
// cluster — Patroni HA state (pgsql-specific)
// ---------------------------------------------------------------------------

// ClusterMetric holds Patroni node and cluster state including DCS health.
type ClusterMetric struct {
	Scope          string          `json:"scope"`
	Role           string          `json:"role"`  // primary | replica
	State          string          `json:"state"` // running | stopped | …
	Timeline       int             `json:"timeline"`
	PatroniVersion string          `json:"patroni_version"`
	ServerVersion  int             `json:"server_version,omitempty"`
	DCSLastSeen    *time.Time      `json:"dcs_last_seen,omitempty"`
	TTL            int             `json:"ttl,omitempty"`
	LoopWait       int             `json:"loop_wait,omitempty"`
	RetryTimeout   int             `json:"retry_timeout,omitempty"`
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
	// LagBytes is WAL lag in bytes behind the leader. Always 0 for the leader.
	LagBytes *int64 `json:"lag_bytes"`
}

// ---------------------------------------------------------------------------
// failover — Patroni timeline history (pgsql-specific)
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
