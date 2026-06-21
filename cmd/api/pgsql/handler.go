package pgsql

import (
	"net/http"
	"strconv"

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

// New creates a Handler with the given services.
func New(cluster *pgsqlcluster.Service, db *pgsqldbmanager.Service, proxyHost string) *Handler {
	return &Handler{cluster: cluster, dbmanager: db, proxyHost: proxyHost}
}

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

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	var req pgsqlcluster.MetricRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.JobID != "" {
		_, _, user, password, nodeIPs, err := h.cluster.ConnectionInfo(r.Context(), req.JobID)
		if err != nil {
			render.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		req.User = user
		req.Password = password
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
