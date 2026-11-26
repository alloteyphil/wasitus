package traffic

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

const bucket = 5 * time.Minute

// at10 is a fixed window start, Tuesday 15 Dec 2026 at 10:00 UTC. Tests never
// read the clock.
var at10 = time.Date(2026, 12, 15, 10, 0, 0, 0, time.UTC)

// counts gives the requests and errors of the bucket that starts at t.
type counts func(t time.Time) (requests, errors int)

// flat is the same load at every time of day.
func flat(requests, errors int) counts {
	return func(time.Time) (int, int) { return requests, errors }
}

// hourly is the lab's made-up normal load for orders-service: it rises through
// the day, so a baseline taken at the wrong time of day gives a wrong answer.
// One request in 50 fails.
func hourly(t time.Time) (int, int) {
	r := 50 + 10*t.Hour()
	return r, r / 50
}

// times multiplies c's load by k.
func times(c counts, k int) counts {
	return func(t time.Time) (int, int) {
		r, e := c(t)
		return r * k, e * k
	}
}

// series returns n consecutive buckets of the given size from start.
func series(start time.Time, n int, size time.Duration, c counts) []Bucket {
	out := make([]Bucket, n)
	for i := range out {
		t := start.Add(time.Duration(i) * size)
		r, e := c(t)
		out[i] = Bucket{Start: t, Requests: r, Errors: e}
	}
	return out
}

// pastDays returns days whole days of 5-minute buckets that end at end.
func pastDays(end time.Time, days int, c counts) []Bucket {
	return series(end.Add(-time.Duration(days)*day), days*int(day/bucket), bucket, c)
}

// without returns bs minus the buckets whose start drop matches.
func without(bs []Bucket, drop func(time.Time) bool) []Bucket {
	return slices.DeleteFunc(slices.Clone(bs), func(b Bucket) bool { return drop(b.Start) })
}

// onDates matches times on the given December 2026 dates (UTC).
func onDates(dates ...int) func(time.Time) bool {
	return func(t time.Time) bool { return slices.Contains(dates, t.UTC().Day()) }
}

// between matches times of day (UTC) in [from, to), given as minutes past midnight.
func between(from, to int) func(time.Time) bool {
	return func(t time.Time) bool {
		m := t.UTC().Hour()*60 + t.UTC().Minute()
		return m >= from && m < to
	}
}

func and(a, b func(time.Time) bool) func(time.Time) bool {
	return func(t time.Time) bool { return a(t) && b(t) }
}

func or(a, b func(time.Time) bool) func(time.Time) bool {
	return func(t time.Time) bool { return a(t) || b(t) }
}

