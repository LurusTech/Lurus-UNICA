// Package identity is the platform's identity and tenant layer: accounts, the
// tenant roster, and the lifecycle that brings a tenant into existence or takes
// it away. Everything a tenant owns afterwards — knowledge, channels, facts, AI
// settings — lives in its own module one layer below and is never reached from
// here except through this package's own orchestration, which is what keeps
// onboarding in one place instead of spread across handlers borrowing each
// other's methods.
package identity

import (
	"encoding/json"
	"net/http"
	"strings"
)

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

// errorJSON writes a JSON error response.
func errorJSON(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// decodeJSON decodes JSON from the request body into the given value.
func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// pathSegments splits the remaining path after a prefix into segments.
func pathSegments(path, prefix string) []string {
	trimmed := strings.TrimPrefix(path, prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
