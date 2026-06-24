package mysql

import "erawan-cluster/internal/cluster/core"

// Metric categories.
const (
	MetricCategoryCluster     = "cluster"
	MetricCategoryUptime      = "uptime"
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
	MetricCategoryConnections,
	MetricCategoryReplication,
	MetricCategoryPerformance,
	MetricCategoryQuery,
	MetricCategoryMaintenance,
	MetricCategorySystem,
}

// Type aliases — same types as core so both engines return an identical JSON shape.
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
// Request — mysql-specific (MysqlExporterPort, NodeExporterPort)
// ---------------------------------------------------------------------------

// MetricRequest defines collection parameters.
// No database credentials are accepted from the client — all data comes from
// Prometheus exporters (mysqld_exporter :9104, node_exporter :9100).
type MetricRequest struct {
	// JobID resolves node IPs from the stored deploy job.
	JobID string `json:"job_id"`

	// ProxyPort is the HAProxy frontend port for this cluster (e.g. 23306).
	ProxyPort int `json:"proxy_port"`

	// Exporter port overrides — omit to use standard defaults.
	MysqlExporterPort int `json:"mysql_exporter_port"` // default 9104
	NodeExporterPort  int `json:"node_exporter_port"`  // default 9100

	// NodeIPs lists all cluster member IPs, used to scrape per-node exporters.
	// Resolved from the stored job when job_id is provided; may be provided directly.
	NodeIPs []string `json:"node_ips,omitempty"`

	// Collection filters.
	Categories []string `json:"categories"` // empty = all

	// Server-side only — never accepted from the client.
	Host       string `json:"-"`
	Port       int    `json:"-"`
	DBHost     string `json:"-"` // primary node IP for direct DB user query
	DBPort     int    `json:"-"` // mysql port for direct DB user query
	DBUser     string `json:"-"`
	DBPassword string `json:"-"`
}

// ---------------------------------------------------------------------------
// cluster — Group Replication membership (mysql-specific)
// ---------------------------------------------------------------------------

// ClusterMetric holds InnoDB Cluster / Group Replication membership state.
// Standard mysqld_exporter does not expose GR member details unless custom
// queries are configured; GREnabled reflects whether GR metrics were found.
type ClusterMetric struct {
	GREnabled   bool            `json:"gr_enabled"`
	PrimaryHost string          `json:"primary_host,omitempty"`
	MemberCount int             `json:"member_count"`
	Members     []ClusterMember `json:"members"`
}

// ClusterMember describes one node in the Group Replication cluster.
type ClusterMember struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	State string `json:"state"` // ONLINE | RECOVERING | OFFLINE | ERROR | UNREACHABLE
	Role  string `json:"role"`  // PRIMARY | SECONDARY
}
