package observability_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
	"github.com/whoyoshome/flashdeal-service/internal/platform/observability"
)

// recordedTracing returns a Tracing whose finished spans land in the
// returned recorder.
func recordedTracing(t *testing.T) (*observability.Tracing, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return observability.TracingWith(tp), rec
}

func attr(span sdktrace.ReadOnlySpan, key string) attribute.Value {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	return attribute.Value{}
}

func TestHTTPMiddleware(t *testing.T) {
	t.Parallel()

	serve := func(t *testing.T, tr *observability.Tracing, status int, headers map[string]string) {
		t.Helper()
		var inSpan bool
		r := chi.NewRouter()
		r.Use(tr.HTTPMiddleware)
		r.Post("/v1/deals/{id}/reservations", func(w http.ResponseWriter, r *http.Request) {
			inSpan = trace.SpanFromContext(r.Context()).IsRecording()
			w.WriteHeader(status)
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/deals/7/reservations", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
		require.True(t, inSpan, "handlers run inside the span")
	}

	t.Run("names the server span after the route pattern and records the status", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)

		serve(t, tr, http.StatusConflict, nil)

		spans := rec.Ended()
		require.Len(t, spans, 1)
		require.Equal(t, "POST /v1/deals/{id}/reservations", spans[0].Name())
		require.Equal(t, trace.SpanKindServer, spans[0].SpanKind())
		require.Equal(t, "/v1/deals/{id}/reservations", attr(spans[0], "http.route").AsString())
		require.EqualValues(t, http.StatusConflict, attr(spans[0], "http.response.status_code").AsInt64())
		require.Equal(t, codes.Unset, spans[0].Status().Code, "a business rejection is not a failure")
	})

	t.Run("marks a server error as failed", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)

		serve(t, tr, http.StatusServiceUnavailable, nil)

		require.Equal(t, codes.Error, rec.Ended()[0].Status().Code)
	})

	t.Run("continues the caller's trace from the traceparent header", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)

		serve(t, tr, http.StatusCreated, map[string]string{
			"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		})

		span := rec.Ended()[0]
		require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", span.SpanContext().TraceID().String())
		require.Equal(t, "00f067aa0ba902b7", span.Parent().SpanID().String())
	})
}

func TestPgxTracer(t *testing.T) {
	t.Parallel()

	t.Run("creates a child span per query with its parameterized SQL", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)
		ctx, end := tr.StartSpan(t.Context(), "parent")
		pt := tr.PgxTracer()

		qctx := pt.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "\n  update deals set reserved = reserved + $2 where id = $1"})
		pt.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("UPDATE 1")})
		end(nil)

		spans := rec.Ended()
		require.Len(t, spans, 2)
		query := spans[0]
		require.Equal(t, "postgres UPDATE", query.Name())
		require.Equal(t, trace.SpanKindClient, query.SpanKind())
		require.Contains(t, attr(query, "db.query.text").AsString(), "reserved + $2")
		require.EqualValues(t, 1, attr(query, "db.response.affected_rows").AsInt64())
		require.Equal(t, spans[1].SpanContext().SpanID(), query.Parent().SpanID())
	})

	t.Run("marks a failed query", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)
		ctx, end := tr.StartSpan(t.Context(), "parent")
		pt := tr.PgxTracer()

		qctx := pt.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "select 1"})
		pt.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{Err: errors.New("deadlock detected")})
		end(nil)

		require.Equal(t, codes.Error, rec.Ended()[0].Status().Code)
		require.Equal(t, codes.Unset, rec.Ended()[1].Status().Code, "the parent decides its own status")
	})

	t.Run("records nothing for a query outside a trace", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)
		pt := tr.PgxTracer()

		qctx := pt.TraceQueryStart(t.Context(), nil, pgx.TraceQueryStartData{SQL: "select 1"})
		pt.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{})

		require.Empty(t, rec.Ended())
	})
}

func TestRedisHook(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, ctx context.Context, tr *observability.Tracing, err error) {
		t.Helper()
		next := func(context.Context, redis.Cmder) error { return err }
		_ = tr.RedisHook().ProcessHook(next)(ctx, redis.NewStringCmd(ctx, "get", "deal:{7}:stock"))
	}

	t.Run("creates a client span per command", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)
		ctx, end := tr.StartSpan(t.Context(), "parent")

		run(t, ctx, tr, nil)
		end(nil)

		cmd := rec.Ended()[0]
		require.Equal(t, "redis get", cmd.Name())
		require.Equal(t, trace.SpanKindClient, cmd.SpanKind())
		require.Equal(t, "redis", attr(cmd, "db.system.name").AsString())
	})

	t.Run("treats a missing key as an answer, not a failure", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)
		ctx, end := tr.StartSpan(t.Context(), "parent")

		run(t, ctx, tr, redis.Nil)
		end(nil)

		require.Equal(t, codes.Unset, rec.Ended()[0].Status().Code)
	})

	t.Run("marks a failed command", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)
		ctx, end := tr.StartSpan(t.Context(), "parent")

		run(t, ctx, tr, errors.New("connection refused"))
		end(nil)

		require.Equal(t, codes.Error, rec.Ended()[0].Status().Code)
	})

	t.Run("records nothing for a command outside a trace", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)

		run(t, t.Context(), tr, nil)

		require.Empty(t, rec.Ended())
	})

	t.Run("creates one span per pipeline", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)
		ctx, end := tr.StartSpan(t.Context(), "parent")
		next := func(context.Context, []redis.Cmder) error { return nil }

		err := tr.RedisHook().ProcessPipelineHook(next)(ctx, []redis.Cmder{redis.NewStatusCmd(ctx, "ping"), redis.NewStatusCmd(ctx, "ping")})
		end(nil)

		require.NoError(t, err)
		require.Equal(t, "redis pipeline", rec.Ended()[0].Name())
		require.EqualValues(t, 2, attr(rec.Ended()[0], "db.operation.batch.size").AsInt64())
	})
}

func TestTracing(t *testing.T) {
	t.Parallel()

	t.Run("StartSpan ends the span with the work's error", func(t *testing.T) {
		t.Parallel()
		tr, rec := recordedTracing(t)

		_, end := tr.StartSpan(t.Context(), "worker.expire")
		end(errors.New("connection reset"))

		require.Equal(t, "worker.expire", rec.Ended()[0].Name())
		require.Equal(t, codes.Error, rec.Ended()[0].Status().Code)
	})

	t.Run("is a no-op when no endpoint is configured", func(t *testing.T) {
		t.Parallel()
		tr, err := observability.NewTracing(t.Context(), config.Tracing{SampleRatio: 1})
		require.NoError(t, err)

		ctx, end := tr.StartSpan(t.Context(), "anything")
		defer end(nil)

		require.False(t, trace.SpanFromContext(ctx).IsRecording())
		require.NoError(t, tr.Shutdown(t.Context()))
	})

	t.Run("exports spans to the collector's OTLP/HTTP traces path", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var paths []string
		collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.Method+" "+r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(collector.Close)
		// A trailing slash on the base URL must not matter.
		tr, err := observability.NewTracing(t.Context(), config.Tracing{OTLPEndpoint: collector.URL + "/", SampleRatio: 1})
		require.NoError(t, err)

		ctx, end := tr.StartSpan(t.Context(), "worker.expire")
		require.True(t, trace.SpanFromContext(ctx).IsRecording())
		end(nil)
		shutdownCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		require.NoError(t, tr.Shutdown(shutdownCtx), "shutdown flushes the batch")

		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, []string{"POST /v1/traces"}, paths)
	})
}
