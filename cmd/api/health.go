package main

import (
	"net/http"

	"erawan-cluster/internal/render"
)

// healthCheckHandler answers liveness/readiness probes with a 200 response that
// reports the service name and running version. It performs no dependency
// checks, so it stays fast and cheap for load balancers to poll.
func (app *application) healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	render.OK(w, "Go erawan-cluster API is healthy", map[string]any{
		"service": "erawan-cluster",
		"version": app.config.version,
	})
}
