// Package handler provides HTTP handlers for the gateway service.
package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"

	"github.com/google/uuid"

	"github.com/kefu/unica/gateway/internal/ratelimit"
	"github.com/kefu/unica/gateway/internal/stream"
	"github.com/kefu/unica/pkg/model"
)

// InboundHandler handles POST /api/v1/gateway/inbound requests.
type InboundHandler struct {
	producer *stream.Producer
	limiter  *ratelimit.Limiter
}

// NewInboundHandler creates a new inbound handler with the given stream producer.
// The limiter parameter is optional; pass nil to disable rate limiting.
func NewInboundHandler(producer *stream.Producer, limiter ...*ratelimit.Limiter) *InboundHandler {
	h := &InboundHandler{producer: producer}
	if len(limiter) > 0 {
		h.limiter = limiter[0]
	}
	return h
}

// ServeHTTP processes an inbound StandardMessage and publishes it to Redis Streams.
func (h *InboundHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg model.StandardMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON: " + err.Error(),
		})
		return
	}

	// Validate the message
	if err := model.ValidateMessage(&msg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "validation failed: " + err.Error(),
		})
		return
	}

	// Rate limit check — applied per channel before further processing
	if h.limiter != nil {
		result, err := h.limiter.Allow(r.Context(), msg.Data.ChannelID)
		if err != nil {
			log.Printf("[inbound] rate limiter error for channel %s: %v", msg.Data.ChannelID, err)
			// Fail open: proceed if rate limiter has an error
		} else if !result.Allowed {
			retryAfterSec := int(math.Ceil(result.RetryAfter.Seconds()))
			if retryAfterSec < 1 {
				retryAfterSec = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSec))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "rate limit exceeded",
			})
			return
		}
	}

	// Generate correlation ID if not provided
	if msg.Data.CorrelationID == "" {
		msg.Data.CorrelationID = uuid.New().String()
	}

	// Check for correlation ID from header (allow override)
	if headerCID := r.Header.Get("X-Correlation-ID"); headerCID != "" {
		msg.Data.CorrelationID = headerCID
	}

	// Publish to inbound stream
	streamID, err := h.producer.Publish(r.Context(), &msg)
	if err != nil {
		log.Printf("[inbound] failed to publish message %s: %v", msg.ID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to publish message",
		})
		return
	}

	// Return 202 Accepted with correlation ID and stream ID
	w.Header().Set("X-Correlation-ID", msg.Data.CorrelationID)
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":         "accepted",
		"stream_id":      streamID,
		"correlation_id": msg.Data.CorrelationID,
	})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[handler] failed to encode response: %v", err)
	}
}
