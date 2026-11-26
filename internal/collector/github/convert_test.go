package github

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alloteyphil/wasitus/internal/event"
)

func TestChangeType(t *testing.T) {
	tests := []struct {
		title, want string
	}{
		{"docs: explain the lookback", "docs"},
		{"docs(readme): explain the lookback", "docs"},
		{"Docs: capitalised type", "docs"},
		{"docs!: breaking docs change", "docs"},
		{"test: cover the wallet-service retry path", "test"},
		{"test(orders): table tests", "test"},
		{"ci: add a coverage floor", "infra"},
		{"ci(release): sign artefacts", "infra"},
		{"build: bump the Go toolchain", "infra"},
		{"build(deps)!: drop an old module", "infra"},
		{"feat: add the github collector", "merge"},
		{"fix(demo): stop using a deprecated call", "merge"},
		{"chore: tidy", "merge"},
		{"refactor(api)!: rename fields", "merge"},
		{"M1: lab services", "merge"},
		{"Update README", "merge"},
		{"", "merge"},
		{"docs", "merge"},                        // no colon: not a conventional prefix
		{"tests: plural is not a type", "merge"}, // only the conventional spelling counts
		{"docs (readme): space before scope", "merge"},
		{"Merge docs: from a branch", "merge"},
		{"  docs: leading space", "docs"},
	}
	for _, tt := range tests {
		if got := changeType(tt.title); got != tt.want {
			t.Errorf("changeType(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestClean(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain title", "plain title"},
		{"  padded  ", "padded"},
		{"two\nlines", "two lines"},
		{"tab\tand\r\nnewline", "tab and newline"},
		{"escape \x1b[31mred\x1b[0m", "escape [31mred [0m"},
		{"many    spaces", "many spaces"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := clean(tt.in); got != tt.want {
			t.Errorf("clean(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWebLink(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/alloteyphil/wasitus/pull/6", "https://github.com/alloteyphil/wasitus/pull/6"},
		{"http://localhost:3000/deploys/1", "http://localhost:3000/deploys/1"},
		{"javascript:alert(1)", ""},
		{"/relative/path", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := webLink(tt.in); got != tt.want {
			t.Errorf("webLink(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNextLink(t *testing.T) {
	tests := []struct{ header, want string }{
		{"", ""},
		{`<https://api.github.com/repositories/1/pulls?page=2>; rel="next", <https://api.github.com/repositories/1/pulls?page=9>; rel="last"`,
			"https://api.github.com/repositories/1/pulls?page=2"},
		{`<https://x/a?page=9>; rel="last", <https://x/a?page=2>; rel="next"`, "https://x/a?page=2"},
		{`<https://x/a?page=1>; rel="prev"`, ""},
		{`<https://x/a?page=2>; rel=next`, "https://x/a?page=2"},
		{`<https://x/a?page=2>; REL="Next"`, "https://x/a?page=2"},
		{`<https://x/a?page=2>; rel="next last"`, "https://x/a?page=2"},
		{`<https://x/a?cursor=a,b>; rel="next"`, "https://x/a?cursor=a,b"}, // comma inside the URL
		{`<https://x/a?page=2>; title="next"; rel="prev"`, ""},
		{`<https://x/a?page=2`, ""},
		{`rel="next"`, ""},
		{`<>; rel="next"`, ""},
	}
	for _, tt := range tests {
		if got := nextLink(tt.header); got != tt.want {
			t.Errorf("nextLink(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestDeploymentTitleFallbacks(t *testing.T) {
	created := time.Date(2026, 11, 26, 17, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		d    deployment
		s    deploymentStatus
		want string
		link string
	}{
		{"ref, environment and state", deployment{ID: 1, Ref: "main", Environment: "lab", CreatedAt: created},
			deploymentStatus{State: "failure", TargetURL: "https://example.com/t"}, "Deployed main to lab (failure)", "https://example.com/t"},
		{"no ref falls back to the short sha", deployment{ID: 2, SHA: "ab8b52697a30e639", Environment: "lab", CreatedAt: created},
			deploymentStatus{State: "success"}, "Deployed ab8b526 to lab (success)", ""},
		{"nothing known", deployment{ID: 3, CreatedAt: created},
			deploymentStatus{LogURL: "ftp://example.com/log"}, "Deployed unknown to unknown (unknown)", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.d.event(repo, "wallet-service", tt.s)
			if e.Title != tt.want || e.Link != tt.link {
				t.Errorf("title %q link %q, want %q and %q", e.Title, e.Link, tt.want, tt.link)
			}
			if err := e.Validate(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestPullTitleFallback(t *testing.T) {
	merged := time.Date(2026, 11, 26, 17, 0, 0, 0, time.UTC)
	e := pullRequest{Number: 3, Title: " \n ", MergedAt: &merged}.event(repo, "")
	if e.Title != "Merged PR #3" {
		t.Errorf("title = %q", e.Title)
	}
	r := release{ID: 4, PublishedAt: &merged}.event(repo, "")
	if r.Title != "Released unknown" {
		t.Errorf("release title = %q", r.Title)
	}
}

func TestShortSHA(t *testing.T) {
	for in, want := range map[string]string{
		"ab8b52697a30e639": "ab8b526",
		"abc":              "abc",
		"ééééééééé":        "ééééééé",
	} {
		if got := shortSHA(in); got != want {
			t.Errorf("shortSHA(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzConvert feeds arbitrary bytes to every JSON-to-event conversion. None may
// panic, and every event they produce must be valid and inside the window.
func FuzzConvert(f *testing.F) {
	for _, name := range []string{
		"pulls_page1.json", "pulls_page2.json", "pulls_mixed.json",
		"deployments.json", "deployment_9002_statuses.json", "releases.json",
	} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, s := range []string{
		"", "null", "[]", "{}", "[null]", `[{}]`, `"text"`,
		`[{"number":1,"title":"docs: x","merged_at":"2026-11-26T17:00:00Z","updated_at":"2026-11-26T17:00:00Z"}]`,
		`[{"number":-1,"title":"\u001b[2J","merged_at":"2026-11-26T17:00:00+05:00","user":{"login":"\n"}}]`,
		`[{"merged_at":"0001-01-01T00:00:00Z"}]`,
		`[{"id":1,"created_at":"2026-11-26T17:00:00Z","ref":"","environment":"","sha":"éééééééé"}]`,
		`[{"id":1,"draft":false,"published_at":"2026-11-26T17:00:00Z","tag_name":""}]`,
		`[{"state":"success","log_url":"javascript:alert(1)"}]`,
		`[{"merged_at":"yesterday"}]`,
		`[{"number":1e400}]`,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		check := func(kind string, events []event.Event, err error) {
			if err != nil && len(events) > 0 {
				t.Errorf("%s: events returned with error %v", kind, err)
			}
			for _, e := range events {
				if err := e.Normalize().Validate(); err != nil {
					t.Errorf("%s: invalid event: %v", kind, err)
				}
				if !window.Contains(e.Time) || e.Time.Location() != time.UTC {
					t.Errorf("%s: event time %v outside window or not UTC", kind, e.Time)
				}
				if !utf8.ValidString(e.Title) {
					t.Errorf("%s: title %q is not valid UTF-8", kind, e.Title)
				}
			}
		}

		events, _, err := pullEvents(repo, "orders-service", data, window)
		check("pulls", events, err)

		deps, _, err := deploymentsIn(data, window)
		status, statusErr := latestStatus(data)
		if statusErr != nil && status != (deploymentStatus{}) {
			t.Errorf("status %+v returned with error %v", status, statusErr)
		}
		var depEvents []event.Event
		for _, d := range deps {
			depEvents = append(depEvents, d.event(repo, "orders-service", status))
		}
		check("deployments", depEvents, err)

		events, _, err = releaseEvents(repo, "orders-service", data, window)
		check("releases", events, err)

		if link := nextLink(string(data)); !strings.Contains(string(data), link) {
			t.Errorf("nextLink returned %q, which is not in the header", link)
		}
		if msg := apiMessage(http.StatusInternalServerError, data); msg == "" || !utf8.ValidString(msg) {
			t.Errorf("apiMessage = %q", msg)
		}
	})
}
