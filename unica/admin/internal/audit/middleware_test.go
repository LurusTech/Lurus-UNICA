package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMethodToAction(t *testing.T) {
	tests := []struct {
		method   string
		expected string
	}{
		{http.MethodPost, "create"},
		{http.MethodPut, "update"},
		{http.MethodPatch, "update"},
		{http.MethodDelete, "delete"},
		{http.MethodGet, ""},
		{http.MethodHead, ""},
		{http.MethodOptions, ""},
	}

	for _, tc := range tests {
		got := methodToAction(tc.method)
		if got != tc.expected {
			t.Errorf("methodToAction(%s) = %q, want %q", tc.method, got, tc.expected)
		}
	}
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		name     string
		xff      string
		xri      string
		remote   string
		expected string
	}{
		{"X-Forwarded-For single", "10.0.0.1", "", "192.168.1.1:1234", "10.0.0.1"},
		{"X-Forwarded-For multiple", "10.0.0.1, 10.0.0.2", "", "192.168.1.1:1234", "10.0.0.1"},
		{"X-Real-IP", "", "10.0.0.5", "192.168.1.1:1234", "10.0.0.5"},
		{"RemoteAddr with port", "", "", "192.168.1.1:1234", "192.168.1.1"},
		{"RemoteAddr IPv6", "", "", "[::1]:1234", "::1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xri != "" {
				r.Header.Set("X-Real-IP", tc.xri)
			}
			got := extractIP(r)
			if got != tc.expected {
				t.Errorf("extractIP() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestExtractCreateResponse(t *testing.T) {
	body := `{"id":"ch-123","product_line_id":"pl-456","name":"test"}`
	afterState, resourceID, productLineID := extractCreateResponse([]byte(body))

	if resourceID != "ch-123" {
		t.Errorf("expected resourceID ch-123, got %s", resourceID)
	}
	if productLineID != "pl-456" {
		t.Errorf("expected productLineID pl-456, got %s", productLineID)
	}
	if afterState == nil {
		t.Error("expected afterState to be set")
	}
}

func TestExtractCreateResponse_Empty(t *testing.T) {
	afterState, resourceID, productLineID := extractCreateResponse(nil)
	if afterState != nil || resourceID != "" || productLineID != "" {
		t.Error("expected all empty for nil body")
	}
}

func TestMiddleware_SkipsGET(t *testing.T) {
	logger := &Logger{
		entries: make(chan *AuditEntry, 10),
		done:    make(chan struct{}),
	}

	innerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	mw := Middleware(logger, "test", nil, nil)
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !innerCalled {
		t.Error("expected inner handler to be called")
	}

	// Should not produce an audit entry
	select {
	case <-logger.entries:
		t.Error("expected no audit entry for GET")
	default:
		// expected
	}
}

func TestMiddleware_LogsCreateOnPOST(t *testing.T) {
	logger := &Logger{
		entries: make(chan *AuditEntry, 10),
		done:    make(chan struct{}),
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"id":              "new-123",
			"product_line_id": "pl-1",
		})
	})

	mw := Middleware(logger, "channel_config", nil, nil)
	handler := mw(inner)

	body := `{"name":"test channel"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.1:5000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	select {
	case entry := <-logger.entries:
		if entry.Action != "create" {
			t.Errorf("expected action create, got %s", entry.Action)
		}
		if entry.ResourceType != "channel_config" {
			t.Errorf("expected resource_type channel_config, got %s", entry.ResourceType)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for audit entry")
	}
}

func TestMiddleware_DoesNotLogOnError(t *testing.T) {
	logger := &Logger{
		entries: make(chan *AuditEntry, 10),
		done:    make(chan struct{}),
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	mw := Middleware(logger, "test", nil, nil)
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	select {
	case <-logger.entries:
		t.Error("expected no audit entry for error response")
	default:
		// expected
	}
}

func TestMiddleware_CapturesBeforeAfterState(t *testing.T) {
	logger := &Logger{
		entries: make(chan *AuditEntry, 10),
		done:    make(chan struct{}),
	}

	beforeState := json.RawMessage(`{"name":"old"}`)
	afterState := json.RawMessage(`{"name":"new"}`)

	beforeFn := func(r *http.Request) (json.RawMessage, string, string, error) {
		return beforeState, "res-1", "pl-1", nil
	}
	afterFn := func(r *http.Request, resourceID string) (json.RawMessage, error) {
		return afterState, nil
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := Middleware(logger, "channel_config", beforeFn, afterFn)
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/channels/res-1", strings.NewReader(`{"name":"new"}`))
	req.RemoteAddr = "10.0.0.1:5000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	select {
	case entry := <-logger.entries:
		if entry.Action != "update" {
			t.Errorf("expected action update, got %s", entry.Action)
		}
		if entry.ResourceID != "res-1" {
			t.Errorf("expected resource_id res-1, got %s", entry.ResourceID)
		}
		if entry.ProductLineID == nil || *entry.ProductLineID != "pl-1" {
			t.Error("expected product_line_id pl-1")
		}
		if entry.BeforeState == nil {
			t.Error("expected before_state")
		}
		if entry.AfterState == nil {
			t.Error("expected after_state")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for audit entry")
	}
}

func TestResponseRecorder(t *testing.T) {
	w := httptest.NewRecorder()
	rr := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

	rr.WriteHeader(http.StatusCreated)
	rr.Write([]byte(`{"id":"1"}`))

	if rr.statusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", rr.statusCode)
	}
	if rr.body.String() != `{"id":"1"}` {
		t.Errorf("expected body to be captured, got %s", rr.body.String())
	}
}
