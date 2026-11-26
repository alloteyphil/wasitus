package traffic

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Result is what Analyze found. Its numbers describe the whole window.
type Result struct {
	// Factor is WindowRequests divided by the expected requests, using the
	// unrounded sum of the baselines with anything under 1 treated as 1, so
	// a zero baseline still gives a finite factor. A quiet window gives a
	// factor below 1. It is 0 unless every window bucket has a baseline.
	Factor float64

	// WindowRequests is the number of requests in the window.
	WindowRequests int

	// ExpectedRequests is the sum of the window buckets' baselines, rounded to
	// a whole number. It is 0 unless every window bucket has a baseline.
	ExpectedRequests int

	// WindowErrorRate is errors divided by requests in the window, and
	// BaselineErrorRate the same over the history buckets the baselines were
	// taken from. Each is 0 when there are no requests. A rate can exceed 1 if
	// the source counts errors that are not requests.
	WindowErrorRate   float64
	BaselineErrorRate float64

	// IsSpike is true when every window bucket has a baseline and Factor is at
	// least Config.SpikeFactor.
	IsSpike bool

	// Reason explains the result in one line, for example "requests 10.2x the
	// 7-day median for this time of day (12240 vs 1200 expected)", or why
	// there was no comparison.
	Reason string

	// PeakBucket is the start of the window bucket with the most requests (the
	// earliest one on a tie). It is zero when the window is empty.
	PeakBucket time.Time

	// WindowBuckets is the number of distinct buckets in the window after
	// alignment, and BaselineBuckets how many of them have a baseline.
	WindowBuckets   int
	BaselineBuckets int
}

// slot is the aligned, summed counts for one bucket.
type slot struct {
	start    time.Time
	requests int
	errors   int
}

