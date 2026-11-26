package correlate

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

// Symptom is the first sign of trouble that changes are ranked against.
type Symptom struct {
	Start   time.Time // when the symptom was first seen
	Service string    // the failing service; empty when unknown
}

// Candidate is a change that happened before a symptom, with its score and
// the reasons for it. The three factor fields are unweighted, each in [0, 1],
// so a reader can check the score by hand:
//
//	Score = w.TimeProximity*TimeProximity + w.SameService*SameService + w.ChangeType*ChangeType
type Candidate struct {
	Change        event.Event
	Score         float64       // weighted sum of the three factors, in [0, 1] for valid weights
	TimeProximity float64       // exp(-Before/Tau): 1 at the symptom, 1/e one Tau earlier
	SameService   float64       // 1 if the change is in the failing service, else 0
	ChangeType    float64       // weight of the change's type; UnknownTypeWeight if missing or unknown
	Before        time.Duration // how long before the symptom the change happened
	Reasons       []string      // one readable line per factor: time, service, type
}

// Rank scores every change that happened at or before s.Start and no more
// than w.Lookback earlier, and returns them best first. Events of other kinds
// are ignored. It returns nil when no change qualifies.
//
// The order is fully deterministic: by score (highest first), then by Before
// (closest to the symptom first), then by change ID, then by source.
//
// Rank does not validate w; call Weights.Validate first. With invalid weights
// the scores are meaningless, but Rank still never panics.
func Rank(s Symptom, events []event.Event, w Weights) []Candidate {
	var out []Candidate
	for _, e := range events {
		if e.Kind != event.KindChange {
			continue
		}
		before := s.Start.Sub(e.Time) // saturates instead of overflowing for far-apart times
		if before < 0 || before > w.Lookback {
			continue
		}
		out = append(out, score(s, e, before, w))
	}
	slices.SortStableFunc(out, compareCandidates)
	return out
}

func compareCandidates(a, b Candidate) int {
	if c := cmp.Compare(b.Score, a.Score); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Before, b.Before); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Change.ID, b.Change.ID); c != 0 {
		return c
	}
	return cmp.Compare(a.Change.Source, b.Change.Source)
}

func score(s Symptom, e event.Event, before time.Duration, w Weights) Candidate {
	c := Candidate{Change: e, Before: before}

	c.TimeProximity = math.Exp(-float64(before) / float64(w.Tau))
	c.Reasons = append(c.Reasons, timeReason(before))

	var reason string
	c.SameService, reason = serviceMatch(s.Service, e.Service)
	c.Reasons = append(c.Reasons, reason)

	typ := normalizeType(e.Attrs[AttrChangeType])
	weight, listed := w.typeWeight(typ)
	c.ChangeType = weight
	c.Reasons = append(c.Reasons, typeReason(typ, weight, listed))

	sum := w.TimeProximity*c.TimeProximity + w.SameService*c.SameService + w.ChangeType*c.ChangeType
	// Valid top-level weights may sum to 1 +/- 1e-9, so clamp to keep the
	// documented [0, 1] range exact.
	c.Score = min(max(sum, 0), 1)
	return c
}

// serviceMatch compares service names case-insensitively. A missing name on
// either side never matches: an unknown service is not evidence.
func serviceMatch(symptom, change string) (float64, string) {
	symptom, change = strings.TrimSpace(symptom), strings.TrimSpace(change)
	switch {
	case change == "":
		return 0, "no service on the change"
	case symptom == "":
		return 0, "no service on the symptom"
	case strings.EqualFold(symptom, change):
		return 1, "same service: " + change
	default:
		return 0, fmt.Sprintf("different service: %s (symptom in %s)", change, symptom)
	}
}

func timeReason(before time.Duration) string {
	if before.Round(time.Second) == 0 {
		return "at the same time as the first symptom"
	}
	return formatDuration(before) + " before the first symptom"
}

func typeReason(typ string, weight float64, listed bool) string {
	switch {
	case typ == "":
		return fmt.Sprintf("type not set (weight %s)", formatWeight(weight))
	case !listed:
		return fmt.Sprintf("unknown type %q (weight %s)", typ, formatWeight(weight))
	default:
		return fmt.Sprintf("type %s (weight %s)", typ, formatWeight(weight))
	}
}

// formatDuration prints a duration rounded to the second without zero parts:
// 17m, 45s, 1h, 1h5m, 17m30s. time.Duration.String would give 17m0s.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d == 0 {
		return "0s"
	}
	var b strings.Builder
	if h := d / time.Hour; h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m := d % time.Hour / time.Minute; m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if s := d % time.Minute / time.Second; s > 0 {
		fmt.Fprintf(&b, "%ds", s)
	}
	return b.String()
}

// formatWeight prints a weight with at least one decimal place, so 1 reads as
// 1.0 next to 0.8, while 0.85 keeps both digits.
func formatWeight(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") && !math.IsNaN(v) && !math.IsInf(v, 0) {
		s += ".0"
	}
	return s
}
