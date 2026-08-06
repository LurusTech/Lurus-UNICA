package handler

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLimitRequestBody covers the arrangement the ai-config route relies on: a
// body-buffering middleware sits inside the ceiling, and the handler's own
// smaller limit still decides the answer.
func TestLimitRequestBody(t *testing.T) {
	const ceiling = 64
	const handlerLimit = 32

	// bufferingMW stands in for the audit middleware, which reads the body whole
	// and hands the handler a replay of it.
	bufferingMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if int64(len(body)) > ceiling {
				t.Errorf("middleware buffered %d bytes, above the %d ceiling", len(body), ceiling)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}

	var read int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, handlerLimit)
		body, err := io.ReadAll(r.Body)
		read = len(body)
		var maxErr *http.MaxBytesError
		if err != nil && errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	stack := LimitRequestBody(ceiling)(bufferingMW(inner))

	cases := []struct {
		name string
		size int
		want int
	}{
		{"under the handler limit", 10, http.StatusOK},
		{"at the handler limit", handlerLimit, http.StatusOK},
		{"over the handler limit", handlerLimit + 1, http.StatusRequestEntityTooLarge},
		{"far over the ceiling", 4096, http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			read = 0
			req := httptest.NewRequest(http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/documents",
				strings.NewReader(strings.Repeat("a", c.size)))
			w := httptest.NewRecorder()
			stack.ServeHTTP(w, req)
			if w.Code != c.want {
				t.Errorf("status = %d, want %d (read %d bytes)", w.Code, c.want, read)
			}
		})
	}
}

func TestExtractPathParam(t *testing.T) {
	tests := []struct {
		path     string
		prefix   string
		expected string
	}{
		{"/api/v1/users/abc-123", "/api/v1/users/", "abc-123"},
		{"/api/v1/users/abc-123/roles", "/api/v1/users/", "abc-123"},
		{"/api/v1/product-lines/pl-1", "/api/v1/product-lines/", "pl-1"},
		{"/api/v1/users/", "/api/v1/users/", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ExtractPathParam(tt.path, tt.prefix)
			if got != tt.expected {
				t.Errorf("ExtractPathParam(%q, %q) = %q, want %q", tt.path, tt.prefix, got, tt.expected)
			}
		})
	}
}

func TestExtractPathSegments(t *testing.T) {
	tests := []struct {
		path     string
		prefix   string
		expected []string
	}{
		{"/api/v1/users/abc-123/roles", "/api/v1/users/", []string{"abc-123", "roles"}},
		{"/api/v1/users/abc-123/roles/role-1", "/api/v1/users/", []string{"abc-123", "roles", "role-1"}},
		{"/api/v1/users/", "/api/v1/users/", nil},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ExtractPathSegments(tt.path, tt.prefix)
			if len(got) != len(tt.expected) {
				t.Errorf("ExtractPathSegments(%q, %q) length = %d, want %d", tt.path, tt.prefix, len(got), len(tt.expected))
				return
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("segment[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}
