package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

const (
	repo      = "alloteyphil/wasitus"
	repoPath  = "/repos/alloteyphil/wasitus"
	testToken = "fake-test-token-not-real"
)

// window covers the recorded merges of PRs #1 to #6 (16:01 to 17:04 UTC) and
// the hand-written deployments and releases.
var window = event.Window{
	From: time.Date(2026, 11, 26, 16, 0, 0, 0, time.UTC),
	To:   time.Date(2026, 11, 26, 17, 30, 0, 0, time.UTC),
}

// reply is one canned response. {server} in a header value is replaced with
// the test server's URL.
type reply struct {
	status int
	header map[string]string
	file   string // testdata file to serve as the body
	body   string // literal body, used when file is empty
}

// fakeGitHub serves canned replies keyed by path, or by path plus "?page=N"
// when the request has a page parameter. It records every request.
type fakeGitHub struct {
	t      *testing.T
	routes map[string]reply

	mu   sync.Mutex
	reqs []*http.Request
}

// emptyRepo answers every list of the test repository with no items.
func emptyRepo() map[string]reply {
	return map[string]reply{
		repoPath + "/pulls":       {body: "[]"},
		repoPath + "/deployments": {body: "[]"},
		repoPath + "/releases":    {body: "[]"},
	}
}

// with returns emptyRepo plus the given routes.
func with(routes map[string]reply) map[string]reply {
	all := emptyRepo()
	for k, v := range routes {
		all[k] = v
	}
	return all
}

func serve(t *testing.T, routes map[string]reply) (*fakeGitHub, *httptest.Server) {
	t.Helper()
	f := &fakeGitHub{t: t, routes: routes}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Clone(context.Background()))
	f.mu.Unlock()

	key := r.URL.Path
	if p := r.URL.Query().Get("page"); p != "" {
		key += "?page=" + p
	}
	rep, ok := f.routes[key]
	if !ok {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.Error(w, `{"message":"no fixture"}`, http.StatusTeapot)
		return
	}
	for k, v := range rep.header {
		w.Header().Set(k, strings.ReplaceAll(v, "{server}", "http://"+r.Host))
	}
	body := []byte(rep.body)
	if rep.file != "" {
		var err error
		if body, err = os.ReadFile(filepath.Join("testdata", rep.file)); err != nil {
			f.t.Errorf("read fixture: %v", err)
		}
	}
	if rep.status != 0 {
		w.WriteHeader(rep.status)
	}
	_, _ = w.Write(body)
}

func (f *fakeGitHub) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *fakeGitHub) paths() []string {
	var out []string
	for _, r := range f.requests() {
		out = append(out, r.URL.Path+"?"+r.URL.RawQuery)
	}
	return out
}

