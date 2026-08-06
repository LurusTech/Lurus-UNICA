package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/auth"
)

// BeforeStateFunc retrieves the current state of a resource before mutation.
// It receives the request and returns the JSON state and the resource ID.
type BeforeStateFunc func(r *http.Request) (state json.RawMessage, resourceID string, productLineID string, err error)

// AfterStateFunc retrieves the state of a resource after mutation.
// It receives the request and the resource ID from the before-state call.
type AfterStateFunc func(r *http.Request, resourceID string) (state json.RawMessage, err error)

// responseRecorder captures the status code from the handler.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	rr.body.Write(b)
	return rr.ResponseWriter.Write(b)
}

// Middleware creates an HTTP middleware that captures audit events around write operations.
// resourceType identifies the kind of resource being mutated (e.g. "channel_config", "ai_config").
// beforeFn retrieves the current state before the handler runs (nil for create operations).
// afterFn retrieves the new state after the handler runs (nil for delete operations).
func Middleware(
	logger *Logger,
	resourceType string,
	beforeFn BeforeStateFunc,
	afterFn AfterStateFunc,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only audit write operations
			action := methodToAction(r.Method)
			if action == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Save request body for replay (needed by the handler)
			var bodyBytes []byte
			if r.Body != nil {
				bodyBytes, _ = io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}

			// Capture before state
			var beforeState json.RawMessage
			var resourceID, productLineID string
			if beforeFn != nil {
				var err error
				beforeState, resourceID, productLineID, err = beforeFn(r)
				if err != nil {
					// If we can't get before state, log it but continue with the handler
					// (don't block the operation because of audit failure)
				}
				// Reset body after before-state read may have consumed it
				if bodyBytes != nil {
					r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				}
			}

			// Execute the handler
			rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rec, r)

			// Only log if the operation succeeded (2xx status)
			if rec.statusCode < 200 || rec.statusCode >= 300 {
				return
			}

			// Capture after state
			var afterState json.RawMessage
			if afterFn != nil {
				if action == "create" {
					// For creates, extract resource ID from response body
					afterState, resourceID, productLineID = extractCreateResponse(rec.body.Bytes())
				} else {
					var err error
					afterState, err = afterFn(r, resourceID)
					if err != nil {
						// Use response body as fallback for after state
						if rec.body.Len() > 0 {
							afterState = json.RawMessage(rec.body.Bytes())
						}
					}
				}
			}

			// Extract actor info from JWT claims
			claims := auth.GetClaims(r.Context())
			actorID := "unknown"
			actorRole := "unknown"
			if claims != nil {
				actorID = claims.UserID
				actorRole = claims.Role
			}

			// Get client IP
			ipAddress := ExtractIP(r)

			var plIDPtr *string
			if productLineID != "" {
				plIDPtr = &productLineID
			}

			var beforePtr, afterPtr *json.RawMessage
			if beforeState != nil {
				beforePtr = &beforeState
			}
			if afterState != nil {
				afterPtr = &afterState
			}

			entry := &AuditEntry{
				ActorID:       actorID,
				ActorRole:     actorRole,
				Action:        action,
				ResourceType:  resourceType,
				ResourceID:    resourceID,
				ProductLineID: plIDPtr,
				BeforeState:   beforePtr,
				AfterState:    afterPtr,
			}
			if ipAddress != "" {
				entry.IPAddress = &ipAddress
			}

			logger.Log(entry)
		})
	}
}

// methodToAction maps HTTP methods to audit actions.
func methodToAction(method string) string {
	switch method {
	case http.MethodPost:
		return "create"
	case http.MethodPut, http.MethodPatch:
		return "update"
	case http.MethodDelete:
		return "delete"
	default:
		return ""
	}
}

// ExtractIP extracts the client IP address from the request, preferring
// proxy headers and stripping the port. Exported because handlers that write
// audit entries directly (ontology, violations) need the same inet-safe value
// the middleware records — audit_logs.ip_address is a Postgres inet column,
// and a raw host:port RemoteAddr fails the insert.
func ExtractIP(r *http.Request) string {
	// Check X-Forwarded-For header first
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	// Check X-Real-IP
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	// Fall back to RemoteAddr
	addr := r.RemoteAddr
	// Remove port number
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		addr = addr[:idx]
	}
	// Clean up IPv6 brackets
	addr = strings.TrimPrefix(addr, "[")
	addr = strings.TrimSuffix(addr, "]")
	if addr == "" {
		return ""
	}
	return addr
}

// extractCreateResponse tries to extract id and product_line_id from a JSON response body.
func extractCreateResponse(body []byte) (afterState json.RawMessage, resourceID, productLineID string) {
	if len(body) == 0 {
		return nil, "", ""
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return json.RawMessage(body), "", ""
	}

	if id, ok := resp["id"].(string); ok {
		resourceID = id
	}
	if plID, ok := resp["product_line_id"].(string); ok {
		productLineID = plID
	}

	return json.RawMessage(body), resourceID, productLineID
}
