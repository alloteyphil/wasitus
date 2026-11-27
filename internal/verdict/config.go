package verdict

import (
	"errors"
	"fmt"
	"math"
)

// Config holds the thresholds Decide uses to reach a verdict and assign
// confidence. Start from DefaultConfig and adjust as needed; call Validate
// before using custom values.
type Config struct {
	// DominanceShare is the minimum fraction of all error samples a single
	// cause class must hold to decide the verdict, in (0.5, 1.0]. Strictly
	// above 0.5 guarantees at most one class can dominate.
	DominanceShare float64

	// MinChangeScore is the minimum correlation score a change candidate must
	// reach for OUR_SIDE / change, in (0, 1.0].
	MinChangeScore float64

	// MinErrorSamples is the minimum total error samples needed for error
	// fingerprinting to decide a verdict (>= 1). Fewer samples yield UNKNOWN
	// (unless a clean traffic surge applies).
	MinErrorSamples int

	// MixTolerance is passed to traffic.ErrorMixStable when checking whether
	// a traffic spike kept the same error mix as baseline, in [0, 1].
	MixTolerance float64

	// HighConfidenceShare is the minimum class share for high confidence, in
	// [DominanceShare, 1.0].
	HighConfidenceShare float64

	// HighConfidenceSamples is the minimum error samples for high confidence
	// (>= MinErrorSamples).
	HighConfidenceSamples int

	// HighConfidenceChangeScore is the minimum top change score for high
	// confidence on OUR_SIDE / change, in [MinChangeScore, 1.0].
	HighConfidenceChangeScore float64

	// HighConfidenceTrafficFactor is the minimum traffic spike factor for
	// high confidence on EXTERNAL / traffic (> 1).
	HighConfidenceTrafficFactor float64
}

// DefaultConfig returns the documented starting thresholds from
// docs/ARCHITECTURE.md.
func DefaultConfig() Config {
	return Config{
		DominanceShare:              0.60,
		MinChangeScore:              0.60,
		MinErrorSamples:             3,
		MixTolerance:                0.15,
		HighConfidenceShare:         0.80,
		HighConfidenceSamples:       10,
		HighConfidenceChangeScore:   0.75,
		HighConfidenceTrafficFactor: 5.0,
	}
}

// Validate reports every problem with c at once.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if invalidFloat(c.DominanceShare) || c.DominanceShare <= 0.5 || c.DominanceShare > 1 {
		add("dominance share must be in (0.5, 1], got %g", c.DominanceShare)
	}
	if invalidFloat(c.MinChangeScore) || c.MinChangeScore <= 0 || c.MinChangeScore > 1 {
		add("min change score must be in (0, 1], got %g", c.MinChangeScore)
	}
	if c.MinErrorSamples < 1 {
		add("min error samples must be >= 1, got %d", c.MinErrorSamples)
	}
	if invalidFloat(c.MixTolerance) || c.MixTolerance < 0 || c.MixTolerance > 1 {
		add("mix tolerance must be in [0, 1], got %g", c.MixTolerance)
	}
	if invalidFloat(c.HighConfidenceShare) || c.HighConfidenceShare < c.DominanceShare || c.HighConfidenceShare > 1 {
		add("high confidence share must be in [dominance share, 1], got %g", c.HighConfidenceShare)
	}
	if c.HighConfidenceSamples < c.MinErrorSamples {
		add("high confidence samples (%d) must be >= min error samples (%d)", c.HighConfidenceSamples, c.MinErrorSamples)
	}
	if invalidFloat(c.HighConfidenceChangeScore) || c.HighConfidenceChangeScore < c.MinChangeScore || c.HighConfidenceChangeScore > 1 {
		add("high confidence change score must be in [min change score, 1], got %g", c.HighConfidenceChangeScore)
	}
	if invalidFloat(c.HighConfidenceTrafficFactor) || c.HighConfidenceTrafficFactor <= 1 {
		add("high confidence traffic factor must be > 1, got %g", c.HighConfidenceTrafficFactor)
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("verdict config: %w", errors.Join(errs...))
}

func invalidFloat(v float64) bool {
	return math.IsNaN(v) || math.IsInf(v, 0)
}
