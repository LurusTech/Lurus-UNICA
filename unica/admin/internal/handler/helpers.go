package handler

import (
	"encoding/json"
	"net/http"
	"strings"
)

// JSON writes a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

// ErrorJSON writes a JSON error response.
func ErrorJSON(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}

// DecodeJSON decodes JSON from the request body into the given value.
func DecodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// LimitRequestBody caps a request body before any middleware wrapped inside it
// reads the body whole. The audit middleware buffers request bodies for replay,
// so without a ceiling above it a single large upload becomes unbounded memory
// there no matter what limit the handler applies.
//
// The ceiling must sit above the handler's own limit: the handler has to be able
// to read past its limit to answer 413, and would otherwise see a body truncated
// to exactly the limit and report it as malformed instead.
func LimitRequestBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ExtractPathParam extracts a path segment from a URL path.
// For example, extracting "id" from "/api/v1/users/{id}" given the prefix "/api/v1/users/".
func ExtractPathParam(path, prefix string) string {
	trimmed := strings.TrimPrefix(path, prefix)
	// Remove trailing slash and any further path segments
	if idx := strings.Index(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return trimmed
}

// ExtractPathSegments splits the remaining path after a prefix into segments.
func ExtractPathSegments(path, prefix string) []string {
	trimmed := strings.TrimPrefix(path, prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
