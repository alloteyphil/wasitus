package changesfile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

var plus1 = time.FixedZone("UTC+1", 60*60)

const sample = `
- time: "2026-11-14 03:31"
  service: orders-service
  title: Deployed orders-service v1.4.0
- time: 2026-11-14T03:05:00Z
  kind: external
  title: "mockpay status page: degraded payments"
  severity: warning
  link: https://status.mockpay.test/incidents/42
`

func TestParse(t *testing.T) {
	events, err := parse([]byte(sample), plus1)
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	deploy := events[0]
	if want := time.Date(2026, 11, 14, 2, 31, 0, 0, time.UTC); !deploy.Time.Equal(want) {
		t.Errorf("local time not read in config zone: got %v, want %v", deploy.Time, want)
	}
	if deploy.Kind != event.KindChange || deploy.Source != Source || deploy.Service != "orders-service" || deploy.ID == "" {
		t.Errorf("deploy event = %+v", deploy)
	}

	status := events[1]
	if want := time.Date(2026, 11, 14, 3, 5, 0, 0, time.UTC); !status.Time.Equal(want) {
		t.Errorf("unquoted RFC 3339 time: got %v, want %v", status.Time, want)
	}
	if status.Kind != event.KindExternal || status.Severity != event.SeverityWarning || status.Link == "" {
		t.Errorf("status event = %+v", status)
	}
}

func TestParseGivesStableIDs(t *testing.T) {
	a, _ := parse([]byte(sample), plus1)
	b, _ := parse([]byte(sample), plus1)
	if a[0].ID != b[0].ID || a[1].ID != b[1].ID {
		t.Error("IDs change between runs")
	}
	if a[0].ID == a[1].ID {
		t.Error("different entries share an ID")
	}
}

