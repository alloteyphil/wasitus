// Package github reads changes from GitHub repositories: merged pull requests,
// deployments and releases, each turned into a change event.
//
// It uses the REST API (version 2022-11-28) with the standard library only and
// never writes. For each repository and window it calls:
//
//	GET /repos/{owner}/{repo}/pulls?state=closed&sort=updated&direction=desc&per_page=100
//	GET /repos/{owner}/{repo}/deployments?per_page=100
//	GET /repos/{owner}/{repo}/deployments/{id}/statuses?per_page=1  (one per deployment in the window)
//	GET /repos/{owner}/{repo}/releases?per_page=100
//
// Lists are followed through their Link rel="next" headers, newest first, and
// stop at the first page that reaches back before the window (or after
// maxPages pages). Next-page links must stay on the API host, so the token is
// never sent anywhere else.
//
// A fine-grained token needs read-only access to pull requests, deployments
// and contents (for releases). Without a token, public repositories still work
// at GitHub's much lower anonymous rate limit. The token is only ever sent in
// the Authorization header; it never appears in errors or events.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
	"github.com/alloteyphil/wasitus/internal/redact"
)

const (
	// Source is the value of Event.Source for events from this collector.
	Source = "github"
	// DefaultBaseURL is the public GitHub REST API.
	DefaultBaseURL = "https://api.github.com"

	apiVersion = "2022-11-28"
	userAgent  = "wasitus"
	perPage    = "100"
	maxPages   = 30       // per list, so a busy repository or a looping Link header cannot run forever
	maxBody    = 10 << 20 // bytes per response
)

// ErrNotFound is returned when GitHub answers 404. GitHub uses 404 both for a
// repository that does not exist and for one the token may not see.
var ErrNotFound = errors.New("repository not found or token lacks access")

// RateLimitError reports that GitHub refused a request because a rate limit
// was reached.
type RateLimitError struct {
	Reset      time.Time     // when the primary rate limit resets; zero if unknown
	RetryAfter time.Duration // how long to wait for a secondary rate limit; zero if unknown
}

func (e *RateLimitError) Error() string {
	switch {
	case !e.Reset.IsZero():
		return "GitHub API rate limit exceeded; it resets at " + e.Reset.UTC().Format(time.RFC3339)
	case e.RetryAfter > 0:
		return "GitHub API rate limit exceeded; retry after " + e.RetryAfter.String()
	}
	return "GitHub API rate limit exceeded"
}

// Collector reads merged pull requests, deployments and releases.
type Collector struct {
	repos      []string
	token      string
	base       *url.URL
	client     *http.Client
	serviceFor func(repo string) string
}

