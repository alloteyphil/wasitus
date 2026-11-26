package event

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Kind says which side of an incident an event belongs to.
type Kind string

const (
	KindChange   Kind = "change"   // something we did: deploy, config or infrastructure change
	KindExternal Kind = "external" // something outside us: provider incident, billing notice
	KindSymptom  Kind = "symptom"  // something went wrong: alert, spike, error group
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindChange, KindExternal, KindSymptom:
		return true
	}
	return false
}

// Severity is how serious an event is.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return true
	}
	return false
}

// Event is the common shape every source is converted into.
type Event struct {
	ID       string            `json:"id"`            // stable within its source, used for de-duplication
	Time     time.Time         `json:"time"`          // always UTC after Normalize
	End      *time.Time        `json:"end,omitempty"` // for events with a duration (alert fired -> resolved)
	Kind     Kind              `json:"kind"`          // change, external or symptom
	Source   string            `json:"source"`        // e.g. "signoz", "github", "changes-file"
	Service  string            `json:"service,omitempty"`
	Title    string            `json:"title"`
	Severity Severity          `json:"severity"`
	Link     string            `json:"link,omitempty"`  // deep link back to the source
	Attrs    map[string]string `json:"attrs,omitempty"` // source-specific detail for later stages
}

// Validate reports every problem with the event at once, so a collector bug is
// easy to diagnose.
func (e Event) Validate() error {
	var errs []error
	if e.ID == "" {
		errs = append(errs, errors.New("missing id"))
	}
	if e.Time.IsZero() {
		errs = append(errs, errors.New("missing time"))
	}
	if !e.Kind.Valid() {
		errs = append(errs, fmt.Errorf("invalid kind %q", e.Kind))
	}
	if e.Source == "" {
		errs = append(errs, errors.New("missing source"))
	}
	if e.Title == "" {
		errs = append(errs, errors.New("missing title"))
	}
	if !e.Severity.Valid() {
		errs = append(errs, fmt.Errorf("invalid severity %q", e.Severity))
	}
	if e.End != nil && e.End.Before(e.Time) {
		errs = append(errs, errors.New("end is before time"))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("event %q: %w", e.ID, errors.Join(errs...))
}

// Normalize returns a copy of e with times in UTC and an empty severity set to
// info. It never modifies e itself.
func (e Event) Normalize() Event {
	e.Time = e.Time.UTC()
	if e.End != nil {
		end := e.End.UTC()
		e.End = &end
	}
	if e.Severity == "" {
		e.Severity = SeverityInfo
	}
	return e
}

// kindOrder puts causes before effects when two events share a timestamp.
var kindOrder = map[Kind]int{KindChange: 0, KindExternal: 1, KindSymptom: 2}

// Sort orders events by time. Ties are broken by kind (changes first), then
// source, then ID, so the output is always the same for the same input.
func Sort(events []Event) {
	slices.SortStableFunc(events, func(a, b Event) int {
		if c := a.Time.Compare(b.Time); c != 0 {
			return c
		}
		if c := cmp.Compare(kindOrder[a.Kind], kindOrder[b.Kind]); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Source, b.Source); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// Dedupe returns the events with repeated (source, ID) pairs removed, keeping
// the first occurrence. The input slice is not modified.
func Dedupe(events []Event) []Event {
	type key struct{ source, id string }
	seen := make(map[key]struct{}, len(events))
	out := make([]Event, 0, len(events))
	for _, e := range events {
		k := key{e.Source, e.ID}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, e)
	}
	return out
}
