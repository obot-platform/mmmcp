package mmmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/obot-platform/mmmcp/storage"
)

const (
	probeTimeout = 500 * time.Millisecond
)

type probeCheck struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type probeResponse struct {
	Status        string                `json:"status"`
	Version       string                `json:"version"`
	UptimeSeconds int64                 `json:"uptimeSeconds"`
	Checks        map[string]probeCheck `json:"checks,omitempty"`
}

func (c *Composite) serveHealth(w http.ResponseWriter, r *http.Request) {
	if !probeMethodAllowed(w, r) {
		return
	}
	status, code := "ok", http.StatusOK
	if c == nil || c.closed.Load() {
		status, code = "unavailable", http.StatusServiceUnavailable
	}
	writeProbe(w, r, code, probeResponse{
		Status: status, Version: implementationVersion, UptimeSeconds: c.uptimeSeconds(),
	})
}

func (c *Composite) serveReady(w http.ResponseWriter, r *http.Request) {
	if !probeMethodAllowed(w, r) {
		return
	}
	response := probeResponse{
		Status: "ok", Version: implementationVersion, UptimeSeconds: c.uptimeSeconds(),
		Checks: map[string]probeCheck{"lifecycle": {Status: "ok"}},
	}
	if c == nil || c.closed.Load() {
		response.Status = "unavailable"
		response.Checks["lifecycle"] = probeCheck{Status: "error", Reason: "server_closed"}
		writeProbe(w, r, http.StatusServiceUnavailable, response)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	if _, _, err := c.registry.Get(ctx, c.defaultConfig); err != nil {
		response.Status = "unavailable"
		response.Checks["catalog"] = probeCheck{Status: "error", Reason: "catalog_unavailable"}
	} else if c.catalogDegraded.Load() {
		response.Status = "degraded"
		response.Checks["catalog"] = probeCheck{Status: "degraded", Reason: "refresh_failed"}
	} else {
		response.Checks["catalog"] = probeCheck{Status: "ok"}
	}

	store, ok := c.defaultStore.(*storage.SQLStore)
	if !ok || store.DB() == nil || store.DB().PingContext(ctx) != nil {
		response.Status = "unavailable"
		response.Checks["storage"] = probeCheck{Status: "error", Reason: "storage_unavailable"}
	} else {
		response.Checks["storage"] = probeCheck{Status: "ok"}
	}

	code := http.StatusOK
	if response.Status == "unavailable" {
		code = http.StatusServiceUnavailable
	}
	writeProbe(w, r, code, response)
}

func (c *Composite) uptimeSeconds() int64 {
	if c == nil || c.startedAt.IsZero() {
		return 0
	}
	seconds := int64(time.Since(c.startedAt) / time.Second)
	if seconds < 0 {
		return 0
	}
	return seconds
}

func probeMethodAllowed(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	writeProbe(w, r, http.StatusMethodNotAllowed, probeResponse{Status: "method_not_allowed", Version: implementationVersion})
	return false
}

func writeProbe(w http.ResponseWriter, r *http.Request, code int, response probeResponse) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(response)
	}
}
