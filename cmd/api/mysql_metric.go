package main

import (
	"net/http"

	mysqlcluster "erawan-cluster/internal/cluster/mysql"
)

// mysqlMetricsHandler collects live metrics from a running MySQL InnoDB Cluster.
//
//	POST /cluster/mysql/metrics
//
// Required body fields: host, user
// Optional: port, password, database, ssl_mode, categories, limit, connect_timeout
//
// Example — only cluster and connections:
//
//	{"host":"10.0.0.1","user":"clusteradmin","password":"s3cr3t","categories":["cluster","connections"]}
func (app *application) mysqlMetricsHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqlcluster.MetricRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.JobID != "" {
		_, _, user, password, err := app.mysqlCluster.ConnectionInfo(req.JobID)
		if err != nil {
			errJSON(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		req.User = user
		req.Password = password
	}

	req.Host = app.config.proxyHost
	req.Port = req.ProxyPort

	if err := mysqlcluster.ValidateMetricRequest(&req); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	result := app.mysqlCluster.CollectMetrics(r.Context(), req)
	ok(w, "metrics collected", result)
}
