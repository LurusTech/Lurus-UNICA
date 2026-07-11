package taobao

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/kefu/unica/gateway/internal/stream"
)

// WebhookHandler handles Taobao webhook HTTP requests.
type WebhookHandler struct {
	adapter  *Adapter
	producer *stream.Producer
}

// NewWebhookHandler creates a handler for the Taobao webhook endpoint.
func NewWebhookHandler(adapter *Adapter, producer *stream.Producer) *WebhookHandler {
	return &WebhookHandler{
		adapter:  adapter,
		producer: producer,
	}
}

// ServeHTTP handles incoming Taobao webhook requests.
// Taobao sends all webhooks as POST requests (both challenge and messages).
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePost processes POST requests which can be either challenge or message.
func (h *WebhookHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	// Verify signature first
	if err := h.adapter.VerifyWebhook(r.Context(), r); err != nil {
		log.Printf("[taobao] signature verification failed: %v", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Read body (already restored by VerifyWebhook)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[taobao] read body error: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Check if this is a challenge request
	if challenge, ok := IsChallenge(body); ok {
		log.Printf("[taobao] webhook challenge received")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := map[string]string{"challenge": challenge}
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Parse inbound message
	var msg InboundMsg
	if err := json.Unmarshal(body, &msg); err != nil {
		log.Printf("[taobao] parse error: %v", err)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	// Convert to StandardMessage using parseInboundFromMsg
	stdMsg, err := h.adapter.parseInboundFromMsg(&msg)
	if err != nil {
		log.Printf("[taobao] convert error: %v", err)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	// Publish to inbound stream
	if h.producer != nil {
		streamID, err := h.producer.Publish(r.Context(), stdMsg)
		if err != nil {
			log.Printf("[taobao] publish error: %v", err)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("success"))
			return
		}

		log.Printf("[taobao] message published: id=%s stream=%s from=%s type=%s",
			stdMsg.ID, streamID, stdMsg.Data.PlatformMeta.PlatformUserID, stdMsg.Data.Content.Type)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("success"))
}
