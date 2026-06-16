package main

import (
	"net/http"

	pgsqlcluster "erawan-cluster/internal/cluster/pgsql"
)

// pgsqlMetricsHandler collects live metrics from a running PostgreSQL/Patroni cluster.
//
//	POST /cluster/pgsql/metrics
//
// Required body fields: host, user
// Optional: port, password, database, ssl_mode, patroni_port,
//
//	categories, limit, connect_timeout
//
// Example — only replication and connections:
//
//	{"host":"10.0.0.1","user":"postgres","password":"s3cr3t","categories":["replication","connections"]}
func (app *application) pgsqlMetricsHandler(w http.ResponseWriter, r *http.Request) {
	var req pgsqlcluster.MetricRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.JobID != "" {
		host, port, user, password, nodeIPs, err := app.pgsqlCluster.ConnectionInfo(r.Context(), req.JobID)
		if err != nil {
			errJSON(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		req.Host = host
		req.Port = port
		req.User = user
		req.Password = password
		req.NodeIPs = nodeIPs
	}

	if req.Host == "" {
		req.Host = app.config.proxyHost
	}

	if err := pgsqlcluster.ValidateMetricRequest(&req); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	result := app.pgsqlCluster.CollectMetrics(r.Context(), req)
	ok(w, "metrics collected", result)
}
