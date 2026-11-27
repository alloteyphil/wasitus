package traffic

import (
	"math"
	"testing"
)

func TestErrorMixStable(t *testing.T) {
	usual := map[string]int{"dependency": 40, "code": 10} // mockpay hiccups and a few bugs

	tests := []struct {
		name             string
		baseline, window map[string]int
		tolerance        float64
		want             bool
	}{
		{"same mix, ten times the errors (S6)", usual, map[string]int{"dependency": 400, "code": 100}, 0.15, true},
		{"429s appear (S4)", usual, map[string]int{"dependency": 40, "code": 10, "limit": 450}, 0.15, false},
		{"small drift", map[string]int{"code": 50, "dependency": 50}, map[string]int{"code": 60, "dependency": 40}, 0.15, true},
		{"change of exactly the tolerance", map[string]int{"code": 50, "dependency": 50}, map[string]int{"code": 65, "dependency": 35}, 0.15, true},
		{"change just over the tolerance", map[string]int{"code": 50, "dependency": 50}, map[string]int{"code": 66, "dependency": 34}, 0.15, false},
		{"a class disappears", map[string]int{"code": 50, "dependency": 50}, map[string]int{"code": 100}, 0.15, false},
		{"a small new class within tolerance", map[string]int{"code": 100}, map[string]int{"code": 90, "expiry": 10}, 0.15, true},
		{"zero tolerance, same mix", usual, map[string]int{"dependency": 4, "code": 1}, 0, true},
		{"zero tolerance, any change", usual, map[string]int{"dependency": 41, "code": 10}, 0, false},
		{"tolerance of 1 accepts any two mixes", map[string]int{"code": 1}, map[string]int{"limit": 1}, 1, true},

		{"no errors at all", nil, nil, 0.15, true},
		{"empty maps", map[string]int{}, map[string]int{}, 0.15, true},
		{"errors stop in the window", usual, nil, 0.15, true},
		{"errors appear in the window", nil, map[string]int{"account": 3}, 0.15, false},
		{"zero and negative counts are ignored", map[string]int{"code": 10, "limit": 0}, map[string]int{"code": 5, "expiry": -3}, 0.15, true},
		{"only zero counts in the window", usual, map[string]int{"code": 0}, 0.15, true},
		{"only zero counts in the baseline", map[string]int{"code": 0}, map[string]int{"code": 3}, 0.15, false},

		{"negative tolerance", usual, usual, -0.1, false},
		{"NaN tolerance", usual, usual, math.NaN(), false},
		{"negative tolerance with no errors", nil, nil, -0.1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ErrorMixStable(tt.baseline, tt.window, tt.tolerance); got != tt.want {
				t.Errorf("ErrorMixStable(%v, %v, %v) = %v, want %v",
					tt.baseline, tt.window, tt.tolerance, got, tt.want)
			}
		})
	}
}
