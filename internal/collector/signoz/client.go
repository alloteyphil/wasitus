package signoz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/alloteyphil/wasitus/internal/redact"
)

const (
	// Source is the value of Event.Source for events from this collector.
	Source = "signoz"

	// TestedVersion is the SigNoz version documented in ADR-0007. A different
	// version still runs, with a warning surfaced to the user.
	TestedVersion = "v0.149.0"

	// DefaultRawPageLimit is the maximum rows SigNoz returns per raw query
	// page (ADR-0007). When a page is full, the client splits the time range.
	DefaultRawPageLimit = 10000

	userAgent          = "wasitus"
	defaultMaxBody     = 32 << 20 // 32 MiB per response (10,000 raw spans can be several MiB)
	maxPages           = 50       // bounds offset/cursor pagination loops
	defaultConcurrency = 4        // concurrent per-rule history fetches
)

var (
	// ErrUnauthorized is returned when SigNoz answers 401 (missing, invalid,
	// or expired API key).
	ErrUnauthorized = errors.New("unauthorized: check SIGNOZ_API_KEY (it may be invalid or expired)")

	// ErrForbidden is returned when SigNoz answers 403 (key lacks viewer access).
	ErrForbidden = errors.New("forbidden: API key lacks permission for this resource")
)

// VersionInfo is the response of GET /api/v1/version.
type VersionInfo struct {
	Version        string `json:"version"`
	EE             string `json:"ee,omitempty"`
	SetupCompleted bool   `json:"setupCompleted"`
}

// ServiceAccount is the identity returned by GET /api/v1/service_accounts/me.
type ServiceAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Client talks to one SigNoz instance over HTTP. It is safe for concurrent use
// once constructed.
type Client struct {
	base               *url.URL
	apiKey             string
	http               *http.Client
	rawPageLimit       int
	maxRuleConcurrency int
	maxBody            int64
}

// NewClient returns a read-only SigNoz client for baseURL. apiKey may be empty
// when the server does not require authentication; when non-empty it is sent
// in the SigNoz-Api-Key header on authenticated endpoints. httpClient nil uses
// http.DefaultClient.
func NewClient(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("signoz: base URL %q must be an http or https URL", baseURL)
	}
	if base.Path == "" {
		base.Path = "/"
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clientCopy := *httpClient
	prevRedirect := clientCopy.CheckRedirect
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !strings.EqualFold(req.URL.Host, base.Host) {
			return fmt.Errorf("refusing cross-host redirect to %s", req.URL.Host)
		}
		if base.Scheme == "https" && !strings.EqualFold(req.URL.Scheme, "https") {
			return fmt.Errorf("refusing redirect downgrade to %s", req.URL.Scheme)
		}
		if prevRedirect != nil {
			return prevRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &Client{
		base:               base,
		apiKey:             apiKey,
		http:               &clientCopy,
		rawPageLimit:       DefaultRawPageLimit,
		maxRuleConcurrency: defaultConcurrency,
		maxBody:            defaultMaxBody,
	}, nil
}

// Version calls GET /api/v1/version without authentication.
func (c *Client) Version(ctx context.Context) (VersionInfo, error) {
	u := c.endpoint(nil, "api", "v1", "version")
	body, err := c.do(ctx, http.MethodGet, u, nil, false)
	if err != nil {
		return VersionInfo{}, endpointError(http.MethodGet, u, err)
	}
	var info VersionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return VersionInfo{}, endpointError(http.MethodGet, u, fmt.Errorf("decode version: %w", err))
	}
	return info, nil
}

// VersionWarning returns a human-readable warning when v does not match
// TestedVersion, or "" when it matches or the version is unknown.
func VersionWarning(v VersionInfo) string {
	got := clean(v.Version)
	if got == "" || got == TestedVersion {
		return ""
	}
	return fmt.Sprintf("SigNoz version %s differs from tested version %s; API behavior may vary", got, TestedVersion)
}

// CheckAuth verifies the API key with GET /api/v1/service_accounts/me.
func (c *Client) CheckAuth(ctx context.Context) (ServiceAccount, error) {
	u := c.endpoint(nil, "api", "v1", "service_accounts", "me")
	body, err := c.do(ctx, http.MethodGet, u, nil, true)
	if err != nil {
		return ServiceAccount{}, endpointError(http.MethodGet, u, err)
	}
	var env struct {
		Data ServiceAccount `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ServiceAccount{}, endpointError(http.MethodGet, u, fmt.Errorf("decode service account: %w", err))
	}
	return env.Data, nil
}

func (c *Client) endpoint(query url.Values, elem ...string) *url.URL {
	u := c.base.JoinPath(elem...)
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u
}

func (c *Client) get(ctx context.Context, u *url.URL) ([]byte, error) {
	return c.do(ctx, http.MethodGet, u, nil, true)
}

func (c *Client) postJSON(ctx context.Context, u *url.URL, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return c.do(ctx, http.MethodPost, u, raw, true)
}

func (c *Client) do(ctx context.Context, method string, u *url.URL, reqBody []byte, auth bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var bodyReader io.Reader
	if reqBody != nil {
		bodyReader = bytes.NewReader(reqBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth && c.apiKey != "" {
		req.Header.Set("SigNoz-Api-Key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	limit := c.maxBody
	if limit <= 0 {
		limit = defaultMaxBody
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response is larger than %d bytes", limit)
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}
	return body, nil
}

func checkStatus(code int, body []byte) error {
	if code >= 200 && code <= 299 {
		return nil
	}
	switch code {
	case http.StatusUnauthorized:
		if msg := apiMessage(code, body); msg != "" && msg != http.StatusText(code) {
			return fmt.Errorf("%w (%s)", ErrUnauthorized, msg)
		}
		return ErrUnauthorized
	case http.StatusForbidden:
		if msg := apiMessage(code, body); msg != "" && msg != http.StatusText(code) {
			return fmt.Errorf("%w (%s)", ErrForbidden, msg)
		}
		return ErrForbidden
	}
	return fmt.Errorf("HTTP %d: %s", code, apiMessage(code, body))
}

// apiMessage extracts a short human-readable error message from a SigNoz error
// body (either {"error":{"message":...}} or {"message":...}), falling back to
// the HTTP status text.
func apiMessage(code int, body []byte) string {
	var e struct {
		Message string `json:"message"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := ""
	if json.Unmarshal(body, &e) == nil {
		if e.Error != nil && e.Error.Message != "" {
			msg = clean(e.Error.Message)
		} else if e.Message != "" {
			msg = clean(e.Message)
		}
	}
	if msg == "" {
		return http.StatusText(code)
	}
	msg = redact.Truncate(msg, 200) // word boundary, so redaction still recognises what is left
	return msg
}

func endpointError(method string, u *url.URL, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %s: %w", method, u.Path, err)
}

func isFatalAuthOrContext(err error) bool {
	return errors.Is(err, ErrUnauthorized) ||
		errors.Is(err, ErrForbidden) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// clean makes outside text safe to show on one line: control characters become
// spaces and runs of space collapse.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// resolveLink resolves a SigNoz-returned relative or absolute URL against base,
// refusing links to other hosts or non-http(s) schemes.
func (c *Client) resolveLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := c.base.Parse(raw)
	if err != nil {
		return ""
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host != c.base.Host {
		return ""
	}
	return clean(u.String())
}

// dedupeStrings returns unique non-empty strings in order of first appearance.
func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