// Analyze decides whether the requests in window were far above normal for
// that time of day, judged against history. It never fails: an invalid cfg, a
// negative count, a bucket Start that is zero or out of range (see Bucket) or
// too little history gives IsSpike false and a Reason that says why.
//
// Baseline. Each window bucket's baseline is the median Requests of the
// history buckets at the same time of day, give or take one bucket, on each
// of the previous cfg.BaselineDays days: up to three buckets per day. A
// bucket has a baseline only when at least cfg.MinBaselineSamples of those
// days have history there. Only history that starts before the window is
// used, so a long window or overlapping history never compares the window
// with itself. Days are 24-hour steps in UTC; in places with daylight saving
// time the local time of day moves by an hour twice a year, which the
// one-bucket allowance does not cover.
//
// Expected requests are the sum of the window buckets' baselines, and
// Factor = window requests / expected. A spike needs every window bucket to
// have a baseline: comparing part of a window could call a spike that the
// rest of it would not support.
//
// Missing buckets. A missing bucket means no data, not zero requests. A
// collector must send zero-request buckets for quiet periods it did observe;
// otherwise a quiet time of day has no baseline and Analyze will not call a
// spike there.
//
// Alignment. Every bucket start is converted to UTC and moved back to the
// nearest multiple of cfg.BucketSize counted from midnight UTC, so a bucket
// that starts at 10:02 is counted in the 10:00 bucket. Buckets that land on
// the same start are added together, which also turns finer buckets (say one
// minute) into cfg.BucketSize ones. Input buckets must not be wider than
// cfg.BucketSize: a one-hour bucket would be counted in a single 5-minute
// bucket.
func Analyze(window, history []Bucket, cfg Config) Result {
	if err := cfg.Validate(); err != nil {
		return Result{Reason: "invalid config: " + strings.ReplaceAll(err.Error(), "\n", "; ")}
	}
	win, err := align(window, cfg.BucketSize)
	if err != nil {
		return Result{Reason: "window: " + err.Error()}
	}
	hist, err := align(history, cfg.BucketSize)
	if err != nil {
		return Result{Reason: "history: " + err.Error()}
	}
	if len(win) == 0 {
		return Result{Reason: "no window buckets to analyse"}
	}

	keys := make([]int64, 0, len(win))
	for k := range win {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var res Result
	res.WindowBuckets = len(keys)
	windowErrors, peak := 0, -1
	for _, k := range keys { // sorted, so the earliest bucket wins a tie
		s := win[k]
		if s.requests > peak {
			peak, res.PeakBucket = s.requests, s.start
		}
		res.WindowRequests = addSat(res.WindowRequests, s.requests)
		windowErrors = addSat(windowErrors, s.errors)
	}
	res.WindowErrorRate = rate(windowErrors, res.WindowRequests)

	b := baselines(keys, hist, cfg)
	res.BaselineBuckets = b.covered
	res.BaselineErrorRate = rate(b.errors, b.requests)
	if b.covered < len(keys) {
		res.Reason = tooLittleHistory(b, len(keys), cfg)
		return res
	}

	res.ExpectedRequests = roundSat(b.expected)
	res.Factor = float64(res.WindowRequests) / math.Max(b.expected, 1)
	res.IsSpike = res.Factor >= cfg.SpikeFactor

	res.Reason = fmt.Sprintf("requests %sx the %d-day median for this time of day (%d vs %d expected",
		formatFactor(res.Factor, cfg.SpikeFactor), cfg.BaselineDays, res.WindowRequests, res.ExpectedRequests)
	if b.expected < 1 {
		res.Reason += "; an expected count under 1 is treated as 1"
	}
	res.Reason += ")"
	if !res.IsSpike {
		// formatFactor keeps the factor's text below the spike factor's
		// exact value, so print that value unrounded.
		res.Reason += fmt.Sprintf(", below the %sx spike factor", strconv.FormatFloat(cfg.SpikeFactor, 'f', -1, 64))
	}
	return res
}

// baseline is the outcome of matching the window buckets with history.
type baseline struct {
	expected         float64 // sum of the covered window buckets' medians
	covered          int     // window buckets with a baseline
	historyBefore    int     // history buckets that start before the window
	requests, errors int     // totals over the history buckets the covered baselines used
}

// baselines computes the baseline of each window bucket (keys, sorted) from
// hist. See Analyze for the rules.
func baselines(keys []int64, hist map[int64]*slot, cfg Config) baseline {
	var b baseline
	first := keys[0]
	for k := range hist {
		if k < first {
			b.historyBefore++
		}
	}

	size := int64(cfg.BucketSize)
	used := make(map[int64]bool)
	var samples []float64
	var sampleKeys []int64
	for _, w := range keys {
		samples, sampleKeys = samples[:0], sampleKeys[:0]
		days := 0
		for d := 1; d <= cfg.BaselineDays; d++ {
			found := false
			for n := int64(-1); n <= 1; n++ {
				h := w - int64(d)*int64(day) + n*size
				s, ok := hist[h]
				if !ok || h >= first {
					continue
				}
				samples = append(samples, float64(s.requests))
				sampleKeys = append(sampleKeys, h)
				found = true
			}
			if found {
				days++
			}
		}
		if days < cfg.MinBaselineSamples {
			continue
		}
		b.covered++
		b.expected += median(samples)
		for _, h := range sampleKeys {
			if !used[h] {
				used[h] = true
				b.requests = addSat(b.requests, hist[h].requests)
				b.errors = addSat(b.errors, hist[h].errors)
			}
		}
	}
	return b
}

func tooLittleHistory(b baseline, windowBuckets int, cfg Config) string {
	need := fmt.Sprintf("data at this time of day on at least %d of the previous %d days",
		cfg.MinBaselineSamples, cfg.BaselineDays)
	switch {
	case b.historyBefore == 0:
		return "no history before the window; a baseline needs " + need
	case b.covered == 0:
		return "too little history: no window bucket has " + need
	default:
		return fmt.Sprintf("too little history: only %d of %d window buckets have %s",
			b.covered, windowBuckets, need)
	}
}

// Bucket starts must fall strictly between minStart and maxStart. A zero
// Start (year 1) or a Unix time of 0 almost always means a missing timestamp.
// The bounds also keep slot keys, in Unix nanoseconds, far enough inside an
// int64 that stepping back maxBaselineDays days cannot overflow.
var (
	minStart = time.Unix(0, 0).UTC()
	maxStart = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
)

// align moves every bucket to the start of its size-wide slot in UTC and adds
// up buckets that share a slot. Slots are keyed by start in Unix nanoseconds.
func align(buckets []Bucket, size time.Duration) (map[int64]*slot, error) {
	out := make(map[int64]*slot, len(buckets))
	for _, b := range buckets {
		start := b.Start.UTC()
		if b.Requests < 0 || b.Errors < 0 {
			return nil, fmt.Errorf("bucket at %s has a negative count (requests %d, errors %d)",
				start.Format(time.RFC3339), b.Requests, b.Errors)
		}
		if !start.After(minStart) || !start.Before(maxStart) {
			return nil, fmt.Errorf("bucket at %s starts outside the supported range (after %s, before %s)",
				start.Format(time.RFC3339), minStart.Format(time.DateOnly), maxStart.Format(time.DateOnly))
		}
		start = start.Truncate(size)
		key := start.UnixNano()
		s := out[key]
		if s == nil {
			s = &slot{start: start}
			out[key] = s
		}
		s.requests = addSat(s.requests, b.Requests)
		s.errors = addSat(s.errors, b.Errors)
	}
	return out, nil
}

// median returns the median of v, or 0 for no values. It reorders v.
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	mid := len(v) / 2
	if len(v)%2 == 1 {
		return v[mid]
	}
	return (v[mid-1] + v[mid]) / 2
}

// rate returns part/whole, or 0 when whole is not positive.
func rate(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

// addSat adds two non-negative counts, stopping at the largest int instead of
// wrapping round to a negative number.
func addSat(a, b int) int {
	if a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}

// roundSat rounds a non-negative f to the nearest int, stopping at the largest
// int.
func roundSat(f float64) int {
	if f >= math.MaxInt {
		return math.MaxInt
	}
	return int(math.Round(f))
}

// formatFactor writes f with at most two decimals and no trailing zeros:
// 10.2, 3, 2.96. When f is below limit it adds decimals until the text is
// below limit too, so that a factor of 2.9958 against a limit of 3 reads
// 2.996, not 3.
func formatFactor(f, limit float64) string {
	for prec := 2; prec <= 15; prec++ {
		p := math.Pow10(prec)
		if r := math.Round(f*p) / p; f >= limit || r < limit {
			return strconv.FormatFloat(r, 'f', -1, 64)
		}
	}
	return strconv.FormatFloat(f, 'f', -1, 64) // shortest exact text, so below limit
}
