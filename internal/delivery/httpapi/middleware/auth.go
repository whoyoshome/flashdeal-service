package middleware

import (
	"context"
	"net/http"
	"unicode"
)

// UserIDHeader identifies the caller. Real authentication is out of scope:
// this middleware is the seam where a JWT check would replace it.
const UserIDHeader = "X-User-ID"

const maxUserIDLen = 128

type userIDKey struct{}

// Auth requires a well-formed X-User-ID header and stores it in the context.
// Requests without one get 401 before reaching any handler.
func Auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(UserIDHeader)
		if !validUserID(id) {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "missing or invalid "+UserIDHeader+" header")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, id)))
	})
}

// UserIDFrom returns the authenticated user ID, or "" outside Auth.
func UserIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(userIDKey{}).(string)
	return id
}

func validUserID(id string) bool {
	if id == "" || len(id) > maxUserIDLen {
		return false
	}
	for _, c := range id {
		if !unicode.IsPrint(c) || unicode.IsSpace(c) {
			return false
		}
	}
	return true
}