func TestParseReportsBadEntriesButKeepsGoodOnes(t *testing.T) {
	const doc = `
- time: "2026-11-14 03:00"
  title: Good entry
- time: "yesterday"
  title: Bad time
- time: "2026-11-14 03:10"
  title: "   "
- time: "2026-11-14 03:20"
  kind: symptom
  title: People should not write symptoms here
`
	events, err := parse([]byte(doc), time.UTC)
	if len(events) != 1 || events[0].Title != "Good entry" {
		t.Errorf("events = %+v, want only the good entry", events)
	}
	if err == nil {
		t.Fatal("parse() error = nil, want errors for entries 2-4")
	}
	for _, want := range []string{"entry 2", "unrecognised time", "entry 3", "missing title", "entry 4", "kind must be change or external"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestParseEmptyAndUnknownFields(t *testing.T) {
	for _, doc := range []string{"", "# just a comment\n"} {
		events, err := parse([]byte(doc), time.UTC)
		if err != nil || len(events) != 0 {
			t.Errorf("parse(%q) = %v, %v; want no events and no error", doc, events, err)
		}
	}
	if _, err := parse([]byte("- titel: typo\n"), time.UTC); err == nil || !strings.Contains(err.Error(), "field titel not found") {
		t.Errorf("unknown field error = %v", err)
	}
}

func TestCollect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changes.yaml")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New(path, plus1)
	if c.Name() != Source {
		t.Errorf("Name() = %q", c.Name())
	}
	events, err := c.Collect(context.Background(), event.Window{})
	if err != nil || len(events) != 2 {
		t.Errorf("Collect() = %d events, %v; want 2, nil", len(events), err)
	}

	_, err = New(filepath.Join(t.TempDir(), "missing.yaml"), time.UTC).Collect(context.Background(), event.Window{})
	if err == nil || !strings.Contains(err.Error(), "read changes file") {
		t.Errorf("Collect(missing file) error = %v", err)
	}
}

func TestParseChangeType(t *testing.T) {
	tests := []struct {
		name      string
		doc       string
		wantAttr  string
		wantNil   bool
		wantError string
	}{
		{
			name:     "explicit deploy",
			doc:      "- time: \"2026-11-14 03:00\"\n  change_type: deploy\n  title: Deployed v1.4.0\n",
			wantAttr: "deploy",
		},
		{
			name:     "trimmed and lowercased",
			doc:      "- time: \"2026-11-14 03:00\"\n  change_type: \"  Config \"\n  title: Raised timeout\n",
			wantAttr: "config",
		},
		{
			name:    "omitted leaves attrs nil",
			doc:     "- time: \"2026-11-14 03:00\"\n  title: Manual note\n",
			wantNil: true,
		},
		{
			name:    "whitespace only leaves attrs nil",
			doc:     "- time: \"2026-11-14 03:00\"\n  change_type: \"   \"\n  title: Manual note\n",
			wantNil: true,
		},
		{
			name:      "rejected on external kind",
			doc:       "- time: \"2026-11-14 03:00\"\n  kind: external\n  change_type: deploy\n  title: Provider outage\n",
			wantError: "change_type is only valid when kind is change",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events, err := parse([]byte(tc.doc), time.UTC)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("parse() error = %v, want substring %q", err, tc.wantError)
				}
				if len(events) != 0 {
					t.Fatalf("got %d events, want 0", len(events))
				}
				return
			}
			if err != nil {
				t.Fatalf("parse() unexpected error: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("got %d events, want 1", len(events))
			}
			if tc.wantNil {
				if events[0].Attrs != nil {
					t.Errorf("Attrs = %v, want nil", events[0].Attrs)
				}
				return
			}
			if got := events[0].Attrs[attrChangeType]; got != tc.wantAttr {
				t.Errorf("Attrs[%q] = %q, want %q", attrChangeType, got, tc.wantAttr)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(sample))
	f.Add([]byte(""))
	f.Add([]byte("# comment only\n"))
	f.Add([]byte("- time: \"2026-11-14 03:00\"\n  change_type: deploy\n  title: Deploy\n"))
	f.Add([]byte("- time: \"bad\"\n  kind: external\n  change_type: deploy\n  title: \"\"\n"))
	f.Add([]byte("not: [valid: yaml"))

	f.Fuzz(func(t *testing.T, data []byte) {
		events, _ := parse(data, time.UTC)
		for _, ev := range events {
			if ev.ID == "" {
				t.Fatalf("parsed event has empty ID: %+v", ev)
			}
			if ev.Time.Location() != time.UTC {
				t.Fatalf("parsed event time not UTC: %v", ev.Time)
			}
		}
	})
}

func TestParseRejectsUnsafeInput(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantError string
	}{
		{"second document", "- time: \"2026-11-14 03:00\"\n  title: a\n---\n- time: \"2026-11-14 04:00\"\n  title: b\n", "only one YAML document"},
		{"javascript link", "- time: \"2026-11-14 03:00\"\n  title: a\n  link: \"javascript:alert(1)\"\n", "link must be an http or https URL"},
		{"relative link", "- time: \"2026-11-14 03:00\"\n  title: a\n  link: /x\n", "link must be an http or https URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parse([]byte(tc.yaml), time.UTC); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("parse() error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestParseStripsControlCharacters(t *testing.T) {
	data := "- time: \"2026-11-14 03:00\"\n  title: \"Deploy\\e]0;pwned\\a done\\e[2J\"\n  service: \"orders\\nservice\"\n  link: https://example.test/x\n"
	events, err := parse([]byte(data), time.UTC)
	if err != nil || len(events) != 1 {
		t.Fatalf("parse() = %v, %v", events, err)
	}
	e := events[0]
	for _, s := range []string{e.Title, e.Service} {
		if strings.ContainsAny(s, "\x1b\a\n") {
			t.Errorf("control characters kept: %q", s)
		}
	}
	if e.Title != "Deploy ]0;pwned  done [2J" || e.Service != "orders service" {
		t.Errorf("title %q, service %q", e.Title, e.Service)
	}
	if e.Link != "https://example.test/x" {
		t.Errorf("link = %q", e.Link)
	}
}
