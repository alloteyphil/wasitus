package config

import (
	"testing"
	"time"
)

// FuzzParse checks that Parse never panics on any input, and that every
// config it accepts satisfies its own validation rules.
func FuzzParse(f *testing.F) {
	f.Add([]byte(fullConfig))
	f.Add([]byte(""))
	f.Add([]byte("timezone: UTC\nsources:\n  changes_file: c.yaml\n"))
	f.Add([]byte("sources:\n  signoz:\n    url: https://admin:hunter2@signoz.example\n"))
	f.Add([]byte("sources:\n  changes_file: a.yaml\n---\nsources:\n  changes_file: b.yaml\n"))
	f.Add([]byte("lookback: 48h\nsources:\n  github:\n    token_env: T\n    repos: [a/b]\n"))
	f.Add([]byte("internal_hosts: [\" \", .corp.example]\nsources:\n  changes_file: c.yaml\n"))
	f.Add([]byte("sources: [not, a, map]"))
	f.Add([]byte("&a [*a]"))

	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := Parse(data, "/base")
		if err != nil {
			if c != nil {
				t.Fatalf("Parse returned a config with an error: %v", err)
			}
			return
		}
		if c.Location() == nil {
			t.Fatal("accepted config has no location")
		}
		if c.Lookback < 0 || c.Lookback > 24*time.Hour {
			t.Fatalf("accepted lookback %s out of range", c.Lookback)
		}
		if c.Sources.SigNoz == nil && c.Sources.GitHub == nil && c.Sources.ChangesFile == "" {
			t.Fatal("accepted a config with no sources")
		}
	})
}
