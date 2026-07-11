package handler

import (
	"testing"
)

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
