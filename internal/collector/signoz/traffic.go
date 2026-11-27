package signoz

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

const (
	// DefaultTrafficStep is the 5-minute bucket size used for spike detection
	// (docs/PLAN.md, ADR-0007).
	DefaultTrafficStep = 5 * time.Minute

	// MaxBaselineChunk is the widest single time-series query sent to SigNoz
	// (120 hours). SigNoz caps trace time-series queries at 1,500 points per
	// series, so 5-minute buckets fit 125 hours; chunking at 120 hours keeps a
	// 7-day baseline at exact 5-minute buckets without silent widening (ADR-0007).
	MaxBaselineChunk = 120 * time.Hour

	// maxPointsPerChunk stays safely below SigNoz's 1,500-point series cap.
	maxPointsPerChunk = 1440
)

// TrafficBucket is the request and error count for one service in one
// fixed-size time bucket. Its fields mirror traffic.Bucket so callers can feed
// it to internal/traffic without this package importing internal/traffic.
type TrafficBucket struct {
	Start    time.Time
	Requests int
	Errors   int
}

type timeSeriesResult struct {
	QueryName    string              `json:"queryName"`
	Aggregations []aggregationBucket `json:"aggregations"`
}

type aggregationBucket struct {
	Index  int          `json:"index"`
	Alias  string       `json:"alias"`
	Series []timeSeries `json:"series"`
}

type timeSeries struct {
	Labels []queryLabel      `json:"labels"`
	Values []timeSeriesValue `json:"values"`
}

type timeSeriesValue struct {
	Timestamp int64   `json:"timestamp"` // Unix milliseconds
	Value     float64 `json:"value"`
	Partial   bool    `json:"partial,omitempty"`
}

// TrafficBuckets queries per-service Server-span request counts and error
// counts across w in step-wide buckets (default 5m), automatically splitting
// windows longer than MaxBaselineChunk (or 1,440 steps) so SigNoz never widens
// the step interval (ADR-0007).
func (c *Client) TrafficBuckets(ctx context.Context, w event.Window, step time.Duration) (map[string][]TrafficBucket, []string, error) {
	if step == 0 {
		step = DefaultTrafficStep
	}
	if step < time.Second || step%time.Second != 0 {
		return nil, nil, fmt.Errorf("signoz: traffic step must be a positive whole number of seconds, got %s", step)
	}

	chunkSpan := min(MaxBaselineChunk, time.Duration(maxPointsPerChunk)*step)
	type slotKey struct {
		service string
		tsMs    int64
	}
	type counts struct {
		requests int
		errors   int
	}
	merged := make(map[slotKey]*counts)
	var warns []string

	for chunkStart := w.From.UTC(); chunkStart.Before(w.To.UTC()); {
		chunkEnd := chunkStart.Add(chunkSpan)
		if chunkEnd.After(w.To.UTC()) {
			chunkEnd = w.To.UTC()
		}

		req := buildTrafficQuery(chunkStart, chunkEnd, step)
		payload, cWarns, err := c.queryRange(ctx, req)
		warns = append(warns, cWarns...)
		if err != nil {
			return nil, dedupeStrings(warns), err
		}
		series, err := decodeTimeSeriesResults(payload.Data.Results)
		if err != nil {
			return nil, dedupeStrings(warns), err
		}

		for _, res := range series {
			isErrQuery := res.QueryName == "B"
			for _, agg := range res.Aggregations {
				for _, s := range agg.Series {
					svc := serviceFromLabels(s.Labels)
					for _, pt := range s.Values {
						if pt.Partial || pt.Timestamp <= 0 || math.IsNaN(pt.Value) || math.IsInf(pt.Value, 0) {
							continue
						}
						n := int(math.Round(pt.Value))
						if n < 0 {
							continue
						}
						k := slotKey{service: svc, tsMs: pt.Timestamp}
						c := merged[k]
						if c == nil {
							c = &counts{}
							merged[k] = c
						}
						if isErrQuery {
							c.errors = max(c.errors, n)
						} else {
							c.requests = max(c.requests, n)
						}
					}
				}
			}
		}

		chunkStart = chunkEnd
	}

	out := make(map[string][]TrafficBucket)
	for k, c := range merged {
		out[k.service] = append(out[k.service], TrafficBucket{
			Start:    time.UnixMilli(k.tsMs).UTC(),
			Requests: c.requests,
			Errors:   c.errors,
		})
	}
	for svc := range out {
		slices.SortFunc(out[svc], func(a, b TrafficBucket) int {
			return cmp.Compare(a.Start.UnixNano(), b.Start.UnixNano())
		})
	}
	return out, dedupeStrings(warns), nil
}

func buildTrafficQuery(from, to time.Time, step time.Duration) queryRangeRequest {
	stepSec := int64(step / time.Second)
	groupBy := []fieldKey{{
		Name:          "service.name",
		FieldContext:  "resource",
		FieldDataType: "string",
	}}
	countAgg := []queryAggregation{{Expression: "count()"}}

	return queryRangeRequest{
		SchemaVersion: "v1",
		Start:         from.UTC().UnixMilli(),
		End:           to.UTC().UnixMilli(),
		RequestType:   "time_series",
		FormatOptions: &formatOptions{FillGaps: true},
		CompositeQuery: compositeQuery{
			Queries: []queryEnvelope{
				{
					Type: "builder_query",
					Spec: builderQuerySpec{
						Name:         "A",
						Signal:       "traces",
						StepInterval: stepSec,
						Aggregations: countAgg,
						Filter:       &queryFilter{Expression: "kind_string = 'Server'"},
						GroupBy:      groupBy,
					},
				},
				{
					Type: "builder_query",
					Spec: builderQuerySpec{
						Name:         "B",
						Signal:       "traces",
						StepInterval: stepSec,
						Aggregations: countAgg,
						Filter:       &queryFilter{Expression: "kind_string = 'Server' AND has_error = true"},
						GroupBy:      groupBy,
					},
				},
			},
		},
	}
}

func decodeTimeSeriesResults(results []json.RawMessage) ([]timeSeriesResult, error) {
	out := make([]timeSeriesResult, 0, len(results))
	for _, raw := range results {
		var ts timeSeriesResult
		if err := json.Unmarshal(raw, &ts); err != nil {
			return nil, fmt.Errorf("decode time_series result: %w", err)
		}
		out = append(out, ts)
	}
	return out, nil
}
