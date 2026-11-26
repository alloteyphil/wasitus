package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
	"github.com/alloteyphil/wasitus/internal/golden"
)

var plus1 = time.FixedZone("Africa/Lagos", 60*60)

func at(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, time.UTC) }

func sampleEvents(day int) []event.Event {
	return []event.Event{
		{ID: "1", Time: at(day, 2, 31), Kind: event.KindChange, Source: "github", Service: "orders-service", Title: "Merged PR #12: refactor order validation"},
		{ID: "2", Time: at(day, 2, 47), Kind: event.KindChange, Source: "github", Service: "orders-service", Title: "Deploy orders-service v1.4.0"},
		{ID: "3", Time: at(day, 3, 5), Kind: event.KindExternal, Source: "changes-file", Title: "mockpay status page:\n\tdegraded   payments"},
		{ID: "4", Time: at(day, 3, 9), Kind: event.KindSymptom, Source: "signoz", Service: "wallet-service", Title: "Alert FIRED: High 5xx rate"},
	}
}

func render(t *testing.T, tl Timeline) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Table(&buf, tl); err != nil {
		t.Fatalf("Table() error = %v", err)
	}
	return buf.Bytes()
}

func TestTableSameDay(t *testing.T) {
	tl := Timeline{
		Window:   event.Window{From: at(14, 3, 0), To: at(14, 4, 0)},
		Lookback: time.Hour,
		Location: plus1,
		Events:   sampleEvents(14),
	}
	golden.Assert(t, "testdata/table_same_day.golden", render(t, tl))
}

func TestTableAcrossMidnight(t *testing.T) {
	tl := Timeline{
		Window:   event.Window{From: at(14, 0, 30), To: at(14, 4, 0)},
		Lookback: 2 * time.Hour, // reaches back to the previous day
		Location: time.UTC,
		Events:   append([]event.Event{{ID: "0", Time: at(13, 23, 10), Kind: event.KindChange, Source: "github", Title: "Late-night config change"}}, sampleEvents(14)...),
	}
	golden.Assert(t, "testdata/table_across_midnight.golden", render(t, tl))
}

func TestTableEmpty(t *testing.T) {
	out := string(render(t, Timeline{Window: event.Window{From: at(14, 3, 0), To: at(14, 4, 0)}, Lookback: time.Hour}))
	if !strings.Contains(out, "No events in this window.") || !strings.Contains(out, "UTC") {
		t.Errorf("empty table = %q", out)
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		time.Hour:        "60m",
		90 * time.Minute: "90m",
		2 * time.Hour:    "2h",
		90 * time.Second: "1m30s",
	}
	for d, want := range cases {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
