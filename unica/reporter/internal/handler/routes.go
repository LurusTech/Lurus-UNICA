// Package handler provides HTTP handlers for the reporter API.
package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/kefu/unica/reporter/internal/metrics"
	"github.com/kefu/unica/reporter/internal/repository"
)

// Handler holds shared dependencies for all report handlers.
type Handler struct {
	repo *repository.Repository
}

// NewHandler creates a new Handler with the given repository.
func NewHandler(repo *repository.Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers all report API routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/reports/ai-effectiveness", h.instrumentHandler("ai-effectiveness", h.handleAIEffectiveness))
	mux.HandleFunc("/api/v1/reports/agent-performance", h.instrumentHandler("agent-performance", h.handleAgentPerformance))
	mux.HandleFunc("/api/v1/reports/top-questions", h.instrumentHandler("top-questions", h.handleTopQuestions))
	mux.HandleFunc("/api/v1/reports/channel-traffic", h.instrumentHandler("channel-traffic", h.handleChannelTraffic))
}

// instrumentHandler wraps an HTTP handler with Prometheus metrics instrumentation.
func (h *Handler) instrumentHandler(reportType string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next(rw, r)
		duration := time.Since(start)

		metrics.ReportsGeneratedTotal.WithLabelValues(reportType).Inc()
		metrics.ReportGenerationDuration.WithLabelValues(reportType).Observe(duration.Seconds())
		metrics.HTTPRequestsTotal.WithLabelValues(r.Method, r.URL.Path, strconv.Itoa(rw.statusCode)).Inc()
		metrics.HTTPRequestDuration.WithLabelValues(r.Method, r.URL.Path).Observe(duration.Seconds())
	}
}

// parseTimeRange extracts start/end query parameters, defaulting to last 7 days.
func parseTimeRange(r *http.Request) repository.TimeRange {
	tr := repository.DefaultTimeRange()

	if s := r.URL.Query().Get("start"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			tr.Start = t
		} else if t, err := time.Parse("2006-01-02", s); err == nil {
			tr.Start = t
		}
	}
	if e := r.URL.Query().Get("end"); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil {
			tr.End = t
		} else if t, err := time.Parse("2006-01-02", e); err == nil {
			tr.End = t.Add(24 * time.Hour) // end of day
		}
	}

	return tr
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[reporter] json encode error: %v", err)
	}
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
