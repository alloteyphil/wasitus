package signoz

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
	"github.com/alloteyphil/wasitus/internal/redact"
)

const (
	errorSpansFilter = "has_error = true"
	errorLogsFilter  = "severity_number >= 17 OR severity_text = 'ERROR'"
)

// ErrorSample is one error span or error log read from SigNoz. Its fields
// mirror fingerprint.Sample so callers can feed it directly to the
// fingerprinting stage without this package importing internal/fingerprint.
type ErrorSample struct {
	ID            string            `json:"id,omitempty"` // span_id for traces, id for logs
	TraceID       string            `json:"trace_id,omitempty"`
	SpanID        string            `json:"span_id,omitempty"`
	Signal        string            `json:"signal"` // "traces" or "logs"
	Time          time.Time         `json:"time"`   // UTC
	Service       string            `json:"service"`
	Outbound      bool              `json:"outbound,omitempty"`
	HTTPStatus    int               `json:"http_status,omitempty"`
	ServerAddress string            `json:"server_address,omitempty"`
	ExceptionType string            `json:"exception_type,omitempty"`
	Message       string            `json:"message,omitempty"`
	Attrs         map[string]string `json:"attrs,omitempty"`
}

// ErrorSpans fetches all error spans (has_error = true) in w via
// POST /api/v5/query_range, splitting the time range whenever a page fills up
// and deduplicating by span_id (ADR-0007).
func (c *Client) ErrorSpans(ctx context.Context, w event.Window) ([]ErrorSample, []string, error) {
	rows, warns, err := c.queryRaw(ctx, w, "traces", errorSpansFilter, spanDedupeKey)
	if err != nil {
		return nil, warns, err
	}
	samples := make([]ErrorSample, 0, len(rows))
	for _, r := range rows {
		s, ok := spanRowToSample(r)
		if !ok || !w.Contains(s.Time) {
			continue
		}
		samples = append(samples, s)
	}
	sortErrorSamples(samples)
	return samples, warns, nil
}

// ErrorLogs fetches all error logs (severity_number >= 17 OR
// severity_text = 'ERROR') in w via POST /api/v5/query_range, splitting the
// time range whenever a page fills up and deduplicating by log id (ADR-0007).
func (c *Client) ErrorLogs(ctx context.Context, w event.Window) ([]ErrorSample, []string, error) {
	rows, warns, err := c.queryRaw(ctx, w, "logs", errorLogsFilter, logDedupeKey)
	if err != nil {
		return nil, warns, err
	}
	samples := make([]ErrorSample, 0, len(rows))
	for _, r := range rows {
		s, ok := logRowToSample(r)
		if !ok || !w.Contains(s.Time) {
			continue
		}
		samples = append(samples, s)
	}
	sortErrorSamples(samples)
	return samples, warns, nil
}

// ErrorSamples fetches both error spans and error logs in w and returns them
// sorted chronologically. If one signal fails (for example, logs are not
// collected), samples from the other signal are still returned with the error.
func (c *Client) ErrorSamples(ctx context.Context, w event.Window) ([]ErrorSample, []string, error) {
	spans, spanWarns, spanErr := c.ErrorSpans(ctx, w)
	if spanErr != nil && isFatalAuthOrContext(spanErr) {
		return spans, spanWarns, spanErr
	}
	logs, logWarns, logErr := c.ErrorLogs(ctx, w)
	samples := make([]ErrorSample, 0, len(spans)+len(logs))
	samples = append(samples, spans...)
	samples = append(samples, logs...)
	sortErrorSamples(samples)
	warns := dedupeStrings(append(spanWarns, logWarns...))
	return samples, warns, errors.Join(spanErr, logErr)
}

func sortErrorSamples(samples []ErrorSample) {
	slices.SortStableFunc(samples, func(a, b ErrorSample) int {
		return cmp.Or(
			a.Time.Compare(b.Time),
			cmp.Compare(a.Signal, b.Signal),
			cmp.Compare(a.Service, b.Service),
			cmp.Compare(a.ID, b.ID),
		)
	})
}

func spanDedupeKey(r rawRow) string {
	if id := stringField(r.Data, "span_id", "spanID"); id != "" {
		return id
	}
	return fallbackRowKey("span", r)
}

func logDedupeKey(r rawRow) string {
	if id := stringField(r.Data, "id"); id != "" {
		return id
	}
	return fallbackRowKey("log", r)
}

func fallbackRowKey(prefix string, r rawRow) string {
	h := sha256.New()
	_, _ = h.Write([]byte(prefix))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(r.Timestamp.UTC().Format(time.RFC3339Nano)))
	_, _ = h.Write([]byte{0})
	if raw, err := json.Marshal(r.Data); err == nil {
		_, _ = h.Write(raw)
	}
	return prefix + ":" + hex.EncodeToString(h.Sum(nil)[:8])
}