func TestAnalyze(t *testing.T) {
	week := pastDays(at10, 7, flat(100, 2)) // 100 requests per 5 minutes, every day
	hour := func(c counts) []Bucket { return series(at10, 12, bucket, c) }
	plus3 := time.FixedZone("UTC+3", 3*60*60)

	// A window from 23:30 to 00:30 UTC, and a week of hourly load before it.
	lateStart := time.Date(2026, 12, 15, 23, 30, 0, 0, time.UTC)
	lateWeek := pastDays(lateStart, 7, hourly)

	// A two-day window, with history that also covers the window itself.
	twoDays := series(at10, 2*int(day/bucket), bucket, flat(1000, 20))

	peakAt1025 := func(t time.Time) (int, int) {
		if t.Equal(at10.Add(25 * time.Minute)) {
			return 1300, 26
		}
		return 1000, 20
	}

	tests := []struct {
		name            string
		window, history []Bucket
		cfg             func(*Config) // changes to DefaultConfig; nil for none
		spike           bool
		factor          float64
		windowRequests  int
		expected        int
		windowBuckets   int
		baselineBuckets int
		peak            time.Time
		reason          string
	}{
		{
			name: "S6 traffic x10", window: hour(peakAt1025), history: week,
			spike: true, factor: 10.25, windowRequests: 12300, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10.Add(25 * time.Minute),
			reason: "requests 10.25x the 7-day median for this time of day (12300 vs 1200 expected)",
		},
		{
			name: "S4 traffic x3 is exactly the spike factor", window: hour(flat(300, 90)), history: week,
			spike: true, factor: 3, windowRequests: 3600, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 3x the 7-day median for this time of day (3600 vs 1200 expected)",
		},
		{
			name: "normal day", window: hour(flat(100, 2)), history: week,
			factor: 1, windowRequests: 1200, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 1x the 7-day median for this time of day (1200 vs 1200 expected), below the 3x spike factor",
		},
		{
			name: "just below the spike factor", window: hour(flat(299, 2)), history: week,
			factor: 2.99, windowRequests: 3588, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 2.99x the 7-day median for this time of day (3588 vs 1200 expected), below the 3x spike factor",
		},
		{
			name: "spike factor from config", window: hour(flat(300, 2)), history: week,
			cfg:    func(c *Config) { c.SpikeFactor = 5 },
			factor: 3, windowRequests: 3600, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 3x the 7-day median for this time of day (3600 vs 1200 expected), below the 5x spike factor",
		},
		{
			// Three whole days missing and a 30-minute hole on the day before
			// still leave 4 of 7 days at every time of day.
			name:   "history with gaps",
			window: hour(flat(1000, 20)),
			history: without(week, or(onDates(9, 11, 13),
				and(onDates(14), between(10*60, 10*60+30)))),
			spike: true, factor: 10, windowRequests: 12000, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 10x the 7-day median for this time of day (12000 vs 1200 expected)",
		},
		{
			name: "two days of history is too little", window: hour(flat(1000, 20)),
			history:        pastDays(at10, 2, flat(100, 2)),
			windowRequests: 12000, windowBuckets: 12, peak: at10,
			reason: "too little history: no window bucket has data at this time of day on at least 3 of the previous 7 days",
		},
		{
			name: "one day of history is enough when the config says so", window: hour(flat(1000, 20)),
			history: pastDays(at10, 1, flat(100, 2)),
			cfg:     func(c *Config) { c.MinBaselineSamples = 1 },
			spike:   true, factor: 10, windowRequests: 12000, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 10x the 7-day median for this time of day (12000 vs 1200 expected)",
		},
		{
			// With 12-hour buckets, history on two days (36 and 84 hours
			// before) would reach three days: each day's neighbouring buckets
			// overlap the next day's.
			name:   "two days of history with half-day buckets",
			window: hour(flat(1000, 20)),
			history: []Bucket{
				{Start: at10.Add(-36 * time.Hour), Requests: 100},
				{Start: at10.Add(-84 * time.Hour), Requests: 100},
			},
			cfg:    func(c *Config) { c.BucketSize = 12 * time.Hour },
			reason: "invalid config: bucket size must be at most 1h0m0s, got 12h0m0s",
		},
		{
			name:    "one day of history with one-day buckets",
			window:  []Bucket{{Start: at10, Requests: 1000}},
			history: []Bucket{{Start: at10.Add(-day), Requests: 100}},
			cfg:     func(c *Config) { c.BucketSize, c.MinBaselineSamples = day, 2 },
			reason:  "invalid config: bucket size must be at most 1h0m0s, got 24h0m0s",
		},
		{
			// The widest buckets allowed still count each day once.
			name:    "two days of history with one-hour buckets is too little",
			window:  series(at10, 1, time.Hour, flat(12000, 240)),
			history: series(at10.Add(-2*day), 48, time.Hour, flat(1200, 24)),
			cfg:     func(c *Config) { c.BucketSize = time.Hour },

			windowRequests: 12000, windowBuckets: 1, peak: at10,
			reason: "too little history: no window bucket has data at this time of day on at least 3 of the previous 7 days",
		},
		{
			name:    "three days of history with one-hour buckets is enough",
			window:  series(at10, 1, time.Hour, flat(12000, 240)),
			history: series(at10.Add(-3*day), 72, time.Hour, flat(1200, 24)),
			cfg:     func(c *Config) { c.BucketSize = time.Hour },
			spike:   true, factor: 10, windowRequests: 12000, expected: 1200,
			windowBuckets: 1, baselineBuckets: 1, peak: at10,
			reason: "requests 10x the 7-day median for this time of day (12000 vs 1200 expected)",
		},
		{
			// 10:30 to 11:00 is missing on 8 to 12 Dec, leaving 2 days. The
			// buckets at 10:30 and 10:55 still have a neighbour (10:25, 11:00)
			// on every day; 10:35 to 10:50 do not.
			name:   "part of the window has too little history",
			window: hour(flat(1000, 20)),
			history: without(week, and(onDates(8, 9, 10, 11, 12),
				between(10*60+30, 11*60))),
			windowRequests: 12000, windowBuckets: 12, baselineBuckets: 8, peak: at10,
			reason: "too little history: only 8 of 12 window buckets have data at this time of day on at least 3 of the previous 7 days",
		},
		{
			name: "no history", window: hour(flat(1000, 20)),
			windowRequests: 12000, windowBuckets: 12, peak: at10,
			reason: "no history before the window; a baseline needs data at this time of day on at least 3 of the previous 7 days",
		},
		{
			name: "history only after the window", window: hour(flat(1000, 20)),
			history:        pastDays(at10.Add(8*day), 7, flat(100, 2)),
			windowRequests: 12000, windowBuckets: 12, peak: at10,
			reason: "no history before the window; a baseline needs data at this time of day on at least 3 of the previous 7 days",
		},
		{
			name: "empty window", history: week,
			reason: "no window buckets to analyse",
		},
		{
			name: "zero baseline and a surge", window: hour(flat(50, 0)), history: pastDays(at10, 7, flat(0, 0)),
			spike: true, factor: 600, windowRequests: 600, expected: 0,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 600x the 7-day median for this time of day (600 vs 0 expected; an expected count under 1 is treated as 1)",
		},
		{
			name: "zero baseline and a few requests", window: series(at10, 2, bucket, flat(1, 0)),
			history: pastDays(at10, 7, flat(0, 0)),
			factor:  2, windowRequests: 2, windowBuckets: 2, baselineBuckets: 2, peak: at10,
			reason: "requests 2x the 7-day median for this time of day (2 vs 0 expected; an expected count under 1 is treated as 1), below the 3x spike factor",
		},
		{
			name: "zero baseline and no requests", window: hour(flat(0, 0)), history: pastDays(at10, 7, flat(0, 0)),
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 0x the 7-day median for this time of day (0 vs 0 expected; an expected count under 1 is treated as 1), below the 3x spike factor",
		},
		{
			// The window starts 2 minutes after the boundary and is given in
			// UTC+3; the history starts 4 minutes after. Both move back to
			// the 5-minute boundaries in UTC.
			name:    "unaligned buckets in another time zone",
			window:  series(at10.Add(2*time.Minute).In(plus3), 12, bucket, flat(1000, 20)),
			history: series(at10.Add(-7*day+4*time.Minute), 7*int(day/bucket)-1, bucket, flat(100, 2)),
			spike:   true, factor: 10, windowRequests: 12000, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 10x the 7-day median for this time of day (12000 vs 1200 expected)",
		},
		{
			name: "one-minute buckets are added up", window: series(at10, 60, time.Minute, flat(200, 4)), history: week,
			spike: true, factor: 10, windowRequests: 12000, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 10x the 7-day median for this time of day (12000 vs 1200 expected)",
		},
		{
			name:    "duplicate buckets are added up",
			window:  append(hour(flat(500, 10)), hour(flat(500, 10))...),
			history: week,
			spike:   true, factor: 10, windowRequests: 12000, expected: 1200,
			windowBuckets: 12, baselineBuckets: 12, peak: at10,
			reason: "requests 10x the 7-day median for this time of day (12000 vs 1200 expected)",
		},
		{
			// Load is 280 per bucket in the 23:00 hour and 50 in the 00:00
			// hour. 23:55 and 00:00 each have one neighbour across midnight;
			// the median keeps them at their own hour's level.
			name:   "normal load across midnight UTC",
			window: series(lateStart, 12, bucket, hourly), history: lateWeek,
			factor: 1, windowRequests: 1980, expected: 1980,
			windowBuckets: 12, baselineBuckets: 12, peak: lateStart,
			reason: "requests 1x the 7-day median for this time of day (1980 vs 1980 expected), below the 3x spike factor",
		},
		{
			name:   "traffic x10 across midnight UTC",
			window: series(lateStart, 12, bucket, times(hourly, 10)), history: lateWeek,
			spike: true, factor: 10, windowRequests: 19800, expected: 1980,
			windowBuckets: 12, baselineBuckets: 12, peak: lateStart,
			reason: "requests 10x the 7-day median for this time of day (19800 vs 1980 expected)",
		},
		{
			// On the second day, "the day before" is inside the window. Only
			// history from before the window counts, so the surge does not
			// become its own baseline.
			name:    "a long window is never its own baseline",
			window:  twoDays,
			history: append(slices.Clone(week), twoDays...),
			spike:   true, factor: 10, windowRequests: 576000, expected: 57600,
			windowBuckets: 576, baselineBuckets: 576, peak: at10,
			reason: "requests 10x the 7-day median for this time of day (576000 vs 57600 expected)",
		},
		{
			name: "invalid config", window: hour(flat(1000, 20)), history: week,
			cfg:    func(c *Config) { c.BucketSize = 7 * time.Minute },
			reason: "invalid config: bucket size must be positive and divide 24h exactly, got 7m0s",
		},
		{
			name: "every config problem on one line", window: hour(flat(1000, 20)), history: week,
			cfg: func(c *Config) { *c = Config{BaselineDays: 7, MinBaselineSamples: 3} },
			reason: "invalid config: bucket size must be positive and divide 24h exactly, got 0s; " +
				"spike factor must be a finite number greater than 1, got 0",
		},
		{
			name:    "negative count in the window",
			window:  []Bucket{{Start: at10, Requests: -1}},
			history: week,
			reason:  "window: bucket at 2026-12-15T10:00:00Z has a negative count (requests -1, errors 0)",
		},
		{
			name:    "negative count in the history",
			window:  hour(flat(1000, 20)),
			history: []Bucket{{Start: at10.Add(-day), Requests: 5, Errors: -1}},
			reason:  "history: bucket at 2026-12-14T10:00:00Z has a negative count (requests 5, errors -1)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			got := Analyze(tt.window, tt.history, cfg)

			if got.Reason != tt.reason {
				t.Errorf("Reason = %q\n                  want %q", got.Reason, tt.reason)
			}
			if got.IsSpike != tt.spike {
				t.Errorf("IsSpike = %v, want %v", got.IsSpike, tt.spike)
			}
			if math.Abs(got.Factor-tt.factor) > 1e-9 {
				t.Errorf("Factor = %v, want %v", got.Factor, tt.factor)
			}
			if got.WindowRequests != tt.windowRequests || got.ExpectedRequests != tt.expected {
				t.Errorf("requests = %d vs %d expected, want %d vs %d",
					got.WindowRequests, got.ExpectedRequests, tt.windowRequests, tt.expected)
			}
			if got.WindowBuckets != tt.windowBuckets || got.BaselineBuckets != tt.baselineBuckets {
				t.Errorf("buckets = %d with %d baselines, want %d with %d",
					got.WindowBuckets, got.BaselineBuckets, tt.windowBuckets, tt.baselineBuckets)
			}
			if !got.PeakBucket.Equal(tt.peak) {
				t.Errorf("PeakBucket = %v, want %v", got.PeakBucket, tt.peak)
			}
			if !got.PeakBucket.IsZero() && got.PeakBucket.Location() != time.UTC {
				t.Errorf("PeakBucket location = %v, want UTC", got.PeakBucket.Location())
			}
		})
	}
}

