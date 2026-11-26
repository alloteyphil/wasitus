package collector

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

type fake struct {
	name     string
	events   []event.Event
	err      error
	panicMsg string
	gotW     *event.Window
}

func (f fake) Name() string { return f.name }

func (f fake) Collect(_ context.Context, w event.Window) ([]event.Event, error) {
	if f.gotW != nil {
		*f.gotW = w
	}
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	return f.events, f.err
}

var (
	start = time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	win   = event.Window{From: start, To: start.Add(time.Hour)}
)

func ev(id string, minutes int, kind event.Kind, source string) event.Event {
	return event.Event{
		ID:     id,
		Time:   start.Add(time.Duration(minutes) * time.Minute),
		Kind:   kind,
		Source: source,
		Title:  "event " + id,
	}
}

func ids(events []event.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

func TestRunMergesSortsAndDedupes(t *testing.T) {
	gotW := new(event.Window)
	github := fake{name: "github", gotW: gotW, events: []event.Event{
		ev("deploy", 20, event.KindChange, "github"),
		ev("pr", 5, event.KindChange, "github"),
		ev("pr", 5, event.KindChange, "github"),     // duplicate
		ev("old", -30, event.KindChange, "github"),  // before the window
		ev("later", 61, event.KindChange, "github"), // after the window
	}}
	signoz := fake{name: "signoz", events: []event.Event{
		ev("alert", 25, event.KindSymptom, "signoz"),
	}}

	res := Run(context.Background(), win, []Collector{github, signoz})

	if want := []string{"pr", "deploy", "alert"}; !slices.Equal(ids(res.Events), want) {
		t.Errorf("events = %v, want %v", ids(res.Events), want)
	}
	if len(res.Failures) != 0 {
		t.Errorf("failures = %v, want none", res.Failures)
	}
	if *gotW != win {
		t.Errorf("collector got window %+v, want %+v", *gotW, win)
	}
}

func TestRunKeepsGoingWhenCollectorsFail(t *testing.T) {
	partial := fake{name: "partial", err: errors.New("page 2: timeout"), events: []event.Event{
		ev("kept", 10, event.KindChange, "partial"),
	}}
	crashing := fake{name: "crashing", panicMsg: "nil map"}
	healthy := fake{name: "healthy", events: []event.Event{ev("ok", 15, event.KindSymptom, "healthy")}}

	res := Run(context.Background(), win, []Collector{partial, crashing, healthy})

	if want := []string{"kept", "ok"}; !slices.Equal(ids(res.Events), want) {
		t.Errorf("events = %v, want %v", ids(res.Events), want)
	}
	if len(res.Failures) != 2 {
		t.Fatalf("failures = %v, want 2", res.Failures)
	}
	if f := res.Failures[0]; f.Collector != "partial" || !strings.Contains(f.Error(), "timeout") {
		t.Errorf("failure[0] = %v", f)
	}
	if f := res.Failures[1]; f.Collector != "crashing" || !strings.Contains(f.Error(), "panic: nil map") {
		t.Errorf("failure[1] = %v", f)
	}
}

func TestRunDropsInvalidEvents(t *testing.T) {
	noTitle := ev("no-title", 1, event.KindChange, "c")
	noTitle.Title = ""
	badKind := ev("bad-kind", 2, "deploy", "c")
	c := fake{name: "c", events: []event.Event{noTitle, badKind, ev("good", 3, event.KindChange, "c")}}

	res := Run(context.Background(), win, []Collector{c})

	if want := []string{"good"}; !slices.Equal(ids(res.Events), want) {
		t.Errorf("events = %v, want %v", ids(res.Events), want)
	}
	if len(res.Failures) != 1 || !strings.Contains(res.Failures[0].Error(), "dropped 2 invalid event(s)") {
		t.Errorf("failures = %v, want one reporting 2 dropped events", res.Failures)
	}
}

func TestRunNormalizes(t *testing.T) {
	plus3 := time.FixedZone("UTC+3", 3*60*60)
	e := ev("tz", 10, event.KindChange, "c")
	e.Time = e.Time.In(plus3)

	res := Run(context.Background(), win, []Collector{fake{name: "c", events: []event.Event{e}}})

	if len(res.Events) != 1 {
		t.Fatalf("events = %v", res.Events)
	}
	got := res.Events[0]
	if got.Time.Location() != time.UTC || got.Severity != event.SeverityInfo {
		t.Errorf("event not normalised: time %v, severity %q", got.Time, got.Severity)
	}
}