// New returns a collector for repos, each in owner/name form.
//
// token may be empty for public repositories. baseURL is the API root; ""
// means DefaultBaseURL. client nil means http.DefaultClient. serviceFor maps a
// repository to its telemetry service name (normally config.ServiceFor); nil
// leaves Service empty.
func New(repos []string, token, baseURL string, client *http.Client, serviceFor func(repo string) string) (*Collector, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("github: base URL %q must be an http or https URL", baseURL)
	}
	if base.Path == "" {
		base.Path = "/" // so endpoint paths, shown in errors, start with a slash
	}
	for _, r := range repos {
		owner, name, ok := strings.Cut(r, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return nil, fmt.Errorf("github: repository %q is not in owner/name form", r)
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	if serviceFor == nil {
		serviceFor = func(string) string { return "" }
	}
	return &Collector{
		repos:      repos,
		token:      token,
		base:       base,
		client:     client,
		serviceFor: serviceFor,
	}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return Source }

// Collect implements collector.Collector. w should already include the
// lookback. A failing repository does not stop the others, and events found
// before an error are returned with it. A rate limit or a cancelled ctx stops
// the whole collection, because every later request would fail the same way.
func (c *Collector) Collect(ctx context.Context, w event.Window) ([]event.Event, error) {
	var events []event.Event
	var errs []error
	for _, repo := range c.repos {
		evs, err := c.collectRepo(ctx, repo, w)
		events = append(events, evs...)
		if err != nil {
			errs = append(errs, err)
			if stopsEverything(err) {
				break
			}
		}
	}
	return events, errors.Join(errs...)
}

func (c *Collector) collectRepo(ctx context.Context, repo string, w event.Window) ([]event.Event, error) {
	service := c.serviceFor(repo)
	steps := []func(context.Context, string, string, event.Window) ([]event.Event, error){
		c.pulls, c.deployments, c.releases,
	}
	var events []event.Event
	var errs []error
	for _, step := range steps {
		evs, err := step(ctx, repo, service, w)
		events = append(events, evs...)
		if err != nil {
			errs = append(errs, err)
			if errors.Is(err, ErrNotFound) || stopsEverything(err) {
				break // the other lists of this repository would fail the same way
			}
		}
	}
	return events, errors.Join(errs...)
}

func stopsEverything(err error) bool {
	var rl *RateLimitError
	return errors.As(err, &rl) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (c *Collector) pulls(ctx context.Context, repo, service string, w event.Window) ([]event.Event, error) {
	u := c.endpoint(repo, url.Values{
		"state": {"closed"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {perPage},
	}, "pulls")
	var events []event.Event
	err := c.list(ctx, u, func(body []byte) (bool, error) {
		evs, done, err := pullEvents(repo, service, body, w)
		events = append(events, evs...)
		return done, err
	})
	return events, endpointError(u, err)
}

func (c *Collector) deployments(ctx context.Context, repo, service string, w event.Window) ([]event.Event, error) {
	u := c.endpoint(repo, url.Values{"per_page": {perPage}}, "deployments")
	var found []deployment
	err := c.list(ctx, u, func(body []byte) (bool, error) {
		in, done, err := deploymentsIn(body, w)
		found = append(found, in...)
		return done, err
	})
	if err != nil {
		return nil, endpointError(u, err)
	}

	events := make([]event.Event, 0, len(found))
	for _, d := range found {
		su := c.endpoint(repo, url.Values{"per_page": {"1"}},
			"deployments", strconv.FormatInt(d.ID, 10), "statuses")
		body, _, err := c.get(ctx, su)
		var s deploymentStatus
		if err == nil {
			s, err = latestStatus(body)
		}
		if err != nil {
			return events, endpointError(su, err)
		}
		events = append(events, d.event(repo, service, s))
	}
	return events, nil
}

func (c *Collector) releases(ctx context.Context, repo, service string, w event.Window) ([]event.Event, error) {
	u := c.endpoint(repo, url.Values{"per_page": {perPage}}, "releases")
	var events []event.Event
	err := c.list(ctx, u, func(body []byte) (bool, error) {
		evs, done, err := releaseEvents(repo, service, body, w)
		events = append(events, evs...)
		return done, err
	})
	return events, endpointError(u, err)
}

// endpoint returns the URL of a repository endpoint under the API root.
func (c *Collector) endpoint(repo string, query url.Values, elem ...string) *url.URL {
	owner, name, _ := strings.Cut(repo, "/") // checked by New
	u := c.base.JoinPath(append([]string{"repos", owner, name}, elem...)...)
	u.RawQuery = query.Encode()
	return u
}

// endpointError names the endpoint in err, which carries the repository too.
func endpointError(u *url.URL, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("GET %s: %w", u.Path, err)
}

// list fetches u and the pages after it, handing each body to page, until page
// reports done, there is no next page or maxPages pages have been read.
func (c *Collector) list(ctx context.Context, u *url.URL, page func(body []byte) (done bool, err error)) error {
	for n := 1; ; n++ {
		body, next, err := c.get(ctx, u)
		if err != nil {
			return err
		}
		done, err := page(body)
		if err != nil || done || next == nil {
			return err
		}
		if n == maxPages {
			return fmt.Errorf("stopped after %d pages; narrow the window", maxPages)
		}
		u = next
	}
}

// get performs one read-only request and returns the body and the next page,
// if the response links to one.
func (c *Collector) get(ctx context.Context, u *url.URL) (body []byte, next *url.URL, err error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req) // a *url.Error names the URL, which never holds the token
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxBody {
		return nil, nil, fmt.Errorf("response is larger than %d bytes", maxBody)
	}
	if err := checkStatus(resp, body); err != nil {
		return nil, nil, err
	}
	next, err = c.nextPage(u, resp.Header.Get("Link"))
	if err != nil {
		return nil, nil, err
	}
	return body, next, nil
}

// nextPage resolves the Link rel="next" target against the current URL. It
// refuses a target on another host, because the request would carry the token.
func (c *Collector) nextPage(cur *url.URL, linkHeader string) (*url.URL, error) {
	ref := nextLink(linkHeader)
	if ref == "" {
		return nil, nil
	}
	next, err := cur.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("parse next page link: %w", err)
	}
	if next.Scheme != c.base.Scheme || next.Host != c.base.Host {
		return nil, fmt.Errorf("next page link leaves %s; not following it", c.base.Host)
	}
	return next, nil
}

// nextLink returns the target of the rel="next" entry in an RFC 8288 Link
// header, or "" if there is none.
func nextLink(header string) string {
	rest := header
	for {
		start := strings.IndexByte(rest, '<')
		if start < 0 {
			return ""
		}
		end := strings.IndexByte(rest[start:], '>')
		if end < 0 {
			return ""
		}
		target := rest[start+1 : start+end]
		rest = rest[start+end+1:]
		params := rest
		if i := strings.IndexByte(rest, '<'); i >= 0 {
			params = rest[:i]
		}
		for _, p := range strings.Split(params, ";") {
			key, value, ok := strings.Cut(strings.Trim(strings.TrimSpace(p), ","), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "rel") {
				continue
			}
			for _, rel := range strings.Fields(strings.Trim(strings.TrimSpace(value), `",`)) {
				if strings.EqualFold(rel, "next") {
					return target
				}
			}
		}
	}
}

// checkStatus turns an unsuccessful response into an error.
func checkStatus(resp *http.Response, body []byte) error {
	code := resp.StatusCode
	if code >= 200 && code <= 299 {
		return nil
	}
	switch code {
	case http.StatusForbidden, http.StatusTooManyRequests:
		if rl := rateLimit(resp.Header); rl != nil {
			return rl
		}
	case http.StatusNotFound:
		return ErrNotFound
	}
	return fmt.Errorf("HTTP %d: %s", code, apiMessage(code, body))
}

// rateLimit reads GitHub's rate-limit headers. It returns nil when the
// response is not about a rate limit (a 403 can also mean "no permission").
func rateLimit(h http.Header) *RateLimitError {
	if strings.TrimSpace(h.Get("X-RateLimit-Remaining")) == "0" {
		rl := &RateLimitError{}
		if s, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil && s > 0 {
			rl.Reset = time.Unix(s, 0).UTC()
		}
		return rl
	}
	if s, err := strconv.ParseInt(strings.TrimSpace(h.Get("Retry-After")), 10, 32); err == nil && s >= 0 {
		return &RateLimitError{RetryAfter: time.Duration(s) * time.Second}
	}
	return nil
}

// apiMessage returns the "message" of a GitHub error body, shortened, or the
// standard status text when the body has none.
func apiMessage(code int, body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(body, &e) == nil {
		msg = clean(e.Message)
	}
	if msg == "" {
		return http.StatusText(code)
	}
	msg = redact.Truncate(msg, 200) // word boundary, so redaction still recognises what is left
	return msg
}
