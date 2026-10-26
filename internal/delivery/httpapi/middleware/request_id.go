// Package middleware holds the HTTP middlewares shared by every route. The
// chain order is fixed in CLAUDE.md: request ID, recoverer, logging, metrics,
// timeout, auth.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// RequestIDHeader carries the request ID in and out of the service.
const RequestIDHeader = "X-Request-ID"

const maxRequestIDLen = 128

type requestIDKey struct{}

// RequestID tags every request with an ID, reusing a well-formed one from the
// caller (for example a load balancer) so a request can be traced across
// services. The ID is echoed in the response header and in error bodies.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// RequestIDFrom returns the request ID stored by RequestID, or "" if none.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// validRequestID accepts short IDs made of safe characters only, so a caller
// cannot inject arbitrary text into logs and headers.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range id {
		if !isAlphaNum(c) && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

func isAlphaNum(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// newRequestID returns 16 random bytes as hex. crypto/rand cannot fail since
// Go 1.24 (it aborts the process instead), so there is no error to handle.
func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
