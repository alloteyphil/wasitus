package signoz

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

const rulesPageSize = 200

type rulesListEnvelope struct {
	Status string        `json:"status"`
	Data   rulesListData `json:"data"`
}

type rulesListData struct {
	Rules []alertRule `json:"rules"`
	Total int64       `json:"total"`
}

type alertRule struct {
	ID        string            `json:"id"`
	Alert     string            `json:"alert"`
	State     string            `json:"state"`
	AlertType string            `json:"alertType"`
	RuleType  string            `json:"ruleType"`
	Disabled  bool              `json:"disabled"`
	Labels    map[string]string `json:"labels"`
}

type ruleHistoryEnvelope struct {
	Status string          `json:"status"`
	Data   ruleHistoryData `json:"data"`
}

type ruleHistoryData struct {
	Items      []ruleHistoryItem `json:"items"`
	Total      uint64            `json:"total"`
	NextCursor string            `json:"nextCursor"`
}

type ruleHistoryItem struct {
	RuleID              string       `json:"ruleId"`
	RuleName            string       `json:"ruleName"`
	OverallState        string       `json:"overallState"`
	OverallStateChanged bool         `json:"overallStateChanged"`
	State               string       `json:"state"`
	StateChanged        bool         `json:"stateChanged"`
	UnixMilli           int64        `json:"unixMilli"`
	Labels              []queryLabel `json:"labels"`
	Fingerprint         uint64       `json:"fingerprint"`
	Value               float64      `json:"value"`
	RelatedTracesLink   string       `json:"relatedTracesLink"`
	RelatedLogsLink     string       `json:"relatedLogsLink"`
}

type queryLabel struct {
	Key   fieldKey `json:"key"`
	Value any      `json:"value"`
}

// Alerts fetches alert rule state transitions (firing, resolved, nodata) in w
// across every alert rule configured in SigNoz. Per ADR-0007 this lists rules
// via GET /api/v3/rules and queries GET /api/v2/rules/{id}/history/timeline
// with bounded concurrency.
//
// If one rule's history call fails, events from the other rules are still
// returned alongside the joined error. Auth failures and context cancellation
// stop immediately.
func (c *Client) Alerts(ctx context.Context, w event.Window) ([]event.Event, []string, error) {
	rules, warns, err := c.listRules(ctx)
	if err != nil {
		return nil, warns, err
	}
	if len(rules) == 0 {
		return nil, warns, nil
	}

	type ruleResult struct {
		events   []event.Event
		warnings []string
		err      error
	}
	results := make([]ruleResult, len(rules))

	workers := c.maxRuleConcurrency
	if workers <= 0 {
		workers = defaultConcurrency
	}
	if workers > len(rules) {
		workers = len(rules)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int, len(rules))
	for i := range rules {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if runCtx.Err() != nil {
					return
				}
				items, rWarns, err := c.ruleHistory(runCtx, rules[idx], w)
				if err != nil {
					results[idx] = ruleResult{warnings: rWarns, err: err}
					if isFatalAuthOrContext(err) {
						cancel()
						return
					}
					continue
				}
				results[idx] = ruleResult{
					events:   c.historyToEvents(rules[idx], items, w),
					warnings: rWarns,
				}
			}
		}()
	}
	wg.Wait()

	var events []event.Event
	var errs []error
	for _, r := range results {
		events = append(events, r.events...)
		warns = append(warns, r.warnings...)
		if r.err != nil {
			errs = append(errs, r.err)
		}
	}
	return events, dedupeStrings(warns), errors.Join(errs...)
}