func spanRowToSample(r rawRow) (ErrorSample, bool) {
	ts := rowTimestamp(r)
	if ts.IsZero() {
		return ErrorSample{}, false
	}

	attrs := extractStringMap(r.Data, "attributes", "attributes_string", "attributes_number", "attributes_bool")
	resource := extractStringMap(r.Data, "resource", "resources_string")

	service := firstNonEmpty(
		resource["service.name"],
		stringField(r.Data, "service.name", "serviceName"),
	)
	kindStr := stringField(r.Data, "kind_string")
	outbound := strings.EqualFold(kindStr, "client") || intField(r.Data, "kind") == 3

	httpStatus := firstHTTPStatus(
		stringField(r.Data, "response_status_code", "http_status_code"),
		attrs["http.response.status_code"],
		attrs["http.status_code"],
	)
	serverAddr := firstNonEmpty(
		attrs["server.address"],
		attrs["net.peer.name"],
		attrs["http.host"],
		attrs["peer.service"],
		stringField(r.Data, "server.address"),
	)

	excType, excMsg := extractSpanException(r.Data["events"])
	if excType == "" {
		excType = attrs["exception.type"]
	}
	if excMsg == "" {
		excMsg = firstNonEmpty(attrs["exception.message"], stringField(r.Data, "status_message"))
	}

	spanID := stringField(r.Data, "span_id", "spanID")
	traceID := stringField(r.Data, "trace_id", "traceID")
	if spanName := stringField(r.Data, "name"); spanName != "" {
		if attrs == nil {
			attrs = map[string]string{}
		}
		if _, exists := attrs["span.name"]; !exists {
			attrs["span.name"] = spanName
		}
	}

	return ErrorSample{
		ID:            spanID,
		TraceID:       traceID,
		SpanID:        spanID,
		Signal:        "traces",
		Time:          ts.UTC(),
		Service:       clean(service),
		Outbound:      outbound,
		HTTPStatus:    httpStatus,
		ServerAddress: clean(serverAddr),
		ExceptionType: clean(excType),
		Message:       clean(excMsg),
		Attrs:         nonEmptyMap(attrs),
	}, true
}

func logRowToSample(r rawRow) (ErrorSample, bool) {
	ts := rowTimestamp(r)
	if ts.IsZero() {
		return ErrorSample{}, false
	}

	attrs := extractStringMap(r.Data, "attributes", "attributes_string", "attributes_number", "attributes_bool")
	resource := extractStringMap(r.Data, "resource", "resources_string")

	service := firstNonEmpty(
		resource["service.name"],
		stringField(r.Data, "service.name", "serviceName"),
	)
	body := clean(anyToString(r.Data["body"]))
	excType := clean(attrs["exception.type"])
	excMsg := clean(attrs["exception.message"])
	msg := body
	switch {
	case msg == "":
		msg = excMsg
	case excMsg != "" && !strings.Contains(msg, excMsg):
		msg = msg + ": " + excMsg
	}

	httpStatus := firstHTTPStatus(
		attrs["http.response.status_code"],
		attrs["http.status_code"],
		stringField(r.Data, "response_status_code", "http_status_code"),
	)
	serverAddr := firstNonEmpty(
		attrs["server.address"],
		attrs["net.peer.name"],
		attrs["http.host"],
		attrs["peer.service"],
	)

	return ErrorSample{
		ID:            stringField(r.Data, "id"),
		TraceID:       stringField(r.Data, "trace_id", "traceID"),
		SpanID:        stringField(r.Data, "span_id", "spanID"),
		Signal:        "logs",
		Time:          ts.UTC(),
		Service:       clean(service),
		HTTPStatus:    httpStatus,
		ServerAddress: clean(serverAddr),
		ExceptionType: excType,
		Message:       msg,
		Attrs:         nonEmptyMap(attrs),
	}, true
}

// extractSpanException scans a span's events slice for an exception event and
// returns its exception.type and exception.message. SigNoz v0.149.0 decodes
// events into objects with "attributes" (or legacy "attributeMap").
func extractSpanException(raw any) (excType, excMsg string) {
	list, ok := raw.([]any)
	if !ok {
		return "", ""
	}
	for _, entry := range list {
		ev, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		attrs := extractStringMap(ev, "attributes", "attributeMap")
		t := attrs["exception.type"]
		m := attrs["exception.message"]
		if t == "" && m == "" {
			continue
		}
		if strings.EqualFold(stringField(ev, "name"), "exception") {
			return t, m
		}
		if excType == "" && excMsg == "" {
			excType, excMsg = t, m
		}
	}
	return excType, excMsg
}

