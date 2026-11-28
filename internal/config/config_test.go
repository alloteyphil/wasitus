package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fullConfig = `
timezone: Africa/Accra
lookback: 30m
sources:
  changes_file: notes/changes.yaml
  signoz:
    url: http://localhost:8080
    api_key_env: SIGNOZ_API_KEY
  github:
    token_env: GITHUB_TOKEN
    repos: [alloteyphil/wasitus]
services:
  alloteyphil/wasitus: orders-service
rules_file: rules/custom.yaml
internal_hosts: [billing-db, .corp.example]
`

func TestParseFullConfig(t *testing.T) {
	c, err := Parse([]byte(fullConfig), "/etc/wasitus")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got := c.Location().String(); got != "Africa/Accra" {
		t.Errorf("Location() = %q, want Africa/Accra", got)
	}
	if got := strings.Join(c.InternalHosts, ","); got != "billing-db,.corp.example" {
		t.Errorf("InternalHosts = %q", got)
	}
	if c.Lookback != 30*time.Minute {
		t.Errorf("Lookback = %s, want 30m", c.Lookback)
	}
	if got, want := c.ChangesFilePath(), filepath.Join("/etc/wasitus", "notes/changes.yaml"); got != want {
		t.Errorf("ChangesFilePath() = %q, want %q", got, want)
	}
	if got, want := c.RulesFilePath(), filepath.Join("/etc/wasitus", "rules/custom.yaml"); got != want {
		t.Errorf("RulesFilePath() = %q, want %q", got, want)
	}
	if c.Sources.SigNoz == nil || c.Sources.SigNoz.URL != "http://localhost:8080" {
		t.Errorf("Sources.SigNoz = %+v", c.Sources.SigNoz)
	}
	if c.Sources.GitHub == nil || c.Sources.GitHub.Repos[0] != "alloteyphil/wasitus" {
		t.Errorf("Sources.GitHub = %+v", c.Sources.GitHub)
	}
	if got := c.ServiceFor("alloteyphil/wasitus"); got != "orders-service" {
		t.Errorf("ServiceFor() = %q, want orders-service", got)
	}
	if got := c.ServiceFor("someone/else"); got != "" {
		t.Errorf("ServiceFor(unmapped) = %q, want empty", got)
	}
}

func TestParseDefaults(t *testing.T) {
	// "/abs/changes.yaml" is absolute on Linux and macOS but not on Windows,
	// which needs a drive letter. t.TempDir() is absolute on every OS.
	abs := filepath.Join(t.TempDir(), "changes.yaml")
	c, err := Parse([]byte("sources:\n  changes_file: '"+abs+"'\n"), "/ignored")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if c.Location() != time.UTC {
		t.Errorf("Location() = %v, want UTC", c.Location())
	}
	if c.Lookback != 60*time.Minute {
		t.Errorf("Lookback = %s, want 60m", c.Lookback)
	}
	if got := c.ChangesFilePath(); got != abs {
		t.Errorf("ChangesFilePath() = %q, absolute path should be kept", got)
	}
	if got := c.RulesFilePath(); got != "" {
		t.Errorf("RulesFilePath() = %q, want empty when unset", got)
	}
	absRules := filepath.Join(t.TempDir(), "rules.yaml")
	c2, err := Parse([]byte("sources:\n  changes_file: '"+abs+"'\nrules_file: '"+absRules+"'\n"), "/ignored")
	if err != nil {
		t.Fatalf("Parse(abs rules_file) error = %v", err)
	}
	if got := c2.RulesFilePath(); got != absRules {
		t.Errorf("RulesFilePath() = %q, absolute path should be kept", got)
	}
}

func TestParseErrors(t *testing.T) {
	const ok = "sources:\n  changes_file: c.yaml\n"
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{"empty", "", []string{"config is empty"}},
		{"typo in key", "sorces:\n  changes_file: c.yaml\n", []string{"field sorces not found"}},
		{"unknown timezone", "timezone: Mars/Base\n" + ok, []string{`timezone: unknown "Mars/Base"`}},
		{"negative lookback", "lookback: -5m\n" + ok, []string{"lookback must be between"}},
		{"lookback too long", "lookback: 48h\n" + ok, []string{"lookback must be between"}},
		{"no sources", "timezone: UTC\n", []string{"no sources configured"}},
		{"signoz without url", "sources:\n  signoz:\n    api_key_env: K\n", []string{"sources.signoz.url is required"}},
		{"signoz bad scheme", "sources:\n  signoz:\n    url: ftp://x\n", []string{"must be an http or https URL"}},
		{"signoz bad env name", "sources:\n  signoz:\n    url: http://x\n    api_key_env: my-key\n", []string{"not a valid environment variable name"}},
		{"github without token", "sources:\n  github:\n    repos: [a/b]\n", []string{"sources.github.token_env is required"}},
		{"github without repos", "sources:\n  github:\n    token_env: T\n", []string{"must list at least one repository"}},
		{"github bad repo", "sources:\n  github:\n    token_env: T\n    repos: [wasitus]\n", []string{`"wasitus" is not in owner/name form`}},
		{"bad service key", ok + "services:\n  orders: orders-service\n", []string{`services: "orders" is not in owner/name form`}},
		{"empty service name", ok + "services:\n  a/b: \"\"\n", []string{"maps to an empty service name"}},
		{"blank internal host", ok + "internal_hosts: [\" \"]\n", []string{"internal_hosts"}},
		{"internal host with a path", ok + "internal_hosts: [billing/api]\n", []string{`internal_hosts: "billing/api"`}},
		{"several problems at once", "timezone: Mars/Base\nlookback: 48h\n", []string{"timezone", "lookback", "no sources"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml), ".")
			if err == nil {
				t.Fatal("Parse() error = nil, want an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("Parse() error = %q, want it to contain %q", err, w)
				}
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wasitus.yaml")
	if err := os.WriteFile(path, []byte("sources:\n  changes_file: changes.yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := c.ChangesFilePath(), filepath.Join(dir, "changes.yaml"); got != want {
		t.Errorf("ChangesFilePath() = %q, want %q (relative to the config file)", got, want)
	}

	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Errorf("Load(missing) error = %v, want a read error", err)
	}
}

func TestLoadShippedConfigs(t *testing.T) {
	for _, path := range []string{
		"../../examples/wasitus.example.yaml",
		"../../demo/wasitus.lab.yaml",
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := Load(path); err != nil {
				t.Fatalf("Load(%s) error = %v", path, err)
			}
		})
	}
}

func TestSecret(t *testing.T) {
	env := map[string]string{"SET": "s3cret", "EMPTY": ""}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	if got, err := Secret("SET", lookup); err != nil || got != "s3cret" {
		t.Errorf("Secret(SET) = %q, %v", got, err)
	}
	for _, name := range []string{"EMPTY", "MISSING"} {
		_, err := Secret(name, lookup)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("Secret(%s) error = %v, want it to name the variable", name, err)
		}
	}
}
