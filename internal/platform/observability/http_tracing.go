package observability

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// statusWriter captures the status code sent to the client.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// HTTPMiddleware starts a server span per request, continuing the caller's
// trace when the request carries a W3C traceparent header. The span is named
// after the route pattern once routing is done ("POST
// /v1/deals/{id}/reservations"), so one route, and thus one use case, is one
// kind of span. Install it inside the chi router so the pattern is known.
func (t *Tracing) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := t.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := t.tracer.Start(ctx, r.Method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("url.path", r.URL.Path),
			),
		)
		defer span.End()

		rec := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r.WithContext(ctx))

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			span.SetName(r.Method + " " + rc.RoutePattern())
			span.SetAttributes(attribute.String("http.route", rc.RoutePattern()))
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		// Client errors are expected outcomes (sold out, limit reached); only
		// server errors mark the span as failed.
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	})
}
