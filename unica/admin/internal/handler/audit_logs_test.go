package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
)

func TestAuditLogHandler_MethodNotAllowed(t *testing.T) {
	h := NewAuditLogHandler(nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audit-logs", nil)
	rec := httptest.NewRecorder()
	h.HandleAuditLogs(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestAuditLogHandler_Unauthorized(t *testing.T) {
	h := NewAuditLogHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs", nil)
	rec := httptest.NewRecorder()
	h.HandleAuditLogs(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuditLogHandler_ProductAdminEmptyPLIDs(t *testing.T) {
	h := NewAuditLogHandler(nil)

	claims := &auth.Claims{
		UserID:         "user-1",
		Role:           "product_admin",
		ProductLineIDs: []string{},
	}
	ctx := context.WithValue(context.Background(), auth.ClaimsKey, claims)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.HandleAuditLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}
