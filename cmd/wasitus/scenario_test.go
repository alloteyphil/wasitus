package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/render"
)

var dominantShareRE = regexp.MustCompile(`(?m)^(\d+)% of errors \(`)

type scenarioFixture struct {
	ID                         string   `json:"id"`
	Name                       string   `json:"name"`
	WindowFrom                 string   `json:"window_from"`
	WindowTo                   string   `json:"window_to"`
	ExpectedSide               string   `json:"expected_side"`
	ExpectedCause              string   `json:"expected_cause"`
	ExpectedLabel              string   `json:"expected_label"`
	ExpectedEvidenceSubstrings []string `json:"expected_evidence_substrings"`
	// ExpectedMinDominantShare, when > 0, requires the "N% of errors" evidence
	// line to report a share of at least this fraction (guards cascade dilution).
	ExpectedMinDominantShare float64                    `json:"expected_min_dominant_share"`
	ChangesYAML              string                     `json:"changes_yaml"`
	GitHubDeployments        []json.RawMessage          `json:"github_deployments"`
	SigNozAlerts             []scenarioAlert            `json:"signoz_alerts"`
	ErrorSpans               []scenarioSpanBatch        `json:"error_spans"`
	ErrorLogs                []scenarioSpanBatch        `json:"error_logs"`
	LookbackErrorSpans       []scenarioSpanBatch        `json:"lookback_error_spans"`
	Traffic                  map[string]scenarioTraffic `json:"traffic"`
}

type scenarioAlert struct {
	RuleID   string  `json:"rule_id"`
	RuleName string  `json:"rule_name"`
	Severity string  `json:"severity"`
	Service  string  `json:"service"`
	FiredAt  string  `json:"fired_at"`
	Value    float64 `json:"value"`
}

type scenarioSpanBatch struct {
	Count         int    `json:"count"`
	Start         string `json:"start"`
	StepSeconds   int    `json:"step_seconds"`
	Service       string `json:"service"`
	Kind          string `json:"kind"`
	ServerAddress string `json:"server_address"`
	HTTPStatus    string `json:"http_status"`
	ExceptionType string `json:"exception_type"`
	Message       string `json:"message"`
}

type scenarioTraffic struct {
	BaselineReq int `json:"baseline_req"`
	BaselineErr int `json:"baseline_err"`
	WindowReq   int `json:"window_req"`
	WindowErr   int `json:"window_err"`
}

// TestScenario replays every staged incident scenario from docs/SCENARIOS.md
// (S1 through S8) end-to-end through the CLI against offline httptest servers.
// Run one scenario with: go test ./... -run Scenario/S3
func TestScenario(t *testing.T) {
	ids := []string{"S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8"}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "scenarios", id, "scenario.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture %s: %v", path, err)
			}
			var sc scenarioFixture
			if err := json.Unmarshal(data, &sc); err != nil {
				t.Fatalf("parse fixture %s: %v", path, err)
			}
			runScenarioCheck(t, sc)
		})
	}
}

func TestScenarioS6WithSteadyBackgroundErrors(t *testing.T) {
	sc := scenarioFixture{
		ID:            "S6-bg",
		Name:          "Traffic surge with steady background errors",
		WindowFrom:    "2026-11-26T09:00:00Z",
		WindowTo:      "2026-11-26T09:15:00Z",
		ExpectedSide:  "EXTERNAL",
		ExpectedCause: "traffic",
		ExpectedLabel: "EXTERNAL / traffic",
		ExpectedEvidenceSubstrings: []string{
			"10x",
			"error mix stayed within",
		},
		LookbackErrorSpans: []scenarioSpanBatch{{
			Count:         4,
			Start:         "2026-11-26T08:15:00Z",
			StepSeconds:   300,
			Service:       "orders-service",
			Kind:          "Server",
			HTTPStatus:    "500",
			ExceptionType: "*errors.errorString",
			Message:       "background validation error",
		}},
		ErrorSpans: []scenarioSpanBatch{{
			Count:         8,
			Start:         "2026-11-26T09:01:00Z",
			StepSeconds:   60,
			Service:       "orders-service",
			Kind:          "Server",
			HTTPStatus:    "500",
			ExceptionType: "*errors.errorString",
			Message:       "background validation error",
		}},
		Traffic: map[string]scenarioTraffic{
			"storefront": {BaselineReq: 600, BaselineErr: 3, WindowReq: 6000, WindowErr: 30},
		},
	}
	runScenarioCheck(t, sc)
}

