package render

import (
	"fmt"
	"io"
	"math"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
	"github.com/alloteyphil/wasitus/internal/redact"
)

// Timeline is everything a renderer needs to print a timeline and optional
// error groups, verdict, and warnings.
type Timeline struct {
	Window      event.Window     // the window the user asked for, without lookback
	Lookback    time.Duration    // how far before Window.From events were collected
	Location    *time.Location   // zone used to display times
	Events      []event.Event    // sorted
	ErrorGroups []ErrorGroup     // optional grouped error signatures
	Verdict     *Verdict         // optional verdict summary
	Warnings    []string         // optional non-fatal warnings (rendered in Markdown and JSON)
	Redactor    *redact.Redactor // optional PII/secret redactor; nil disables redaction
}

// Table writes the timeline (and any error groups and verdict) as aligned
// plain-text tables for the terminal.
func Table(w io.Writer, t Timeline) error {
	t = redactTimeline(t)
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
		if _, err := fmt.Fprintln(w, "No events in this window."); err != nil {
			return err
		}
	} else {
		layout := "15:04"
		if !sameDay(from.Add(-t.Lookback), to) {
			layout = "Jan 02 15:04"
		}

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TIME\tKIND\tSOURCE\tSERVICE\tEVENT")
		for _, e := range t.Events {
			service := oneLine(e.Service)
			if service == "" {
				service = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				e.Time.In(loc).Format(layout), e.Kind, e.Source, service, oneLine(e.Title))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(t.ErrorGroups) > 0 {
		if _, err := fmt.Fprintln(w, "\nERROR GROUPS"); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "CLASS\tSHARE\tCOUNT\tSERVICE\tSIGNATURE")
		for _, g := range t.ErrorGroups {
			service := oneLine(g.Service)
			if service == "" {
				service = "-"
			}
			fmt.Fprintf(tw, "%s\t%.0f%%\t%d\t%s\t%s\n",
				oneLine(g.Class), math.Round(g.Share*100), g.Count, service, oneLine(g.Signature))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if t.Verdict != nil {
		if _, err := fmt.Fprintf(w, "\nVERDICT: %s   confidence: %s\n",
			oneLine(t.Verdict.Label), oneLine(t.Verdict.Confidence)); err != nil {
			return err
		}
		for _, line := range t.Verdict.Evidence {
			if _, err := fmt.Fprintf(w, "  - %s\n", oneLine(line)); err != nil {
				return err
			}
		}
	}
	return nil
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
