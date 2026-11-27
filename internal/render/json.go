package render

import (
	"encoding/json"
	"io"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

type jsonReport struct {
	Window      jsonWindow    `json:"window"`
	Events      []event.Event `json:"events"`
	ErrorGroups []ErrorGroup  `json:"error_groups,omitempty"`
	Verdict     *Verdict      `json:"verdict,omitempty"`
	Warnings    []string      `json:"warnings,omitempty"`
}

type jsonWindow struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Lookback string    `json:"lookback"`
	Timezone string    `json:"timezone"`
}

// JSON writes the timeline, error groups, verdict, and warnings as indented
// JSON for scripting and other tools. Times are always serialized in UTC.
func JSON(w io.Writer, t Timeline) error {
	t = redactTimeline(t)
	loc := t.Location
	if loc == nil {
		loc = time.UTC
	}
	events := t.Events
	if events == nil {
		events = []event.Event{}
	}
	rep := jsonReport{
		Window: jsonWindow{
			From:     t.Window.From.UTC(),
			To:       t.Window.To.UTC(),
			Lookback: shortDuration(t.Lookback),
			Timezone: loc.String(),
		},
		Events:      events,
		ErrorGroups: t.ErrorGroups,
		Verdict:     t.Verdict,
		Warnings:    t.Warnings,
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
