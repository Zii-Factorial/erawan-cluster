package mysql

import (
	"net/http"
	"strconv"

	mysqlcluster "erawan-cluster/internal/cluster/mysql"
	mysqldbmanager "erawan-cluster/internal/cluster/mysql/dbmanager"
	"erawan-cluster/internal/render"

	"github.com/go-chi/chi/v5"
)

// Handler holds MySQL cluster and DB manager services for HTTP route handling.
type Handler struct {
	cluster   *mysqlcluster.Service
	dbmanager *mysqldbmanager.Service
	proxyHost string
}

/**
 * New creates a Handler with the given services.
 *
 * Params:
 *   cluster *mysqlcluster.Service - the cluster (*mysqlcluster.Service)
 *   db *mysqldbmanager.Service - the db (*mysqldbmanager.Service)
 *   proxyHost string - the proxyHost string
 *
 * Returns:
 *   *Handler - the resulting *Handler
 */
func New(cluster *mysqlcluster.Service, db *mysqldbmanager.Service, proxyHost string) *Handler {
	return &Handler{cluster: cluster, dbmanager: db, proxyHost: proxyHost}
}

/**
 * Deploy.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) Deploy(w http.ResponseWriter, r *http.Request) {
	var req mysqlcluster.DeployRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := h.cluster.Deploy(r.Context(), req)
	if err != nil {
		if job != nil {
			render.JSON(w, http.StatusUnprocessableEntity, render.Envelope{
				"status":  "error",
				"message": err.Error(),
				"data":    job,
			})
			return
		}
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	secret, _ := h.cluster.GetSecret(job.ID)
	render.Accepted(w, "MySQL cluster deployment started", struct {
		*mysqlcluster.Job
		Secret *mysqlcluster.StoredSecret `json:"secret,omitempty"`
	}{job, secret})
}

/**
 * GetJob.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	job, err := h.cluster.Get(jobID)
	if err != nil {
		render.Error(w, http.StatusNotFound, err.Error())
		return
	}
	secret, _ := h.cluster.GetSecret(jobID)
	job.Request.SSHPrivateKeyPath = ""
	render.OK(w, "success", struct {
		*mysqlcluster.Job
		Secret *mysqlcluster.StoredSecret `json:"secret,omitempty"`
	}{job, secret})
}

/**
 * ListJobs.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	jobs, err := h.cluster.List(limit)
	if err != nil {
		render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	render.OK(w, "success", jobs)
}

/**
 * ResumeJob.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) ResumeJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	var req mysqlcluster.ResumeRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := h.cluster.Resume(r.Context(), jobID, req)
	if err != nil {
		if job != nil {
			render.JSON(w, http.StatusUnprocessableEntity, render.Envelope{
				"status":  "error",
				"message": err.Error(),
				"data":    job,
			})
			return
		}
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	secret, _ := h.cluster.GetSecret(job.ID)
	render.Accepted(w, "MySQL cluster job resumed", struct {
		*mysqlcluster.Job
		Secret *mysqlcluster.StoredSecret `json:"secret,omitempty"`
	}{job, secret})
}

/**
 * RecoverJob triggers a post-outage cluster recovery for the deploy job identified
 * by {jobID}. It runs the boot_recovery Ansible step (plus bootstrap_router when
 * configured) using stored credentials — no request body is required.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) RecoverJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	job, err := h.cluster.Recover(r.Context(), jobID)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	render.Accepted(w, "MySQL cluster recovery started", job)
}

/**
 * RollbackJob.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) RollbackJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	var req mysqlcluster.RollbackRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := h.cluster.Rollback(r.Context(), jobID, req)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	render.OK(w, "MySQL cluster rollback executed", job)
}

/**
 * AddMember.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) AddMember(w http.ResponseWriter, r *http.Request) {
	var req mysqlcluster.AddMemberRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := h.cluster.AddMember(r.Context(), req)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	render.Accepted(w, "MySQL cluster member addition started", job)
}

/**
 * RemoveMember.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	var req mysqlcluster.RemoveMemberRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := h.cluster.RemoveMember(r.Context(), req)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	render.Accepted(w, "MySQL cluster member removal started", job)
}

/**
 * Metrics.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	var req mysqlcluster.MetricRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.JobID != "" {
		_, _, _, _, nodeIPs, err := h.cluster.ConnectionInfo(req.JobID)
		if err != nil {
			render.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		req.NodeIPs = nodeIPs
	}

	req.Host = h.proxyHost
	req.Port = req.ProxyPort

	if err := mysqlcluster.ValidateMetricRequest(&req); err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	result := h.cluster.CollectMetrics(r.Context(), req)
	render.OK(w, "metrics collected", result)
}

/**
 * CreateUser.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.CreateUserRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.dbmanager.CreateUser(r.Context(), req); err != nil {
		render.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	render.OK(w, "user created", nil)
}

/**
 * UpdateUser.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.UpdateUserRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.dbmanager.UpdateUser(r.Context(), req); err != nil {
		render.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	render.OK(w, "user renamed", nil)
}

/**
 * ResetPassword.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.ResetPasswordRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.dbmanager.ResetPassword(r.Context(), req); err != nil {
		render.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	render.OK(w, "password reset", nil)
}

/**
 * DeleteUser.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.DeleteUserRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.dbmanager.DeleteUser(r.Context(), req); err != nil {
		render.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	render.OK(w, "user deleted", nil)
}

/**
 * CreateDatabase.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) CreateDatabase(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.CreateDatabaseRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.dbmanager.CreateDatabase(r.Context(), req); err != nil {
		render.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	render.OK(w, "database created", nil)
}

/**
 * UpdateDatabase.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) UpdateDatabase(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.UpdateDatabaseRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.dbmanager.UpdateDatabase(r.Context(), req); err != nil {
		render.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	render.OK(w, "database renamed", nil)
}

/**
 * DeleteDatabase.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) DeleteDatabase(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.DeleteDatabaseRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.dbmanager.DeleteDatabase(r.Context(), req); err != nil {
		render.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	render.OK(w, "database deleted", nil)
}