func runScenarioCheck(t *testing.T, sc scenarioFixture) {
	t.Helper()

	winFrom, err := time.Parse(time.RFC3339, sc.WindowFrom)
	if err != nil {
		t.Fatalf("parse window_from: %v", err)
	}
	winTo, err := time.Parse(time.RFC3339, sc.WindowTo)
	if err != nil {
		t.Fatalf("parse window_to: %v", err)
	}

	szSrv := startScenarioSigNozServer(t, sc, winFrom, winTo)
	t.Setenv(testSigNozKeyEnv, "scenario-signoz-key")

	dir := t.TempDir()
	var cfgBuf strings.Builder
	fmt.Fprintf(&cfgBuf, "timezone: UTC\nlookback: 60m\nsources:\n  signoz:\n    url: %s\n    api_key_env: %s\n", szSrv.URL, testSigNozKeyEnv)

	if sc.ChangesYAML != "" {
		changesPath := filepath.Join(dir, "changes.yaml")
		write(t, changesPath, sc.ChangesYAML)
		fmt.Fprintf(&cfgBuf, "  changes_file: changes.yaml\n")
	}

	if len(sc.GitHubDeployments) > 0 {
		t.Setenv(testTokenEnv, "scenario-github-token")
		deploysJSON, err := json.Marshal(sc.GitHubDeployments)
		if err != nil {
			t.Fatal(err)
		}
		fakeGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/repos/alloteyphil/wasitus/deployments" {
				_, _ = w.Write(deploysJSON)
				return
			}
			_, _ = w.Write([]byte("[]"))
		})
		fmt.Fprintf(&cfgBuf, "  github:\n    token_env: %s\n    repos: [alloteyphil/wasitus]\nservices:\n  alloteyphil/wasitus: orders-service\n", testTokenEnv)
	}

	cfgPath := filepath.Join(dir, "wasitus.yaml")
	write(t, cfgPath, cfgBuf.String())

	// 1. Run with JSON output and verify the structured verdict contract.
	code, out, errOut := runCLI("timeline",
		"--config", cfgPath,
		"--from", sc.WindowFrom,
		"--to", sc.WindowTo,
		"--format", "json",
	)
	if code != exitOK {
		t.Fatalf("%s (%s): exit = %d, stderr = %q", sc.ID, sc.Name, code, errOut)
	}

	var report struct {
		Verdict *render.Verdict `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("%s: decode JSON output: %v\n%s", sc.ID, err, out)
	}
	if report.Verdict == nil {
		t.Fatalf("%s: missing verdict in JSON output:\n%s", sc.ID, out)
	}
	if report.Verdict.Side != sc.ExpectedSide {
		t.Errorf("%s (%s): Side = %q, want %q (evidence: %v)", sc.ID, sc.Name, report.Verdict.Side, sc.ExpectedSide, report.Verdict.Evidence)
	}
	if report.Verdict.Cause != sc.ExpectedCause {
		t.Errorf("%s (%s): Cause = %q, want %q (evidence: %v)", sc.ID, sc.Name, report.Verdict.Cause, sc.ExpectedCause, report.Verdict.Evidence)
	}
	if report.Verdict.Label != sc.ExpectedLabel {
		t.Errorf("%s (%s): Label = %q, want %q", sc.ID, sc.Name, report.Verdict.Label, sc.ExpectedLabel)
	}
	evidenceJoined := strings.Join(report.Verdict.Evidence, "\n")
	for _, want := range sc.ExpectedEvidenceSubstrings {
		if !strings.Contains(evidenceJoined, want) {
			t.Errorf("%s (%s): evidence missing %q\ngot:\n%s", sc.ID, sc.Name, want, evidenceJoined)
		}
	}
	if sc.ExpectedMinDominantShare > 0 {
		m := dominantShareRE.FindStringSubmatch(evidenceJoined)
		if m == nil {
			t.Errorf("%s (%s): missing \"N%% of errors\" evidence line:\n%s", sc.ID, sc.Name, evidenceJoined)
		} else {
			pct, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("%s: parse dominant share: %v", sc.ID, err)
			}
			got := float64(pct) / 100
			if got+1e-9 < sc.ExpectedMinDominantShare {
				t.Errorf("%s (%s): dominant share %.0f%% < required %.0f%% (cascade dilution too high):\n%s",
					sc.ID, sc.Name, got*100, sc.ExpectedMinDominantShare*100, evidenceJoined)
			}
		}
	}

	// 2. Verify table and markdown formats also render cleanly and carry the verdict label.
	for _, format := range []string{"table", "markdown"} {
		c2, o2, e2 := runCLI("timeline",
			"--config", cfgPath,
			"--from", sc.WindowFrom,
			"--to", sc.WindowTo,
			"--format", format,
		)
		if c2 != exitOK {
			t.Errorf("%s (%s) format=%s: exit = %d, stderr = %q", sc.ID, sc.Name, format, c2, e2)
		}
		if !strings.Contains(o2, sc.ExpectedLabel) {
			t.Errorf("%s (%s) format=%s: output missing verdict label %q:\n%s", sc.ID, sc.Name, format, sc.ExpectedLabel, o2)
		}
	}
}

func startScenarioSigNozServer(t *testing.T, sc scenarioFixture, winFrom, winTo time.Time) *httptest.Server {
	t.Helper()

	allSpans := expandSpanBatches(sc.ID+"-win", sc.ErrorSpans)
	allSpans = append(allSpans, expandSpanBatches(sc.ID+"-lb", sc.LookbackErrorSpans)...)
	allLogs := expandSpanBatches(sc.ID+"-log", sc.ErrorLogs)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/version":
			fmt.Fprint(w, `{"version":"v0.149.0","setupCompleted":true}`)
		case r.URL.Path == "/api/v3/rules":
			writeRulesResponse(w, sc.SigNozAlerts)
		case strings.HasPrefix(r.URL.Path, "/api/v2/rules/") && strings.HasSuffix(r.URL.Path, "/history/timeline"):
			ruleID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v2/rules/"), "/history/timeline")
			writeRuleHistoryResponse(w, ruleID, sc.SigNozAlerts)
		case r.URL.Path == "/api/v5/query_range":
			var req struct {
				Start          int64  `json:"start"`
				End            int64  `json:"end"`
				RequestType    string `json:"requestType"`
				CompositeQuery struct {
					Queries []struct {
						Spec struct {
							Signal       string `json:"signal"`
							StepInterval int64  `json:"stepInterval"`
						} `json:"spec"`
					} `json:"queries"`
				} `json:"compositeQuery"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.RequestType == "time_series" {
				var stepSec int64
				if len(req.CompositeQuery.Queries) > 0 {
					stepSec = req.CompositeQuery.Queries[0].Spec.StepInterval
				}
				writeTrafficResponse(w, req.Start, req.End, stepSec, sc.Traffic, winFrom, winTo)
				return
			}
			signal := ""
			if len(req.CompositeQuery.Queries) > 0 {
				signal = req.CompositeQuery.Queries[0].Spec.Signal
			}
			if signal == "logs" {
				writeErrorLogsResponse(w, req.Start, req.End, allLogs)
				return
			}
			writeErrorSpansResponse(w, req.Start, req.End, allSpans)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type syntheticSpan struct {
	ID            string
	Time          time.Time
	Service       string
	Kind          string
	ServerAddress string
	HTTPStatus    string
	ExceptionType string
	Message       string
}

func expandSpanBatches(prefix string, batches []scenarioSpanBatch) []syntheticSpan {
	var out []syntheticSpan
	seq := 0
	for _, b := range batches {
		start, err := time.Parse(time.RFC3339, b.Start)
		if err != nil {
			panic(err)
		}
		step := time.Duration(b.StepSeconds) * time.Second
		if step <= 0 {
			step = 10 * time.Second
		}
		for i := range b.Count {
			seq++
			out = append(out, syntheticSpan{
				ID:            fmt.Sprintf("%s-%04d", prefix, seq),
				Time:          start.Add(time.Duration(i) * step),
				Service:       b.Service,
				Kind:          b.Kind,
				ServerAddress: b.ServerAddress,
				HTTPStatus:    b.HTTPStatus,
				ExceptionType: b.ExceptionType,
				Message:       b.Message,
			})
		}
	}
	return out
}

func writeRulesResponse(w http.ResponseWriter, alerts []scenarioAlert) {
	seen := make(map[string]bool)
	var rules []map[string]any
	for _, a := range alerts {
		if seen[a.RuleID] {
			continue
		}
		seen[a.RuleID] = true
		rules = append(rules, map[string]any{
			"id":     a.RuleID,
			"alert":  a.RuleName,
			"labels": map[string]string{"severity": a.Severity},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"data": map[string]any{
			"rules": rules,
			"total": len(rules),
		},
	})
}

func writeRuleHistoryResponse(w http.ResponseWriter, ruleID string, alerts []scenarioAlert) {
	var items []map[string]any
	for i, a := range alerts {
		if a.RuleID != ruleID {
			continue
		}
		ts, err := time.Parse(time.RFC3339, a.FiredAt)
		if err != nil {
			panic(err)
		}
		items = append(items, map[string]any{
			"ruleId":       a.RuleID,
			"ruleName":     a.RuleName,
			"state":        "firing",
			"stateChanged": true,
			"unixMilli":    ts.UnixMilli(),
			"fingerprint":  i + 1,
			"value":        a.Value,
			"labels": []map[string]any{
				{"key": map[string]string{"name": "service.name"}, "value": a.Service},
			},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"data": map[string]any{
			"items": items,
			"total": len(items),
		},
	})
}

func writeErrorSpansResponse(w http.ResponseWriter, startMs, endMs int64, spans []syntheticSpan) {
	var rows []map[string]any
	for _, s := range spans {
		ms := s.Time.UnixMilli()
		if ms < startMs || ms > endMs {
			continue
		}
		attrs := map[string]any{}
		if s.ServerAddress != "" {
			attrs["server.address"] = s.ServerAddress
		}
		events := []map[string]any{}
		if s.ExceptionType != "" || s.Message != "" {
			events = append(events, map[string]any{
				"name": "exception",
				"attributes": map[string]any{
					"exception.type":    s.ExceptionType,
					"exception.message": s.Message,
				},
			})
		}
		rowData := map[string]any{
			"span_id":              s.ID,
			"trace_id":             "tr-" + s.ID,
			"kind_string":          s.Kind,
			"response_status_code": s.HTTPStatus,
			"resource":             map[string]any{"service.name": s.Service},
			"attributes":           attrs,
			"events":               events,
		}
		rows = append(rows, map[string]any{
			"timestamp": s.Time.UTC().Format(time.RFC3339Nano),
			"data":      rowData,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"data": map[string]any{
			"type": "raw",
			"data": map[string]any{
				"results": []map[string]any{{
					"queryName": "A",
					"rows":      rows,
				}},
			},
		},
	})
}

func writeErrorLogsResponse(w http.ResponseWriter, startMs, endMs int64, logs []syntheticSpan) {
	var rows []map[string]any
	for _, s := range logs {
		ms := s.Time.UnixMilli()
		if ms < startMs || ms > endMs {
			continue
		}
		attrsNum := map[string]any{}
		if s.HTTPStatus != "" {
			if n, err := strconv.Atoi(s.HTTPStatus); err == nil {
				attrsNum["http.response.status_code"] = n
			}
		}
		attrsStr := map[string]any{}
		if s.ServerAddress != "" {
			attrsStr["server.address"] = s.ServerAddress
		}
		rows = append(rows, map[string]any{
			"data": map[string]any{
				"id":                "log-" + s.ID,
				"timestamp":         s.Time.UnixNano(),
				"trace_id":          "tr-" + s.ID,
				"span_id":           s.ID,
				"severity_text":     "ERROR",
				"severity_number":   17,
				"body":              s.Message,
				"resources_string":  map[string]any{"service.name": s.Service},
				"attributes_string": attrsStr,
				"attributes_number": attrsNum,
			},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"data": map[string]any{
			"type": "raw",
			"data": map[string]any{
				"results": []map[string]any{{
					"queryName": "A",
					"rows":      rows,
				}},
			},
		},
	})
}

func writeTrafficResponse(
	w http.ResponseWriter,
	startMs, endMs, stepSec int64,
	spec map[string]scenarioTraffic,
	winFrom, winTo time.Time,
) {
	if stepSec <= 0 {
		stepSec = 300
	}
	step := time.Duration(stepSec) * time.Second
	start := time.UnixMilli(startMs).UTC().Truncate(step)
	end := time.UnixMilli(endMs).UTC()

	var svcs []string
	for svc := range spec {
		svcs = append(svcs, svc)
	}
	slices.Sort(svcs)

	var reqSeries, errSeries []map[string]any
	for _, svc := range svcs {
		st := spec[svc]
		var reqVals, errVals []map[string]any
		for cur := start; cur.Before(end); cur = cur.Add(step) {
			inWindow := !cur.Before(winFrom) && cur.Before(winTo)
			reqs, errs := st.BaselineReq, st.BaselineErr
			if inWindow {
				reqs, errs = st.WindowReq, st.WindowErr
			}
			// Only emit baseline buckets around the incident time-of-day (plus/minus
			// 15 minutes) to keep test payloads compact while covering every day's
			// same-time-of-day slot required by traffic.Analyze.
			if !inWindow {
				tod := cur.Hour()*60 + cur.Minute()
				fromTod := winFrom.Hour()*60 + winFrom.Minute()
				toTod := winTo.Hour()*60 + winTo.Minute()
				if tod < fromTod-15 || tod > toTod+15 {
					continue
				}
			}
			reqVals = append(reqVals, map[string]any{"timestamp": cur.UnixMilli(), "value": float64(reqs)})
			errVals = append(errVals, map[string]any{"timestamp": cur.UnixMilli(), "value": float64(errs)})
		}
		labels := []map[string]any{{"key": map[string]string{"name": "service.name"}, "value": svc}}
		reqSeries = append(reqSeries, map[string]any{"labels": labels, "values": reqVals})
		errSeries = append(errSeries, map[string]any{"labels": labels, "values": errVals})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"data": map[string]any{
			"type": "time_series",
			"data": map[string]any{
				"results": []map[string]any{
					{"queryName": "A", "aggregations": []map[string]any{{"index": 0, "series": reqSeries}}},
					{"queryName": "B", "aggregations": []map[string]any{{"index": 0, "series": errSeries}}},
				},
			},
		},
	})
}

func TestTimelineFormatsRedactionAndUserRules(t *testing.T) {
	dir := t.TempDir()
	changesPath := filepath.Join(dir, "changes.yaml")
	write(t, changesPath, "- time: \"2026-11-26 09:30\"\n  service: orders-service\n  title: \"Notified alice@example.com about deploy\"\n")

	cfgPath := filepath.Join(dir, "wasitus.yaml")
	write(t, cfgPath, "timezone: UTC\nsources:\n  changes_file: changes.yaml\n")

	// Default redaction strips email across formats.
	for _, format := range []string{"table", "markdown", "json"} {
		code, out, errOut := runCLI("timeline", "--config", cfgPath, "--from", "-1h", "--format", format)
		if code != exitOK || errOut != "" {
			t.Fatalf("format=%s: exit %d, stderr %q", format, code, errOut)
		}
		if strings.Contains(out, "alice@example.com") {
			t.Errorf("format=%s: leaked email with default redaction:\n%s", format, out)
		}
		if !strings.Contains(out, "[email]") {
			t.Errorf("format=%s: missing [email] placeholder:\n%s", format, out)
		}
	}

	// --no-redact disables redaction locally.
	code, out, errOut := runCLI("timeline", "--config", cfgPath, "--from", "-1h", "--no-redact")
	if code != exitOK || errOut != "" {
		t.Fatalf("--no-redact: exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "alice@example.com") {
		t.Errorf("--no-redact: expected raw email in output:\n%s", out)
	}

	// Invalid --format returns exitUsage.
	code, _, errOut = runCLI("timeline", "--config", cfgPath, "--from", "-1h", "--format", "xml")
	if code != exitUsage || !strings.Contains(errOut, "unknown format") {
		t.Errorf("invalid format: exit %d, stderr %q; want exit %d", code, errOut, exitUsage)
	}

	// Invalid rules_file fails fast with exitError.
	badRules := filepath.Join(dir, "bad-rules.yaml")
	write(t, badRules, "- id: bad\n  class: nonsense\n  match:\n    any:\n      - http_status: 500\n")
	badCfg := filepath.Join(dir, "bad-rules-wasitus.yaml")
	write(t, badCfg, "timezone: UTC\nsources:\n  changes_file: changes.yaml\nrules_file: bad-rules.yaml\n")
	code, _, errOut = runCLI("timeline", "--config", badCfg, "--from", "-1h")
	if code != exitError || !strings.Contains(errOut, "load rules_file") {
		t.Errorf("bad rules_file: exit %d, stderr %q; want exit %d", code, errOut, exitError)
	}

	// Missing rules_file fails fast with exitError.
	missingCfg := filepath.Join(dir, "missing-rules-wasitus.yaml")
	write(t, missingCfg, "timezone: UTC\nsources:\n  changes_file: changes.yaml\nrules_file: does-not-exist.yaml\n")
	code, _, errOut = runCLI("timeline", "--config", missingCfg, "--from", "-1h")
	if code != exitError || !strings.Contains(errOut, "read rules_file") {
		t.Errorf("missing rules_file: exit %d, stderr %q; want exit %d", code, errOut, exitError)
	}

	// Custom rules_file overrides classification end-to-end.
	customRules := filepath.Join(dir, "custom-rules.yaml")
	write(t, customRules, "- id: custom-billing-code\n  class: account\n  match:\n    any:\n      - message_contains: \"CUSTOM_BILLING_HOLD\"\n")
	sc := scenarioFixture{
		ID:         "custom",
		WindowFrom: "2026-11-26T09:00:00Z",
		WindowTo:   "2026-11-26T09:15:00Z",
		ErrorSpans: []scenarioSpanBatch{{
			Count:       5,
			Start:       "2026-11-26T09:01:00Z",
			StepSeconds: 30,
			Service:     "wallet-service",
			Kind:        "Server",
			HTTPStatus:  "500",
			Message:     "payment rejected: CUSTOM_BILLING_HOLD",
		}},
	}
	winFrom, _ := time.Parse(time.RFC3339, sc.WindowFrom)
	winTo, _ := time.Parse(time.RFC3339, sc.WindowTo)
	szSrv := startScenarioSigNozServer(t, sc, winFrom, winTo)
	customCfg := filepath.Join(dir, "custom-wasitus.yaml")
	write(t, customCfg, fmt.Sprintf("timezone: UTC\nsources:\n  signoz:\n    url: %s\nrules_file: custom-rules.yaml\n", szSrv.URL))
	code, out, errOut = runCLI("timeline", "--config", customCfg, "--from", sc.WindowFrom, "--to", sc.WindowTo, "--format", "json")
	if code != exitOK || errOut != "" {
		t.Fatalf("custom rules: exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, `"EXTERNAL / account"`) || !strings.Contains(out, `"custom-billing-code"`) {
		t.Errorf("custom rules output did not apply custom rule:\n%s", out)
	}
}

func TestExternalEventDoesNotPoisonInternalHosts(t *testing.T) {
	sc := scenarioFixture{
		ID:            "ext-host",
		Name:          "External event naming mockpay",
		WindowFrom:    "2026-11-26T09:00:00Z",
		WindowTo:      "2026-11-26T09:15:00Z",
		ExpectedSide:  "EXTERNAL",
		ExpectedCause: "dependency",
		ExpectedLabel: "EXTERNAL / dependency",
		ChangesYAML:   "- time: \"2026-11-26T09:01:00Z\"\n  kind: external\n  service: mockpay\n  title: \"mockpay status page: degraded\"\n",
		ErrorSpans: []scenarioSpanBatch{{
			Count:         8,
			Start:         "2026-11-26T09:01:00Z",
			StepSeconds:   30,
			Service:       "wallet-service",
			Kind:          "Client",
			ServerAddress: "mockpay",
			HTTPStatus:    "503",
			Message:       "mockpay rejected the charge: HTTP 503 ServiceUnavailable",
		}},
	}
	runScenarioCheck(t, sc)
}

func TestUnalignedTrafficWindowDetectsSpike(t *testing.T) {
	winFrom := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	winTo := time.Date(2026, 11, 26, 9, 15, 0, 0, time.UTC)
	sc := scenarioFixture{
		ID:         "S6-unaligned",
		WindowFrom: "2026-11-26T09:02:13Z",
		WindowTo:   "2026-11-26T09:17:42Z",
		Traffic: map[string]scenarioTraffic{
			"storefront": {BaselineReq: 600, BaselineErr: 0, WindowReq: 6000, WindowErr: 0},
		},
	}
	szSrv := startScenarioSigNozServer(t, sc, winFrom, winTo)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "wasitus.yaml")
	write(t, cfgPath, fmt.Sprintf("timezone: UTC\nsources:\n  signoz:\n    url: %s\n", szSrv.URL))

	code, out, errOut := runCLI("timeline",
		"--config", cfgPath,
		"--from", sc.WindowFrom,
		"--to", sc.WindowTo,
		"--format", "json",
	)
	if code != exitOK || errOut != "" {
		t.Fatalf("unaligned traffic: exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, `"EXTERNAL / traffic"`) {
		t.Errorf("unaligned window missed traffic spike:\n%s", out)
	}
}

func TestAnalyzeIncidentPartialFailureAndStderrRedaction(t *testing.T) {
	const rawEmail = "bob@example.com"
	spans := expandSpanBatches("p", []scenarioSpanBatch{{
		Count:         5,
		Start:         "2026-11-26T09:01:00Z",
		StepSeconds:   30,
		Service:       "wallet-service",
		Kind:          "Client",
		ServerAddress: "mockpay",
		HTTPStatus:    "402",
		Message:       "mockpay rejected the charge: HTTP 402 AccountSuspended",
	}})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			fmt.Fprint(w, `{"version":"v0.149.0","setupCompleted":true}`)
		case "/api/v3/rules":
			fmt.Fprint(w, `{"status":"success","data":{"rules":[],"total":0}}`)
		case "/api/v5/query_range":
			var req struct {
				Start          int64  `json:"start"`
				End            int64  `json:"end"`
				RequestType    string `json:"requestType"`
				CompositeQuery struct {
					Queries []struct {
						Spec struct {
							Signal string `json:"signal"`
						} `json:"spec"`
					} `json:"queries"`
				} `json:"compositeQuery"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.RequestType == "time_series" {
				http.Error(w, `{"error":{"message":"query failed for `+rawEmail+`"}}`, http.StatusInternalServerError)
				return
			}
			if len(req.CompositeQuery.Queries) > 0 && req.CompositeQuery.Queries[0].Spec.Signal == "logs" {
				fmt.Fprint(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[]}]}}}`)
				return
			}
			writeErrorSpansResponse(w, req.Start, req.End, spans)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "wasitus.yaml")
	write(t, cfgPath, fmt.Sprintf("timezone: UTC\nsources:\n  signoz:\n    url: %s\n", srv.URL))

	// Default: traffic fails -> exitPartial, verdict still rendered, stderr redacted.
	code, out, errOut := runCLI("timeline",
		"--config", cfgPath,
		"--from", "2026-11-26T09:00:00Z",
		"--to", "2026-11-26T09:15:00Z",
		"--format", "json",
	)
	if code != exitPartial {
		t.Fatalf("partial analysis exit = %d, want %d (stderr: %q)", code, exitPartial, errOut)
	}
	if !strings.Contains(out, `"EXTERNAL / account"`) {
		t.Errorf("partial analysis should still render verdict:\n%s", out)
	}
	if strings.Contains(errOut, rawEmail) || !strings.Contains(errOut, "[email]") {
		t.Errorf("stderr should redact email by default, got: %q", errOut)
	}

	// --no-redact: raw email preserved on stderr.
	code, _, errOut = runCLI("timeline",
		"--config", cfgPath,
		"--from", "2026-11-26T09:00:00Z",
		"--to", "2026-11-26T09:15:00Z",
		"--no-redact",
	)
	if code != exitPartial || !strings.Contains(errOut, rawEmail) {
		t.Errorf("--no-redact stderr = %q (exit %d), want raw email", errOut, code)
	}
}
