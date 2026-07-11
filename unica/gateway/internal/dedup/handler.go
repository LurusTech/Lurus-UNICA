package dedup

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"
)

// DeadLetterHandler serves the dead-letter query API.
type DeadLetterHandler struct {
	dlq *DeadLetterQueue
}

// NewDeadLetterHandler creates a new handler for dead-letter queries.
func NewDeadLetterHandler(dlq *DeadLetterQueue) *DeadLetterHandler {
	return &DeadLetterHandler{dlq: dlq}
}

// DeadLetterResponse is the JSON response for the dead-letter query endpoint.
type DeadLetterResponse struct {
	Items []DeadLetterEntry `json:"items"`
	Count int               `json:"count"`
}

// ServeHTTP handles GET /api/v1/gateway/dead-letter requests.
// Query params: start (RFC3339), end (RFC3339), channel_id, limit (int).
func (h *DeadLetterHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error": "method not allowed",
		})
		return
	}

	filter := DeadLetterFilter{}
	q := r.URL.Query()

	if startStr := q.Get("start"); startStr != "" {
		t, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid start time, use RFC3339 format",
			})
			return
		}
		filter.StartTime = t
	}

	if endStr := q.Get("end"); endStr != "" {
		t, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid end time, use RFC3339 format",
			})
			return
		}
		filter.EndTime = t
	}

	if channelID := q.Get("channel_id"); channelID != "" {
		filter.ChannelID = channelID
	}

	if limitStr := q.Get("limit"); limitStr != "" {
		n, err := strconv.ParseInt(limitStr, 10, 64)
		if err != nil || n <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid limit, must be a positive integer",
			})
			return
		}
		filter.Count = n
	}

	entries, err := h.dlq.QueryDeadLetters(r.Context(), filter)
	if err != nil {
		log.Printf("[deadletter] query error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to query dead-letter stream",
		})
		return
	}

	writeJSON(w, http.StatusOK, DeadLetterResponse{
		Items: entries,
		Count: len(entries),
	})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[deadletter] failed to encode response: %v", err)
	}
}
