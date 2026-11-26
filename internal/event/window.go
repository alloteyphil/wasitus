package event

import (
	"errors"
	"fmt"
	"time"
)

// Window is the time range under investigation. Both ends are inclusive.
type Window struct {
	From time.Time
	To   time.Time
}

// NewWindow returns a UTC window, or an error if the range is empty or reversed.
func NewWindow(from, to time.Time) (Window, error) {
	if from.IsZero() || to.IsZero() {
		return Window{}, errors.New("window needs both a start and an end")
	}
	if !to.After(from) {
		return Window{}, fmt.Errorf("window end %s is not after start %s",
			to.UTC().Format(time.RFC3339), from.UTC().Format(time.RFC3339))
	}
	return Window{From: from.UTC(), To: to.UTC()}, nil
}

// WithLookback returns a copy of w that starts d earlier. The cause of an
// incident usually comes before its first symptom, so collectors always look
// back a little.
func (w Window) WithLookback(d time.Duration) Window {
	w.From = w.From.Add(-d)
	return w
}

// Contains reports whether t falls inside the window.
func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.From) && !t.After(w.To)
}
