package traffic

import "math"

// mixEpsilon absorbs float rounding, so that a share change of exactly the
// tolerance (0.5 to 0.35 with a tolerance of 0.15) counts as within it.
const mixEpsilon = 1e-9

// ErrorMixStable reports whether window has the same kinds of error as
// baseline: for every class in either map, its share of all errors changes by
// at most tolerance. Counts are errors per class. Classes are plain strings
// (the fingerprint cause classes, such as "code" or "limit") so that this
// package does not depend on the fingerprint package.
//
// Only the mix matters, not the volume: ten times the errors in the same
// proportions is stable. Counts of zero or less are ignored. Empty inputs:
//
//   - No errors in window (whatever baseline has): true. Nothing new appeared,
//     so the errors cannot point at a cause other than the traffic.
//   - Errors in window but none in baseline: false. Every class is new.
//
// A negative or NaN tolerance is invalid and gives false, even for empty
// inputs. A tolerance of 1 or more accepts any two mixes that both have errors.
func ErrorMixStable(baseline, window map[string]int, tolerance float64) bool {
	if math.IsNaN(tolerance) || tolerance < 0 {
		return false
	}
	baseTotal, windowTotal := total(baseline), total(window)
	if windowTotal == 0 {
		return true
	}
	if baseTotal == 0 {
		return false
	}
	for _, counts := range []map[string]int{baseline, window} {
		for class := range counts {
			change := math.Abs(share(baseline[class], baseTotal) - share(window[class], windowTotal))
			if change > tolerance+mixEpsilon {
				return false
			}
		}
	}
	return true
}

// total adds up the positive counts in m.
func total(m map[string]int) int {
	t := 0
	for _, n := range m {
		if n > 0 {
			t = addSat(t, n)
		}
	}
	return t
}

// share is n's share of total, treating counts of zero or less as none.
func share(n, total int) float64 {
	if n <= 0 {
		return 0
	}
	return rate(n, total)
}
