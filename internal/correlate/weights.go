package correlate

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// AttrChangeType is the event attribute that holds a change's type, such as
// "deploy" or "config". Collectors set it in lowercase; the vocabulary is the
// set of keys in DefaultWeights().ChangeTypes.
const AttrChangeType = "change_type"

// UnknownTypeWeight is the type weight of a change whose type is missing or
// not listed in Weights.ChangeTypes: neither trusted nor dismissed.
const UnknownTypeWeight = 0.5

// sumTolerance is how far the three top-level weights may sum from exactly 1,
// so values such as 0.1+0.2+0.7 written in a config file still pass.
const sumTolerance = 1e-9

// Weights controls how Rank scores a change. The three top-level weights sum
// to 1, and every factor they multiply is in [0, 1], so a score is in [0, 1]
// too. Call Validate before using weights that did not come from
// DefaultWeights.
type Weights struct {
	TimeProximity float64            // weight of the time factor exp(-Δt/Tau)
	SameService   float64            // weight of "the change is in the failing service"
	ChangeType    float64            // weight of the change type's own weight
	Tau           time.Duration      // decay constant: the time factor is 1/e after Tau
	Lookback      time.Duration      // only changes at most this long before the symptom count
	ChangeTypes   map[string]float64 // lowercase change type -> weight in [0, 1]
}

// DefaultWeights returns the documented weights: 0.5 time, 0.3 service and
// 0.2 type, a 15 minute decay, a 60 minute lookback, and type weights ranked
// from deploy (1.0) down to docs and test (0.1). Each call returns a fresh
// map, so callers may change it.
func DefaultWeights() Weights {
	return Weights{
		TimeProximity: 0.5,
		SameService:   0.3,
		ChangeType:    0.2,
		Tau:           15 * time.Minute,
		Lookback:      60 * time.Minute,
		ChangeTypes: map[string]float64{
			"deploy":  1.0,
			"release": 0.9,
			"config":  0.8,
			"infra":   0.8,
			"merge":   0.4,
			"docs":    0.1,
			"test":    0.1,
		},
	}
}

// Validate reports every problem with w at once: a negative or NaN weight,
// top-level weights that do not sum to 1, a type weight above 1, a type name
// that is not lowercase, or a non-positive Tau or Lookback.
func (w Weights) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	for _, f := range []struct {
		name  string
		value float64
	}{
		{"time proximity", w.TimeProximity},
		{"same service", w.SameService},
		{"change type", w.ChangeType},
	} {
		if math.IsNaN(f.value) || f.value < 0 {
			add("%s weight must be >= 0, got %v", f.name, f.value)
		}
	}
	if sum := w.TimeProximity + w.SameService + w.ChangeType; math.IsNaN(sum) || math.Abs(sum-1) > sumTolerance {
		// %.10g keeps the message readable: 0.4+0.3+0.2 prints as 0.9, not 0.8999999999999999.
		add("time proximity, same service and change type weights must sum to 1, got %.10g", sum)
	}
	if w.Tau <= 0 {
		add("tau must be > 0, got %s", w.Tau)
	}
	if w.Lookback <= 0 {
		add("lookback must be > 0, got %s", w.Lookback)
	}
	for _, name := range slices.Sorted(maps.Keys(w.ChangeTypes)) {
		switch {
		case name == "":
			add("change type name is empty")
		case name != normalizeType(name):
			add("change type %q must be lowercase with no surrounding spaces", name)
		}
		if v := w.ChangeTypes[name]; math.IsNaN(v) || v < 0 || v > 1 {
			add("change type %q weight must be between 0 and 1, got %v", name, v)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("correlation weights: %w", errors.Join(errs...))
}

// typeWeight returns the weight of a normalised change type, and whether the
// type is listed. A missing or unlisted type weighs UnknownTypeWeight.
func (w Weights) typeWeight(changeType string) (float64, bool) {
	if changeType == "" {
		return UnknownTypeWeight, false
	}
	v, ok := w.ChangeTypes[changeType]
	if !ok {
		return UnknownTypeWeight, false
	}
	return v, true
}

// normalizeType trims and lowercases a change type so "Deploy " matches "deploy".
func normalizeType(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
