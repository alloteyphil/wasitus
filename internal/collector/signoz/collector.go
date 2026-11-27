package signoz

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/alloteyphil/wasitus/internal/event"
)

// Collector implements collector.Collector for SigNoz. It reads alert rule
// transitions and error-span summaries for the timeline, checks SigNoz's
// server version against TestedVersion, and records non-fatal warnings (such
// as version mismatches and SigNoz query warnings) for the caller to display.
type Collector struct {
	client *Client

	mu       sync.Mutex
	warnings []string
}

// New returns a SigNoz collector for baseURL. See NewClient for parameter
// details.
func New(baseURL, apiKey string, httpClient *http.Client) (*Collector, error) {
	c, err := NewClient(baseURL, apiKey, httpClient)
	if err != nil {
		return nil, err
	}
	return &Collector{client: c}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return Source }

// Client returns the underlying SigNoz API client for downstream stages that
// need raw error samples or traffic baseline buckets.
func (c *Collector) Client() *Client { return c.client }

// Warnings returns a snapshot of non-fatal warnings collected during the most
// recent Collect call (such as an untested SigNoz version or query warnings).
func (c *Collector) Warnings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.warnings) == 0 {
		return nil
	}
	out := make([]string, len(c.warnings))
	copy(out, c.warnings)
	return out
}

// Collect implements collector.Collector. It gathers alert history and
// error-span symptom summaries in w. If one query fails, events from the other
// are still returned alongside the error; auth failures and context
// cancellation stop immediately.
func (c *Collector) Collect(ctx context.Context, w event.Window) ([]event.Event, error) {
	var warns []string

	if v, err := c.client.Version(ctx); err == nil {
		if warn := VersionWarning(v); warn != "" {
			warns = append(warns, warn)
		}
	}

	var events []event.Event
	var errs []error

	alerts, alertWarns, alertErr := c.client.Alerts(ctx, w)
	events = append(events, alerts...)
	warns = append(warns, alertWarns...)
	if alertErr != nil {
		errs = append(errs, alertErr)
		if isFatalAuthOrContext(alertErr) {
			c.setWarnings(warns)
			return events, errors.Join(errs...)
		}
	}

	spans, spanWarns, spanErr := c.client.ErrorSpans(ctx, w)
	events = append(events, summarizeErrorSpans(spans)...)
	warns = append(warns, spanWarns...)
	if spanErr != nil {
		errs = append(errs, spanErr)
	}

	c.setWarnings(warns)
	return events, errors.Join(errs...)
}

func (c *Collector) setWarnings(warns []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warnings = dedupeStrings(warns)
}