func (c *Client) listRules(ctx context.Context) ([]alertRule, []string, error) {
	var all []alertRule
	var warns []string
	seen := make(map[string]struct{})
	offset := 0

	for page := 1; ; page++ {
		q := url.Values{
			"limit":  {strconv.Itoa(rulesPageSize)},
			"offset": {strconv.Itoa(offset)},
		}
		u := c.endpoint(q, "api", "v3", "rules")
		body, err := c.get(ctx, u)
		if err != nil {
			return all, warns, endpointError(http.MethodGet, u, err)
		}
		data, err := decodeRulesList(body)
		if err != nil {
			return all, warns, endpointError(http.MethodGet, u, err)
		}
		for _, r := range data.Rules {
			if r.ID == "" {
				continue
			}
			if _, dup := seen[r.ID]; dup {
				continue
			}
			seen[r.ID] = struct{}{}
			all = append(all, r)
		}
		offset += len(data.Rules)
		if len(data.Rules) < rulesPageSize || (data.Total > 0 && int64(offset) >= data.Total) {
			break
		}
		if page == maxPages {
			warns = append(warns, fmt.Sprintf("signoz rules list stopped after %d pages (%d rules); some rules may be omitted", maxPages, len(all)))
			break
		}
	}
	return all, warns, nil
}

func decodeRulesList(body []byte) (rulesListData, error) {
	var env rulesListEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return rulesListData{}, fmt.Errorf("decode rules list: %w", err)
	}
	return env.Data, nil
}

func (c *Client) ruleHistory(ctx context.Context, rule alertRule, w event.Window) ([]ruleHistoryItem, []string, error) {
	var all []ruleHistoryItem
	var warns []string
	cursor := ""
	seenCursors := make(map[string]struct{})

	for page := 1; ; page++ {
		q := url.Values{
			"start": {strconv.FormatInt(w.From.UTC().UnixMilli(), 10)},
			"end":   {strconv.FormatInt(w.To.UTC().UnixMilli(), 10)},
			"limit": {strconv.Itoa(rulesPageSize)},
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		u := c.endpoint(q, "api", "v2", "rules", rule.ID, "history", "timeline")
		body, err := c.get(ctx, u)
		if err != nil {
			return all, warns, endpointError(http.MethodGet, u, err)
		}
		data, err := decodeRuleHistory(body)
		if err != nil {
			return all, warns, endpointError(http.MethodGet, u, err)
		}
		all = append(all, data.Items...)
		if data.NextCursor == "" || len(data.Items) == 0 {
			break
		}
		if _, dup := seenCursors[data.NextCursor]; dup {
			warns = append(warns, fmt.Sprintf("signoz alert history for rule %q repeated pagination cursor; stopping", rule.Alert))
			break
		}
		seenCursors[data.NextCursor] = struct{}{}
		if page == maxPages {
			warns = append(warns, fmt.Sprintf("signoz alert history for rule %q stopped after %d pages; narrow the window", rule.Alert, maxPages))
			break
		}
		cursor = data.NextCursor
	}
	return all, warns, nil
}

func decodeRuleHistory(body []byte) (ruleHistoryData, error) {
	var env ruleHistoryEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return ruleHistoryData{}, fmt.Errorf("decode alert history: %w", err)
	}
	return env.Data, nil
}

