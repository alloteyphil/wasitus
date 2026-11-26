package main

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// maxMultiplier is the largest multiplier the rate accepts. The scenarios use
// x3 (S4) and x10 (S6); x50 of the default 5 rps is still easy on a laptop.
const maxMultiplier = 50

// RateState is the rate at one moment, as the admin API reports it.
type RateState struct {
	Multiplier float64 `json:"multiplier"`
	BaseRPS    float64 `json:"base_rps"`
	TargetRPS  float64 `json:"target_rps"`
}

// Rate is the target request rate: a base rate (TRAFFIC_RPS) times a
// multiplier that can change while the generator runs. It is safe for
// concurrent use.
type Rate struct {
	base float64

	mu         sync.Mutex
	multiplier float64
}

// NewRate returns a rate of base requests per second, at multiplier 1.
func NewRate(base float64) *Rate {
	return &Rate{base: base, multiplier: 1}
}

// Get returns the current rate.
func (r *Rate) Get() RateState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state(r.multiplier)
}

// Set changes the multiplier and returns the rate before and after. It
// rejects a multiplier that is not above 0 and at most maxMultiplier.
func (r *Rate) Set(multiplier float64) (from, to RateState, err error) {
	if math.IsNaN(multiplier) || multiplier <= 0 || multiplier > maxMultiplier {
		return RateState{}, RateState{}, fmt.Errorf("multiplier must be above 0 and at most %d, got %v", maxMultiplier, multiplier)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	from = r.state(r.multiplier)
	r.multiplier = multiplier
	return from, r.state(multiplier), nil
}

func (r *Rate) state(multiplier float64) RateState {
	return RateState{Multiplier: multiplier, BaseRPS: r.base, TargetRPS: r.base * multiplier}
}

// maxCatchUp bounds how much missed schedule one tick makes up for. A process
// that stalls (a laptop waking from sleep) then sends at most one second's
// worth of requests at once, not a burst of everything it missed.
const maxCatchUp = time.Second

// pacer turns a rate into the number of requests due at each tick. It
// carries the fraction of a request over to the next tick, so 5 rps with
// 10 ms ticks sends exactly 5 requests a second: one every 20 ticks.
//
// A rate change applies from the next tick. Only the goroutine that runs
// the schedule uses a pacer.
type pacer struct {
	last   time.Time // the previous tick; zero before the first
	credit float64   // requests owed but not yet due, below 1 after each tick
}

// due returns how many requests are due at now, at rps requests per second.
// The first tick only starts the clock.
func (p *pacer) due(now time.Time, rps float64) int {
	if p.last.IsZero() {
		p.last = now
		return 0
	}
	if !now.After(p.last) {
		return 0
	}
	elapsed := min(now.Sub(p.last), maxCatchUp)
	p.last = now
	p.credit += elapsed.Seconds() * rps
	// The epsilon absorbs float rounding: 100 ticks of 0.05 must make 5, not
	// 4.999999.
	n := math.Floor(p.credit + 1e-9)
	p.credit -= n
	return int(n)
}
