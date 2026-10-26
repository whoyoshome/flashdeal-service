package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// IdempotencyKeyHeader makes reservation requests safe to retry.
const IdempotencyKeyHeader = "Idempotency-Key"

const (
	// maxBodyBytes caps request bodies; every request in the API is tiny.
	maxBodyBytes         = 64 << 10
	maxIdempotencyKeyLen = 255
)

// decodeJSON reads exactly one JSON object into dst, rejecting unknown
// fields so client typos fail loudly instead of being silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return domain.NewValidation("request body too large")
		}
		return domain.NewValidation("request body must be a valid JSON object with known fields")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.NewValidation("request body must contain a single JSON object")
	}
	return nil
}

// dealIDParam reads the {id} path parameter as a positive deal ID.
func dealIDParam(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.NewValidation("deal id must be a positive integer")
	}
	return id, nil
}

// idempotencyKey reads the required Idempotency-Key header.
func idempotencyKey(r *http.Request) (string, error) {
	key := r.Header.Get(IdempotencyKeyHeader)
	if key == "" || len(key) > maxIdempotencyKeyLen {
		return "", domain.NewValidation("Idempotency-Key header is required (1-255 characters)")
	}
	for _, c := range key {
		if !unicode.IsPrint(c) {
			return "", domain.NewValidation("Idempotency-Key header contains invalid characters")
		}
	}
	return key, nil
}

// queryInt64 parses an optional non-negative integer query parameter.
func queryInt64(r *http.Request, name string) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, domain.NewValidation(name + " must be a non-negative integer")
	}
	return v, nil
}
