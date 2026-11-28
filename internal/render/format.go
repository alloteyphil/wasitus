package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/alloteyphil/wasitus/internal/event"
)

// Format selects the output format for Render.
type Format string

const (
	FormatTable    Format = "table"
	FormatMarkdown Format = "markdown"
	FormatJSON     Format = "json"
)

// ParseFormat converts a CLI flag value ("table", "markdown", "md", "json")
// into a Format.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "table":
		return FormatTable, nil
	case "markdown", "md":
		return FormatMarkdown, nil
	case "json":
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("unknown format %q (valid: table, markdown, json)", s)
	}
}

// Render writes t to w in the requested format.
func Render(w io.Writer, format Format, t Timeline) error {
	switch format {
	case FormatTable:
		return Table(w, t)
	case FormatMarkdown:
		return Markdown(w, t)
	case FormatJSON:
		return JSON(w, t)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

// Verdict is the summary rendered alongside a timeline.
type Verdict struct {
	Label      string   `json:"label"`
	Side       string   `json:"side"`
	Cause      string   `json:"cause,omitempty"`
	Confidence string   `json:"confidence"`
	Evidence   []string `json:"evidence"`
}

// ErrorGroup is one grouped error signature rendered in a report.
type ErrorGroup struct {
	Class     string  `json:"class"`
	Service   string  `json:"service,omitempty"`
	Signature string  `json:"signature"`
	RuleID    string  `json:"rule_id,omitempty"`
	Count     int     `json:"count"`
	Share     float64 `json:"share"` // 0..1
}

// redactTimeline returns a copy of t with all free-text fields redacted when
// t.Redactor is non-nil. The caller's slices and maps are never modified.
func redactTimeline(t Timeline) Timeline {
	r := t.Redactor
	if r == nil {
		return t
	}
	cp := t
	if len(t.Events) > 0 {
		cp.Events = make([]event.Event, len(t.Events))
		for i, e := range t.Events {
			// IDs can embed service names or PR titles ("signoz:errors:<service>:..."),
			// so they are redacted like any other text.
			e.ID = r.String(e.ID)
			e.Service = r.String(e.Service)
			e.Title = r.String(e.Title)
			e.Link = r.String(e.Link)
			e.Attrs = r.Map(e.Attrs)
			cp.Events[i] = e
		}
	}
	if len(t.ErrorGroups) > 0 {
		cp.ErrorGroups = make([]ErrorGroup, len(t.ErrorGroups))
		for i, g := range t.ErrorGroups {
			g.Service = r.String(g.Service)
			g.Signature = r.String(g.Signature)
			g.RuleID = r.String(g.RuleID) // user rules files name their own rules
			cp.ErrorGroups[i] = g
		}
	}
	if t.Verdict != nil {
		v := *t.Verdict
		v.Label = r.String(v.Label)
		if len(v.Evidence) > 0 {
			v.Evidence = make([]string, len(t.Verdict.Evidence))
			for i, line := range t.Verdict.Evidence {
				v.Evidence[i] = r.String(line)
			}
		}
		cp.Verdict = &v
	}
	if len(t.Warnings) > 0 {
		cp.Warnings = make([]string, len(t.Warnings))
		for i, warn := range t.Warnings {
			cp.Warnings[i] = r.String(warn)
		}
	}
	return cp
}
