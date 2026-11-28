package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
	"github.com/alloteyphil/wasitus/internal/golden"
	"github.com/alloteyphil/wasitus/internal/redact"
)

func fullReportTimeline() Timeline {
	evs := sampleEvents(14)
	evs[0].Link = "https://github.com/alloteyphil/wasitus/pull/12"
	evs[3].Link = "http://localhost:8080/traces-explorer?rule=1(a)"
	return Timeline{
		Window:   event.Window{From: at(14, 3, 0), To: at(14, 4, 0)},
		Lookback: time.Hour,
		Location: time.UTC,
		Events:   evs,
		ErrorGroups: []ErrorGroup{
			{
				Class:     "account",
				Service:   "wallet-service",
				Signature: "mockpay rejected the charge: HTTP <n> AccountSuspended",
				RuleID:    "account-suspended",
				Count:     28,
				Share:     0.9333,
			},
			{
				Class:     "code",
				Signature: "unexpected nil | empty body",
				RuleID:    "fallback-ours",
				Count:     2,
				Share:     0.0667,
			},
		},
		Verdict: &Verdict{
			Label:      "EXTERNAL / account",
			Side:       "EXTERNAL",
			Cause:      "account",
			Confidence: "high",
			Evidence: []string{
				"93% of errors (28/30) classified as account",
				"top error group in wallet-service: mockpay rejected the charge: HTTP <n> AccountSuspended (28 occurrences, rule account-suspended)",
				`recent change "Deploy orders-service v1.4.0" (score 0.82) was not blamed because 93% of errors are account failures`,
			},
		},
		Warnings: []string{
			"signoz: SigNoz version v0.151.0 differs from tested version v0.149.0; API behavior may vary",
		},
	}
}

func TestParseFormatAndRender(t *testing.T) {
	tests := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		{"", FormatTable, false},
		{"table", FormatTable, false},
		{" TABLE ", FormatTable, false},
		{"markdown", FormatMarkdown, false},
		{"md", FormatMarkdown, false},
		{"json", FormatJSON, false},
		{"yaml", "", true},
	}
	for _, tt := range tests {
		got, err := ParseFormat(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseFormat(%q) = (%q, %v), want (%q, err=%v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}

	var buf bytes.Buffer
	if err := Render(&buf, Format("xml"), Timeline{}); err == nil {
		t.Error("Render(xml) error = nil, want error")
	}
}

func TestGoldenReports(t *testing.T) {
	tl := fullReportTimeline()

	tests := []struct {
		name   string
		format Format
		golden string
	}{
		{"table", FormatTable, "testdata/table_verdict.golden"},
		{"markdown", FormatMarkdown, "testdata/markdown_report.golden"},
		{"json", FormatJSON, "testdata/json_report.golden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Render(&buf, tt.format, tl); err != nil {
				t.Fatalf("Render(%s) error = %v", tt.format, err)
			}
			golden.Assert(t, tt.golden, buf.Bytes())
			if tt.format == FormatJSON {
				var parsed map[string]any
				if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
					t.Fatalf("JSON output is not valid JSON: %v", err)
				}
			}
		})
	}
}

func TestMarkdownEmptyAndAcrossMidnight(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, Timeline{Window: event.Window{From: at(14, 3, 0), To: at(14, 4, 0)}, Lookback: time.Hour}); err != nil {
		t.Fatalf("Markdown(empty) error = %v", err)
	}
	if !strings.Contains(buf.String(), "No events in this window.") {
		t.Errorf("Markdown(empty) = %q", buf.String())
	}

	buf.Reset()
	tl := Timeline{
		Window:   event.Window{From: at(14, 0, 30), To: at(14, 4, 0)},
		Lookback: 2 * time.Hour,
		Location: time.UTC,
		Events:   []event.Event{{ID: "0", Time: at(13, 23, 10), Kind: event.KindChange, Source: "github", Title: "Pipe | title"}},
	}
	if err := Markdown(&buf, tl); err != nil {
		t.Fatalf("Markdown(midnight) error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Nov 13 23:10") || !strings.Contains(out, `Pipe \| title`) {
		t.Errorf("Markdown(midnight) = %q", out)
	}
}

func TestJSONEmptyEventsIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, Timeline{
		Window:   event.Window{From: at(14, 3, 0), To: at(14, 4, 0)},
		Lookback: time.Hour,
		Verdict:  &Verdict{Label: "UNKNOWN", Side: "UNKNOWN", Confidence: "low"},
	}); err != nil {
		t.Fatalf("JSON(empty) error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"events": []`) {
		t.Errorf("JSON(empty) should serialize empty events as [], got:\n%s", out)
	}
	if !strings.Contains(out, `"evidence": []`) {
		t.Errorf("JSON(nil verdict evidence) should serialize evidence as [], got:\n%s", out)
	}
}

