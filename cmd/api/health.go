package main

import (
	"net/http"

	"erawan-cluster/internal/render"
)

/**
 * healthCheckHandler answers liveness/readiness probes with a 200 response that
 * reports the service name and running version. It performs no dependency
 * checks, so it stays fast and cheap for load balancers to poll.
 *
 * Receiver:
 *   app *application - the dependency container; only config.version is read.
 * Params:
 *   w http.ResponseWriter - the response writer the JSON body is written to.
 *   r *http.Request - the incoming probe request (unused).
 */
func (app *application) healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	render.OK(w, "Go erawan-cluster API is healthy", map[string]any{
		"service": "erawan-cluster",
		"version": app.config.version,
	})
}