func newCollector(t *testing.T, srv *httptest.Server, token string, repos ...string) *Collector {
	t.Helper()
	if len(repos) == 0 {
		repos = []string{repo}
	}
	services := map[string]string{repo: "orders-service"}
	c, err := New(repos, token, srv.URL, srv.Client(), func(r string) string { return services[r] })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ids(events []event.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

func find(t *testing.T, events []event.Event, id string) event.Event {
	t.Helper()
	for _, e := range events {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no event %q in %v", id, ids(events))
	return event.Event{}
}

// TestCollectRecordedPulls replays the two recorded pages: page 1 links to
// page 2 the way GitHub does, through /repositories/{id}/pulls.
func TestCollectRecordedPulls(t *testing.T) {
	f, srv := serve(t, with(map[string]reply{
		repoPath + "/pulls": {
			file: "pulls_page1.json",
			header: map[string]string{"Link": `<{server}/repositories/1472921483/pulls?state=closed&sort=updated&direction=desc&per_page=3&page=2>; rel="next", ` +
				`<{server}/repositories/1472921483/pulls?state=closed&sort=updated&direction=desc&per_page=3&page=2>; rel="last"`},
		},
		"/repositories/1472921483/pulls?page=2": {file: "pulls_page2.json"},
	}))

	events, err := newCollector(t, srv, testToken).Collect(context.Background(), window)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"github:alloteyphil/wasitus:pr:6", "github:alloteyphil/wasitus:pr:5", "github:alloteyphil/wasitus:pr:4",
		"github:alloteyphil/wasitus:pr:3", "github:alloteyphil/wasitus:pr:2", "github:alloteyphil/wasitus:pr:1",
	}
	if got := ids(events); !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}

	pr6 := find(t, events, "github:alloteyphil/wasitus:pr:6")
	wantPR6 := event.Event{
		ID:       "github:alloteyphil/wasitus:pr:6",
		Time:     time.Date(2026, 11, 26, 17, 4, 45, 0, time.UTC),
		Kind:     event.KindChange,
		Source:   "github",
		Service:  "orders-service",
		Title:    "Merged PR #6: ci: solid rules — ci-ok gate, coverage floor, vuln scan, make check, agent rules",
		Severity: event.SeverityInfo,
		Link:     "https://github.com/alloteyphil/wasitus/pull/6",
		Attrs: map[string]string{
			"repo":        repo,
			"number":      "6",
			"author":      "alloteyphil",
			"sha":         "ab8b52697a30e639cb0f0a859541015f38bb9d78",
			"change_type": "infra",
		},
	}
	assertEvent(t, pr6, wantPR6)
	if got := find(t, events, "github:alloteyphil/wasitus:pr:5").Attrs["change_type"]; got != "merge" {
		t.Errorf("fix(demo) PR change_type = %q, want merge", got)
	}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Error(err)
		}
	}

	wantPaths := []string{
		repoPath + "/pulls?direction=desc&per_page=100&sort=updated&state=closed",
		"/repositories/1472921483/pulls?state=closed&sort=updated&direction=desc&per_page=3&page=2",
		repoPath + "/deployments?per_page=100",
		repoPath + "/releases?per_page=100",
	}
	if got := f.paths(); !slices.Equal(got, wantPaths) {
		t.Errorf("requests =\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(wantPaths, "\n"))
	}
}

func assertEvent(t *testing.T, got, want event.Event) {
	t.Helper()
	if got.ID != want.ID || !got.Time.Equal(want.Time) || got.Time.Location() != time.UTC ||
		got.Kind != want.Kind || got.Source != want.Source || got.Service != want.Service ||
		got.Title != want.Title || got.Severity != want.Severity || got.Link != want.Link || got.End != nil {
		t.Errorf("event =\n%+v\nwant\n%+v", got, want)
	}
	if len(got.Attrs) != len(want.Attrs) {
		t.Errorf("attrs = %v, want %v", got.Attrs, want.Attrs)
	}
	for k, v := range want.Attrs {
		if got.Attrs[k] != v {
			t.Errorf("attrs[%q] = %q, want %q", k, got.Attrs[k], v)
		}
	}
}

func TestCollectWindowFilteringAndEarlyStop(t *testing.T) {
	// The recorded PRs were merged between 16:01 and 17:04. A window from 16:45
	// keeps #4 to #6. Every PR on page 1 was updated after 16:45, so page 2 is
	// read; it holds PRs updated before 16:45, which are dropped and end the
	// pagination even though page 2 links to a page 3.
	late := event.Window{From: time.Date(2026, 11, 26, 16, 45, 0, 0, time.UTC), To: window.To}
	f, srv := serve(t, with(map[string]reply{
		repoPath + "/pulls": {
			file:   "pulls_page1.json",
			header: map[string]string{"Link": `<{server}/repositories/1/pulls?page=2>; rel="next"`},
		},
		"/repositories/1/pulls?page=2": {
			file:   "pulls_page2.json",
			header: map[string]string{"Link": `<{server}/repositories/1/pulls?page=3>; rel="next"`},
		},
	}))

	events, err := newCollector(t, srv, "").Collect(context.Background(), late)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"github:alloteyphil/wasitus:pr:6", "github:alloteyphil/wasitus:pr:5", "github:alloteyphil/wasitus:pr:4"}
	if got := ids(events); !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	for _, p := range f.paths() {
		if strings.Contains(p, "page=3") {
			t.Errorf("requested %s after reaching PRs older than the window", p)
		}
	}
}

