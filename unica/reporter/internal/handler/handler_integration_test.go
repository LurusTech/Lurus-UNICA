package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kefu/unica/reporter/internal/repository"
)

// TestHandlerRegistration verifies all 4 API endpoints are registered and respond.
func TestHandlerRegistration(t *testing.T) {
	// We can't connect to a real DB in unit tests, but we can verify
	// that the routes are properly registered and handle GET vs non-GET.
	h := NewHandler(nil) // nil repo will panic on actual query, but not on method check
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	endpoints := []string{
		"/api/v1/reports/ai-effectiveness",
		"/api/v1/reports/agent-performance",
		"/api/v1/reports/top-questions",
		"/api/v1/reports/channel-traffic",
	}

	for _, ep := range endpoints {
		t.Run("POST_"+ep, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, ep, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("POST %s: got %d, want %d", ep, w.Code, http.StatusMethodNotAllowed)
			}
		})

		t.Run("DELETE_"+ep, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, ep, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("DELETE %s: got %d, want %d", ep, w.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

// TestNewHandler verifies handler creation.
func TestNewHandler(t *testing.T) {
	repo := repository.NewRepository(nil)
	h := NewHandler(repo)
	if h == nil {
		t.Fatal("handler should not be nil")
	}
	if h.repo != repo {
		t.Error("handler repo not set correctly")
	}
}

// TestErrorResponseFormat verifies error responses have correct JSON format.
func TestErrorResponseFormat(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusInternalServerError, "something went wrong")

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if resp["error"] != "something went wrong" {
		t.Errorf("error message: got %q, want %q", resp["error"], "something went wrong")
	}
}