func TestAnalyzeErrorRates(t *testing.T) {
	tests := []struct {
		name               string
		window, history    []Bucket
		wantWindow, wantBL float64
	}{
		{
			name:       "rates from the window and the baseline's history",
			window:     series(at10, 12, bucket, flat(300, 90)),
			history:    pastDays(at10, 7, flat(100, 2)),
			wantWindow: 0.3, wantBL: 0.02,
		},
		{
			// Only the buckets the baseline used count: the 22:00 to 23:00
			// history with its many errors is far from 10:00.
			name:   "history the baseline did not use is left out",
			window: series(at10, 12, bucket, flat(100, 2)),
			history: slices.Concat(pastDays(at10, 7, flat(100, 2)),
				series(at10.Add(-day+12*time.Hour), 12, bucket, flat(100, 100))),
			wantWindow: 0.02, wantBL: 0.02,
		},
		{
			name:    "no requests gives rates of zero",
			window:  series(at10, 12, bucket, flat(0, 0)),
			history: pastDays(at10, 7, flat(0, 0)),
		},
		{
			name:       "no baseline gives a baseline rate of zero",
			window:     series(at10, 12, bucket, flat(100, 5)),
			wantWindow: 0.05,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Analyze(tt.window, tt.history, DefaultConfig())
			if math.Abs(got.WindowErrorRate-tt.wantWindow) > 1e-9 ||
				math.Abs(got.BaselineErrorRate-tt.wantBL) > 1e-9 {
				t.Errorf("error rates = %v window, %v baseline; want %v, %v",
					got.WindowErrorRate, got.BaselineErrorRate, tt.wantWindow, tt.wantBL)
			}
		})
	}
}