func TestCollectStopsAtFirstPageOlderThanWindow(t *testing.T) {
	// Window after every recorded merge: page 1 already holds PRs updated
	// before it starts, so page 2 is never requested.
	after := event.Window{From: time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 26, 19, 0, 0, 0, time.UTC)}
	f, srv := serve(t, with(map[string]reply{
		repoPath + "/pulls": {
			file:   "pulls_page1.json",
			header: map[string]string{"Link": `<{server}/repositories/1/pulls?page=2>; rel="next"`},
		},
	}))
	events, err := newCollector(t, srv, "").Collect(context.Background(), after)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("events = %v, want none", ids(events))
	}
	if n := len(f.requests()); n != 3 {
		t.Errorf("made %d requests, want 3 (one per list): %v", n, f.paths())
	}
}

func TestCollectMixedPulls(t *testing.T) {
	_, srv := serve(t, with(map[string]reply{repoPath + "/pulls": {file: "pulls_mixed.json"}}))
	events, err := newCollector(t, srv, "").Collect(context.Background(), window)
	if err != nil {
		t.Fatal(err)
	}
	// #9 was closed without merging; #7 was merged before the window.
	if got, want := ids(events), []string{"github:alloteyphil/wasitus:pr:8"}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	assertEvent(t, events[0], event.Event{
		ID:       "github:alloteyphil/wasitus:pr:8",
		Time:     time.Date(2026, 11, 26, 17, 20, 0, 0, time.UTC),
		Kind:     event.KindChange,
		Source:   "github",
		Service:  "orders-service",
		Title:    "Merged PR #8: docs(readme): explain the lookback",
		Severity: event.SeverityInfo,
		Link:     "https://github.com/alloteyphil/wasitus/pull/8",
		Attrs:    map[string]string{"repo": repo, "number": "8", "change_type": "docs"}, // null user and sha are left out
	})
}

func TestCollectDeployments(t *testing.T) {
	f, srv := serve(t, with(map[string]reply{
		repoPath + "/deployments":               {file: "deployments.json"},
		repoPath + "/deployments/9002/statuses": {file: "deployment_9002_statuses.json"},
		repoPath + "/deployments/9001/statuses": {body: "[]"},
	}))
	events, err := newCollector(t, srv, "").Collect(context.Background(), window)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(events), []string{
		"github:alloteyphil/wasitus:deployment:9002", "github:alloteyphil/wasitus:deployment:9001",
	}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v (8001 is older than the window)", got, want)
	}
	assertEvent(t, events[0], event.Event{
		ID:       "github:alloteyphil/wasitus:deployment:9002",
		Time:     time.Date(2026, 11, 26, 17, 10, 0, 0, time.UTC),
		Kind:     event.KindChange,
		Source:   "github",
		Service:  "orders-service",
		Title:    "Deployed main to lab (success)",
		Severity: event.SeverityInfo,
		Link:     "https://example.com/lab/deploys/9002",
		Attrs: map[string]string{
			"repo": repo, "change_type": "deploy", "environment": "lab", "ref": "main",
			"sha": "ab8b52697a30e639cb0f0a859541015f38bb9d78", "state": "success",
		},
	})
	assertEvent(t, events[1], event.Event{
		ID:       "github:alloteyphil/wasitus:deployment:9001",
		Time:     time.Date(2026, 11, 26, 16, 55, 0, 0, time.UTC),
		Kind:     event.KindChange,
		Source:   "github",
		Service:  "orders-service",
		Title:    "Deployed v0.1.0 to staging (unknown)",
		Severity: event.SeverityInfo,
		Attrs: map[string]string{
			"repo": repo, "change_type": "deploy", "environment": "staging", "ref": "v0.1.0",
			"sha": "7569254a41867197a7f82037f652682dbebc1fdc", "state": "unknown",
		},
	})
	if !slices.Contains(f.paths(), repoPath+"/deployments/9002/statuses?per_page=1") {
		t.Errorf("statuses not requested with per_page=1: %v", f.paths())
	}
}

