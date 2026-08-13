package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/rbac"
)

type contextKey string

const (
	// ClaimsKey is the context key for storing JWT claims.
	ClaimsKey contextKey = "claims"
	// TenantIDKey is the context key for the tenant a request resolved to.
	TenantIDKey contextKey = "tenant_id"
)

// MeTenant is the path alias a caller uses instead of spelling out its own
// tenant id. It resolves to the tenant in the caller's claims.
const MeTenant = "me"

// GetClaims extracts JWT claims from the request context.
func GetClaims(ctx context.Context) *Claims {
	claims, ok := ctx.Value(ClaimsKey).(*Claims)
	if !ok {
		return nil
	}
	return claims
}

// TenantID returns the tenant a tenant-scoped route resolved the request to,
// or "" outside those routes.
func TenantID(ctx context.Context) string {
	id, ok := ctx.Value(TenantIDKey).(string)
	if !ok {
		return ""
	}
	return id
}

// RequestTenant reports the single tenant a request is confined to: the one the
// route resolved from the path, or the caller's own outside those routes. An
// empty result means unscoped, which only an administrator ever is.
func RequestTenant(r *http.Request) string {
	if tenant := TenantID(r.Context()); tenant != "" {
		return tenant
	}
	claims := GetClaims(r.Context())
	if claims == nil || rbac.IsAdmin(claims.Role) {
		return ""
	}
	return claims.TenantID
}

// TenantScopeAllowed applies the tenant scoping rule shared by every per-tenant
// resource.
//
// The tenant routes resolve one tenant from the path before any handler runs,
// and that tenant is the whole answer: a request addressed to one tenant may
// not reach another's rows, whoever sent it. This matters for the resources
// addressed by their own id rather than the tenant's — a violation, say — where
// the row's tenant and the route's must be the same one.
//
// Outside those routes an admin is unscoped and a user is confined to its own
// tenant. A request without claims is left to the auth middleware; by the time
// a handler runs it means a package-internal test.
func TenantScopeAllowed(r *http.Request, tenantID string) bool {
	if tenant := TenantID(r.Context()); tenant != "" {
		return tenantID == tenant
	}
	claims := GetClaims(r.Context())
	if claims == nil || rbac.IsAdmin(claims.Role) {
		return true
	}
	return claims.TenantID != "" && tenantID == claims.TenantID
}

// AuthMiddleware validates JWT tokens from the Authorization header.
func AuthMiddleware(jwtMgr *JWTManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
				http.Error(w, `{"error":"invalid authorization format"}`, http.StatusUnauthorized)
				return
			}

			claims, err := jwtMgr.ValidateToken(parts[1])
			if err != nil {
				http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin admits only the platform administrator role. It guards the
// surfaces that are about the platform rather than about one tenant: accounts
// and the tenant roster itself.
func RequireAdmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			if !rbac.IsAdmin(claims.Role) {
				http.Error(w, `{"error":"administrator role required"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// TenantAuth guards the tenant-scoped route family that lives under prefix
// (".../tenants/"). It reads the {id} segment that follows the prefix, resolves
// the "me" alias, and applies the one access rule this platform has: an admin
// may act on any tenant, a user only on its own.
//
// The resolved id is put in the request context and written back into the URL
// path, so no handler below has to know that "me" exists or that the caller's
// claims are where it came from.
func TenantAuth(prefix string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			rest := strings.TrimPrefix(r.URL.Path, prefix)
			id := rest
			if i := strings.Index(id, "/"); i >= 0 {
				id = id[:i]
			}
			if id == "" {
				http.Error(w, `{"error":"tenant id required"}`, http.StatusBadRequest)
				return
			}

			resolved := id
			if id == MeTenant {
				if claims.TenantID == "" {
					// An admin belongs to no tenant, so "me" names nothing it
					// could stand for. Answering 403 here would claim the
					// account lacks access it in fact has; the request is
					// simply unanswerable as written.
					http.Error(w,
						`{"error":"me does not resolve: this account belongs to no tenant, name the tenant id explicitly"}`,
						http.StatusBadRequest)
					return
				}
				resolved = claims.TenantID
			}

			if !rbac.IsAdmin(claims.Role) && resolved != claims.TenantID {
				http.Error(w, `{"error":"access denied for this tenant"}`, http.StatusForbidden)
				return
			}

			ctx := context.WithValue(r.Context(), TenantIDKey, resolved)
			r2 := r.Clone(ctx)
			u := *r.URL
			u.Path = prefix + resolved + rest[len(id):]
			r2.URL = &u
			next.ServeHTTP(w, r2)
		})
	}
}
