package correlate

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

// checkCandidates fails the test unless every candidate holds the invariants
// promised for valid weights: factors and score in [0, 1], the score equal to
// the documented formula, Before inside the lookback, three reasons, and the
// list sorted best first.
func checkCandidates(t *testing.T, cs []Candidate, w Weights) {
	t.Helper()
	for i, c := range cs {
		if c.Score < 0 || c.Score > 1 || math.IsNaN(c.Score) {
			t.Fatalf("candidate %d: Score = %v, want it in [0, 1] (weights %+v)", i, c.Score, w)
		}
		for name, f := range map[string]float64{"TimeProximity": c.TimeProximity, "SameService": c.SameService, "ChangeType": c.ChangeType} {
			if f < 0 || f > 1 || math.IsNaN(f) {
				t.Fatalf("candidate %d: %s = %v, want it in [0, 1]", i, name, f)
			}
		}
		if c.SameService != 0 && c.SameService != 1 {
			t.Fatalf("candidate %d: SameService = %v, want 0 or 1", i, c.SameService)
		}
		want := w.TimeProximity*c.TimeProximity + w.SameService*c.SameService + w.ChangeType*c.ChangeType
		if math.Abs(c.Score-want) > 1e-9 {
			t.Fatalf("candidate %d: Score = %v, but the formula gives %v", i, c.Score, want)
		}
		if c.Before < 0 || c.Before > w.Lookback {
			t.Fatalf("candidate %d: Before = %s, want it in [0, %s]", i, c.Before, w.Lookback)
		}
		if len(c.Reasons) != 3 {
			t.Fatalf("candidate %d: %d reasons, want 3: %q", i, len(c.Reasons), c.Reasons)
		}
		if i > 0 && compareCandidates(cs[i-1], c) > 0 {
			t.Fatalf("candidates %d and %d are out of order", i-1, i)
		}
	}
}

// TestScoreInRangeForValidWeights checks the [0, 1] property over many random
// valid weights and events. The seed is fixed, so failures are repeatable.
func TestScoreInRangeForValidWeights(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261126, 4))
	services := []string{"orders-service", "Orders-Service", "wallet-service", "storefront", ""}
	types := []string{"deploy", "release", "config", "infra", "merge", "docs", "test", "", "rollback", "DEPLOY"}

	for iter := range 2000 {
		// Two cut points in [0, 1] split it into three non-negative weights summing to 1.
		a, b := rng.Float64(), rng.Float64()
		lo, hi := min(a, b), max(a, b)
		w := Weights{
			TimeProximity: lo,
			SameService:   hi - lo,
			ChangeType:    1 - hi,
			Tau:           time.Second + time.Duration(rng.Int64N(int64(2*time.Hour))),
			Lookback:      time.Minute + time.Duration(rng.Int64N(int64(3*time.Hour))),
			ChangeTypes:   map[string]float64{},
		}
		switch iter % 4 { // make sure the corners are covered, not just the middle
		case 1:
			w.TimeProximity, w.SameService, w.ChangeType = 1, 0, 0
		case 2:
			w.TimeProximity, w.SameService, w.ChangeType = 0, 1, 0
		case 3:
			w.TimeProximity, w.SameService, w.ChangeType = 0, 0, 1
		}
		for _, typ := range types[:7] {
			w.ChangeTypes[typ] = rng.Float64()
		}
		w.ChangeTypes["deploy"] = 1 // a factor at its maximum
		if err := w.Validate(); err != nil {
			t.Fatalf("iteration %d: generated invalid weights: %v", iter, err)
		}

		events := make([]event.Event, 20)
		for i := range events {
			before := time.Duration(rng.Int64N(int64(3*w.Lookback))) - w.Lookback // some after the symptom, some too early
			if i == 0 {
				before = 0 // the time factor at its maximum
			}
			events[i] = change("c", before, services[rng.IntN(len(services))], types[rng.IntN(len(types))])
			events[i].ID = string(rune('a' + i))
		}
		s := symptom(services[rng.IntN(len(services))])
		checkCandidates(t, Rank(s, events, w), w)
	}
}

// FuzzRank checks that Rank never panics, whatever the weights, times and
// strings, and that valid weights always give scores in [0, 1].
func FuzzRank(f *testing.F) {
	f.Add(int64(5*time.Minute), 0.5, 0.3, int64(15*time.Minute), int64(time.Hour), "deploy", 1.0, "orders-service", "orders-service")
	f.Add(int64(60*time.Minute), 0.5, 0.3, int64(15*time.Minute), int64(time.Hour), "docs", 0.1, "wallet-service", "orders-service")
	f.Add(int64(0), 1.0, 0.0, int64(time.Second), int64(time.Minute), "", 0.0, "", "")
	f.Add(int64(0), 0.0, 1.0, int64(time.Minute), int64(time.Minute), "DEPLOY ", 1.0, "Orders-Service", "orders-service")
	f.Add(int64(-time.Minute), 0.5, 0.3, int64(15*time.Minute), int64(time.Hour), "config", 0.8, "orders-service", "")
	f.Add(int64(math.MaxInt64), 0.5, 0.3, int64(15*time.Minute), int64(math.MaxInt64), "infra", 0.8, "mockpay", "mockpay")
	f.Add(int64(math.MinInt64), 0.5, 0.3, int64(1), int64(math.MaxInt64), "release", 0.9, "x", "x")
	f.Add(int64(time.Minute), math.NaN(), 0.3, int64(0), int64(-1), "merge", math.Inf(1), "\x00", "\xff")
	f.Add(int64(time.Minute), -1.0, 2.0, int64(-time.Hour), int64(time.Hour), "test", -0.5, "orders-service", "ORDERS-SERVICE")

	f.Fuzz(func(t *testing.T, beforeNanos int64, tp, ss float64, tauNanos, lookbackNanos int64,
		changeType string, typeWeight float64, changeService, symptomService string) {
		w := Weights{
			TimeProximity: tp,
			SameService:   ss,
			ChangeType:    1 - tp - ss,
			Tau:           time.Duration(tauNanos),
			Lookback:      time.Duration(lookbackNanos),
			ChangeTypes:   map[string]float64{"deploy": typeWeight},
		}
		events := []event.Event{
			change("fuzzed", time.Duration(beforeNanos), changeService, changeType),
			change("at-start", 0, symptomService, "deploy"),
		}
		got := Rank(symptom(symptomService), events, w) // must not panic
		if w.Validate() != nil {
			return
		}
		checkCandidates(t, got, w)
	})
}
