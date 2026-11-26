// Package labtest helps the lab services' tests check the telemetry they
// produce: what each service would send to SigNoz, and so what wasitus will
// later read. Only tests import it.
package labtest

import (
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/alloteyphil/wasitus/demo/internal/lab"
)

// Telemetry returns telemetry that keeps finished spans in memory and
// discards logs.
func Telemetry() (*lab.Telemetry, *tracetest.SpanRecorder) {
	rec := tracetest.NewSpanRecorder()
	return &lab.Telemetry{
		Tracer:     sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)),
		Propagator: propagation.TraceContext{},
		Log:        slog.New(slog.DiscardHandler),
	}, rec
}

// OnlySpan returns the single finished span of the given kind, failing the
// test if there is not exactly one.
func OnlySpan(t testing.TB, rec *tracetest.SpanRecorder, kind trace.SpanKind) sdktrace.ReadOnlySpan {
	t.Helper()
	var found []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.SpanKind() == kind {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %s span, got %d", kind, len(found))
	}
	return found[0]
}

// Attr returns a span attribute as a string, or "" if the span lacks it.
func Attr(s sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

// Exception returns the type and message of the span's first exception event.
func Exception(s sdktrace.ReadOnlySpan) (typ, msg string, ok bool) {
	for _, e := range s.Events() {
		if e.Name != "exception" {
			continue
		}
		for _, kv := range e.Attributes {
			switch kv.Key {
			case "exception.type":
				typ = kv.Value.Emit()
			case "exception.message":
				msg = kv.Value.Emit()
			}
		}
		return typ, msg, true
	}
	return "", "", false
}
