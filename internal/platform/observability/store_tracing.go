package observability

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Store calls only get a span inside a trace that is being recorded. Calls
// made outside any request or worker pass, such as connection health checks,
// would otherwise each start a trace of their own.

// pgxSpanKey carries the span started by TraceQueryStart to TraceQueryEnd.
// Keying it, instead of reading the current span, guarantees TraceQueryEnd
// never ends a span it did not start.
type pgxSpanKey struct{}

type pgxTracer struct {
	tracer trace.Tracer
}

// PgxTracer returns a pgx.QueryTracer that creates one client span per
// query, labelled with its parameterized SQL (values are never recorded).
func (t *Tracing) PgxTracer() pgx.QueryTracer {
	return pgxTracer{tracer: t.tracer}
}

func (p pgxTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return ctx
	}
	ctx, span := p.tracer.Start(ctx, "postgres "+sqlOperation(data.SQL),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system.name", "postgresql"),
			attribute.String("db.query.text", data.SQL),
		),
	)
	return context.WithValue(ctx, pgxSpanKey{}, span)
}

func (p pgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(pgxSpanKey{}).(trace.Span)
	if !ok {
		return
	}
	span.SetAttributes(attribute.Int64("db.response.affected_rows", data.CommandTag.RowsAffected()))
	endWithError(span, data.Err)
	span.End()
}

// sqlOperation returns the statement's leading keyword (SELECT, UPDATE,
// WITH, BEGIN...), a low-cardinality span name.
func sqlOperation(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "QUERY"
	}
	return strings.ToUpper(fields[0])
}

type redisHook struct {
	tracer trace.Tracer
}

var _ redis.Hook = redisHook{}

// RedisHook returns a go-redis hook that creates one client span per command
// or pipeline. Arguments are never recorded.
func (t *Tracing) RedisHook() redis.Hook {
	return redisHook{tracer: t.tracer}
}

func (h redisHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h redisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if !trace.SpanFromContext(ctx).IsRecording() {
			return next(ctx, cmd)
		}
		ctx, span := h.tracer.Start(ctx, "redis "+cmd.Name(),
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system.name", "redis"),
				attribute.String("db.operation.name", cmd.Name()),
			),
		)
		defer span.End()
		err := next(ctx, cmd)
		endWithError(span, redisFailure(err))
		return err
	}
}

func (h redisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if !trace.SpanFromContext(ctx).IsRecording() {
			return next(ctx, cmds)
		}
		ctx, span := h.tracer.Start(ctx, "redis pipeline",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system.name", "redis"),
				attribute.Int("db.operation.batch.size", len(cmds)),
			),
		)
		defer span.End()
		err := next(ctx, cmds)
		endWithError(span, redisFailure(err))
		return err
	}
}

// redisFailure ignores redis.Nil: a missing key is an answer, not a failure.
func redisFailure(err error) error {
	if errors.Is(err, redis.Nil) {
		return nil
	}
	return err
}
