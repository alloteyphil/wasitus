package event

import (
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	plus3 := time.FixedZone("UTC+3", 3*60*60)
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	at := func(h, m, s int) time.Time { return time.Date(2026, 11, 14, h, m, s, 0, time.UTC) }

	tests := []struct {
		in   string
		want time.Time
	}{
		{"now", now},
		{"-90m", now.Add(-90 * time.Minute)},
		{"-2h", now.Add(-2 * time.Hour)},
		{"2026-11-14T02:47:00Z", at(2, 47, 0)},
		{"2026-11-14T05:47:00+03:00", at(2, 47, 0)},
		{"2026-11-14T02:47:00.5Z", at(2, 47, 0).Add(500 * time.Millisecond)},
		{"2026-11-14 05:47", at(2, 47, 0)},
		{"2026-11-14 05:47:30", at(2, 47, 30)},
		{"2026-11-14T05:47", at(2, 47, 0)},
		{"  2026-11-14 05:47  ", at(2, 47, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTime(tt.in, plus3, now)
			if err != nil {
				t.Fatalf("ParseTime(%q) error = %v", tt.in, err)
			}
			if !got.Equal(tt.want) || got.Location() != time.UTC {
				t.Errorf("ParseTime(%q) = %v, want %v (UTC)", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseTimeErrors(t *testing.T) {
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	for _, in := range []string{"", "yesterday", "-abc", "+2h", "2h", "2026-13-01 00:00", "14/11/2026 02:47"} {
		t.Run(in, func(t *testing.T) {
			if got, err := ParseTime(in, time.UTC, now); err == nil {
				t.Errorf("ParseTime(%q) = %v, want an error", in, got)
			}
		})
	}
}

func TestParseAbsoluteTimeRejectsRelative(t *testing.T) {
	for _, in := range []string{"now", "-2h"} {
		if got, err := ParseAbsoluteTime(in, time.UTC); err == nil {
			t.Errorf("ParseAbsoluteTime(%q) = %v, want an error", in, got)
		}
	}
}
