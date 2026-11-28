package signoz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

var (
	winStart = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	winEnd   = time.Date(2026, 11, 26, 9, 30, 0, 0, time.UTC)
)

func testWindow(t *testing.T) event.Window {
	t.Helper()
	w, err := event.NewWindow(winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestNewClientValidation(t *testing.T) {
	bad := []string{"", "ftp://localhost:8080", "localhost:8080", "http://", "://bad"}
	for _, u := range bad {
		if _, err := NewClient(u, "k", nil); err == nil {
			t.Errorf("NewClient(%q) error = nil, want error", u)
		}
		if _, err := New(u, "k", nil); err == nil {
			t.Errorf("New(%q) error = nil, want error", u)
		}
	}
	c, err := NewClient("http://localhost:8080/", "", nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if c.http == nil || c.http == http.DefaultClient {
		t.Error("expected nil httpClient to use an isolated copy, not http.DefaultClient directly")
	}
}

func TestVersionAndCheckAuth(t *testing.T) {
	const secretKey = "lab-viewer-secret-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			if r.Header.Get("SigNoz-Api-Key") != "" {
				t.Errorf("/api/v1/version must not send SigNoz-Api-Key")
			}
			_, _ = w.Write(fixture(t, "version.json"))
		case "/api/v1/service_accounts/me":
			if got := r.Header.Get("SigNoz-Api-Key"); got != secretKey {
				t.Errorf("SigNoz-Api-Key = %q, want %q", got, secretKey)
			}
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("Authorization = %q, must not send bearer token", got)
			}
			_, _ = w.Write(fixture(t, "service_account_me.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, secretKey, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	ver, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if ver.Version != TestedVersion || !ver.SetupCompleted {
		t.Errorf("Version() = %+v", ver)
	}
	if warn := VersionWarning(ver); warn != "" {
		t.Errorf("VersionWarning(tested) = %q, want empty", warn)
	}
	if warn := VersionWarning(VersionInfo{Version: "v0.155.0"}); !strings.Contains(warn, "v0.155.0") || !strings.Contains(warn, TestedVersion) {
		t.Errorf("VersionWarning(other) = %q, want mismatch warning", warn)
	}
	if warn := VersionWarning(VersionInfo{}); warn != "" {
		t.Errorf("VersionWarning(empty) = %q, want empty", warn)
	}

	acct, err := c.CheckAuth(context.Background())
	if err != nil {
		t.Fatalf("CheckAuth() error = %v", err)
	}
	if acct.Name != "wasitus-reader" || acct.ID == "" {
		t.Errorf("CheckAuth() = %+v", acct)
	}
}

func TestAlertsReplay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rules":
			_, _ = w.Write(fixture(t, "rules.json"))
		case "/api/v2/rules/rule-err-rate/history/timeline":
			if r.URL.Query().Get("cursor") == "page-2-cursor" {
				_, _ = w.Write(fixture(t, "rule_history_page2.json"))
				return
			}
			_, _ = w.Write(fixture(t, "rule_history_page1.json"))
		case "/api/v2/rules/rule-p99-latency/history/timeline":
			fmt.Fprint(w, `{"status":"success","data":{"items":[],"total":0}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "test-key", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	events, warns, err := c.Alerts(context.Background(), testWindow(t))
	if err != nil {
		t.Fatalf("Alerts() error = %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (fired + resolved): %+v", len(events), events)
	}

	fired := events[0]
	if fired.Kind != event.KindSymptom || fired.Source != Source || fired.Service != "wallet-service" {
		t.Errorf("fired event metadata = %+v", fired)
	}
	if fired.Title != "Alert FIRED: Lab service error rate above 5%" {
		t.Errorf("fired.Title = %q", fired.Title)
	}
	if fired.Severity != event.SeverityCritical {
		t.Errorf("fired.Severity = %q, want critical", fired.Severity)
	}
	wantFired := time.Date(2026, 11, 26, 9, 3, 0, 0, time.UTC)
	wantResolved := time.Date(2026, 11, 26, 9, 15, 0, 0, time.UTC)
	if !fired.Time.Equal(wantFired) {
		t.Errorf("fired.Time = %s, want %s", fired.Time, wantFired)
	}
	if fired.End == nil || !fired.End.Equal(wantResolved) {
		t.Errorf("fired.End = %v, want %s", fired.End, wantResolved)
	}
	if !strings.HasPrefix(fired.Link, srv.URL+"/traces-explorer?") {
		t.Errorf("fired.Link = %q, want resolved link on server host", fired.Link)
	}
	if fired.Attrs["value"] != "31.4" || fired.Attrs["rule_id"] != "rule-err-rate" {
		t.Errorf("fired.Attrs = %+v", fired.Attrs)
	}

	resolved := events[1]
	if resolved.Title != "Alert RESOLVED: Lab service error rate above 5%" || resolved.Severity != event.SeverityInfo {
		t.Errorf("resolved = %+v", resolved)
	}
	if !resolved.Time.Equal(wantResolved) {
		t.Errorf("resolved.Time = %s, want %s", resolved.Time, wantResolved)
	}
	for _, ev := range events {
		if err := ev.Normalize().Validate(); err != nil {
			t.Errorf("event failed Validate(): %v", err)
		}
	}
}

func TestAlertsEdgeCases(t *testing.T) {
	// Tests nodata state, external link rejection, repeating cursor warning,
	// and partial failure across rules.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rules":
			fmt.Fprint(w, `{"status":"success","data":{"rules":[
				{"id":"r1","alert":"Rule One","labels":{"severity":"info"}},
				{"id":"r2","alert":"Rule Two","labels":{}}
			],"total":2}}`)
		case "/api/v2/rules/r1/history/timeline":
			// Repeats the same cursor on page 2 to verify loop protection.
			fmt.Fprint(w, `{"status":"success","data":{"items":[
				{"ruleId":"r1","state":"nodata","stateChanged":true,"unixMilli":1795683900000,"labels":[{"key":{"name":"service"},"value":"orders-service"}],"fingerprint":7,"relatedTracesLink":"https://evil.example/steal","relatedLogsLink":"/logs?q=1"}
			],"total":2,"nextCursor":"loop-me"}}`)
		case "/api/v2/rules/r2/history/timeline":
			http.Error(w, `{"status":"error","error":{"message":"backend timeout"}}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	events, warns, err := c.Alerts(context.Background(), testWindow(t))
	if err == nil || !strings.Contains(err.Error(), "backend timeout") {
		t.Fatalf("Alerts() error = %v, want backend timeout", err)
	}
	if len(events) == 0 {
		t.Fatal("expected events from r1 even though r2 failed")
	}
	if events[0].Title != "Alert NO DATA: Rule One" || events[0].Service != "orders-service" {
		t.Errorf("unexpected event: %+v", events[0])
	}
	if !strings.HasPrefix(events[0].Link, srv.URL+"/logs?q=1") {
		t.Errorf("Link = %q, expected evil external link rejected and logs link used", events[0].Link)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "repeated pagination cursor") {
		t.Errorf("warns = %v, want repeating cursor warning", warns)
	}
}

func TestErrorSpansAndLogsReplay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/query_range" {
			http.NotFound(w, r)
			return
		}
		var req queryRangeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.CompositeQuery.Queries) == 0 {
			http.Error(w, "no queries", http.StatusBadRequest)
			return
		}
		switch req.CompositeQuery.Queries[0].Spec.Signal {
		case "traces":
			_, _ = w.Write(fixture(t, "error_spans.json"))
		case "logs":
			_, _ = w.Write(fixture(t, "error_logs.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	samples, warns, err := c.ErrorSamples(context.Background(), testWindow(t))
	if err != nil {
		t.Fatalf("ErrorSamples() error = %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(samples) != 4 {
		t.Fatalf("got %d samples, want 4 (3 spans + 1 log): %+v", len(samples), samples)
	}

	// Verify chronological order and field extraction.
	byID := make(map[string]ErrorSample, len(samples))
	for _, s := range samples {
		byID[s.ID] = s
	}

	clientSpan := byID["span-wallet-client-1"]
	if !clientSpan.Outbound || clientSpan.HTTPStatus != 402 || clientSpan.ServerAddress != "mockpay" || clientSpan.Service != "wallet-service" {
		t.Errorf("clientSpan = %+v", clientSpan)
	}

	serverSpan := byID["span-wallet-server-1"]
	if serverSpan.Outbound || serverSpan.HTTPStatus != 502 || serverSpan.ExceptionType != "*fmt.wrapError" ||
		!strings.Contains(serverSpan.Message, "AccountSuspended") || serverSpan.Attrs["payment.provider.error_code"] != "AccountSuspended" {
		t.Errorf("serverSpan = %+v", serverSpan)
	}

	panicSpan := byID["span-orders-panic-1"]
	if panicSpan.Service != "orders-service" || panicSpan.ExceptionType != "runtime.boundsError" ||
		!strings.Contains(panicSpan.Message, "index out of range") {
		t.Errorf("panicSpan = %+v", panicSpan)
	}

	logSample := byID["log-0001"]
	if logSample.Signal != "logs" || logSample.Service != "wallet-service" || logSample.HTTPStatus != 502 ||
		!strings.Contains(logSample.Message, "AccountSuspended") {
		t.Errorf("logSample = %+v", logSample)
	}
	wantLogTime := time.Unix(0, 1795683670550000000).UTC()
	if !logSample.Time.Equal(wantLogTime) {
		t.Errorf("logSample.Time = %s, want %s (from data.timestamp ns)", logSample.Time, wantLogTime)
	}
}

func TestErrorSpansTimeSplitPaginationAndDedupe(t *testing.T) {
	// Configure rawPageLimit = 2 so a 4-span window forces recursive time
	// splitting with inclusive boundaries, verifying zero lost spans and
	// deduplication at the shared midpoint millisecond.
	type spanSpec struct {
		id string
		ts time.Time
	}
	all := []spanSpec{
		{"s1", winStart.Add(1 * time.Second)},
		{"s2", winStart.Add(5 * time.Second)},
		{"s3", winStart.Add(5 * time.Second)}, // shares ms with s2!
		{"s4", winStart.Add(9 * time.Second)},
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req queryRangeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var rows []map[string]any
		for _, sp := range all {
			ms := sp.ts.UnixMilli()
			if ms >= req.Start && ms <= req.End {
				rows = append(rows, map[string]any{
					"timestamp": sp.ts.Format(time.RFC3339Nano),
					"data": map[string]any{
						"span_id":        sp.id,
						"kind_string":    "Server",
						"status_message": "boom",
						"resource":       map[string]any{"service.name": "orders-service"},
					},
				})
				if len(rows) == req.CompositeQuery.Queries[0].Spec.Limit {
					break
				}
			}
		}
		resp := map[string]any{
			"status": "success",
			"data": map[string]any{
				"type": "raw",
				"data": map[string]any{
					"results": []any{map[string]any{"queryName": "A", "rows": rows}},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.rawPageLimit = 3

	w, _ := event.NewWindow(winStart, winStart.Add(10*time.Second))
	samples, warns, err := c.ErrorSpans(context.Background(), w)
	if err != nil {
		t.Fatalf("ErrorSpans() error = %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if calls < 3 {
		t.Errorf("expected time-splitting to make >= 3 calls, got %d", calls)
	}
	if len(samples) != 4 {
		t.Fatalf("got %d samples, want all 4 unique spans: %+v", len(samples), samples)
	}
}

func TestErrorSpansUnsplittableWarning(t *testing.T) {
	// Two spans in the exact same millisecond with rawPageLimit = 2 cannot split
	// further once width <= 1ms; verify we warn instead of looping forever.
	ts := winStart.Add(100 * time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"status": "success",
			"data": map[string]any{
				"type": "raw",
				"data": map[string]any{
					"results": []any{map[string]any{
						"queryName": "A",
						"rows": []any{
							map[string]any{"timestamp": ts.Format(time.RFC3339Nano), "data": map[string]any{"span_id": "a"}},
							map[string]any{"timestamp": ts.Format(time.RFC3339Nano), "data": map[string]any{"span_id": "b"}},
						},
					}},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.rawPageLimit = 2
	w, _ := event.NewWindow(ts, ts.Add(time.Millisecond))
	samples, warns, err := c.ErrorSpans(context.Background(), w)
	if err != nil {
		t.Fatalf("ErrorSpans() error = %v", err)
	}
	if len(samples) != 2 {
		t.Errorf("got %d samples, want 2", len(samples))
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "could not split further") {
		t.Errorf("warns = %v, want truncation warning", warns)
	}
}

func TestTrafficBucketsReplayAndChunking(t *testing.T) {
	t.Run("replay fixture with warnings and partial skip", func(t *testing.T) {
		var gotReq queryRangeRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = w.Write(fixture(t, "traffic_series.json"))
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL, "k", srv.Client())
		if err != nil {
			t.Fatal(err)
		}

		buckets, warns, err := c.TrafficBuckets(context.Background(), testWindow(t), 0)
		if err != nil {
			t.Fatalf("TrafficBuckets() error = %v", err)
		}
		if gotReq.FormatOptions == nil || !gotReq.FormatOptions.FillGaps {
			t.Errorf("expected formatOptions.fillGaps=true, got %+v", gotReq.FormatOptions)
		}
		if len(gotReq.CompositeQuery.Queries) != 2 || gotReq.CompositeQuery.Queries[0].Spec.StepInterval != 300 {
			t.Errorf("unexpected queries: %+v", gotReq.CompositeQuery.Queries)
		}
		if len(warns) != 2 {
			t.Errorf("warns = %v, want 2 warnings from data.warning", warns)
		}
		if got := len(buckets["storefront"]); got != 2 {
			t.Fatalf("storefront buckets = %d, want 2 (partial point skipped): %+v", got, buckets["storefront"])
		}
		ws := buckets["wallet-service"]
		if len(ws) != 2 || ws[0].Requests != 598 || ws[0].Errors != 190 || ws[1].Requests != 610 || ws[1].Errors != 205 {
			t.Errorf("wallet-service buckets = %+v", ws)
		}
	})

	t.Run("7-day baseline splits into <=120h chunks", func(t *testing.T) {
		var mu sync.Mutex
		var chunks [][2]int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req queryRangeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			chunks = append(chunks, [2]int64{req.Start, req.End})
			mu.Unlock()
			fmt.Fprintf(w, `{"status":"success","data":{"type":"time_series","data":{"results":[{"queryName":"A","aggregations":[{"index":0,"series":[{"labels":[{"key":{"name":"service.name"},"value":"storefront"}],"values":[{"timestamp":%d,"value":100}]}]}]}]}}}`, req.Start)
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL, "k", srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		sevenDays, _ := event.NewWindow(winStart.Add(-7*24*time.Hour), winStart)
		buckets, _, err := c.TrafficBuckets(context.Background(), sevenDays, 5*time.Minute)
		if err != nil {
			t.Fatalf("TrafficBuckets(7d) error = %v", err)
		}
		if len(chunks) != 2 {
			t.Fatalf("got %d chunks for 7 days, want 2 (120h + 48h): %v", len(chunks), chunks)
		}
		if got := time.Duration(chunks[0][1]-chunks[0][0]) * time.Millisecond; got != MaxBaselineChunk {
			t.Errorf("first chunk width = %s, want %s", got, MaxBaselineChunk)
		}
		if got := time.Duration(chunks[1][1]-chunks[1][0]) * time.Millisecond; got != 48*time.Hour {
			t.Errorf("second chunk width = %s, want 48h", got)
		}
		if len(buckets["storefront"]) != 2 {
			t.Errorf("expected 2 merged buckets across chunks, got %+v", buckets["storefront"])
		}
	})

	t.Run("invalid step rejected", func(t *testing.T) {
		c, _ := NewClient("http://localhost:8080", "", nil)
		for _, bad := range []time.Duration{-time.Minute, 500 * time.Millisecond, 1500 * time.Millisecond} {
			if _, _, err := c.TrafficBuckets(context.Background(), testWindow(t), bad); err == nil {
				t.Errorf("TrafficBuckets(step=%s) error = nil, want error", bad)
			}
		}
	})
}

func TestCollectorCollectAndWarnings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			fmt.Fprint(w, `{"version":"v0.151.0","setupCompleted":true}`)
		case "/api/v3/rules":
			_, _ = w.Write(fixture(t, "rules.json"))
		case "/api/v2/rules/rule-err-rate/history/timeline":
			if r.URL.Query().Get("cursor") == "page-2-cursor" {
				_, _ = w.Write(fixture(t, "rule_history_page2.json"))
				return
			}
			_, _ = w.Write(fixture(t, "rule_history_page1.json"))
		case "/api/v2/rules/rule-p99-latency/history/timeline":
			fmt.Fprint(w, `{"status":"success","data":{"items":[],"total":0}}`)
		case "/api/v5/query_range":
			_, _ = w.Write(fixture(t, "error_spans.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	col, err := New(srv.URL, "viewer-key", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if col.Name() != Source || col.Client() == nil {
		t.Fatalf("unexpected collector name/client")
	}

	events, err := col.Collect(context.Background(), testWindow(t))
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	// Expect 2 alert events (fired + resolved) + 2 error summary events (orders-service, wallet-service).
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(events), events)
	}
	for _, ev := range events {
		if err := ev.Normalize().Validate(); err != nil {
			t.Errorf("invalid event %+v: %v", ev, err)
		}
	}
	warns := col.Warnings()
	if len(warns) != 1 || !strings.Contains(warns[0], "v0.151.0") {
		t.Errorf("Warnings() = %v, want version mismatch warning", warns)
	}
}

func TestHTTPErrorsAndNoSecretLeak(t *testing.T) {
	const secret = "signoz-super-secret-value-999"
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
		wantMsg string
	}{
		{"401 json", http.StatusUnauthorized, `{"status":"error","error":{"message":"invalid api key"}}`, ErrUnauthorized, "invalid api key"},
		{"401 empty", http.StatusUnauthorized, ``, ErrUnauthorized, "unauthorized"},
		{"403 json", http.StatusForbidden, `{"status":"error","error":{"message":"viewer only"}}`, ErrForbidden, "viewer only"},
		{"500 json", http.StatusInternalServerError, `{"message":"clickhouse unreachable"}`, nil, "clickhouse unreachable"},
		{"500 plain", http.StatusInternalServerError, `not json`, nil, "Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			col, err := New(srv.URL, secret, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = col.Collect(context.Background(), testWindow(t))
			if err == nil {
				t.Fatal("Collect() error = nil, want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("errors.Is(%v, %v) = false", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want %q", err.Error(), tt.wantMsg)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error leaked secret: %q", err.Error())
			}
		})
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c, err := NewClient("http://localhost:8080", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Version(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Version(cancelled) = %v, want context.Canceled", err)
	}
	if _, _, err := c.Alerts(ctx, testWindow(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("Alerts(cancelled) = %v, want context.Canceled", err)
	}
}

func TestRulesPaginationAndEmptyAndAuthStop(t *testing.T) {
	t.Run("paginates rules across multiple pages and dedupes", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v3/rules":
				offset := r.URL.Query().Get("offset")
				if offset == "0" {
					rules := make([]alertRule, 0, rulesPageSize)
					rules = append(rules, alertRule{ID: "", Alert: "Skip empty ID"})
					for i := 1; i < rulesPageSize; i++ {
						rules = append(rules, alertRule{ID: fmt.Sprintf("r-%03d", i), Alert: fmt.Sprintf("Rule %d", i)})
					}
					_ = json.NewEncoder(w).Encode(rulesListEnvelope{
						Status: "success",
						Data:   rulesListData{Rules: rules, Total: rulesPageSize + 2},
					})
					return
				}
				_ = json.NewEncoder(w).Encode(rulesListEnvelope{
					Status: "success",
					Data: rulesListData{
						Rules: []alertRule{
							{ID: "r-001", Alert: "Duplicate from page 1"},
							{ID: "r-last", Alert: "Last rule"},
						},
						Total: rulesPageSize + 2,
					},
				})
			default:
				fmt.Fprint(w, `{"status":"success","data":{"items":[],"total":0}}`)
			}
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL, "k", srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		rules, warns, err := c.listRules(context.Background())
		if err != nil {
			t.Fatalf("listRules() error = %v", err)
		}
		if len(warns) != 0 {
			t.Errorf("unexpected warns: %v", warns)
		}
		if len(rules) != rulesPageSize { // 199 from page 1 + 1 new on page 2
			t.Errorf("len(rules) = %d, want %d", len(rules), rulesPageSize)
		}
	})

	t.Run("empty rules list returns nil", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"status":"success","data":{"rules":[],"total":0}}`)
		}))
		defer srv.Close()
		c, _ := NewClient(srv.URL, "k", srv.Client())
		events, warns, err := c.Alerts(context.Background(), testWindow(t))
		if err != nil || len(events) != 0 || len(warns) != 0 {
			t.Errorf("Alerts(empty) = %v, %v, %v", events, warns, err)
		}
	})

	t.Run("auth error inside rule history stops workers", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v3/rules" {
				fmt.Fprint(w, `{"status":"success","data":{"rules":[{"id":"r1","alert":"A"},{"id":"r2","alert":"B"}],"total":2}}`)
				return
			}
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		c, _ := NewClient(srv.URL, "k", srv.Client())
		_, _, err := c.Alerts(context.Background(), testWindow(t))
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("Alerts() error = %v, want ErrForbidden", err)
		}
	})
}

func TestClientDecodingAndSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version", "/api/v1/service_accounts/me", "/api/v3/rules":
			fmt.Fprint(w, `{bad json`)
		case "/api/v5/query_range":
			fmt.Fprint(w, strings.Repeat("x", 1024))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "k", srv.Client())
	if _, err := c.Version(context.Background()); err == nil || !strings.Contains(err.Error(), "decode version") {
		t.Errorf("Version(bad json) = %v", err)
	}
	if _, err := c.CheckAuth(context.Background()); err == nil || !strings.Contains(err.Error(), "decode service account") {
		t.Errorf("CheckAuth(bad json) = %v", err)
	}
	if _, _, err := c.Alerts(context.Background(), testWindow(t)); err == nil || !strings.Contains(err.Error(), "decode rules list") {
		t.Errorf("Alerts(bad json) = %v", err)
	}

	c.maxBody = 100
	if _, _, err := c.ErrorSpans(context.Background(), testWindow(t)); err == nil || !strings.Contains(err.Error(), "larger than 100 bytes") {
		t.Errorf("ErrorSpans(oversized) = %v", err)
	}

	// Long API message truncation and control-character cleaning.
	// It is cut at a word boundary, so it is at most 200 runes plus "...".
	longMsg := strings.Repeat("abcd ", 50) + "\n\t\x1b[31mred"
	body, _ := json.Marshal(map[string]string{"message": longMsg})
	got := apiMessage(http.StatusBadGateway, body)
	if !strings.HasSuffix(got, "abcd...") || len([]rune(got)) > 203 || strings.ContainsAny(got, "\n\t\x1b") {
		t.Errorf("apiMessage(long) = %q (len %d)", got, len([]rune(got)))
	}
	if clean("a\nb\tc\x00d") != "a b c d" {
		t.Errorf("clean() = %q", clean("a\nb\tc\x00d"))
	}
	if got := c.resolveLink("://not-a-url"); got != "" {
		t.Errorf("resolveLink(bad) = %q, want empty", got)
	}
	// Credentials in the base URL must never be copied into a link.
	withUser, err := NewClient("https://admin:hunter2@signoz.example", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := withUser.resolveLink("/alerts/history?ruleId=1"); got != "https://signoz.example/alerts/history?ruleId=1" {
		t.Errorf("resolveLink with user info = %q", got)
	}
	if got := dedupeStrings([]string{"", "a", "b", "a", ""}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("dedupeStrings() = %v", got)
	}
}

func TestRowTimestampAndHelpers(t *testing.T) {
	want := time.Date(2026, 11, 26, 9, 1, 10, 0, time.UTC)
	tests := []struct {
		name string
		row  rawRow
		want time.Time
	}{
		{"rfc3339 row.Timestamp", rawRow{Timestamp: want}, want},
		{"ns json.Number", rawRow{Data: map[string]any{"timestamp": json.Number(fmt.Sprintf("%d", want.UnixNano()))}}, want},
		{"ms json.Number", rawRow{Data: map[string]any{"timestamp": json.Number(fmt.Sprintf("%d", want.UnixMilli()))}}, want},
		{"s json.Number", rawRow{Data: map[string]any{"timestamp": json.Number(fmt.Sprintf("%d", want.Unix()))}}, want},
		{"ms float64", rawRow{Data: map[string]any{"timestamp": float64(want.UnixMilli())}}, want},
		{"int64 ns", rawRow{Data: map[string]any{"timestamp": want.UnixNano()}}, want},
		{"string rfc3339", rawRow{Data: map[string]any{"timestamp": want.Format(time.RFC3339)}}, want},
		{"string unix ms", rawRow{Data: map[string]any{"timestamp": fmt.Sprintf("%d", want.UnixMilli())}}, want},
		{"missing", rawRow{Data: map[string]any{}}, time.Time{}},
		{"negative int64", rawRow{Data: map[string]any{"timestamp": int64(-1)}}, time.Time{}},
		{"zero float64", rawRow{Data: map[string]any{"timestamp": float64(0)}}, time.Time{}},
		{"bad string", rawRow{Data: map[string]any{"timestamp": "nope"}}, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rowTimestamp(tt.row); !got.Equal(tt.want) {
				t.Errorf("rowTimestamp() = %v, want %v", got, tt.want)
			}
		})
	}

	// anyToString and intField coverage across JSON-decoded types.
	if anyToString(nil) != "" || anyToString(true) != "true" || anyToString(7) != "7" ||
		anyToString(int64(9)) != "9" || anyToString(float64(12.5)) != "12.5" || anyToString(float64(12)) != "12" ||
		anyToString(map[string]any{"a": 1}) != `{"a":1}` {
		t.Error("anyToString returned unexpected value")
	}
	m := map[string]any{"num": json.Number("3"), "flt": float64(3), "i": 3, "i64": int64(3), "s": "3", "bad": true}
	for _, k := range []string{"num", "flt", "i", "i64", "s"} {
		if intField(m, k) != 3 {
			t.Errorf("intField(%s) = %d, want 3", k, intField(m, k))
		}
	}
	if intField(m, "bad") != 0 || intField(m, "missing") != 0 {
		t.Error("intField expected 0 for non-numeric/missing")
	}
}

func TestErrorSampleAndSummaryEdgeCases(t *testing.T) {
	// Numeric kind=3 (Client), legacy attributeMap event, status_message fallback,
	// missing timestamp skipped, and log body + exception.message combination.
	if _, ok := spanRowToSample(rawRow{Data: map[string]any{"span_id": "no-ts"}}); ok {
		t.Error("expected span without timestamp to be skipped")
	}
	if _, ok := logRowToSample(rawRow{Data: map[string]any{"id": "no-ts"}}); ok {
		t.Error("expected log without timestamp to be skipped")
	}

	s, ok := spanRowToSample(rawRow{
		Timestamp: winStart.Add(time.Minute),
		Data: map[string]any{
			"span_id":        "sp-legacy",
			"kind":           json.Number("3"),
			"serviceName":    "wallet-service",
			"status_message": "fallback status",
			"events": []any{
				map[string]any{
					"name":         "other",
					"attributeMap": map[string]any{"exception.type": "net.OpError", "exception.message": "dial tcp: connection refused"},
				},
			},
		},
	})
	if !ok || !s.Outbound || s.ExceptionType != "net.OpError" || !strings.Contains(s.Message, "connection refused") {
		t.Errorf("spanRowToSample(legacy) = %+v, %v", s, ok)
	}

	ls, ok := logRowToSample(rawRow{
		Timestamp: winStart.Add(time.Minute),
		Data: map[string]any{
			"id":                "log-combo",
			"body":              "charge failed",
			"attributes_string": map[string]any{"exception.message": "timeout exceeded"},
		},
	})
	if !ok || ls.Message != "charge failed: timeout exceeded" {
		t.Errorf("logRowToSample(combo) = %+v, %v", ls, ok)
	}

	// Summary severity >= 5 -> critical, long detail truncated, fallbacks for empty message.
	var many []ErrorSample
	for i := range 5 {
		many = append(many, ErrorSample{
			Service: "orders-service",
			Time:    winStart.Add(time.Duration(i) * time.Second),
			Message: strings.Repeat("m", 120),
		})
	}
	evs := summarizeErrorSpans(many)
	if len(evs) != 1 || evs[0].Severity != event.SeverityCritical || !strings.HasSuffix(evs[0].Title, "...)") {
		t.Errorf("summarizeErrorSpans(5 long) = %+v", evs)
	}
	if got := formatErrorSummary(2, ErrorSample{ExceptionType: "runtime.errorString"}); got != "2 error spans (runtime.errorString)" {
		t.Errorf("formatErrorSummary(exc) = %q", got)
	}
	if got := formatErrorSummary(1, ErrorSample{HTTPStatus: 503}); got != "1 error span (HTTP 503)" {
		t.Errorf("formatErrorSummary(http) = %q", got)
	}
	if got := formatErrorSummary(3, ErrorSample{}); got != "3 error spans" {
		t.Errorf("formatErrorSummary(bare) = %q", got)
	}

	// ErrorSamples partial failure: spans succeed while logs fail.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req queryRangeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.CompositeQuery.Queries[0].Spec.Signal == "traces" {
			_, _ = w.Write(fixture(t, "error_spans.json"))
			return
		}
		http.Error(w, `{"message":"logs unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "k", srv.Client())
	samples, _, err := c.ErrorSamples(context.Background(), testWindow(t))
	if err == nil || !strings.Contains(err.Error(), "logs unavailable") {
		t.Errorf("ErrorSamples(partial) err = %v", err)
	}
	if len(samples) != 3 {
		t.Errorf("ErrorSamples(partial) returned %d spans, want 3", len(samples))
	}

	// Collector with matching version has nil Warnings().
	col, _ := New(srv.URL, "k", srv.Client())
	if col.Warnings() != nil {
		t.Errorf("fresh Collector.Warnings() = %v, want nil", col.Warnings())
	}
}

func TestCrossHostRedirectBlocked(t *testing.T) {
	otherHit := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHit = true
		if r.Header.Get("SigNoz-Api-Key") != "" {
			t.Error("SigNoz-Api-Key leaked to cross-host redirect target")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer other.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "super-secret-api-key", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CheckAuth(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing cross-host redirect") {
		t.Errorf("CheckAuth(redirect) err = %v, want cross-host redirect refusal", err)
	}
	if otherHit {
		t.Error("redirect target should not have been contacted")
	}
}

func TestRawQueryPageCapStopsAmplification(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		// Always return 2 rows (== rawPageLimit) regardless of time range.
		fmt.Fprint(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[
			{"timestamp":"2026-11-26T09:05:00Z","data":{"span_id":"s1","resource":{"service.name":"wallet-service"}}},
			{"timestamp":"2026-11-26T09:06:00Z","data":{"span_id":"s2","resource":{"service.name":"wallet-service"}}}
		]}]}}}`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.rawPageLimit = 2

	samples, warns, err := c.ErrorSpans(context.Background(), testWindow(t))
	if err != nil {
		t.Fatalf("ErrorSpans() error = %v", err)
	}
	if got := int(calls.Load()); got > maxRawPages {
		t.Errorf("made %d requests, want <= %d", got, maxRawPages)
	}
	if len(samples) != 2 {
		t.Errorf("got %d samples, want 2 deduplicated samples retained on cap", len(samples))
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "could not split further") {
		t.Errorf("warns = %v, want page-limit warning", warns)
	}
}

func TestAlertDuplicateFiringPreservesEnd(t *testing.T) {
	c, err := NewClient("http://localhost:8080", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	rule := alertRule{ID: "r1", Alert: "Lab error rate", Labels: map[string]string{"severity": "critical"}}
	firedAt := time.Date(2026, 11, 26, 9, 5, 0, 0, time.UTC).UnixMilli()
	resolvedAt := time.Date(2026, 11, 26, 9, 10, 0, 0, time.UTC).UnixMilli()

	items := []ruleHistoryItem{
		{RuleID: "r1", RuleName: "Lab error rate", State: "firing", StateChanged: true, UnixMilli: firedAt, Fingerprint: 42},
		{RuleID: "r1", RuleName: "Lab error rate", State: "FIRING", StateChanged: true, UnixMilli: firedAt, Fingerprint: 42},
		{RuleID: "r1", RuleName: "Lab error rate", State: "inactive", StateChanged: true, UnixMilli: resolvedAt, Fingerprint: 42},
	}
	evs := c.historyToEvents(rule, items, testWindow(t))
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2 (1 fired, 1 resolved): %+v", len(evs), evs)
	}
	if evs[0].End == nil || !evs[0].End.Equal(time.UnixMilli(resolvedAt).UTC()) {
		t.Errorf("fired event End = %v, want %v", evs[0].End, time.UnixMilli(resolvedAt).UTC())
	}
}

func TestTrafficOverflowAndInvalidValues(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"status":"success","data":{"type":"time_series","data":{"results":[
			{"queryName":"A","aggregations":[{"index":0,"series":[{"labels":[{"key":{"name":"service.name"},"value":"orders-service"}],"values":[
				{"timestamp":1795683600000,"value":100},
				{"timestamp":1795683900000,"value":-0.4},
				{"timestamp":1795684200000,"value":1e20}
			]}]}]},
			{"queryName":"C","aggregations":[{"index":0,"series":[{"labels":[{"key":{"name":"service.name"},"value":"orders-service"}],"values":[
				{"timestamp":1795683600000,"value":999}
			]}]}]}
		]}}}`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	// Step > math.MaxInt64/1440 (~74 days) must not overflow chunkSpan into an infinite loop.
	buckets, _, err := c.TrafficBuckets(context.Background(), testWindow(t), 100*24*time.Hour)
	if err != nil {
		t.Fatalf("TrafficBuckets(large step) error = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
	bs := buckets["orders-service"]
	if len(bs) != 1 || bs[0].Requests != 100 || bs[0].Errors != 0 {
		t.Errorf("buckets = %+v, want single valid bucket (100 req, 0 err)", bs)
	}
}
