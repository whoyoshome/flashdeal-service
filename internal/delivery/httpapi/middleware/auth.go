package middleware

import (
	"context"
	"net/http"

	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
)

type identityKey struct{}

// Authenticator verifies Bearer JWTs. *auth.Service implements it.
type Authenticator interface {
	ParseAuthorization(header string) (auth.Identity, error)
}

// Auth requires a valid Bearer JWT and stores the identity in the context.
// Requests without one get 401 before reaching any handler.
func Auth(tokens Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := tokens.ParseAuthorization(r.Header.Get("Authorization"))
			if err != nil {
				writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "missing or invalid Bearer token")
				return
			}
			ctx := context.WithValue(r.Context(), identityKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin rejects callers whose JWT role is not admin with 403.
// It must run after Auth.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RoleFrom(r.Context()) != auth.RoleAdmin {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// UserIDFrom returns the authenticated subject, or "" outside Auth.
func UserIDFrom(ctx context.Context) string {
	if id, ok := ctx.Value(identityKey{}).(auth.Identity); ok {
		return id.Subject
	}
	return ""
}

// RoleFrom returns the authenticated role, or "" outside Auth.
func RoleFrom(ctx context.Context) auth.Role {
	if id, ok := ctx.Value(identityKey{}).(auth.Identity); ok {
		return id.Role
	}
	return ""
}

// IdentityFrom returns the verified identity, or false outside Auth.
func IdentityFrom(ctx context.Context) (auth.Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(auth.Identity)
	return id, ok
}
