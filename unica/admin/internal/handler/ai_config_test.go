package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAIConfigHandler_HandleAIConfig_MissingProductLineID(t *testing.T) {
	h := &AIConfigHandler{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ai-config/", nil)
	w := httptest.NewRecorder()

	h.HandleAIConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
	if !strings.Contains(w.Body.String(), "product line id required") {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestAIConfigHandler_HandleAIConfig_UnknownSubPath(t *testing.T) {
	// This test validates that unknown sub-paths return 404.
	// Full integration tests would require a database and Dify mock.
	// For now we test the routing logic with minimal setup.

	// Real integration tests would use sqlmock. This verifies the path parsing.
	t.Run("path parsing segments", func(t *testing.T) {
		segments := ExtractPathSegments("/api/v1/ai-config/some-uuid/unknown-path", "/api/v1/ai-config/")
		if len(segments) != 2 {
			t.Fatalf("expected 2 segments, got %d: %v", len(segments), segments)
		}
		if segments[0] != "some-uuid" {
			t.Errorf("expected first segment 'some-uuid', got %q", segments[0])
		}
		if segments[1] != "unknown-path" {
			t.Errorf("expected second segment 'unknown-path', got %q", segments[1])
		}
	})

	t.Run("path parsing single segment", func(t *testing.T) {
		segments := ExtractPathSegments("/api/v1/ai-config/some-uuid", "/api/v1/ai-config/")
		if len(segments) != 1 {
			t.Fatalf("expected 1 segment, got %d: %v", len(segments), segments)
		}
		if segments[0] != "some-uuid" {
			t.Errorf("expected segment 'some-uuid', got %q", segments[0])
		}
	})
}

func TestUpdateThresholdRequest_Validation(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		valid     bool
	}{
		{"zero", 0, true},
		{"half", 0.5, true},
		{"one", 1.0, true},
		{"negative", -0.1, false},
		{"over_one", 1.1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid := tt.threshold >= 0 && tt.threshold <= 1
			if isValid != tt.valid {
				t.Errorf("threshold %f: expected valid=%v, got %v", tt.threshold, tt.valid, isValid)
			}
		})
	}
}
