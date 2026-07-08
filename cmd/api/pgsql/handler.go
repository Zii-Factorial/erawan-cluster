package pgsql

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	pgsqlcluster "erawan-cluster/internal/cluster/pgsql"
	pgsqldbmanager "erawan-cluster/internal/cluster/pgsql/dbmanager"
	"erawan-cluster/internal/render"

	"github.com/go-chi/chi/v5"
)

// Handler holds PostgreSQL cluster and DB manager services for HTTP route handling.
type Handler struct {
	cluster   *pgsqlcluster.Service
	dbmanager *pgsqldbmanager.Service
	proxyHost string
}

/**
 * New creates a Handler with the given services.
 *
 * Params:
 *   cluster *pgsqlcluster.Service - the cluster (*pgsqlcluster.Service)
 *   db *pgsqldbmanager.Service - the db (*pgsqldbmanager.Service)
 *   proxyHost string - the proxyHost string
 *
 * Returns:
 *   *Handler - the resulting *Handler
 */
func New(cluster *pgsqlcluster.Service, db *pgsqldbmanager.Service, proxyHost string) *Handler {
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
	var req pgsqlcluster.DeployRequest
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
	render.Accepted(w, "PostgreSQL cluster deployment started", struct {
		*pgsqlcluster.Job
		Secret *pgsqlcluster.StoredSecret `json:"secret,omitempty"`
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
		*pgsqlcluster.Job
		Secret *pgsqlcluster.StoredSecret `json:"secret,omitempty"`
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
	var req pgsqlcluster.ResumeRequest
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
	render.Accepted(w, "PostgreSQL cluster job resumed", struct {
		*pgsqlcluster.Job
		Secret *pgsqlcluster.StoredSecret `json:"secret,omitempty"`
	}{job, secret})
}

/**
 * RecoverJob triggers a post-outage cluster recovery for the deploy job identified
 * by {jobID}. It runs cluster_bootstrap and verify_cluster using stored credentials
 * — no request body is required.
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
	render.Accepted(w, "PostgreSQL cluster recovery started", job)
}

// serviceOpRequest is the body of the stop/start cluster endpoints: the deploy
// job that owns the cluster, passed as payload like the member endpoints.
type serviceOpRequest struct {
	JobID string `json:"job_id"`
}

func decodeServiceOpRequest(r *http.Request) (string, error) {
	var req serviceOpRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		return "", err
	}
	jobID := strings.TrimSpace(req.JobID)
	if jobID == "" {
		return "", errors.New("job_id is required")
	}
	return jobID, nil
}

/**
 * StartJob starts a stopped cluster. Starting is the same operation as
 * post-outage recovery — cluster_bootstrap re-registers the cluster in the
 * DCS and starts Patroni on all nodes without touching data directories —
 * so this delegates to Recover. The deploy job ID comes in the request body.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) StartJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := decodeServiceOpRequest(r)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := h.cluster.Recover(r.Context(), jobID)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	render.Accepted(w, "PostgreSQL cluster start initiated", job)
}

/**
 * StopJob gracefully stops the cluster owned by the job_id in the request
 * body without touching any data: Patroni is stopped on standbys first, then
 * the primary, then etcd on all nodes. Restart with StartJob.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) StopJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := decodeServiceOpRequest(r)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := h.cluster.Stop(r.Context(), jobID)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	render.Accepted(w, "PostgreSQL cluster stop initiated", job)
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
	var req pgsqlcluster.AddMemberRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := h.cluster.AddMember(r.Context(), req)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	render.Accepted(w, "PostgreSQL cluster member addition started", job)
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
	var req pgsqlcluster.RemoveMemberRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	job, err := h.cluster.RemoveMember(r.Context(), req)
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	render.Accepted(w, "PostgreSQL cluster member removal started", job)
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
	var req pgsqlcluster.MetricRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.JobID != "" {
		_, _, _, _, nodeIPs, err := h.cluster.ConnectionInfo(r.Context(), req.JobID)
		if err != nil {
			render.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		req.NodeIPs = nodeIPs
	}

	req.Host = h.proxyHost
	req.Port = req.ProxyPort

	if err := pgsqlcluster.ValidateMetricRequest(&req); err != nil {
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
	var req pgsqldbmanager.CreateUserRequest
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
	var req pgsqldbmanager.UpdateUserRequest
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
	var req pgsqldbmanager.ResetPasswordRequest
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
	var req pgsqldbmanager.DeleteUserRequest
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
	var req pgsqldbmanager.CreateDatabaseRequest
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
	var req pgsqldbmanager.UpdateDatabaseRequest
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
	var req pgsqldbmanager.DeleteDatabaseRequest
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
