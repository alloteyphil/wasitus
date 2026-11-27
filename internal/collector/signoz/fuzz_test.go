package signoz

import (
	"net/http"
	"testing"

	"github.com/alloteyphil/wasitus/internal/event"
)

func testWindowForFuzz() (event.Window, error) {
	return event.NewWindow(winStart, winEnd)
}

func FuzzDecodeRulesList(f *testing.F) {
	f.Add([]byte(`{"status":"success","data":{"rules":[],"total":0}}`))
	f.Add([]byte(`{"status":"success","data":{"rules":[{"id":"r1","alert":"Err","labels":{"severity":"critical"}}],"total":1}}`))
	f.Add([]byte(`{`))
	f.Add([]byte(`null`))

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = decodeRulesList(data)
		_ = apiMessage(http.StatusInternalServerError, data)
	})
}

func FuzzDecodeRuleHistory(f *testing.F) {
	f.Add([]byte(`{"status":"success","data":{"items":[{"ruleId":"r1","state":"firing","stateChanged":true,"unixMilli":1795683780000,"labels":[{"key":{"name":"service.name"},"value":"wallet-service"}],"fingerprint":1,"value":12.5}],"total":1}}`))
	f.Add([]byte(`{"status":"success","data":{"items":[{"labels":[{"key":{"name":"service.name"},"value":123}]}]}}`))
	f.Add([]byte(`not json`))

	c, err := NewClient("http://localhost:8080", "", nil)
	if err != nil {
		f.Fatal(err)
	}
	w, err := testWindowForFuzz()
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(_ *testing.T, data []byte) {
		hist, err := decodeRuleHistory(data)
		if err != nil {
			return
		}
		_ = c.historyToEvents(alertRule{ID: "r1", Alert: "Rule"}, hist.Items, w)
	})
}

func FuzzDecodeQueryRange(f *testing.F) {
	f.Add([]byte(`{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[{"timestamp":"2026-11-26T09:01:10Z","data":{"span_id":"s1","kind_string":"Client","response_status_code":"503","events":[{"name":"exception","attributes":{"exception.type":"err","exception.message":"boom"}}]}}]}]},"warning":{"message":"warn"}}}`))
	f.Add([]byte(`{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[{"data":{"id":"l1","timestamp":1795683670550000000,"body":"failed","resources_string":{"service.name":"wallet"}}}]}]}}}`))
	f.Add([]byte(`{"status":"success","data":{"type":"time_series","data":{"results":[{"queryName":"A","aggregations":[{"index":0,"series":[{"labels":[{"key":{"name":"service.name"},"value":"storefront"}],"values":[{"timestamp":1795683600000,"value":42}]}]}]}]}}}`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(_ *testing.T, data []byte) {
		payload, _, err := decodeQueryRange(data)
		if err != nil {
			return
		}
		if rows, err := decodeRawRows(payload.Data.Results); err == nil {
			var spans []ErrorSample
			for _, r := range rows {
				_ = spanDedupeKey(r)
				_ = logDedupeKey(r)
				if s, ok := spanRowToSample(r); ok {
					spans = append(spans, s)
				}
				_, _ = logRowToSample(r)
			}
			_ = summarizeErrorSpans(spans)
		}
		_, _ = decodeTimeSeriesResults(payload.Data.Results)
	})
}