func TestJSONNormalizesEventTimesToUTCWithoutMutatingCaller(t *testing.T) {
	wat := time.FixedZone("WAT", 3600)
	startLocal := time.Date(2026, 11, 14, 4, 15, 0, 0, wat)
	endLocal := time.Date(2026, 11, 14, 4, 30, 0, 0, wat)
	tl := Timeline{
		Window:   event.Window{From: at(14, 3, 0), To: at(14, 4, 0)},
		Lookback: time.Hour,
		Location: wat,
		Events: []event.Event{{
			ID:     "e1",
			Time:   startLocal,
			End:    &endLocal,
			Kind:   event.KindSymptom,
			Source: "signoz",
			Title:  "Alert FIRED",
		}},
	}
	var buf bytes.Buffer
	if err := JSON(&buf, tl); err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"time": "2026-11-14T03:15:00Z"`) || !strings.Contains(out, `"end": "2026-11-14T03:30:00Z"`) {
		t.Errorf("JSON() did not normalize event times to UTC:\n%s", out)
	}
	if tl.Events[0].Time.Location() != wat || tl.Events[0].End.Location() != wat {
		t.Error("JSON() mutated caller's Event.Time or Event.End location")
	}
}

func TestMarkdownEscapesLinksHeadingsAndURLs(t *testing.T) {
	var buf bytes.Buffer
	tl := Timeline{
		Window:   event.Window{From: at(14, 3, 0), To: at(14, 4, 0)},
		Lookback: time.Hour,
		Location: time.UTC,
		Events: []event.Event{{
			ID:     "e1",
			Time:   at(14, 3, 5),
			Kind:   event.KindSymptom,
			Source: "signoz",
			Title:  "index out of range [3]",
			Link:   "https://example.test/q?expr=a|b\nc",
		}},
		Verdict: &Verdict{
			Label:      "EXTERNAL\n/ dependency",
			Confidence: "high\n",
		},
	}
	if err := Markdown(&buf, tl); err != nil {
		t.Fatalf("Markdown() error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `**EXTERNAL / dependency** (confidence: high)`) {
		t.Errorf("Markdown() verdict heading not collapsed to one line:\n%s", out)
	}
	if !strings.Contains(out, `[index out of range \[3\]](https://example.test/q?expr=a%7Cb`+`c)`) {
		t.Errorf("Markdown() link text/URL not safely escaped:\n%s", out)
	}
}

func TestRedactionAcrossFormatsAndNoMutation(t *testing.T) {
	rawEmail := "alice@example.com"
	rawToken := "gh" + "p_" + strings.Repeat("lab0", 9)
	tl := Timeline{
		Window:   event.Window{From: at(14, 3, 0), To: at(14, 4, 0)},
		Lookback: time.Hour,
		Location: time.UTC,
		Events: []event.Event{{
			ID:     "e1",
			Time:   at(14, 3, 5),
			Kind:   event.KindSymptom,
			Source: "signoz",
			Title:  "Charge failed for " + rawEmail + " using " + rawToken,
			Link:   "https://example.test/user/" + rawEmail,
			Attrs:  map[string]string{"customer_email": rawEmail, "api_key": "secret-123"},
		}},
		ErrorGroups: []ErrorGroup{{
			Class:     "account",
			Service:   "wallet-service",
			Signature: "customer " + rawEmail + " failed",
			Count:     10,
			Share:     1.0,
		}},
		Verdict: &Verdict{
			Label:      "EXTERNAL / account",
			Side:       "EXTERNAL",
			Cause:      "account",
			Confidence: "high",
			Evidence:   []string{"failed for " + rawEmail + " with " + rawToken},
		},
		Warnings: []string{"token " + rawToken + " seen"},
		Redactor: redact.New(),
	}

	for _, format := range []Format{FormatTable, FormatMarkdown, FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			var buf bytes.Buffer
			if err := Render(&buf, format, tl); err != nil {
				t.Fatalf("Render(%s) error = %v", format, err)
			}
			out := buf.String()
			if strings.Contains(out, rawEmail) || strings.Contains(out, rawToken) || strings.Contains(out, "secret-123") {
				t.Errorf("%s output leaked sensitive data:\n%s", format, out)
			}
			if !strings.Contains(out, "[email]") || !strings.Contains(out, "[token]") {
				t.Errorf("%s output missing redaction placeholders:\n%s", format, out)
			}
		})
	}

	// Verify the caller's original data was not mutated in place.
	if !strings.Contains(tl.Events[0].Title, rawEmail) ||
		!strings.Contains(tl.Events[0].Link, rawEmail) ||
		tl.Events[0].Attrs["api_key"] != "secret-123" ||
		!strings.Contains(tl.ErrorGroups[0].Signature, rawEmail) ||
		!strings.Contains(tl.Verdict.Evidence[0], rawEmail) ||
		!strings.Contains(tl.Warnings[0], rawToken) {
		t.Error("Render mutated the caller's Timeline")
	}
}

// TestMarkdownTreatsTextAsText checks that titles, evidence and warnings
// cannot add images, links, HTML or emphasis to a pasted report, and that
// only http(s) links become links.
func TestMarkdownTreatsTextAsText(t *testing.T) {
	tl := Timeline{
		Window:   event.Window{From: at(14, 3, 0), To: at(14, 4, 0)},
		Location: time.UTC,
		Events: []event.Event{
			{ID: "1", Time: at(14, 3, 1), Kind: event.KindChange, Source: "github",
				Title: "![t](https://evil.test/x.png) <img src=x> **bold**", Link: "https://github.com/a/b/pull/1"},
			{ID: "2", Time: at(14, 3, 2), Kind: event.KindExternal, Source: "changes-file",
				Title: `ends with a backslash\`, Link: "javascript:alert(1)"},
		},
		Verdict:  &Verdict{Label: "UNKNOWN", Confidence: "low", Evidence: []string{"top change candidate: [x](https://evil.test)"}},
		Warnings: []string{"<script>alert(1)</script>"},
	}
	var buf bytes.Buffer
	if err := Render(&buf, FormatMarkdown, tl); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, bad := range []string{"![t](", "<img", "**bold**", "<script>", "](javascript:", "[x](https://evil.test)"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains live Markdown/HTML %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{
		`!\[t\](https://evil.test/x.png)`, // the image becomes plain text
		"&lt;img src=x&gt;",
		`\*\*bold\*\*`,
		`ends with a backslash\\`, // backslash escaped, no link built
		"](https://github.com/a/b/pull/1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
