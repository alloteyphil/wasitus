package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/collector"
	"github.com/alloteyphil/wasitus/internal/collector/signoz"
)

// failingRawProxy forwards every request to inner except raw
// /api/v5/query_range calls (error spans and logs), which fail with HTTP 500.
func failingRawProxy(t *testing.T, inner string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/api/v5/query_range" {
			var req struct {
				RequestType string `json:"requestType"`
			}
			if err := json.Unmarshal(body, &req); err == nil && req.RequestType == "raw" {
				http.Error(w, `{"error":{"message":"clickhouse timeout"}}`, http.StatusInternalServerError)
				return
			}
		}
		preq, err := http.NewRequestWithContext(r.Context(), r.Method, inner+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		preq.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(preq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			t.Logf("proxy copy: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestErrorQueriesFailDuringSurge is the regression test for a confident,
// false EXTERNAL / traffic verdict: traffic is 10x baseline with half of all
// requests failing, but every error-sample query fails. The verdict must be
// UNKNOWN and say why, not "no new error classes appeared".
func TestErrorQueriesFailDuringSurge(t *testing.T) {
	sc := scenarioFixture{
		ID:         "error-queries-fail",
		WindowFrom: "2026-11-26T09:00:00Z",
		WindowTo:   "2026-11-26T09:15:00Z",
		Traffic: map[string]scenarioTraffic{
			"storefront": {BaselineReq: 600, WindowReq: 6000, WindowErr: 3000},
		},
	}
	winFrom, err := time.Parse(time.RFC3339, sc.WindowFrom)
	if err != nil {
		t.Fatal(err)
	}
	winTo, err := time.Parse(time.RFC3339, sc.WindowTo)
	if err != nil {
		t.Fatal(err)
	}
	srv := failingRawProxy(t, startScenarioSigNozServer(t, sc, winFrom, winTo).URL)

	cfg := filepath.Join(t.TempDir(), "wasitus.yaml")
	write(t, cfg, fmt.Sprintf("timezone: UTC\nsources:\n  signoz:\n    url: %s\n", srv.URL))

	code, out, errOut := runCLI("timeline", "--config", cfg,
		"--from", sc.WindowFrom, "--to", sc.WindowTo, "--format", "json")
	if code != exitPartial {
		t.Errorf("exit code = %d, want %d (partial)\nstderr: %s", code, exitPartial, errOut)
	}
	var rep struct {
		Verdict *struct {
			Side       string   `json:"side"`
			Confidence string   `json:"confidence"`
			Evidence   []string `json:"evidence"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out)
	}
	if rep.Verdict == nil {
		t.Fatalf("no verdict in output:\n%s", out)
	}
	if rep.Verdict.Side != "UNKNOWN" || rep.Verdict.Confidence != "low" {
		t.Errorf("verdict = (%s, %s), want (UNKNOWN, low)\nevidence: %v",
			rep.Verdict.Side, rep.Verdict.Confidence, rep.Verdict.Evidence)
	}
	ev := strings.Join(rep.Verdict.Evidence, "\n")
	if !strings.Contains(ev, "error samples could not be read completely") {
		t.Errorf("evidence does not name the missing error samples:\n%s", ev)
	}
	if strings.Contains(ev, "no new error classes") {
		t.Errorf("evidence still claims no new error classes:\n%s", ev)
	}
}

func TestChangeSourceFailed(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name     string
		failures []collector.Failure
		want     bool
	}{
		{"none", nil, false},
		{"signoz only", []collector.Failure{{Collector: signoz.Source, Err: boom}}, false},
		{"github", []collector.Failure{{Collector: "github", Err: boom}}, true},
		{"changes file after signoz", []collector.Failure{
			{Collector: signoz.Source, Err: boom}, {Collector: "changes-file", Err: boom},
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := changeSourceFailed(tc.failures); got != tc.want {
				t.Errorf("changeSourceFailed = %v, want %v", got, tc.want)
			}
		})
	}
}
