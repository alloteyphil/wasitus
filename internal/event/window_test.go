package event

import (
	"strings"
	"testing"
	"time"
)

func TestNewWindow(t *testing.T) {
	plus3 := time.FixedZone("UTC+3", 3*60*60)
	from := time.Date(2026, 11, 14, 6, 0, 0, 0, plus3)
	to := from.Add(time.Hour)

	w, err := NewWindow(from, to)
	if err != nil {
		t.Fatalf("NewWindow() error = %v", err)
	}
	if w.From.Location() != time.UTC || !w.From.Equal(from) || !w.To.Equal(to) {
		t.Errorf("NewWindow() = %+v, want same instants in UTC", w)
	}

	errorCases := []struct {
		name     string
		from, to time.Time
		want     string
	}{
		{"empty range", from, from, "not after start"},
		{"reversed", to, from, "not after start"},
		{"missing start", time.Time{}, to, "needs both"},
		{"missing end", from, time.Time{}, "needs both"},
	}
	for _, tt := range errorCases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWindow(tt.from, tt.to)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewWindow() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestWindowContainsAndLookback(t *testing.T) {
	from := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	w, err := NewWindow(from, from.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		at   time.Time
		want bool
	}{
		{from, true},
		{from.Add(time.Hour), true},
		{from.Add(-time.Second), false},
		{from.Add(time.Hour + time.Second), false},
	}
	for _, c := range checks {
		if got := w.Contains(c.at); got != c.want {
			t.Errorf("Contains(%s) = %v, want %v", c.at.Format(time.RFC3339), got, c.want)
		}
	}

	wider := w.WithLookback(time.Hour)
	if !wider.Contains(from.Add(-time.Hour)) {
		t.Error("window with 1h lookback does not contain from-1h")
	}
	if !w.From.Equal(from) {
		t.Error("WithLookback modified the original window")
	}
}
