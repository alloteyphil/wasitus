package event

import (
	"slices"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 14, 2, 47, 0, 0, time.UTC)

func validEvent() Event {
	return Event{
		ID:       "1",
		Time:     t0,
		Kind:     KindChange,
		Source:   "github",
		Title:    "Deploy orders-service",
		Severity: SeverityInfo,
	}
}

func TestValidate(t *testing.T) {
	before := t0.Add(-time.Minute)
	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr string
	}{
		{"valid", func(*Event) {}, ""},
		{"missing id", func(e *Event) { e.ID = "" }, "missing id"},
		{"zero time", func(e *Event) { e.Time = time.Time{} }, "missing time"},
		{"unknown kind", func(e *Event) { e.Kind = "deploy" }, `invalid kind "deploy"`},
		{"missing source", func(e *Event) { e.Source = "" }, "missing source"},
		{"missing title", func(e *Event) { e.Title = "" }, "missing title"},
		{"unknown severity", func(e *Event) { e.Severity = "urgent" }, `invalid severity "urgent"`},
		{"end before start", func(e *Event) { e.End = &before }, "end is before time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEvent()
			tt.mutate(&e)
			err := e.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	err := Event{}.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for an empty event")
	}
	for _, want := range []string{"missing id", "missing time", "invalid kind", "missing source", "missing title", "invalid severity"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	plus3 := time.FixedZone("UTC+3", 3*60*60)
	start := time.Date(2026, 11, 14, 5, 47, 0, 0, plus3)
	end := start.Add(30 * time.Minute)
	e := Event{Time: start, End: &end}

	n := e.Normalize()

	if n.Time.Location() != time.UTC || !n.Time.Equal(start) {
		t.Errorf("Time = %v, want %v in UTC", n.Time, start)
	}
	if n.End.Location() != time.UTC || !n.End.Equal(end) {
		t.Errorf("End = %v, want %v in UTC", n.End, end)
	}
	if n.Severity != SeverityInfo {
		t.Errorf("Severity = %q, want default %q", n.Severity, SeverityInfo)
	}
	if e.End.Location() != plus3 {
		t.Error("Normalize modified the original event's End")
	}
}

func TestSort(t *testing.T) {
	events := []Event{
		{ID: "alert", Time: t0, Kind: KindSymptom, Source: "signoz"},
		{ID: "b", Time: t0, Kind: KindChange, Source: "github"},
		{ID: "status", Time: t0, Kind: KindExternal, Source: "changes-file"},
		{ID: "a", Time: t0, Kind: KindChange, Source: "github"},
		{ID: "pr", Time: t0.Add(-time.Minute), Kind: KindChange, Source: "github"},
		{ID: "note", Time: t0, Kind: KindChange, Source: "changes-file"},
	}

	Sort(events)

	var got []string
	for _, e := range events {
		got = append(got, e.ID)
	}
	want := []string{"pr", "note", "a", "b", "status", "alert"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestDedupe(t *testing.T) {
	events := []Event{
		{ID: "1", Source: "github", Title: "first"},
		{ID: "1", Source: "signoz", Title: "same id, other source"},
		{ID: "1", Source: "github", Title: "duplicate"},
		{ID: "2", Source: "github", Title: "second"},
	}

	got := Dedupe(events)

	var titles []string
	for _, e := range got {
		titles = append(titles, e.Title)
	}
	want := []string{"first", "same id, other source", "second"}
	if !slices.Equal(titles, want) {
		t.Errorf("titles = %v, want %v", titles, want)
	}
	if len(events) != 4 || events[2].Title != "duplicate" {
		t.Error("Dedupe modified its input")
	}
}
