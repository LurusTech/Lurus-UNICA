package wechat

import (
	"log"
	"net/http"

	"github.com/kefu/unica/gateway/internal/stream"
)

// WebhookHandler handles WeChat webhook HTTP requests (both GET verification and POST messages).
type WebhookHandler struct {
	adapter  *Adapter
	producer *stream.Producer
}

// NewWebhookHandler creates a handler for the WeChat webhook endpoint.
func NewWebhookHandler(adapter *Adapter, producer *stream.Producer) *WebhookHandler {
	return &WebhookHandler{
		adapter:  adapter,
		producer: producer,
	}
}

// ServeHTTP routes GET (verification) and POST (message) requests.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleVerify(w, r)
	case http.MethodPost:
		h.handleMessage(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleVerify responds to WeChat's webhook URL verification request.
// WeChat sends GET with signature, timestamp, nonce, echostr.
// If signature is valid, return echostr as plain text.
func (h *WebhookHandler) handleVerify(w http.ResponseWriter, r *http.Request) {
	if err := h.adapter.VerifyWebhook(r.Context(), r); err != nil {
		log.Printf("[wechat] verification failed: %v", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	echostr := r.URL.Query().Get("echostr")
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(echostr))
	log.Printf("[wechat] webhook verification successful")
}

// handleMessage processes an inbound WeChat message.
func (h *WebhookHandler) handleMessage(w http.ResponseWriter, r *http.Request) {
	// Verify signature first
	if err := h.adapter.VerifyWebhook(r.Context(), r); err != nil {
		log.Printf("[wechat] message signature invalid: %v", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Parse inbound message
	msg, err := h.adapter.ParseInbound(r.Context(), r)
	if err != nil {
		log.Printf("[wechat] parse error: %v", err)
		// Return 200 to prevent WeChat from retrying on parse errors
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	// Publish to inbound stream
	streamID, err := h.producer.Publish(r.Context(), msg)
	if err != nil {
		log.Printf("[wechat] publish error: %v", err)
		// Return 200 anyway — WeChat requires fast response, retries are handled internally
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	log.Printf("[wechat] message published: id=%s stream=%s from=%s type=%s",
		msg.ID, streamID, msg.Data.PlatformMeta.PlatformUserID, msg.Data.Content.Type)

	// WeChat expects "success" or empty response
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("success"))
}
