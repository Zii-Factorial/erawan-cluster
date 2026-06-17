package main

import (
	"net/http"
	"strconv"

	mysqlcluster "erawan-cluster/internal/cluster/mysql"
	"github.com/go-chi/chi/v5"
)

func (app *application) deployMySQLClusterHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqlcluster.DeployRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := app.mysqlCluster.Deploy(r.Context(), req)
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

	secret, _ := app.mysqlCluster.GetSecret(job.ID)
	accepted(w, "MySQL cluster deployment started", struct {
		*mysqlcluster.Job
		Secret *mysqlcluster.StoredSecret `json:"secret,omitempty"`
	}{job, secret})
}

func (app *application) getMySQLClusterJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	job, err := app.mysqlCluster.Get(jobID)
	if err != nil {
		errJSON(w, http.StatusNotFound, err.Error())
		return
	}
	secret, _ := app.mysqlCluster.GetSecret(jobID)
	job.Request.SSHPrivateKeyPath = ""
	ok(w, "success", struct {
		*mysqlcluster.Job
		Secret *mysqlcluster.StoredSecret `json:"secret,omitempty"`
	}{job, secret})
}

func (app *application) listMySQLClusterJobsHandler(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	jobs, err := app.mysqlCluster.List(limit)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, "success", jobs)
}

func (app *application) resumeMySQLClusterJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	var req mysqlcluster.ResumeRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := app.mysqlCluster.Resume(r.Context(), jobID, req)
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

	secret, _ := app.mysqlCluster.GetSecret(job.ID)
	accepted(w, "MySQL cluster job resumed", struct {
		*mysqlcluster.Job
		Secret *mysqlcluster.StoredSecret `json:"secret,omitempty"`
	}{job, secret})
}

func (app *application) rollbackMySQLClusterJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	var req mysqlcluster.RollbackRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := app.mysqlCluster.Rollback(r.Context(), jobID, req)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	ok(w, "MySQL cluster rollback executed", job)
}

func (app *application) addMySQLMemberHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqlcluster.AddMemberRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	result, err := app.mysqlCluster.AddMember(r.Context(), req)
	if err != nil {
		if result != nil {
			writeJSON(w, http.StatusUnprocessableEntity, envelope{
				"status":  "error",
				"message": err.Error(),
				"data":    result,
			})
			return
		}
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	ok(w, "MySQL cluster member added", result)
}

func (app *application) removeMySQLMemberHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqlcluster.RemoveMemberRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	result, err := app.mysqlCluster.RemoveMember(r.Context(), req)
	if err != nil {
		if result != nil {
			writeJSON(w, http.StatusUnprocessableEntity, envelope{
				"status":  "error",
				"message": err.Error(),
				"data":    result,
			})
			return
		}
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	ok(w, "MySQL cluster member removed", result)
}
