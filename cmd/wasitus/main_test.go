package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/config"
	"github.com/alloteyphil/wasitus/internal/golden"
)

var fixedNow = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut, func() time.Time { return fixedNow })
	return code, out.String(), errOut.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := runCLI("version")
	if code != exitOK || out != "wasitus 0.0.0-dev\n" {
		t.Errorf("version = %d, %q", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	const cfg = "../../examples/wasitus.example.yaml"
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no command", nil, "Usage:"},
		{"unknown command", []string{"blame"}, `unknown command "blame"`},
		{"missing --from", []string{"timeline", "--config", cfg}, "--from is required"},
		{"unparseable --from", []string{"timeline", "--config", cfg, "--from", "yesterday"}, "--from: unrecognised time"},
		{"reversed window", []string{"timeline", "--config", cfg, "--from", "-1h", "--to", "-2h"}, "is not after start"},
		{"stray argument", []string{"timeline", "--config", cfg, "--from", "-1h", "extra"}, `unexpected argument "extra"`},
		{"unknown flag", []string{"timeline", "--form", "-1h"}, "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errOut := runCLI(tt.args...)
			if code != exitUsage || !strings.Contains(errOut, tt.want) {
				t.Errorf("exit %d, stderr %q; want exit %d and %q", code, errOut, exitUsage, tt.want)
			}
		})
	}
}

// TestTimelineExample runs the real binary path against the shipped example
// files, so the examples can never silently break.
func TestTimelineExample(t *testing.T) {
	code, out, errOut := runCLI("timeline",
		"--config", "../../examples/wasitus.example.yaml",
		"--from", "2026-11-14 03:00", "--to", "2026-11-14 04:00")
	if code != exitOK || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	golden.Assert(t, "testdata/timeline_example.golden", []byte(out))
}

func TestTimelinePartialFailure(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "wasitus.yaml"), "sources:\n  changes_file: changes.yaml\n")
	write(t, filepath.Join(dir, "changes.yaml"), `
- time: "2026-11-26 09:30"
  title: Good note
- time: "not a time"
  title: Bad note
`)
	code, out, errOut := runCLI("timeline", "--config", filepath.Join(dir, "wasitus.yaml"), "--from", "-1h")
	if code != exitPartial {
		t.Errorf("exit = %d, want %d", code, exitPartial)
	}
	if !strings.Contains(out, "Good note") {
		t.Errorf("stdout does not show the good entry:\n%s", out)
	}
	if !strings.Contains(errOut, "warning: changes-file: entry 2") {
		t.Errorf("stderr = %q, want a warning about entry 2", errOut)
	}
}

func TestTimelineConfigProblems(t *testing.T) {
	dir := t.TempDir()
	code, _, errOut := runCLI("timeline", "--config", filepath.Join(dir, "nope.yaml"), "--from", "-1h")
	if code != exitError || !strings.Contains(errOut, "read config") {
		t.Errorf("exit %d, stderr %q; want exit %d and read config", code, errOut, exitError)
	}
	if _, err := buildCollectors(&config.Config{}, os.LookupEnv); err == nil || !strings.Contains(err.Error(), "no collectors configured") {
		t.Errorf("buildCollectors(empty) error = %v, want no collectors configured", err)
	}
}

const testSigNozKeyEnv = "WASITUS_TEST_SIGNOZ_API_KEY"

func sigNozConfig(t *testing.T, url, keyEnv string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wasitus.yaml")
	var b strings.Builder
	fmt.Fprintf(&b, "sources:\n  signoz:\n    url: %s\n", url)
	if keyEnv != "" {
		fmt.Fprintf(&b, "    api_key_env: %s\n", keyEnv)
	}
	write(t, path, b.String())
	return path
}

func TestTimelineSigNoz(t *testing.T) {
	t.Setenv(testSigNozKeyEnv, "fake-signoz-key-for-tests")
	var mu sync.Mutex
	var keys []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			fmt.Fprint(w, `{"version":"v0.150.0","setupCompleted":true}`)
		case "/api/v3/rules":
			mu.Lock()
			keys = append(keys, r.Header.Get("SigNoz-Api-Key"))
			mu.Unlock()
			fmt.Fprint(w, `{"status":"success","data":{"rules":[{"id":"r1","alert":"Lab error rate","labels":{"severity":"critical"}}],"total":1}}`)
		case "/api/v2/rules/r1/history/timeline":
			mu.Lock()
			keys = append(keys, r.Header.Get("SigNoz-Api-Key"))
			mu.Unlock()
			fmt.Fprint(w, `{"status":"success","data":{"items":[{"ruleId":"r1","ruleName":"Lab error rate","state":"firing","stateChanged":true,"unixMilli":1795683780000,"labels":[{"key":{"name":"service.name"},"value":"wallet-service"}],"fingerprint":1,"value":31.4}],"total":1}}`)
		case "/api/v5/query_range":
			mu.Lock()
			keys = append(keys, r.Header.Get("SigNoz-Api-Key"))
			mu.Unlock()
			fmt.Fprint(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[{"timestamp":"2026-11-26T09:01:10Z","data":{"span_id":"sp1","kind_string":"Server","response_status_code":"502","resource":{"service.name":"wallet-service"},"events":[{"name":"exception","attributes":{"exception.message":"HTTP 402 AccountSuspended"}}]}}]}]}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	code, out, errOut := runCLI("timeline", "--config", sigNozConfig(t, srv.URL, testSigNozKeyEnv), "--from", "-1h")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"09:01  symptom  signoz  wallet-service  1 error span (HTTP 402 AccountSuspended)",
		"09:03  symptom  signoz  wallet-service  Alert FIRED: Lab error rate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "warning: signoz: SigNoz version v0.150.0 differs from tested version") {
		t.Errorf("stderr = %q, want SigNoz version warning", errOut)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) == 0 {
		t.Fatal("no authenticated requests reached fake SigNoz")
	}
	for _, k := range keys {
		if k != "fake-signoz-key-for-tests" {
			t.Errorf("SigNoz-Api-Key = %q, want key from %s", k, testSigNozKeyEnv)
		}
	}
}

