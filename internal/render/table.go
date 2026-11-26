package render

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

// Timeline is everything a renderer needs to print a timeline.
type Timeline struct {
	Window   event.Window   // the window the user asked for, without lookback
	Lookback time.Duration  // how far before Window.From events were collected
	Location *time.Location // zone used to display times
	Events   []event.Event  // sorted
}

// Table writes the timeline as an aligned plain-text table for the terminal.
func Table(w io.Writer, t Timeline) error {
	loc := t.Location
	if loc == nil {
		loc = time.UTC
	}
	from, to := t.Window.From.In(loc), t.Window.To.In(loc)
	if _, err := fmt.Fprintf(w, "Window: %s -> %s %s (plus %s lookback)\n\n",
		from.Format("2006-01-02 15:04"), to.Format("2006-01-02 15:04"), loc, shortDuration(t.Lookback)); err != nil {
		return err
	}
	if len(t.Events) == 0 {
		_, err := fmt.Fprintln(w, "No events in this window.")
		return err
	}

	layout := "15:04"
	if !sameDay(from.Add(-t.Lookback), to) {
		layout = "Jan 02 15:04"
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tKIND\tSOURCE\tSERVICE\tEVENT")
	for _, e := range t.Events {
		service := e.Service
		if service == "" {
			service = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			e.Time.In(loc).Format(layout), e.Kind, e.Source, oneLine(service), oneLine(e.Title))
	}
	return tw.Flush()
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// shortDuration prints 60m as "60m" and 2h as "2h" instead of "1h0m0s".
func shortDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0 && d >= 2*time.Hour:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

// oneLine keeps free text from breaking the table layout.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
