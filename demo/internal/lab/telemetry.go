// Package lab holds what the demo lab's services share: OpenTelemetry setup,
// the JSON error format, a traced HTTP client and server, and graceful
// shutdown. mockpay deliberately does not use it: it plays an outside company
// and sends no telemetry.
package lab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Telemetry is one service's tracing and logging setup.
type Telemetry struct {
	Tracer     trace.TracerProvider
	Propagator propagation.TextMapPropagator
	Log        *slog.Logger
	shutdown   []func(context.Context) error
}

// Setup configures tracing and logging for a service.
//
// Logs always go to stdout as JSON, so `docker compose logs` works. When
// OTEL_EXPORTER_OTLP_ENDPOINT is set (to SigNoz's collector in the lab),
// traces and logs are also exported over OTLP/HTTP; the exporters read the
// standard OTEL_* environment variables. Without it, nothing is exported,
// which is how `go run` and the tests use the services.
func Setup(ctx context.Context, service, version string) (*Telemetry, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", service),
			attribute.String("service.version", version),
			attribute.String("deployment.environment.name", "lab"),
		),
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES can add or override
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	t := &Telemetry{Propagator: propagation.TraceContext{}}
	stdout := slog.NewJSONHandler(os.Stdout, nil)

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res))
		t.Tracer, t.Log = tp, slog.New(stdout)
		t.shutdown = append(t.shutdown, tp.Shutdown)
		return t, nil
	}

	spans, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("trace exporter: %w", err)
	}
	logs, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("log exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(spans))
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(logs)))

	t.Tracer = tp
	t.Log = slog.New(fanout{stdout, otelslog.NewHandler(service, otelslog.WithLoggerProvider(lp))})
	t.shutdown = append(t.shutdown, tp.Shutdown, lp.Shutdown)
	return t, nil
}

// Shutdown flushes anything not yet exported. Call it before the process exits.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, f := range t.shutdown {
		errs = append(errs, f(ctx))
	}
	return errors.Join(errs...)
}

// fanout sends each log record to several handlers (stdout and OpenTelemetry).
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