// rowTimestamp returns the row's UTC timestamp. Raw spans populate row.Timestamp
// (RFC 3339), while raw logs may only carry data.timestamp in Unix nanoseconds
// (ADR-0007).
func rowTimestamp(r rawRow) time.Time {
	if !r.Timestamp.IsZero() {
		return r.Timestamp.UTC()
	}
	raw, ok := r.Data["timestamp"]
	if !ok || raw == nil {
		return time.Time{}
	}
	switch v := raw.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return time.Time{}
		}
		return unixFlexible(int64(v))
	case int64:
		if v <= 0 {
			return time.Time{}
		}
		return unixFlexible(v)
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return unixFlexible(n)
		}
	case string:
		v = strings.TrimSpace(v)
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC()
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return unixFlexible(n)
		}
	}
	return time.Time{}
}

// unixFlexible interprets positive Unix timestamps in nanoseconds (logs),
// milliseconds, or seconds based on magnitude.
func unixFlexible(n int64) time.Time {
	switch {
	case n >= 1e16:
		return time.Unix(0, n).UTC()
	case n >= 1e12:
		return time.UnixMilli(n).UTC()
	default:
		return time.Unix(n, 0).UTC()
	}
}

// summarizeErrorSpans turns error spans into one timeline symptom event per
// failing service, taking the symptom's start and end from the actual error
// span timestamps rather than the lagging alert evaluation time (ADR-0007).
func summarizeErrorSpans(samples []ErrorSample) []event.Event {
	type group struct {
		service string
		count   int
		first   time.Time
		last    time.Time
		example ErrorSample
	}
	byService := make(map[string]*group)
	for _, s := range samples {
		svc := s.Service
		g := byService[svc]
		if g == nil {
			g = &group{service: svc, first: s.Time, last: s.Time, example: s}
			byService[svc] = g
		}
		g.count++
		if s.Time.Before(g.first) {
			g.first = s.Time
		}
		if s.Time.After(g.last) {
			g.last = s.Time
		}
		if g.example.Message == "" && s.Message != "" {
			g.example = s
		}
	}

	services := make([]string, 0, len(byService))
	for svc := range byService {
		services = append(services, svc)
	}
	slices.Sort(services)

	events := make([]event.Event, 0, len(services))
	for _, svc := range services {
		g := byService[svc]
		title := formatErrorSummary(g.count, g.example)
		var end *time.Time
		if g.last.After(g.first) {
			t := g.last
			end = &t
		}
		sev := event.SeverityWarning
		if g.count >= 5 {
			sev = event.SeverityCritical
		}
		attrs := map[string]string{
			"signal":      "traces",
			"error_count": strconv.Itoa(g.count),
		}
		if g.example.HTTPStatus > 0 {
			attrs["http_status"] = strconv.Itoa(g.example.HTTPStatus)
		}
		if g.example.ServerAddress != "" {
			attrs["server_address"] = g.example.ServerAddress
		}
		if g.example.ExceptionType != "" {
			attrs["exception_type"] = g.example.ExceptionType
		}
		events = append(events, event.Event{
			ID:       fmt.Sprintf("signoz:errors:%s:%d", svc, g.first.UnixMilli()),
			Time:     g.first,
			End:      end,
			Kind:     event.KindSymptom,
			Source:   Source,
			Service:  svc,
			Title:    title,
			Severity: sev,
			Attrs:    attrs,
		})
	}
	return events
}

func formatErrorSummary(count int, example ErrorSample) string {
	noun := "error spans"
	if count == 1 {
		noun = "error span"
	}
	detail := example.Message
	if detail == "" && example.ExceptionType != "" {
		detail = example.ExceptionType
	}
	if detail == "" && example.HTTPStatus > 0 {
		detail = fmt.Sprintf("HTTP %d", example.HTTPStatus)
	}
	if detail == "" {
		return fmt.Sprintf("%d %s", count, noun)
	}
	// Cut at a word boundary: a split email or card number would no longer
	// be recognised by the redactor that runs at render time.
	detail = redact.Truncate(detail, 100)
	return fmt.Sprintf("%d %s (%s)", count, noun, detail)
}

func extractStringMap(m map[string]any, keys ...string) map[string]string {
	var out map[string]string
	for _, key := range keys {
		sub, ok := m[key].(map[string]any)
		if !ok || len(sub) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(sub))
		}
		for k, v := range sub {
			if s := anyToString(v); s != "" {
				out[k] = s
			}
		}
	}
	return out
}

func nonEmptyMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s := anyToString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func intField(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}

func firstHTTPStatus(vals ...string) int {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			n := int(math.Round(f))
			if n >= 100 && n <= 599 {
				return n
			}
		}
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func anyToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return ""
		}
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
