package signoz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

// maxSplitDepth caps recursive time-range splitting for raw queries, and
// maxRawPages caps the total number of raw query requests per call so a dense
// burst or misbehaving proxy cannot trigger exponential request amplification.
const (
	maxSplitDepth = 30
	maxRawPages   = 50
)

// queryRangeRequest matches SigNoz v0.149.0 POST /api/v5/query_range.
type queryRangeRequest struct {
	SchemaVersion  string         `json:"schemaVersion"`
	Start          int64          `json:"start"`
	End            int64          `json:"end"`
	RequestType    string         `json:"requestType"`
	CompositeQuery compositeQuery `json:"compositeQuery"`
	FormatOptions  *formatOptions `json:"formatOptions,omitempty"`
}

type formatOptions struct {
	FillGaps bool `json:"fillGaps,omitempty"`
}

type compositeQuery struct {
	Queries []queryEnvelope `json:"queries"`
}

type queryEnvelope struct {
	Type string           `json:"type"`
	Spec builderQuerySpec `json:"spec"`
}

type builderQuerySpec struct {
	Name         string             `json:"name"`
	Signal       string             `json:"signal"`
	StepInterval int64              `json:"stepInterval,omitempty"`
	Aggregations []queryAggregation `json:"aggregations,omitempty"`
	Filter       *queryFilter       `json:"filter,omitempty"`
	GroupBy      []fieldKey         `json:"groupBy,omitempty"`
	Limit        int                `json:"limit,omitempty"`
}

type queryAggregation struct {
	Expression string `json:"expression"`
}

type queryFilter struct {
	Expression string `json:"expression"`
}

type fieldKey struct {
	Name          string `json:"name"`
	FieldContext  string `json:"fieldContext,omitempty"`
	FieldDataType string `json:"fieldDataType,omitempty"`
}

// queryRangeEnvelope is the outer render.Success wrapper returned by
// POST /api/v5/query_range.
type queryRangeEnvelope struct {
	Status string            `json:"status"`
	Data   queryRangePayload `json:"data"`
}

type queryRangePayload struct {
	Type    string         `json:"type"`
	Data    queryData      `json:"data"`
	Warning *queryWarnData `json:"warning,omitempty"`
}

type queryData struct {
	Results []json.RawMessage `json:"results"`
}

type queryWarnData struct {
	Message  string `json:"message"`
	Warnings []struct {
		Message string `json:"message"`
	} `json:"warnings,omitempty"`
}

type rawResult struct {
	QueryName  string   `json:"queryName"`
	NextCursor string   `json:"nextCursor"`
	Rows       []rawRow `json:"rows"`
}

type rawRow struct {
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

func (c *Client) queryRange(ctx context.Context, req queryRangeRequest) (queryRangePayload, []string, error) {
	u := c.endpoint(nil, "api", "v5", "query_range")
	body, err := c.postJSON(ctx, u, req)
	if err != nil {
		return queryRangePayload{}, nil, endpointError(http.MethodPost, u, err)
	}
	payload, warnings, err := decodeQueryRange(body)
	if err != nil {
		return queryRangePayload{}, nil, endpointError(http.MethodPost, u, err)
	}
	return payload, warnings, nil
}

func decodeQueryRange(body []byte) (queryRangePayload, []string, error) {
	var env queryRangeEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return queryRangePayload{}, nil, fmt.Errorf("decode query_range response: %w", err)
	}
	return env.Data, extractWarnings(env.Data.Warning), nil
}

func extractWarnings(w *queryWarnData) []string {
	if w == nil {
		return nil
	}
	var out []string
	if msg := clean(w.Message); msg != "" {
		out = append(out, msg)
	}
	for _, sub := range w.Warnings {
		if msg := clean(sub.Message); msg != "" {
			out = append(out, msg)
		}
	}
	return dedupeStrings(out)
}

// queryRaw executes a raw traces or logs query over w. Because SigNoz's raw
// nextCursor skips rows that share a millisecond (ADR-0007), whenever a page
// hits rawPageLimit rows this splits the time range in half with inclusive
// boundaries and deduplicates rows using keyFn.
func (c *Client) queryRaw(ctx context.Context, w event.Window, signal, filterExpr string, keyFn func(rawRow) string) ([]rawRow, []string, error) {
	startMs := w.From.UTC().UnixMilli()
	endMs := w.To.UTC().UnixMilli()
	if w.To.UTC().Nanosecond()%int(time.Millisecond) != 0 {
		endMs++
	}
	if endMs <= startMs {
		return nil, nil, nil
	}

	var rows []rawRow
	var warnings []string
	seen := make(map[string]struct{})
	// Reserve 1 request for the root query; each split reserves 2 more up front
	// so left/right halves always run as a pair and total requests stay <= maxRawPages.
	budget := maxRawPages - 1
	err := c.queryRawRange(ctx, startMs, endMs, 0, &budget, signal, filterExpr, keyFn, seen, &rows, &warnings)
	return rows, dedupeStrings(warnings), err
}

func (c *Client) queryRawRange(
	ctx context.Context,
	startMs, endMs int64,
	depth int,
	budget *int,
	signal, filterExpr string,
	keyFn func(rawRow) string,
	seen map[string]struct{},
	rows *[]rawRow,
	warnings *[]string,
) error {
	limit := c.rawPageLimit
	if limit <= 0 {
		limit = DefaultRawPageLimit
	}
	req := queryRangeRequest{
		SchemaVersion: "v1",
		Start:         startMs,
		End:           endMs,
		RequestType:   "raw",
		CompositeQuery: compositeQuery{
			Queries: []queryEnvelope{{
				Type: "builder_query",
				Spec: builderQuerySpec{
					Name:   "A",
					Signal: signal,
					Filter: &queryFilter{Expression: filterExpr},
					Limit:  limit,
				},
			}},
		},
	}
	payload, warns, err := c.queryRange(ctx, req)
	*warnings = append(*warnings, warns...)
	if err != nil {
		return err
	}

	pageRows, err := decodeRawRows(payload.Data.Results)
	if err != nil {
		return err
	}

	if len(pageRows) >= limit {
		if endMs-startMs > 1 && depth < maxSplitDepth && *budget >= 2 {
			*budget -= 2
			mid := startMs + (endMs-startMs)/2
			if err := c.queryRawRange(ctx, startMs, mid, depth+1, budget, signal, filterExpr, keyFn, seen, rows, warnings); err != nil {
				return err
			}
			return c.queryRawRange(ctx, mid, endMs, depth+1, budget, signal, filterExpr, keyFn, seen, rows, warnings)
		}
		*warnings = append(*warnings, fmt.Sprintf(
			"signoz %s query hit the %d-row page limit between %d and %d ms and could not split further; results may be incomplete",
			signal, limit, startMs, endMs))
	}

	for _, r := range pageRows {
		k := keyFn(r)
		if k != "" {
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
		}
		*rows = append(*rows, r)
	}
	return nil
}

func decodeRawRows(results []json.RawMessage) ([]rawRow, error) {
	var out []rawRow
	for _, raw := range results {
		var rr rawResult
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber() // preserve full 64-bit nanosecond log timestamps in data.timestamp
		if err := dec.Decode(&rr); err != nil {
			return nil, fmt.Errorf("decode raw query result: %w", err)
		}
		out = append(out, rr.Rows...)
	}
	return out, nil
}
