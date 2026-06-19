package main

import (
	"net/http"

	"erawan-cluster/internal/render"
)

func (app *application) healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	render.OK(w, "Go erawan-cluster API is healthy", map[string]any{
		"service": "erawan-cluster",
		"version": app.config.version,
	})
}
