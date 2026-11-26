package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	githubOnly := filepath.Join(dir, "github-only.yaml")
	write(t, githubOnly, "sources:\n  github:\n    token_env: GITHUB_TOKEN\n    repos: [a/b]\n")

	tests := []struct {
		name, config, want string
	}{
		{"missing file", filepath.Join(dir, "nope.yaml"), "read config"},
		{"only unsupported sources", githubOnly, "none of the configured sources is supported yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errOut := runCLI("timeline", "--config", tt.config, "--from", "-1h")
			if code != exitError || !strings.Contains(errOut, tt.want) {
				t.Errorf("exit %d, stderr %q; want exit %d and %q", code, errOut, exitError, tt.want)
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
