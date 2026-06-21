package haproxy

import (
	"encoding/json"
	"fmt"
	"net/http"

	"erawan-cluster/internal/haproxy"
	"erawan-cluster/internal/render"
)

// Handler holds the HAProxy service for HTTP route handling.
type Handler struct {
	service *haproxy.Service
}

/**
 * New creates a Handler with the given service.
 *
 * Params:
 *   svc *haproxy.Service - the svc (*haproxy.Service)
 *
 * Returns:
 *   *Handler - the resulting *Handler
 */
func New(svc *haproxy.Service) *Handler {
	return &Handler{service: svc}
}

// stringList accepts either a JSON string or an array of strings.
type stringList []string

/**
 * UnmarshalJSON.
 *
 * Receiver:
 *   s *stringList - pointer receiver; the method may mutate this stringList instance
 *
 * Params:
 *   data []byte - the data bytes
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *stringList) UnmarshalJSON(data []byte) error {
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*s = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*s = []string{one}
		return nil
	}
	return fmt.Errorf("node_ips must be a string or array of strings")
}

type createMySQLRequest struct {
	Port    int        `json:"port"`
	NodeIPs stringList `json:"node_ips"`
	NodeIP  string     `json:"node_ip"`
	DBPort  int        `json:"db_port"`
}

type createPGSQLRequest struct {
	Port        int        `json:"port"`
	NodeIPs     stringList `json:"node_ips"`
	NodeIP      string     `json:"node_ip"`
	DBPort      int        `json:"db_port"`
	PatroniPort int        `json:"patroni_port"`
}

type addMemberRequest struct {
	Port   int    `json:"port"`
	NodeIP string `json:"node_ip"`
}

type deleteRequest struct {
	Port int `json:"port"`
}

/**
 * resolveNodeIPs.
 *
 * Params:
 *   list stringList - the list (stringList)
 *   single string - the single string
 *
 * Returns:
 *   []string - the resulting []string
 */
func resolveNodeIPs(list stringList, single string) []string {
	if len(list) > 0 {
		return []string(list)
	}
	if single != "" {
		return []string{single}
	}
	return []string{}
}

/**
 * CreateMySQLConfig.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) CreateMySQLConfig(w http.ResponseWriter, r *http.Request) {
	var req createMySQLRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	nodes := resolveNodeIPs(req.NodeIPs, req.NodeIP)

	if err := h.service.CreateMySQLConfig(r.Context(), haproxy.CreateMySQLConfigInput{
		Port:    req.Port,
		NodeIPs: nodes,
		DBPort:  req.DBPort,
	}); err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	render.OK(w, "HAProxy MySQL config created and reloaded", map[string]any{
		"port":     req.Port,
		"node_ips": nodes,
		"db_port":  req.DBPort,
	})
}

/**
 * AddMySQLMember.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) AddMySQLMember(w http.ResponseWriter, r *http.Request) {
	var req addMemberRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := h.service.AddMySQLMember(r.Context(), haproxy.AddMemberConfigInput{
		Port:   req.Port,
		NodeIP: req.NodeIP,
	}); err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	render.OK(w, "HAProxy MySQL config updated and reloaded", map[string]any{
		"port":    req.Port,
		"node_ip": req.NodeIP,
	})
}

/**
 * CreatePGSQLConfig.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) CreatePGSQLConfig(w http.ResponseWriter, r *http.Request) {
	var req createPGSQLRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	nodes := resolveNodeIPs(req.NodeIPs, req.NodeIP)

	if err := h.service.CreatePGSQLConfig(r.Context(), haproxy.CreatePGSQLConfigInput{
		Port:        req.Port,
		NodeIPs:     nodes,
		DBPort:      req.DBPort,
		PatroniPort: req.PatroniPort,
	}); err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	render.OK(w, "HAProxy PostgreSQL config created and reloaded", map[string]any{
		"port":         req.Port,
		"node_ips":     nodes,
		"db_port":      req.DBPort,
		"patroni_port": req.PatroniPort,
	})
}

/**
 * AddPGSQLMember.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) AddPGSQLMember(w http.ResponseWriter, r *http.Request) {
	var req addMemberRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := h.service.AddPGSQLMember(r.Context(), haproxy.AddMemberConfigInput{
		Port:   req.Port,
		NodeIP: req.NodeIP,
	}); err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	render.OK(w, "HAProxy PostgreSQL config updated and reloaded", map[string]any{
		"port":    req.Port,
		"node_ip": req.NodeIP,
	})
}

/**
 * DeleteConfig.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) DeleteConfig(w http.ResponseWriter, r *http.Request) {
	var req deleteRequest
	if err := render.DecodeJSON(r, &req); err != nil {
		render.Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	deleted, err := h.service.DeleteConfig(r.Context(), haproxy.DeleteConfigInput{Port: req.Port})
	if err != nil {
		render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if !deleted {
		render.Error(w, http.StatusNotFound, "no config found for port")
		return
	}

	render.OK(w, "HAProxy config deleted and reloaded", map[string]any{"port": req.Port})
}

/**
 * ListConfigs.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) ListConfigs(w http.ResponseWriter, r *http.Request) {
	files, err := h.service.ListConfigs()
	if err != nil {
		render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	render.OK(w, "success", files)
}

/**
 * DownloadZip.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) DownloadZip(w http.ResponseWriter, r *http.Request) {
	data, err := h.service.ZipTenantsDir()
	if err != nil {
		render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="tenants.zip"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

/**
 * Reload.
 *
 * Receiver:
 *   h *Handler - pointer receiver; the method may mutate this Handler instance
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   r *http.Request - the incoming HTTP request
 */
func (h *Handler) Reload(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Reload(r.Context()); err != nil {
		render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	render.OK(w, "HAProxy reloaded successfully", nil)
}
