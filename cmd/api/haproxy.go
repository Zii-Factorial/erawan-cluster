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

type createHAProxyRequest struct {
	Port        int        `json:"port"`
	NodeIPs     stringList `json:"node_ips"`
	NodeIP      string     `json:"node_ip"`
	DBPort      int        `json:"db_port"`
	PatroniPort int        `json:"patroni_port"`
}

type deleteHAProxyRequest struct {
	Port int `json:"port"`
}

func (app *application) createHAProxyConfigHandler(w http.ResponseWriter, r *http.Request) {
	var req createHAProxyRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	nodes := make([]string, 0)
	if len(req.NodeIPs) > 0 {
		nodes = append(nodes, req.NodeIPs...)
	} else if req.NodeIP != "" {
		nodes = append(nodes, req.NodeIP)
	}

	err := app.haproxy.CreateConfig(r.Context(), haproxy.CreateConfigInput{
		Port:        req.Port,
		NodeIPs:     nodes,
		DBPort:      req.DBPort,
		PatroniPort: req.PatroniPort,
	})
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	ok(w, "HAProxy config created and reloaded", map[string]any{
		"port":     req.Port,
		"node_ips": nodes,
		"db_port":  req.DBPort,
	})
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
