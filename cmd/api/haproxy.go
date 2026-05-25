package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"erawan-cluster/internal/haproxy"
)

type stringList []string

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

type createMySQLHAProxyRequest struct {
	Port    int        `json:"port"`
	NodeIPs stringList `json:"node_ips"`
	NodeIP  string     `json:"node_ip"`
	DBPort  int        `json:"db_port"`
}

type createPGSQLHAProxyRequest struct {
	Port        int        `json:"port"`
	NodeIPs     stringList `json:"node_ips"`
	NodeIP      string     `json:"node_ip"`
	DBPort      int        `json:"db_port"`
	PatroniPort int        `json:"patroni_port"`
}

type deleteHAProxyRequest struct {
	Port int `json:"port"`
}

func (app *application) createMySQLHAProxyConfigHandler(w http.ResponseWriter, r *http.Request) {
	var req createMySQLHAProxyRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	nodes := resolveNodeIPs(req.NodeIPs, req.NodeIP)

	if err := app.haproxy.CreateMySQLConfig(r.Context(), haproxy.CreateMySQLConfigInput{
		Port:    req.Port,
		NodeIPs: nodes,
		DBPort:  req.DBPort,
	}); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	ok(w, "HAProxy MySQL config created and reloaded", map[string]any{
		"port":     req.Port,
		"node_ips": nodes,
		"db_port":  req.DBPort,
	})
}

func (app *application) createPGSQLHAProxyConfigHandler(w http.ResponseWriter, r *http.Request) {
	var req createPGSQLHAProxyRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	nodes := resolveNodeIPs(req.NodeIPs, req.NodeIP)

	if err := app.haproxy.CreatePGSQLConfig(r.Context(), haproxy.CreatePGSQLConfigInput{
		Port:        req.Port,
		NodeIPs:     nodes,
		DBPort:      req.DBPort,
		PatroniPort: req.PatroniPort,
	}); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	ok(w, "HAProxy PostgreSQL config created and reloaded", map[string]any{
		"port":         req.Port,
		"node_ips":     nodes,
		"db_port":      req.DBPort,
		"patroni_port": req.PatroniPort,
	})
}

func resolveNodeIPs(list stringList, single string) []string {
	if len(list) > 0 {
		return []string(list)
	}
	if single != "" {
		return []string{single}
	}
	return []string{}
}

func (app *application) deleteHAProxyConfigHandler(w http.ResponseWriter, r *http.Request) {
	var req deleteHAProxyRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	deleted, err := app.haproxy.DeleteConfig(r.Context(), haproxy.DeleteConfigInput{Port: req.Port})
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if !deleted {
		errJSON(w, http.StatusNotFound, "No config found for port")
		return
	}

	ok(w, "HAProxy config deleted and reloaded", map[string]any{"port": req.Port})
}

func (app *application) listHAProxyConfigsHandler(w http.ResponseWriter, r *http.Request) {
	files, err := app.haproxy.ListConfigs()
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, "success", files)
}

func (app *application) reloadHAProxyHandler(w http.ResponseWriter, r *http.Request) {
	if err := app.haproxy.Reload(r.Context()); err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, "HAProxy reloaded successfully", nil)
}
