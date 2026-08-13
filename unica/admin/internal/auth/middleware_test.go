package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/rbac"
)

const tenantPrefix = "/api/v1/tenants/"

func TestAuthMiddleware_ValidToken(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)
	pair, _ := mgr.GenerateTokenPair("user-1", "a@b.com", rbac.RoleAdmin, "")

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

func TestRequireAdmin(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	cases := []struct {
		name     string
		role     string
		tenantID string
		want     int
	}{
		{"admin admitted", rbac.RoleAdmin, "", http.StatusOK},
		{"user refused", rbac.RoleUser, "tenant-1", http.StatusForbidden},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pair, _ := mgr.GenerateTokenPair("user-1", "a@b.com", c.role, c.tenantID)
			handler := AuthMiddleware(mgr)(RequireAdmin()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})))

			req := httptest.NewRequest("GET", "/api/v1/users", nil)
			req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != c.want {
				t.Errorf("status = %d, want %d", rr.Code, c.want)
			}
		})
	}
}

func TestRequireAdmin_Anonymous(t *testing.T) {
	handler := RequireAdmin()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/users", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

// tenantAuthProbe serves a tenant route and reports the tenant the middleware
// resolved, both from the context and from the rewritten path.
func tenantAuthProbe(t *testing.T, mgr *JWTManager, role, tenantID, path string) (int, string, string) {
	t.Helper()
	var gotCtx, gotPath string
	handler := AuthMiddleware(mgr)(TenantAuth(tenantPrefix)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCtx = TenantID(r.Context())
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})))

	pair, _ := mgr.GenerateTokenPair("user-1", "a@b.com", role, tenantID)
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr.Code, gotCtx, gotPath
}

func TestTenantAuth_AdminReachesAnyTenant(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	code, tenant, path := tenantAuthProbe(t, mgr, rbac.RoleAdmin, "", tenantPrefix+"pl-9/channels")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if tenant != "pl-9" {
		t.Errorf("resolved tenant = %q, want pl-9", tenant)
	}
	if path != tenantPrefix+"pl-9/channels" {
		t.Errorf("path = %q, want it untouched", path)
	}
}

func TestTenantAuth_UserReachesOwnTenant(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	code, tenant, _ := tenantAuthProbe(t, mgr, rbac.RoleUser, "pl-1", tenantPrefix+"pl-1/knowledge")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if tenant != "pl-1" {
		t.Errorf("resolved tenant = %q, want pl-1", tenant)
	}
}

func TestTenantAuth_UserRefusedOnAnotherTenant(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	code, _, _ := tenantAuthProbe(t, mgr, rbac.RoleUser, "pl-1", tenantPrefix+"pl-2/knowledge")
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
}

// TestTenantAuth_MeResolvesToOwnTenant pins that "me" both authorises and
// rewrites: the handler below must see a concrete id.
func TestTenantAuth_MeResolvesToOwnTenant(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	code, tenant, path := tenantAuthProbe(t, mgr, rbac.RoleUser, "pl-1", tenantPrefix+"me/facts/config")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if tenant != "pl-1" {
		t.Errorf("resolved tenant = %q, want pl-1", tenant)
	}
	if path != tenantPrefix+"pl-1/facts/config" {
		t.Errorf("path = %q, want the alias replaced by the tenant id", path)
	}
}

// TestTenantAuth_MeWithoutTenantIsRejected covers the admin using the alias:
// it belongs to no tenant, so "me" names nothing and the request is malformed
// rather than forbidden.
func TestTenantAuth_MeWithoutTenantIsRejected(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	code, _, _ := tenantAuthProbe(t, mgr, rbac.RoleAdmin, "", tenantPrefix+"me/channels")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

func TestTenantAuth_Anonymous(t *testing.T) {
	handler := TenantAuth(tenantPrefix)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", tenantPrefix+"pl-1/channels", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestTenantAuth_MissingTenantID(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)

	code, _, _ := tenantAuthProbe(t, mgr, rbac.RoleAdmin, "", tenantPrefix)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}
