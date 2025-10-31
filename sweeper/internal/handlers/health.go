package handlers

import (
	"net/http"
	"sync"
)

func Health() *HealthHandler {
	return &HealthHandler{}
}

// HealthHandler provides simple HTTP endpoints for Kubernetes probes.
type HealthHandler struct {
	ready bool
	mu    sync.RWMutex
}

// SetReady marks the service as ready or not ready.
func (h *HealthHandler) SetReady(r bool) {
	h.mu.Lock()
	h.ready = r
	h.mu.Unlock()
}

// LivenessProbe returns 200 OK if process is running.
func (h *HealthHandler) LivenessProbe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// ReadinessProbe returns 200 OK only if the service is ready.
func (h *HealthHandler) ReadinessProbe(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.ready {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	} else {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}
}
