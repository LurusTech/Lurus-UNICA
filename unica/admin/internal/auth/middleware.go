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
)

// GetClaims extracts JWT claims from the request context.
func GetClaims(ctx context.Context) *Claims {
	claims, ok := ctx.Value(ClaimsKey).(*Claims)
	if !ok {
		return nil
	}
	return claims
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

// RequirePermission returns middleware that checks if the user has the required permission.
// For product-line scoped resources, it checks the product_line_id query param or route param.
func RequirePermission(permission rbac.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			// SuperAdmin bypasses all permission checks
			if claims.Role == string(rbac.RoleSuperAdmin) {
				next.ServeHTTP(w, r)
				return
			}

			// Check if any of the user's roles grant the required permission
			hasPermission := false
			for _, roleName := range claims.Roles {
				role := rbac.Role(roleName)
				if rbac.HasPermission(role, permission) {
					hasPermission = true
					break
				}
			}

			if !hasPermission {
				http.Error(w, `{"error":"insufficient permissions"}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireProductLineAccess verifies the user has access to the specified product line.
// productLineIDFunc extracts the product line ID from the request.
func RequireProductLineAccess(productLineIDFunc func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			// SuperAdmin has global access
			if claims.Role == string(rbac.RoleSuperAdmin) {
				next.ServeHTTP(w, r)
				return
			}

			// Empty product line IDs with non-SuperAdmin means no scoped access
			plID := productLineIDFunc(r)
			if plID == "" {
				next.ServeHTTP(w, r)
				return
			}

			for _, id := range claims.ProductLineIDs {
				if id == plID {
					next.ServeHTTP(w, r)
					return
				}
			}

			http.Error(w, `{"error":"access denied for this product line"}`, http.StatusForbidden)
		})
	}
}