func TestAnalyzeDoesNotChangeItsInput(t *testing.T) {
	window := series(at10.Add(2*time.Minute), 12, bucket, flat(1000, 20))
	history := pastDays(at10, 7, flat(100, 2))
	wantWindow, wantHistory := slices.Clone(window), slices.Clone(history)

	Analyze(window, history, DefaultConfig())

	if !slices.Equal(window, wantWindow) || !slices.Equal(history, wantHistory) {
		t.Error("Analyze changed its input buckets")
	}
}

func TestMedian(t *testing.T) {
	tests := []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{7}, 7},
		{[]float64{9, 1, 5}, 5},
		{[]float64{4, 1, 3, 2}, 2.5},
		{[]float64{280, 50, 280}, 280},
	}
	for _, tt := range tests {
		if got := median(slices.Clone(tt.in)); got != tt.want {
			t.Errorf("median(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSaturatingArithmetic(t *testing.T) {
	if got := addSat(math.MaxInt-1, 5); got != math.MaxInt {
		t.Errorf("addSat(MaxInt-1, 5) = %d, want MaxInt", got)
	}
	if got := addSat(2, 3); got != 5 {
		t.Errorf("addSat(2, 3) = %d, want 5", got)
	}
	if got := roundSat(1e30); got != math.MaxInt {
		t.Errorf("roundSat(1e30) = %d, want MaxInt", got)
	}
	if got := roundSat(2.5); got != 3 {
		t.Errorf("roundSat(2.5) = %d, want 3", got)
	}
}

// FuzzAnalyze checks the promises Analyze makes for any input: it does not
// panic, its numbers are finite and never negative, its Reason is one
// non-empty line, and it only calls a spike that its own numbers support.
func FuzzAnalyze(f *testing.F) {
	f.Add(100, 1000, 20, int64(0), 7, 3)
	f.Add(0, 0, 0, int64(120), 1, 1)
	f.Add(0, 500, 0, int64(-7), 3, 3)
	f.Add(-1, 5, 2, int64(1), 7, 3)
	f.Add(100, 5, -2, int64(1), 7, 3)
	f.Add(math.MaxInt, math.MaxInt, math.MaxInt, int64(0), 7, 3)
	f.Add(100, 1000, 20, int64(math.MaxInt64), 0, 0)
	f.Fuzz(func(t *testing.T, normal, now, errs int, offsetSeconds int64, days, minDays int) {
		cfg := DefaultConfig()
		cfg.BaselineDays, cfg.MinBaselineSamples = days, minDays
		window := series(at10.Add(time.Duration(offsetSeconds)*time.Second), 6, bucket, flat(now, errs))
		history := pastDays(at10, 3, flat(normal, errs))

		res := Analyze(window, history, cfg)

		for name, v := range map[string]float64{
			"Factor":            res.Factor,
			"WindowErrorRate":   res.WindowErrorRate,
			"BaselineErrorRate": res.BaselineErrorRate,
		} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				t.Errorf("%s = %v, want a finite number >= 0", name, v)
			}
		}
		if res.WindowRequests < 0 || res.ExpectedRequests < 0 {
			t.Errorf("requests = %d vs %d expected, want both >= 0", res.WindowRequests, res.ExpectedRequests)
		}
		if res.Reason == "" || strings.Contains(res.Reason, "\n") {
			t.Errorf("Reason = %q, want one non-empty line", res.Reason)
		}
		if res.IsSpike && (res.Factor < cfg.SpikeFactor || res.BaselineBuckets != res.WindowBuckets) {
			t.Errorf("IsSpike with Factor %v and %d of %d baselines", res.Factor, res.BaselineBuckets, res.WindowBuckets)
		}
	})
}
