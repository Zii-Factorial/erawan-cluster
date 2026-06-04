package main

import (
	"net/http"
	"strconv"

	pgsqlcluster "erawan-cluster/internal/cluster/pgsql"

	"github.com/go-chi/chi/v5"
)

func (app *application) deployPGSQLClusterHandler(w http.ResponseWriter, r *http.Request) {
	var req pgsqlcluster.DeployRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := app.pgsqlCluster.Deploy(r.Context(), req)
	if err != nil {
		if job != nil {
			writeJSON(w, http.StatusUnprocessableEntity, envelope{
				"status":  "error",
				"message": err.Error(),
				"data":    job,
			})
			return
		}
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	accepted(w, "PostgreSQL cluster deployment started", job)
}

func (app *application) getPGSQLClusterJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	job, err := app.pgsqlCluster.Get(jobID)
	if err != nil {
		errJSON(w, http.StatusNotFound, err.Error())
		return
	}
	secret, _ := app.pgsqlCluster.GetSecret(jobID)
	ok(w, "success", struct {
		*pgsqlcluster.Job
		Secret *pgsqlcluster.StoredSecret `json:"secret,omitempty"`
	}{job, secret})
}

func (app *application) listPGSQLClusterJobsHandler(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	jobs, err := app.pgsqlCluster.List(limit)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, "success", jobs)
}

func (app *application) resumePGSQLClusterJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	var req pgsqlcluster.ResumeRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := app.pgsqlCluster.Resume(r.Context(), jobID, req)
	if err != nil {
		if job != nil {
			writeJSON(w, http.StatusUnprocessableEntity, envelope{
				"status":  "error",
				"message": err.Error(),
				"data":    job,
			})
			return
		}
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	accepted(w, "PostgreSQL cluster job resumed", job)
}
