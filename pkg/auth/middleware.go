package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
)

// AllWarehouses is the warehouse scope that covers every warehouse.
const AllWarehouses = "ALL"

type claimsKey struct{}

// ClaimsFrom returns the verified claims that RequireAuth stored in ctx.
func ClaimsFrom(ctx context.Context) (*Claims, bool) {
	claims, ok := ctx.Value(claimsKey{}).(*Claims)
	return claims, ok && claims != nil
}

// RequireAuth verifies the Bearer access token and puts its claims in the request context.
// It fails closed: a missing, malformed, expired, wrongly signed or non-access token is a 401.
// Services verify the token themselves; they never trust identity headers.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, hasBearer := strings.CutPrefix(header, "Bearer ")
		token = strings.TrimSpace(token)
		if !hasBearer || token == "" {
			unauthorized(w, r, "UNAUTHORIZED", "A Bearer access token is required")
			return
		}
		claims, err := ParseAccessToken(token)
		if err != nil {
			unauthorized(w, r, "INVALID_ACCESS_TOKEN", "The access token is invalid or expired")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey{}, claims)))
	})
}

// RequireRole allows the request only when the token carries at least one of roles.
// It must run after RequireAuth; without claims it answers 401. Only the plural roles claim counts.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFrom(r.Context())
			if !ok {
				unauthorized(w, r, "UNAUTHORIZED", "Authentication is required")
				return
			}
			for _, role := range roles {
				if claims.HasRole(role) {
					next.ServeHTTP(w, r)
					return
				}
			}
			response.Error(w, r, http.StatusForbidden, "FORBIDDEN", "You do not have permission to perform this action", nil)
		})
	}
}

// CanAccessWarehouse reports whether claims allow acting in warehouseID. An empty warehouse id,
// missing claims or an empty scope list all deny access.
func CanAccessWarehouse(claims *Claims, warehouseID string) bool {
	if claims == nil || warehouseID == "" {
		return false
	}
	for _, scope := range claims.WarehouseScopes {
		if scope == AllWarehouses || scope == warehouseID {
			return true
		}
	}
	return false
}

// RequireWarehouseScope allows the request only when the token's warehouse scopes cover the warehouse that
// fromRequest names (path, query or similar). A request that names no warehouse is denied. It must run after
// RequireAuth. Handlers that only learn the warehouse from stored data call CanAccessWarehouse themselves.
func RequireWarehouseScope(fromRequest func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFrom(r.Context())
			if !ok {
				unauthorized(w, r, "UNAUTHORIZED", "Authentication is required")
				return
			}
			if !CanAccessWarehouse(claims, fromRequest(r)) {
				ForbiddenWarehouse(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ForbiddenWarehouse answers the standard 403 for a warehouse the caller may not act in.
func ForbiddenWarehouse(w http.ResponseWriter, r *http.Request) {
	response.Error(w, r, http.StatusForbidden, "FORBIDDEN_WAREHOUSE_SCOPE", "Warehouse access is forbidden", nil)
}

func unauthorized(w http.ResponseWriter, r *http.Request, code, message string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	response.Error(w, r, http.StatusUnauthorized, code, message, nil)
}
