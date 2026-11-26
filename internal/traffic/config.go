package traffic

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Bucket is the number of requests, and of those the errors, in one
// fixed-size slice of time that starts at Start. Analyze explains how a Start
// that is not on a bucket boundary is handled. Start must be after the Unix
// epoch and before the year 2200; Analyze refuses a zero Start as a missing
// timestamp.
type Bucket struct {
	Start    time.Time
	Requests int
	Errors   int
}

const (
	day = 24 * time.Hour

	// maxBucketSize bounds BucketSize. The baseline looks at the same time of
	// day give or take one bucket, so a wide bucket blurs which time of day it
	// compares. From 12 hours up it is wrong as well as blurred: the buckets
	// either side of one day reach into the next day's, so a single day of
	// history would count as two towards MinBaselineSamples.
	maxBucketSize = time.Hour

	// maxBaselineDays bounds BaselineDays. A baseline longer than a quarter
	// mostly measures growth rather than normal load, and the bound keeps the
	// work Analyze does proportional to its input.
	maxBaselineDays = 90
)

// Config tunes spike detection. Start from DefaultConfig and change what you
// need; Analyze refuses a Config that does not pass Validate.
type Config struct {
	// BucketSize is the width of one bucket, at most one hour. It must divide
	// 24 hours exactly, so that a time of day falls on the same bucket
	// boundary every day.
	BucketSize time.Duration

	// SpikeFactor is how many times the expected requests the window must
	// reach to count as a spike. It must be greater than 1.
	SpikeFactor float64

	// MinBaselineSamples is how many of the previous BaselineDays days must
	// have history at a window bucket's time of day (give or take one bucket)
	// for that bucket to have a baseline. It counts days, not buckets: the
	// neighbouring buckets of a single day are not independent evidence of
	// what is normal, so one day of history must never be enough on its own.
	MinBaselineSamples int

	// BaselineDays is how many previous days the baseline looks at, 1 to 90.
	BaselineDays int

	// MixTolerance is the largest change in any error class's share of all
	// errors that still counts as the same error mix, from 0 to 1. Analyze
	// does not use it; pass it to ErrorMixStable.
	MixTolerance float64
}

// DefaultConfig returns the starting values from docs/PLAN.md (M3): 5-minute
// buckets compared with a 7-day baseline. The scenario suite tunes them.
func DefaultConfig() Config {
	return Config{
		BucketSize:         5 * time.Minute,
		SpikeFactor:        3.0,
		MinBaselineSamples: 3,
		BaselineDays:       7,
		MixTolerance:       0.15,
	}
}

// Validate reports every problem with c at once, so they can all be fixed in
// one go.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	switch {
	case c.BucketSize <= 0 || day%c.BucketSize != 0:
		add("bucket size must be positive and divide 24h exactly, got %s", c.BucketSize)
	case c.BucketSize > maxBucketSize:
		add("bucket size must be at most %s, got %s", maxBucketSize, c.BucketSize)
	}
	if math.IsNaN(c.SpikeFactor) || math.IsInf(c.SpikeFactor, 0) || c.SpikeFactor <= 1 {
		add("spike factor must be a finite number greater than 1, got %g", c.SpikeFactor)
	}
	if c.BaselineDays < 1 || c.BaselineDays > maxBaselineDays {
		add("baseline days must be between 1 and %d, got %d", maxBaselineDays, c.BaselineDays)
	}
	if c.MinBaselineSamples < 1 || c.MinBaselineSamples > c.BaselineDays {
		add("min baseline samples must be between 1 and baseline days (%d), got %d",
			c.BaselineDays, c.MinBaselineSamples)
	}
	if math.IsNaN(c.MixTolerance) || c.MixTolerance < 0 || c.MixTolerance > 1 {
		add("mix tolerance must be between 0 and 1, got %g", c.MixTolerance)
	}
	return errors.Join(errs...)
}
