package traffic

import "testing"

// TestScenarios runs the traffic side of the staged scenarios in
// docs/SCENARIOS.md the way the verdict stage will: traffic is the cause only
// when requests spiked and the error mix stayed the same.
func TestScenarios(t *testing.T) {
	cfg := DefaultConfig()
	week := pastDays(at10, 7, hourly)
	usualErrors := map[string]int{"dependency": 12, "code": 3} // background mockpay hiccups and bugs

	tests := []struct {
		name         string
		window       []Bucket
		windowErrors map[string]int
		wantSpike    bool
		wantStable   bool
	}{
		{
			name:         "S6 traffic surge: x10, no faults",
			window:       series(at10, 12, bucket, times(hourly, 10)),
			windowErrors: map[string]int{"dependency": 120, "code": 30},
			wantSpike:    true,
			wantStable:   true,
		},
		{
			// x3 is exactly the default spike factor (3.0 >= 3.0), so any
			// noise in the lab's load can flip this; tune it in M3.
			name:         "S4 rate limited: x3 and mockpay answers 429",
			window:       series(at10, 12, bucket, times(hourly, 3)),
			windowErrors: map[string]int{"dependency": 12, "code": 3, "limit": 400},
			wantSpike:    true,
			wantStable:   false,
		},
		{
			name:         "normal day",
			window:       series(at10, 12, bucket, hourly),
			windowErrors: map[string]int{"dependency": 11, "code": 4},
			wantSpike:    false,
			wantStable:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Analyze(tt.window, week, cfg)
			stable := ErrorMixStable(usualErrors, tt.windowErrors, cfg.MixTolerance)

			if res.IsSpike != tt.wantSpike {
				t.Errorf("IsSpike = %v (%s), want %v", res.IsSpike, res.Reason, tt.wantSpike)
			}
			if stable != tt.wantStable {
				t.Errorf("ErrorMixStable = %v, want %v", stable, tt.wantStable)
			}
			if got, want := res.IsSpike && stable, tt.wantSpike && tt.wantStable; got != want {
				t.Errorf("traffic explains it = %v, want %v", got, want)
			}
		})
	}
}