// historyToEvents converts one rule's history transitions into timeline events.
// Transitions are sorted chronologically per fingerprint so each firing event
// can record the matching resolution time in Event.End.
func (c *Client) historyToEvents(rule alertRule, items []ruleHistoryItem, w event.Window) []event.Event {
	if len(items) == 0 {
		return nil
	}
	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, func(a, b ruleHistoryItem) int {
		return cmp.Or(
			cmp.Compare(a.Fingerprint, b.Fingerprint),
			cmp.Compare(a.UnixMilli, b.UnixMilli),
			cmp.Compare(a.State, b.State),
		)
	})

	events := make([]event.Event, 0, len(sorted))
	// openFiring tracks the index in events of the most recent unresolved
	// firing event per fingerprint, so a subsequent inactive transition sets End.
	openFiring := make(map[uint64]int)

	for _, item := range sorted {
		if !item.StateChanged || item.UnixMilli <= 0 {
			continue
		}
		ts := time.UnixMilli(item.UnixMilli).UTC()
		if !w.Contains(ts) {
			continue
		}

		state := strings.ToLower(strings.TrimSpace(item.State))
		ruleID := item.RuleID
		if ruleID == "" {
			ruleID = rule.ID
		}
		ruleName := clean(item.RuleName)
		if ruleName == "" {
			ruleName = clean(rule.Alert)
		}
		if ruleName == "" {
			ruleName = ruleID
		}

		service := serviceFromLabels(item.Labels)
		link := c.resolveLink(item.RelatedTracesLink)
		if link == "" {
			link = c.resolveLink(item.RelatedLogsLink)
		}

		switch state {
		case "firing":
			ev := event.Event{
				ID:       alertEventID(ruleID, item.Fingerprint, item.UnixMilli, "firing"),
				Time:     ts,
				Kind:     event.KindSymptom,
				Source:   Source,
				Service:  service,
				Title:    "Alert FIRED: " + ruleName,
				Severity: ruleSeverity(rule.Labels),
				Link:     link,
				Attrs:    alertAttrs(ruleID, ruleName, "firing", rule.AlertType, item.Fingerprint, item.Value),
			}
			openFiring[item.Fingerprint] = len(events)
			events = append(events, ev)

		case "inactive":
			if idx, ok := openFiring[item.Fingerprint]; ok {
				end := ts
				events[idx].End = &end
				delete(openFiring, item.Fingerprint)
			}
			ev := event.Event{
				ID:       alertEventID(ruleID, item.Fingerprint, item.UnixMilli, "resolved"),
				Time:     ts,
				Kind:     event.KindSymptom,
				Source:   Source,
				Service:  service,
				Title:    "Alert RESOLVED: " + ruleName,
				Severity: event.SeverityInfo,
				Link:     link,
				Attrs:    alertAttrs(ruleID, ruleName, "resolved", rule.AlertType, item.Fingerprint, item.Value),
			}
			events = append(events, ev)

		case "nodata":
			ev := event.Event{
				ID:       alertEventID(ruleID, item.Fingerprint, item.UnixMilli, "nodata"),
				Time:     ts,
				Kind:     event.KindSymptom,
				Source:   Source,
				Service:  service,
				Title:    "Alert NO DATA: " + ruleName,
				Severity: event.SeverityWarning,
				Link:     link,
				Attrs:    alertAttrs(ruleID, ruleName, "nodata", rule.AlertType, item.Fingerprint, item.Value),
			}
			events = append(events, ev)
		}
	}
	return events
}

func alertEventID(ruleID string, fingerprint uint64, unixMilli int64, state string) string {
	return fmt.Sprintf("signoz:alert:%s:%d:%d:%s", ruleID, fingerprint, unixMilli, state)
}

func alertAttrs(ruleID, ruleName, state, alertType string, fingerprint uint64, val float64) map[string]string {
	m := map[string]string{
		"rule_id":     ruleID,
		"rule_name":   ruleName,
		"state":       state,
		"fingerprint": strconv.FormatUint(fingerprint, 10),
	}
	if alertType != "" {
		m["alert_type"] = alertType
	}
	if !math.IsNaN(val) && !math.IsInf(val, 0) && val != 0 {
		m["value"] = strconv.FormatFloat(val, 'f', -1, 64)
	}
	return m
}

func serviceFromLabels(labels []queryLabel) string {
	for _, want := range []string{"service.name", "serviceName", "service"} {
		for _, l := range labels {
			if l.Key.Name == want {
				if v := clean(anyToString(l.Value)); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func ruleSeverity(labels map[string]string) event.Severity {
	switch strings.ToLower(strings.TrimSpace(labels["severity"])) {
	case "critical", "fatal", "error", "p1", "sev1":
		return event.SeverityCritical
	case "info", "low":
		return event.SeverityInfo
	default:
		return event.SeverityWarning
	}
}
