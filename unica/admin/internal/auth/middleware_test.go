package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/rbac"
)

func TestAuthMiddleware_ValidToken(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)
	pair, _ := mgr.GenerateTokenPair("user-1", "a@b.com", "super_admin", []string{"super_admin"}, nil)

	handler := AuthMiddleware(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil {
			t.Fatal("expected claims in context")
		}
		if claims.UserID != "user-1" {
			t.Errorf("expected user_id 'user-1', got '%s'", claims.UserID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuthMiddleware_MissingHeader(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	handler := AuthMiddleware(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	handler := AuthMiddleware(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMiddleware_InvalidFormat(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	handler := AuthMiddleware(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "NotBearer token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestRequirePermission_SuperAdmin(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)
	pair, _ := mgr.GenerateTokenPair("user-1", "a@b.com", "super_admin", []string{"super_admin"}, nil)

	// SuperAdmin should bypass any permission check
	handler := AuthMiddleware(mgr)(RequirePermission(rbac.PermManageProductLines)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for SuperAdmin, got %d", rr.Code)
	}
}

func TestRequirePermission_InsufficientRole(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)
	pair, _ := mgr.GenerateTokenPair("user-1", "a@b.com", "agent", []string{"agent"}, []string{"pl-1"})

	// Agent should NOT have manage_product_lines permission
	handler := AuthMiddleware(mgr)(RequirePermission(rbac.PermManageProductLines)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	})))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

func TestRequireProductLineAccess(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)
	pair, _ := mgr.GenerateTokenPair("user-1", "a@b.com", "product_admin", []string{"product_admin"}, []string{"pl-1", "pl-2"})

	plExtractor := func(r *http.Request) string {
		return r.URL.Query().Get("product_line_id")
	}

	handler := AuthMiddleware(mgr)(RequireProductLineAccess(plExtractor)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	// Access allowed product line
	req := httptest.NewRequest("GET", "/test?product_line_id=pl-1", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	// Access denied product line
	req = httptest.NewRequest("GET", "/test?product_line_id=pl-3", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}
