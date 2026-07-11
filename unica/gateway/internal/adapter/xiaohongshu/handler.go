package xiaohongshu

import (
	"io"
	"log"
	"net/http"

	"github.com/kefu/unica/gateway/internal/stream"
)

// WebhookHandler handles XHS webhook HTTP requests (challenge verification and messages).
type WebhookHandler struct {
	adapter  *Adapter
	producer *stream.Producer
}

// NewWebhookHandler creates a handler for the XHS webhook endpoint.
func NewWebhookHandler(adapter *Adapter, producer *stream.Producer) *WebhookHandler {
	return &WebhookHandler{
		adapter:  adapter,
		producer: producer,
	}
}

// ServeHTTP routes incoming webhook requests.
// XHS uses POST for both challenge verification and message delivery.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePost processes POST requests: challenge verification or inbound messages.
func (h *WebhookHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	// Verify signature (this also reads and re-buffers the body)
	if err := h.adapter.VerifyWebhook(r.Context(), r); err != nil {
		log.Printf("[xiaohongshu] verification failed: %v", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Peek at the body to check for challenge
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[xiaohongshu] read body error: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Check if this is a challenge request
	if challenge, ok := IsChallenge(body); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"challenge":"` + challenge + `"}`))
		log.Printf("[xiaohongshu] webhook challenge verified")
		return
	}

	// Re-buffer body for ParseInbound
	r.Body = io.NopCloser(readCloserFromBytes(body))

	// Parse inbound message
	msg, err := h.adapter.ParseInbound(r.Context(), r)
	if err != nil {
		log.Printf("[xiaohongshu] parse error: %v", err)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":0}`))
		return
	}

	// Publish to inbound stream
	streamID, err := h.producer.Publish(r.Context(), msg)
	if err != nil {
		log.Printf("[xiaohongshu] publish error: %v", err)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":0}`))
		return
	}

	log.Printf("[xiaohongshu] message published: id=%s stream=%s from=%s type=%s",
		msg.ID, streamID, msg.Data.PlatformMeta.PlatformUserID, msg.Data.Content.Type)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"code":0}`))
}

// readCloserFromBytes wraps a byte slice into a bytes.Reader.
func readCloserFromBytes(b []byte) *bytesReader {
	return &bytesReader{data: b, pos: 0}
}

// bytesReader is a simple io.Reader over a byte slice.
type bytesReader struct {
	data []byte
	pos  int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
