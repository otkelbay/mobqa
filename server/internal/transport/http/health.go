package http

import (
	"context"
	"net/http"
	"time"
)

// healthCheckTimeout keeps a probe from hanging behind a stuck database.
const healthCheckTimeout = 2 * time.Second

type healthResponse struct {
	Status string `json:"status"`
}

type healthHandler struct {
	pinger Pinger
}

func (h healthHandler) serve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
	defer cancel()

	if err := h.pinger.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}
