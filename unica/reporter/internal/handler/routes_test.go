package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseTimeRange_Defaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reports/ai-effectiveness", nil)
	tr := parseTimeRange(req)

	now := time.Now().UTC()
	expectedStart := now.AddDate(0, 0, -7)

	if tr.Start.Sub(expectedStart).Abs() > 2*time.Second {
		t.Errorf("default start mismatch: got %v, want ~%v", tr.Start, expectedStart)
	}
	if tr.End.Sub(now).Abs() > 2*time.Second {
		t.Errorf("default end mismatch: got %v, want ~%v", tr.End, now)
	}
}

func TestParseTimeRange_RFC3339(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/reports/ai-effectiveness?start=2026-01-01T00:00:00Z&end=2026-01-31T23:59:59Z", nil)
	tr := parseTimeRange(req)

	expectedStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)

	if !tr.Start.Equal(expectedStart) {
		t.Errorf("start: got %v, want %v", tr.Start, expectedStart)
	}
	if !tr.End.Equal(expectedEnd) {
		t.Errorf("end: got %v, want %v", tr.End, expectedEnd)
	}
}

func TestParseTimeRange_DateOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/reports/ai-effectiveness?start=2026-01-01&end=2026-01-31", nil)
	tr := parseTimeRange(req)

	expectedStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) // end of Jan 31

	if !tr.Start.Equal(expectedStart) {
		t.Errorf("start: got %v, want %v", tr.Start, expectedStart)
	}
	if !tr.End.Equal(expectedEnd) {
		t.Errorf("end: got %v, want %v", tr.End, expectedEnd)
	}
}

func TestParseTimeRange_InvalidFallsBackToDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/reports/ai-effectiveness?start=invalid&end=also-invalid", nil)
	tr := parseTimeRange(req)

	now := time.Now().UTC()
	expectedStart := now.AddDate(0, 0, -7)

	if tr.Start.Sub(expectedStart).Abs() > 2*time.Second {
		t.Errorf("invalid start should fall back to default: got %v", tr.Start)
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"key": "value"})

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q, want %q", ct, "application/json")
	}
	body := w.Body.String()
	if body == "" {
		t.Error("body should not be empty")
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "bad request")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
	body := w.Body.String()
	if body == "" {
		t.Error("body should not be empty")
	}
}

func TestResponseWriter_CapturesStatusCode(t *testing.T) {
	w := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	rw.WriteHeader(http.StatusNotFound)

	if rw.statusCode != http.StatusNotFound {
		t.Errorf("captured status: got %d, want %d", rw.statusCode, http.StatusNotFound)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	// Create a handler and register routes
	h := NewHandler(nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// POST should return 405
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reports/ai-effectiveness", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST should return 405, got %d", w.Code)
	}
}