func TestCollectReleases(t *testing.T) {
	_, srv := serve(t, with(map[string]reply{repoPath + "/releases": {file: "releases.json"}}))
	events, err := newCollector(t, srv, "").Collect(context.Background(), window)
	if err != nil {
		t.Fatal(err)
	}
	// 7003 is a draft; 7001 was published before the window.
	if got, want := ids(events), []string{"github:alloteyphil/wasitus:release:7002"}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	assertEvent(t, events[0], event.Event{
		ID:       "github:alloteyphil/wasitus:release:7002",
		Time:     time.Date(2026, 11, 26, 17, 15, 0, 0, time.UTC),
		Kind:     event.KindChange,
		Source:   "github",
		Service:  "orders-service",
		Title:    "Released v0.1.0",
		Severity: event.SeverityInfo,
		Link:     "https://github.com/alloteyphil/wasitus/releases/tag/v0.1.0",
		Attrs:    map[string]string{"repo": repo, "change_type": "release", "tag": "v0.1.0", "author": "lab-bot"},
	})
}

func TestServiceMapping(t *testing.T) {
	const other = "alloteyphil/lab-wallet"
	routes := with(map[string]reply{repoPath + "/releases": {file: "releases.json"}})
	routes["/repos/"+other+"/pulls"] = reply{body: "[]"}
	routes["/repos/"+other+"/deployments"] = reply{body: "[]"}
	routes["/repos/"+other+"/releases"] = reply{file: "releases.json"}
	_, srv := serve(t, routes)

	events, err := newCollector(t, srv, "", repo, other).Collect(context.Background(), window)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, events, "github:alloteyphil/wasitus:release:7002").Service; got != "orders-service" {
		t.Errorf("mapped repo service = %q, want orders-service", got)
	}
	if got := find(t, events, "github:alloteyphil/lab-wallet:release:7002").Service; got != "" {
		t.Errorf("unmapped repo service = %q, want empty", got)
	}

	// A nil mapping is allowed and leaves every service empty.
	c, err := New([]string{repo}, "", srv.URL, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	events, err = c.Collect(context.Background(), window)
	if err != nil || len(events) != 1 || events[0].Service != "" {
		t.Errorf("nil mapping: events %+v, err %v", events, err)
	}
}

func TestRequestHeaders(t *testing.T) {
	tests := []struct {
		name, token, wantAuth string
	}{
		{"with token", testToken, "Bearer " + testToken},
		{"without token", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, srv := serve(t, emptyRepo())
			if _, err := newCollector(t, srv, tt.token).Collect(context.Background(), window); err != nil {
				t.Fatal(err)
			}
			reqs := f.requests()
			if len(reqs) != 3 {
				t.Fatalf("made %d requests, want 3", len(reqs))
			}
			for _, r := range reqs {
				h := r.Header
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET (read-only)", r.Method)
				}
				if got := h.Get("Accept"); got != "application/vnd.github+json" {
					t.Errorf("Accept = %q", got)
				}
				if got := h.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
					t.Errorf("X-GitHub-Api-Version = %q", got)
				}
				if got := h.Get("User-Agent"); got != "wasitus" {
					t.Errorf("User-Agent = %q", got)
				}
				if got := h.Get("Authorization"); got != tt.wantAuth {
					t.Errorf("Authorization = %q, want %q", got, tt.wantAuth)
				}
				if _, set := h["Authorization"]; tt.token == "" && set {
					t.Error("Authorization header sent without a token")
				}
			}
		})
	}
}

