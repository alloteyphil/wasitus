package main

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)

// runPacer ticks p every step for d after from, and returns how many
// requests fell due and the time of the last tick.
func runPacer(p *pacer, from time.Time, step, d time.Duration, rps float64) (int, time.Time) {
	total, now := 0, from
	for now.Before(from.Add(d)) {
		now = now.Add(step)
		total += p.due(now, rps)
	}
	return total, now
}

func TestPacerSendsTheRate(t *testing.T) {
	tests := []struct {
		name string
		rps  float64
		step time.Duration
		over time.Duration
		want int
	}{
		{"default 5 rps", 5, 10 * time.Millisecond, time.Second, 5},
		{"x3 (S4)", 15, 10 * time.Millisecond, time.Second, 15},
		{"x10 (S6)", 50, 10 * time.Millisecond, time.Second, 50},
		{"more than one per tick", 250, 10 * time.Millisecond, time.Second, 250},
		{"fractional rate carries over", 0.5, 10 * time.Millisecond, 3 * time.Second, 1},
		{"uneven ticks", 7, 3 * time.Millisecond, 3 * time.Second, 21},
		{"one tick a second", 5, time.Second, 10 * time.Second, 50},
		{"long run does not drift", 5, 10 * time.Millisecond, 10 * time.Minute, 3000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &pacer{}
			p.due(t0, tt.rps) // the first tick starts the clock
			if got, _ := runPacer(p, t0, tt.step, tt.over, tt.rps); got != tt.want {
				t.Errorf("%v rps over %s = %d requests, want %d", tt.rps, tt.over, got, tt.want)
			}
		})
	}
}

func TestPacerFirstTickOnlyStartsTheClock(t *testing.T) {
	p := &pacer{}
	if n := p.due(t0, 1000); n != 0 {
		t.Errorf("first tick: %d due, want 0", n)
	}
}

// The scenarios change the multiplier mid-run: x3, x10, then back to 1.
func TestPacerFollowsRateChanges(t *testing.T) {
	p := &pacer{}
	p.due(t0, 5)
	now := t0
	for _, phase := range []struct {
		rps  float64
		want int
	}{{5, 5}, {15, 15}, {50, 50}, {5, 5}} {
		var got int
		got, now = runPacer(p, now, 10*time.Millisecond, time.Second, phase.rps)
		if got != phase.want {
			t.Errorf("one second at %v rps: %d due, want %d", phase.rps, got, phase.want)
		}
	}
}

func TestPacerCatchUpIsBounded(t *testing.T) {
	p := &pacer{}
	p.due(t0, 5)
	if n := p.due(t0.Add(time.Hour), 5); n != 5 {
		t.Errorf("after a one-hour stall: %d due, want 5 (one second's worth)", n)
	}
}

func TestPacerIgnoresTicksThatDoNotMoveForward(t *testing.T) {
	p := &pacer{}
	p.due(t0, 5)
	p.due(t0.Add(time.Second), 5)
	for _, now := range []time.Time{t0.Add(time.Second), t0} {
		if n := p.due(now, 5); n != 0 {
			t.Errorf("tick at %s: %d due, want 0", now.Sub(t0), n)
		}
	}
	if n := p.due(t0.Add(2*time.Second), 5); n != 5 {
		t.Errorf("next real tick: %d due, want 5", n)
	}
}

func TestRateSet(t *testing.T) {
	tests := []struct {
		multiplier float64
		wantErr    bool
	}{
		{1, false},
		{3, false},
		{10, false},
		{0.5, false},
		{maxMultiplier, false},
		{0, true},
		{-1, true},
		{maxMultiplier + 0.001, true},
		{math.Inf(1), true},
		{math.NaN(), true},
	}
	for _, tt := range tests {
		r := NewRate(5)
		if _, _, err := r.Set(2); err != nil {
			t.Fatal(err)
		}
		from, to, err := r.Set(tt.multiplier)
		if (err != nil) != tt.wantErr {
			t.Errorf("Set(%v) error = %v, want error %v", tt.multiplier, err, tt.wantErr)
			continue
		}
		got := r.Get()
		if tt.wantErr {
			if got.Multiplier != 2 {
				t.Errorf("Set(%v) failed but changed the multiplier to %v", tt.multiplier, got.Multiplier)
			}
			continue
		}
		want := RateState{Multiplier: tt.multiplier, BaseRPS: 5, TargetRPS: 5 * tt.multiplier}
		if got != want || to != want {
			t.Errorf("Set(%v): Get() = %+v, returned %+v; want %+v", tt.multiplier, got, to, want)
		}
		if from.Multiplier != 2 {
			t.Errorf("Set(%v): from = %+v, want multiplier 2", tt.multiplier, from)
		}
	}
}

func TestNewRateStartsAtOne(t *testing.T) {
	want := RateState{Multiplier: 1, BaseRPS: 5, TargetRPS: 5}
	if got := NewRate(5).Get(); got != want {
		t.Errorf("NewRate(5).Get() = %+v, want %+v", got, want)
	}
}
