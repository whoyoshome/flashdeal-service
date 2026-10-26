package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// IdempotencyKeyHeader carries the client's idempotency key.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyReplayedHeader marks a response served from the store instead
// of running the request again.
const IdempotencyReplayedHeader = "Idempotency-Replayed"

const (
	// maxIdempotentBody caps how much of the body is read to fingerprint the
	// request; it matches the handlers' own limit.
	maxIdempotentBody = 64 << 10
	// storeTimeout bounds store writes that run after the response, detached
	// from the request.
	storeTimeout = 2 * time.Second
)

// IdempotencyConfig tunes the idempotency middleware.
type IdempotencyConfig struct {
	// LockTTL is how long a request holds its key while running. It must
	// exceed the request timeout so a live request never loses its claim.
	LockTTL time.Duration
	// ResponseTTL is how long a final response is replayed (24h by design).
	ResponseTTL time.Duration
}

// Idempotency makes a request safe to retry. The first request with a key
// runs and its final response (2xx or 4xx) is stored; retries with the same
// key and the same request get that response back without running again.
// A retry while the first is still running gets 409 IDEMPOTENCY_IN_PROGRESS,
// and the same key with a different request gets 409 CONFLICT. 5xx answers
// are not stored: they are retryable, so the next attempt runs for real.
//
// It must run after Auth, because keys are scoped per user. Requests without
// a key pass through; the handler rejects them.
func Idempotency(store usecase.IdempotencyStore, cfg IdempotencyConfig, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			fingerprint, err := fingerprintOf(r)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, string(domain.CodeValidation), "request body too large")
				return
			}

			ref := usecase.IdempotencyRef{UserID: UserIDFrom(r.Context()), Key: key}
			// The token identifies this request's claim; it reuses the request ID
			// generator (16 random bytes).
			token := newRequestID()
			claim, err := store.Begin(r.Context(), ref, fingerprint, token, cfg.LockTTL)
			if err != nil {
				// Without the store a retry could reserve twice: fail closed.
				log.ErrorContext(r.Context(), "idempotency store unavailable",
					slog.String("request_id", RequestIDFrom(r.Context())), slog.Any("error", err))
				writeError(w, r, http.StatusServiceUnavailable, string(domain.CodeUnavailable), "stock service unavailable")
				return
			}

			switch {
			case claim.Fingerprint != fingerprint:
				writeError(w, r, http.StatusConflict, string(domain.CodeConflict),
					"idempotency key already used for a different request")
			case claim.State == usecase.IdempotencyInProgress:
				w.Header().Set("Retry-After", "1")
				writeError(w, r, http.StatusConflict, codeIdempotencyInProgress,
					"a request with this idempotency key is still in progress")
			case claim.State == usecase.IdempotencyDone:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set(IdempotencyReplayedHeader, "true")
				w.WriteHeader(claim.Status)
				_, _ = w.Write(claim.Body) // a write error means the client is gone
			default:
				runAndStore(w, r, next, store, ref, fingerprint, token, cfg, log)
			}
		})
	}
}

// codeIdempotencyInProgress is only produced by this middleware.
const codeIdempotencyInProgress = "IDEMPOTENCY_IN_PROGRESS"

// runAndStore runs the request it owns the key for and records the outcome.
// Store writes run detached from the request so a client disconnect cannot
// leave the key claimed until its lock expires.
func runAndStore(
	w http.ResponseWriter, r *http.Request, next http.Handler, store usecase.IdempotencyStore,
	ref usecase.IdempotencyRef, fingerprint, token string, cfg IdempotencyConfig, log *slog.Logger,
) {
	rec := &capturingWriter{ResponseWriter: w}
	completed := false
	defer func() {
		if completed {
			return
		}
		// A 5xx or a panic: drop the claim so a retry runs again.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), storeTimeout)
		defer cancel()
		if err := store.Release(ctx, ref, token); err != nil {
			log.WarnContext(ctx, "idempotency release failed; the claim expires on its own",
				slog.String("request_id", RequestIDFrom(r.Context())), slog.Any("error", err))
		}
	}()

	next.ServeHTTP(rec, r)

	if rec.statusCode() >= http.StatusInternalServerError {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), storeTimeout)
	defer cancel()
	if err := store.Complete(ctx, ref, fingerprint, rec.statusCode(), rec.body.Bytes(), cfg.ResponseTTL); err != nil {
		// The response already went out. A retry will run again and the
		// database's UNIQUE (user_id, idempotency_key) returns the original.
		log.WarnContext(ctx, "idempotency complete failed",
			slog.String("request_id", RequestIDFrom(r.Context())), slog.Any("error", err))
		return
	}
	completed = true
}

// fingerprintOf hashes what defines the request (method, route and body)
// and restores the body for the handler.
func fingerprintOf(r *http.Request) (string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxIdempotentBody+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxIdempotentBody {
		return "", errors.New("body too large")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.Path + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// capturingWriter passes the response through while keeping a copy to store.
type capturingWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *capturingWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *capturingWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}

func (c *capturingWriter) statusCode() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *capturingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
