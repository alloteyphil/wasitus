package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// APIError is the error body every lab service and mockpay return:
// {"error": {"code": "AccountSuspended", "message": "..."}}.
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

type errorBody struct {
	Error APIError `json:"error"`
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the lab's error body.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, errorBody{Error: APIError{Code: code, Message: message}})
}

// ReadJSON decodes a JSON request body of at most 1 MiB into v.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v)
}

// Fail answers a request with an error and records it on the request's span
// (as an exception event, plus attrs), and in the log. This is what our
// telemetry shows when a call fails, so it is what wasitus will read.
func Fail(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, code string, err error, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(attrs...)
	span.RecordError(err)
	if status >= 500 {
		span.SetStatus(codes.Error, err.Error())
	}
	args := []any{"code", code, "status", status, "error", err}
	for _, a := range attrs {
		args = append(args, string(a.Key), a.Value.AsInterface())
	}
	log.ErrorContext(r.Context(), "request failed", args...)
	WriteError(w, status, code, err.Error())
}

// Client calls another service with JSON and a timeout. Each call becomes a
// client span, and the trace context travels in the traceparent header, so a
// checkout shows up in SigNoz as one trace across all services.
type Client struct {
	http *http.Client
}

// NewClient returns a traced client that gives up after timeout.
func NewClient(t *Telemetry, timeout time.Duration) *Client {
	return &Client{http: &http.Client{
		Timeout: timeout,
		Transport: otelhttp.NewTransport(http.DefaultTransport,
			otelhttp.WithTracerProvider(t.Tracer), otelhttp.WithPropagators(t.Propagator)),
	}}
}

// PostJSON sends in as JSON and decodes a 2xx response into out. A non-2xx
// answer comes back as *APIError. Anything else (timeout, refused connection)
// is returned as is; check it with IsTimeout.
func (c *Client) PostJSON(ctx context.Context, url string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e errorBody
		if json.Unmarshal(data, &e) != nil || e.Error.Code == "" {
			e.Error = APIError{Code: "UnexpectedResponse", Message: http.StatusText(resp.StatusCode)}
		}
		e.Error.Status = resp.StatusCode
		return &e.Error
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response from %s: %w", url, err)
	}
	return nil
}

// IsTimeout reports whether err is a client giving up waiting.
func IsTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Mux routes requests. Every route gets a server span named after its
// pattern, and a panic in a handler becomes a recorded exception and a 500
// instead of a dropped connection. /healthz is answered without a span, so
// health checks do not flood the traces.
type Mux struct {
	mux *http.ServeMux
	tel *Telemetry
}

// NewMux returns a Mux with GET /healthz already registered.
func NewMux(t *Telemetry) *Mux {
	m := &Mux{mux: http.NewServeMux(), tel: t}
	m.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return m
}

// HandleFunc registers a traced handler for pattern, such as "POST /orders".
func (m *Mux) HandleFunc(pattern string, h http.HandlerFunc) {
	m.mux.Handle(pattern, otelhttp.NewHandler(recoverPanics(h, m.tel.Log), pattern,
		otelhttp.WithTracerProvider(m.tel.Tracer), otelhttp.WithPropagators(m.tel.Propagator)))
}

func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mux.ServeHTTP(w, r)
}

// recoverPanics turns a panic into an exception on the span (with a stack
// trace) and a 500. Runtime panics keep their Go type, such as
// runtime.boundsError, which is what a real code bug looks like in SigNoz.
func recoverPanics(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			err, ok := v.(error)
			if !ok {
				err = fmt.Errorf("%v", v)
			}
			if errors.Is(err, http.ErrAbortHandler) {
				panic(v) // the server's own signal to drop the connection
			}
			span := trace.SpanFromContext(r.Context())
			span.RecordError(err, trace.WithStackTrace(true))
			span.SetStatus(codes.Error, err.Error())
			log.ErrorContext(r.Context(), "panic", "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "InternalError", "Something went wrong.")
		}()
		next.ServeHTTP(w, r)
	})
}
