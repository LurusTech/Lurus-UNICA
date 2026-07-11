package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

// HealthHandler provides the /healthz endpoint with Redis connectivity check.
type HealthHandler struct {
	rdb *redis.Client
}

// NewHealthHandler creates a new health check handler.
func NewHealthHandler(rdb *redis.Client) *HealthHandler {
	return &HealthHandler{rdb: rdb}
}

// ServeHTTP checks Redis connectivity and returns 200 or 503.
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := h.rdb.Ping(ctx).Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unhealthy",
			"error":  "redis: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "healthy",
	})
}
