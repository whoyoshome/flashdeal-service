package observability

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
)

const (
	// instrumentationName identifies the spans this service creates.
	instrumentationName = "github.com/whoyoshome/flashdeal-service"
	serviceName         = "flashdeal-api"
)

// Tracing owns the service's OpenTelemetry tracer provider and hands out the
// instrumentation built on it: the HTTP middleware, the pgx query tracer, the
// go-redis hook and spans for background work. The provider and propagator
// are passed around explicitly instead of through the OpenTelemetry globals;
// the only global ever set is the SDK error handler (see ReportErrorsTo).
type Tracing struct {
	provider   trace.TracerProvider
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
	shutdown   func(context.Context) error
}

// NewTracing exports spans over OTLP/HTTP to cfg.OTLPEndpoint, keeping
// cfg.SampleRatio of new traces. With no endpoint it returns a Tracing whose
// spans are no-ops, so instrumented code never checks whether tracing is on.
func NewTracing(ctx context.Context, cfg config.Tracing) (*Tracing, error) {
	if cfg.OTLPEndpoint == "" {
		return TracingWith(noop.NewTracerProvider()), nil
	}
	// WithEndpointURL takes the full URL, path included; the configuration
	// holds the collector's base URL, as OTEL_EXPORTER_OTLP_ENDPOINT does.
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(tracesURL(cfg.OTLPEndpoint)))
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
		// A request that arrives inside a sampled trace stays in it; only new
		// traces are sampled by ratio.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	t := TracingWith(provider)
	t.shutdown = provider.Shutdown
	return t, nil
}

// tracesURL appends the OTLP/HTTP traces path to a collector base URL.
func tracesURL(base string) string {
	return strings.TrimSuffix(base, "/") + "/v1/traces"
}

// ReportErrorsTo routes the OpenTelemetry SDK's internal errors, such as a
// failed export, to log. The SDK only reports them through its global error
// handler, which drops them by default, so a collector that is down would
// otherwise go unnoticed. Call it once, from main.
func ReportErrorsTo(log *slog.Logger) {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn("opentelemetry", slog.Any("error", err))
	}))
}

// TracingWith builds a Tracing on an existing provider. Tests use it with an
// in-memory span recorder.
func TracingWith(provider trace.TracerProvider) *Tracing {
	return &Tracing{
		provider:   provider,
		tracer:     provider.Tracer(instrumentationName),
		propagator: propagation.TraceContext{},
		shutdown:   func(context.Context) error { return nil },
	}
}

// Shutdown flushes buffered spans and stops the exporter.
func (t *Tracing) Shutdown(ctx context.Context) error {
	if err := t.shutdown(ctx); err != nil {
		return fmt.Errorf("shut down tracing: %w", err)
	}
	return nil
}

// StartSpan starts an internal span, for work that no request carries such
// as a worker pass. Call the returned function with the work's error, or
// nil, to end it.
func (t *Tracing) StartSpan(ctx context.Context, name string) (context.Context, func(error)) {
	ctx, span := t.tracer.Start(ctx, name)
	return ctx, func(err error) {
		endWithError(span, err)
		span.End()
	}
}

// endWithError marks span as failed when err is set.
func endWithError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