func TestCollectErrors(t *testing.T) {
	reset := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		routes   map[string]reply
		want     []string // substrings of the error
		wantErr  error    // errors.Is target, if any
		wantRate bool     // errors.As *RateLimitError
		requests int      // requests made before giving up
	}{
		{
			name: "primary rate limit on 403",
			routes: with(map[string]reply{repoPath + "/pulls": {status: http.StatusForbidden, body: `{"message":"API rate limit exceeded"}`,
				header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1795716000"}}}),
			want:     []string{"GET " + repoPath + "/pulls", "rate limit exceeded", "resets at " + reset.Format(time.RFC3339)},
			wantRate: true,
			requests: 1,
		},
		{
			name: "primary rate limit on 429",
			routes: with(map[string]reply{repoPath + "/releases": {status: http.StatusTooManyRequests,
				header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1795716000"}}}),
			want:     []string{"GET " + repoPath + "/releases", "resets at 2026-11-26T18:00:00Z"},
			wantRate: true,
			requests: 3,
		},
		{
			name: "rate limit without a reset time",
			routes: with(map[string]reply{repoPath + "/pulls": {status: http.StatusForbidden,
				header: map[string]string{"X-RateLimit-Remaining": "0"}}}),
			want:     []string{"GitHub API rate limit exceeded"},
			wantRate: true,
			requests: 1,
		},
		{
			name: "secondary rate limit",
			routes: with(map[string]reply{repoPath + "/pulls": {status: http.StatusForbidden, body: `{"message":"secondary rate limit"}`,
				header: map[string]string{"Retry-After": "60"}}}),
			want:     []string{"retry after 1m0s"},
			wantRate: true,
			requests: 1,
		},
		{
			name: "403 without rate-limit headers is a permission problem",
			routes: with(map[string]reply{repoPath + "/deployments": {status: http.StatusForbidden,
				body: `{"message":"Resource not accessible by personal access token"}`}}),
			want:     []string{"GET " + repoPath + "/deployments", "HTTP 403: Resource not accessible by personal access token"},
			requests: 3, // pulls and releases still read
		},
		{
			name:     "404",
			routes:   map[string]reply{repoPath + "/pulls": {status: http.StatusNotFound, body: `{"message":"Not Found"}`}},
			want:     []string{"GET " + repoPath + "/pulls", "repository not found or token lacks access"},
			wantErr:  ErrNotFound,
			requests: 1, // deployments and releases are skipped
		},
		{
			name:     "server error without a message",
			routes:   with(map[string]reply{repoPath + "/pulls": {status: http.StatusBadGateway, body: "<html>bad gateway</html>"}}),
			want:     []string{"HTTP 502: Bad Gateway"},
			requests: 3,
		},
		{
			name:     "long error message is shortened",
			routes:   with(map[string]reply{repoPath + "/pulls": {status: http.StatusUnprocessableEntity, body: `{"message":"` + strings.Repeat("x", 300) + `"}`}}),
			want:     []string{"HTTP 422: " + strings.Repeat("x", 200) + "..."},
			requests: 3,
		},
		{
			name:     "malformed pull requests",
			routes:   with(map[string]reply{repoPath + "/pulls": {body: `[{"number": 1, "merged_at": "yesterday"}]`}}),
			want:     []string{"GET " + repoPath + "/pulls", "decode pull requests"},
			requests: 3,
		},
		{
			name:     "malformed deployments",
			routes:   with(map[string]reply{repoPath + "/deployments": {body: `{"not": "a list"}`}}),
			want:     []string{"GET " + repoPath + "/deployments", "decode deployments"},
			requests: 3,
		},
		{
			name: "malformed deployment statuses",
			routes: with(map[string]reply{
				repoPath + "/deployments":               {file: "deployments.json"},
				repoPath + "/deployments/9002/statuses": {body: `[{"state": 7}]`},
			}),
			want:     []string{"GET " + repoPath + "/deployments/9002/statuses", "decode deployment statuses"},
			requests: 4,
		},
		{
			name: "deployment statuses not readable",
			routes: with(map[string]reply{
				repoPath + "/deployments":               {file: "deployments.json"},
				repoPath + "/deployments/9002/statuses": {status: http.StatusInternalServerError, body: `{"message":"boom"}`},
			}),
			want:     []string{"GET " + repoPath + "/deployments/9002/statuses", "HTTP 500: boom"},
			requests: 4,
		},
		{
			name:     "malformed releases",
			routes:   with(map[string]reply{repoPath + "/releases": {body: `[`}}),
			want:     []string{"GET " + repoPath + "/releases", "decode releases"},
			requests: 3,
		},
		{
			name: "next link to another host",
			routes: with(map[string]reply{repoPath + "/pulls": {body: "[]",
				header: map[string]string{"Link": `<https://attacker.example/steal?page=2>; rel="next"`}}}),
			want:     []string{"next page link leaves", "not following it"},
			requests: 3,
		},
		{
			name: "unparseable next link",
			routes: with(map[string]reply{repoPath + "/pulls": {body: "[]",
				header: map[string]string{"Link": `<http://%zz>; rel="next"`}}}),
			want:     []string{"parse next page link"},
			requests: 3,
		},
		{
			name: "endless pagination",
			routes: with(map[string]reply{repoPath + "/pulls": {body: "[]",
				header: map[string]string{"Link": `<{server}` + repoPath + `/pulls>; rel="next"`}}}),
			want:     []string{"stopped after 30 pages"},
			requests: 30 + 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, srv := serve(t, tt.routes)
			_, err := newCollector(t, srv, testToken).Collect(context.Background(), window)
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error leaks the token: %q", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error %v is not %v", err, tt.wantErr)
			}
			var rl *RateLimitError
			if got := errors.As(err, &rl); got != tt.wantRate {
				t.Errorf("rate-limit error = %v, want %v (%v)", got, tt.wantRate, err)
			}
			if got := len(f.requests()); got != tt.requests {
				t.Errorf("made %d requests, want %d: %v", got, tt.requests, f.paths())
			}
		})
	}
}

func TestRateLimitStopsOtherRepos(t *testing.T) {
	const other = "alloteyphil/lab-wallet"
	routes := with(map[string]reply{repoPath + "/pulls": {status: http.StatusTooManyRequests,
		header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1795716000"}}})
	f, srv := serve(t, routes)
	_, err := newCollector(t, srv, "", repo, other).Collect(context.Background(), window)
	var rl *RateLimitError
	if !errors.As(err, &rl) || !rl.Reset.Equal(time.Unix(1795716000, 0)) {
		t.Fatalf("err = %v, want a rate-limit error with the reset time", err)
	}
	if got := len(f.requests()); got != 1 {
		t.Errorf("made %d requests after the rate limit, want 1: %v", got, f.paths())
	}
}

func TestNotFoundRepoDoesNotStopOthers(t *testing.T) {
	const missing = "alloteyphil/no-such-repo"
	routes := with(map[string]reply{repoPath + "/releases": {file: "releases.json"}})
	routes["/repos/"+missing+"/pulls"] = reply{status: http.StatusNotFound, body: `{"message":"Not Found"}`}
	_, srv := serve(t, routes)

	events, err := newCollector(t, srv, "", missing, repo).Collect(context.Background(), window)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v, want not found naming %s", err, missing)
	}
	if got := ids(events); !slices.Equal(got, []string{"github:alloteyphil/wasitus:release:7002"}) {
		t.Errorf("events from the healthy repo = %v", got)
	}
}

// TestPartialResults checks that events found before an error are kept.
func TestPartialResults(t *testing.T) {
	_, srv := serve(t, with(map[string]reply{
		repoPath + "/pulls": {
			file:   "pulls_page1.json",
			header: map[string]string{"Link": `<{server}/repositories/1/pulls?page=2>; rel="next"`},
		},
		"/repositories/1/pulls?page=2": {status: http.StatusInternalServerError, body: `{"message":"boom"}`},
		repoPath + "/releases":         {file: "releases.json"},
	}))
	events, err := newCollector(t, srv, "").Collect(context.Background(), window)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500: boom") {
		t.Errorf("err = %v, want the page 2 failure", err)
	}
	want := []string{
		"github:alloteyphil/wasitus:pr:6", "github:alloteyphil/wasitus:pr:5", "github:alloteyphil/wasitus:pr:4",
		"github:alloteyphil/wasitus:release:7002",
	}
	if got := ids(events); !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestContextCancellation(t *testing.T) {
	t.Run("before collecting", func(t *testing.T) {
		f, srv := serve(t, emptyRepo())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := newCollector(t, srv, "", repo, "alloteyphil/lab-wallet").Collect(ctx, window)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if n := len(f.requests()); n != 0 {
			t.Errorf("made %d requests after cancellation", n)
		}
	})

	t.Run("during a request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done() // hold the request until the client gives up
		}))
		defer srv.Close()
		go func() {
			<-started
			cancel()
		}()
		c, err := New([]string{repo}, testToken, srv.URL, srv.Client(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Collect(ctx, window)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if err != nil && strings.Contains(err.Error(), testToken) {
			t.Errorf("error leaks the token: %q", err)
		}
	})
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		repos   []string
		baseURL string
		wantErr string
	}{
		{"defaults", []string{repo}, "", ""},
		{"trailing slash", []string{repo}, "https://ghe.example/api/v3/", ""},
		{"not a URL", []string{repo}, "://nope", "must be an http or https URL"},
		{"wrong scheme", []string{repo}, "ftp://example.com", "must be an http or https URL"},
		{"no host", []string{repo}, "https://", "must be an http or https URL"},
		{"repo without owner", []string{"wasitus"}, "", `"wasitus" is not in owner/name form`},
		{"repo with extra part", []string{"a/b/c"}, "", "not in owner/name form"},
		{"empty owner", []string{"/b"}, "", "not in owner/name form"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.repos, "", tt.baseURL, nil, nil)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Name() != "github" || c.client != http.DefaultClient {
				t.Errorf("name %q, client %v", c.Name(), c.client)
			}
		})
	}

	c, err := New([]string{repo}, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.endpoint(repo, nil, "pulls").String(); got != "https://api.github.com/repos/alloteyphil/wasitus/pulls" {
		t.Errorf("default endpoint = %q", got)
	}
	c, err = New([]string{repo}, "", "https://ghe.example/api/v3/", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.endpoint(repo, nil, "releases").String(); got != "https://ghe.example/api/v3/repos/alloteyphil/wasitus/releases" {
		t.Errorf("enterprise endpoint = %q", got)
	}
}

func TestRateLimitErrorMessages(t *testing.T) {
	tests := []struct {
		err  RateLimitError
		want string
	}{
		{RateLimitError{Reset: time.Date(2026, 11, 26, 18, 0, 0, 0, time.FixedZone("GMT+1", 3600))}, "GitHub API rate limit exceeded; it resets at 2026-11-26T17:00:00Z"},
		{RateLimitError{RetryAfter: 90 * time.Second}, "GitHub API rate limit exceeded; retry after 1m30s"},
		{RateLimitError{}, "GitHub API rate limit exceeded"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
}

func TestOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("["))
		chunk := []byte(strings.Repeat(" ", 1<<20))
		for range 11 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	c, err := New([]string{repo}, "", srv.URL, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Collect(context.Background(), window)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err = %v, want a size error", err)
	}
}