func TestTimelineSigNozFailureIsPartial(t *testing.T) {
	t.Setenv(testSigNozKeyEnv, "fake-signoz-secret-99")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"status":"error","error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	code, _, errOut := runCLI("timeline", "--config", sigNozConfig(t, srv.URL, testSigNozKeyEnv), "--from", "-1h")
	if code != exitPartial {
		t.Errorf("exit = %d, want %d", code, exitPartial)
	}
	if !strings.Contains(errOut, "warning: signoz:") || !strings.Contains(errOut, "unauthorized") {
		t.Errorf("stderr = %q, want signoz unauthorized warning", errOut)
	}
	if strings.Contains(errOut, "fake-signoz-secret-99") {
		t.Errorf("stderr leaked API key: %q", errOut)
	}
}

func TestTimelineSigNozAPIKeyMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s without API key", r.URL)
	}))
	defer srv.Close()

	for _, unset := range []bool{false, true} {
		t.Setenv(testSigNozKeyEnv, "")
		if unset {
			if err := os.Unsetenv(testSigNozKeyEnv); err != nil {
				t.Fatal(err)
			}
		}
		code, _, errOut := runCLI("timeline", "--config", sigNozConfig(t, srv.URL, testSigNozKeyEnv), "--from", "-1h")
		want := "environment variable " + testSigNozKeyEnv + " is not set"
		if code != exitError || !strings.Contains(errOut, want) {
			t.Errorf("exit %d, stderr %q; want exit %d and %q", code, errOut, exitError, want)
		}
	}
}

// testTokenEnv is an environment variable no real setup uses, so a token in
// the developer's own environment can never reach these tests.
const testTokenEnv = "WASITUS_TEST_GITHUB_TOKEN"

// githubConfig writes a config that reads only GitHub, with the given repo
// mapped to orders-service.
func githubConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wasitus.yaml")
	write(t, path, "sources:\n  github:\n    token_env: "+testTokenEnv+
		"\n    repos: [alloteyphil/wasitus]\nservices:\n  alloteyphil/wasitus: orders-service\n")
	return path
}

// fakeGitHubAPI points the timeline command at a local server for the test.
func fakeGitHubAPI(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old := githubAPI
	githubAPI = srv.URL
	t.Cleanup(func() { githubAPI = old })
}

func TestTimelineGitHub(t *testing.T) {
	t.Setenv(testTokenEnv, "fake-token-for-tests")
	var mu sync.Mutex
	var auth []string
	fakeGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/alloteyphil/wasitus/pulls":
			fmt.Fprint(w, `[{"number":7,"title":"feat(orders): retry wallet-service calls","html_url":"https://github.com/alloteyphil/wasitus/pull/7",
				"updated_at":"2026-11-26T09:31:00Z","merged_at":"2026-11-26T09:30:00Z","user":{"login":"lab-bot"}}]`)
		case "/repos/alloteyphil/wasitus/releases":
			fmt.Fprint(w, `[{"id":1,"tag_name":"v1.4.0","draft":false,"created_at":"2026-11-26T09:44:00Z","published_at":"2026-11-26T09:45:00Z"}]`)
		default:
			fmt.Fprint(w, `[]`)
		}
	})

	code, out, errOut := runCLI("timeline", "--config", githubConfig(t), "--from", "-1h")
	if code != exitOK || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"09:30  change  github  orders-service  Merged PR #7: feat(orders): retry wallet-service calls",
		"09:45  change  github  orders-service  Released v1.4.0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, out)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range auth {
		if a != "Bearer fake-token-for-tests" {
			t.Errorf("Authorization = %q, want the token from %s", a, testTokenEnv)
		}
	}
}

func TestTimelineGitHubFailureIsPartial(t *testing.T) {
	t.Setenv(testTokenEnv, "fake-token-for-tests")
	fakeGitHubAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	code, _, errOut := runCLI("timeline", "--config", githubConfig(t), "--from", "-1h")
	if code != exitPartial {
		t.Errorf("exit = %d, want %d", code, exitPartial)
	}
	want := "warning: github: GET /repos/alloteyphil/wasitus/pulls: repository not found or token lacks access"
	if !strings.Contains(errOut, want) {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
	if strings.Contains(errOut, "fake-token-for-tests") {
		t.Errorf("stderr leaks the token: %q", errOut)
	}
}

func TestTimelineGitHubTokenMissing(t *testing.T) {
	fakeGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s without a token", r.URL)
	})
	tests := []struct {
		name  string
		unset bool
	}{
		{"empty", false},
		{"not set", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(testTokenEnv, "")
			if tt.unset {
				if err := os.Unsetenv(testTokenEnv); err != nil {
					t.Fatal(err)
				}
			}
			code, _, errOut := runCLI("timeline", "--config", githubConfig(t), "--from", "-1h")
			want := "environment variable " + testTokenEnv + " is not set"
			if code != exitError || !strings.Contains(errOut, want) {
				t.Errorf("exit %d, stderr %q; want exit %d and %q", code, errOut, exitError, want)
			}
		})
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
