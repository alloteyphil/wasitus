package event

import (
	"fmt"
	"strings"
	"time"
)

// Layouts accepted without a time zone. They are read in the configured zone.
var localLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
}

// ParseAbsoluteTime parses an RFC 3339 timestamp, or a date and time without a
// zone, which is read in loc. The result is in UTC.
func ParseAbsoluteTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	for _, layout := range localLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised time %q (use RFC 3339 or \"YYYY-MM-DD HH:MM\")", s)
}

// ParseTime accepts everything ParseAbsoluteTime does, plus "now" and offsets
// into the past such as "-90m" or "-2h". The result is in UTC.
func ParseTime(s string, loc *time.Location, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "now":
		return now.UTC(), nil
	case strings.HasPrefix(s, "-"):
		d, err := time.ParseDuration(s)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid offset %q (use e.g. -90m or -2h)", s)
		}
		return now.Add(d).UTC(), nil
	}
	return ParseAbsoluteTime(s, loc)
}
