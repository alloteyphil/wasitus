package render

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

// Markdown writes the timeline, error groups, verdict, and warnings as a
// Markdown report ready to paste into a post-incident review.
func Markdown(w io.Writer, t Timeline) error {
	t = redactTimeline(t)
	loc := t.Location
	if loc == nil {
		loc = time.UTC
	}
	from, to := t.Window.From.In(loc), t.Window.To.In(loc)

	var b strings.Builder
	fmt.Fprintf(&b, "# Incident timeline\n\n")
	fmt.Fprintf(&b, "- **Window:** %s -> %s %s (plus %s lookback)\n",
		from.Format("2006-01-02 15:04"), to.Format("2006-01-02 15:04"), loc, shortDuration(t.Lookback))

	if t.Verdict != nil {
		fmt.Fprintf(&b, "\n## Verdict\n\n")
		fmt.Fprintf(&b, "**%s** (confidence: %s)\n", mdEscape(oneLine(t.Verdict.Label)), mdEscape(oneLine(t.Verdict.Confidence)))
		if len(t.Verdict.Evidence) > 0 {
			b.WriteByte('\n')
			for _, line := range t.Verdict.Evidence {
				fmt.Fprintf(&b, "- %s\n", mdEscape(oneLine(line)))
			}
		}
	}

	fmt.Fprintf(&b, "\n## Timeline\n\n")
	if len(t.Events) == 0 {
		fmt.Fprintf(&b, "No events in this window.\n")
	} else {
		layout := "15:04"
		if !sameDay(from.Add(-t.Lookback), to) {
			layout = "Jan 02 15:04"
		}
		fmt.Fprintf(&b, "| Time | Kind | Source | Service | Event |\n")
		fmt.Fprintf(&b, "| --- | --- | --- | --- | --- |\n")
		for _, e := range t.Events {
			service := oneLine(e.Service)
			if service == "" {
				service = "-"
			}
			title := mdEscape(oneLine(e.Title))
			if link := strings.TrimSpace(e.Link); isWebLink(link) {
				title = fmt.Sprintf("[%s](%s)", title, mdEscapeURL(link))
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				e.Time.In(loc).Format(layout),
				mdEscape(oneLine(string(e.Kind))),
				mdEscape(oneLine(e.Source)),
				mdEscape(service),
				title,
			)
		}
	}

	if len(t.ErrorGroups) > 0 {
		fmt.Fprintf(&b, "\n## Error groups\n\n")
		fmt.Fprintf(&b, "| Class | Share | Count | Service | Signature |\n")
		fmt.Fprintf(&b, "| --- | ---: | ---: | --- | --- |\n")
		for _, g := range t.ErrorGroups {
			svc := oneLine(g.Service)
			if svc == "" {
				svc = "-"
			}
			fmt.Fprintf(&b, "| %s | %.0f%% | %d | %s | %s |\n",
				mdEscape(oneLine(g.Class)),
				math.Round(g.Share*100),
				g.Count,
				mdEscape(svc),
				mdEscape(oneLine(g.Signature)),
			)
		}
	}

	if len(t.Warnings) > 0 {
		fmt.Fprintf(&b, "\n## Warnings\n\n")
		for _, warn := range t.Warnings {
			fmt.Fprintf(&b, "- %s\n", mdEscape(oneLine(warn)))
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// mdEscape makes free text (titles, signatures, evidence, warnings) render
// as literal text: a merged PR title must not add images, links, emphasis or
// HTML to a pasted post-incident report. Underscores and '#' are left alone:
// they are common in IDs and do nothing inside a word or mid-line.
var mdEscaper = strings.NewReplacer(
	`\`, `\\`,
	"|", `\|`,
	"*", `\*`,
	"`", "\\`",
	"[", `\[`,
	"]", `\]`,
	"<", "&lt;",
	">", "&gt;",
)

// placeholderUnescaper puts redaction placeholders back as they were: a
// bare [email] is not followed by "(" so Markdown shows it literally.
var placeholderUnescaper = strings.NewReplacer(
	`\[email\]`, "[email]",
	`\[phone\]`, "[phone]",
	`\[card\]`, "[card]",
	`\[token\]`, "[token]",
)

func mdEscape(s string) string { return placeholderUnescaper.Replace(mdEscaper.Replace(s)) }

// isWebLink reports whether link is an http or https URL; anything else (a
// javascript: link from a changes file, say) is shown as plain text.
func isWebLink(link string) bool {
	l := strings.ToLower(link)
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://")
}

func mdEscapeURL(s string) string {
	replacer := strings.NewReplacer(
		"\r", "",
		"\n", "",
		"\t", "",
		" ", "%20",
		"(", "%28",
		")", "%29",
		"|", "%7C",
	)
	return replacer.Replace(s)
}
