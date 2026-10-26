package middleware

import (
	"encoding/json"
	"net/http"
)

// errorBody mirrors the API error format so responses written by middleware
// look exactly like those written by handlers.
type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// writeError writes the standard error body. The body is a fixed struct of
// strings, so encoding cannot fail; a write error means the client is gone.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	body, _ := json.Marshal(errorBody{Code: code, Message: msg, RequestID: RequestIDFrom(r.Context())})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
