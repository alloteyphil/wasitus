package traffic

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfigIsValid(t *testing.T) {
	c := DefaultConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("DefaultConfig().Validate() = %v, want nil", err)
	}
	want := Config{
		BucketSize:         5 * time.Minute,
		SpikeFactor:        3.0,
		MinBaselineSamples: 3,
		BaselineDays:       7,
		MixTolerance:       0.15,
	}
	if c != want {
		t.Errorf("DefaultConfig() = %+v, want %+v", c, want)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
		want   []string // substrings of the error; none means valid
	}{
		{"one-minute buckets", func(c *Config) { c.BucketSize = time.Minute }, nil},
		{"one-hour buckets", func(c *Config) { c.BucketSize = time.Hour }, nil},
		{"one baseline day", func(c *Config) { c.BaselineDays, c.MinBaselineSamples = 1, 1 }, nil},
		{"longest baseline", func(c *Config) { c.BaselineDays = maxBaselineDays }, nil},
		{"tolerance bounds", func(c *Config) { c.MixTolerance = 1 }, nil},
		{"zero tolerance", func(c *Config) { c.MixTolerance = 0 }, nil},

		{"zero bucket size", func(c *Config) { c.BucketSize = 0 }, []string{"bucket size"}},
		{"negative bucket size", func(c *Config) { c.BucketSize = -time.Minute }, []string{"bucket size"}},
		{"bucket size does not divide a day", func(c *Config) { c.BucketSize = 7 * time.Minute }, []string{"divide 24h"}},
		{"bucket wider than a day", func(c *Config) { c.BucketSize = 2 * day }, []string{"divide 24h"}},
		{"two-hour buckets", func(c *Config) { c.BucketSize = 2 * time.Hour }, []string{"at most 1h0m0s, got 2h0m0s"}},
		// From 12h up, one day's neighbouring buckets reach the next day's.
		{"half-day buckets", func(c *Config) { c.BucketSize = 12 * time.Hour }, []string{"at most 1h0m0s"}},
		{"one-day buckets", func(c *Config) { c.BucketSize = day }, []string{"at most 1h0m0s"}},
		{"spike factor of 1", func(c *Config) { c.SpikeFactor = 1 }, []string{"spike factor"}},
		{"NaN spike factor", func(c *Config) { c.SpikeFactor = math.NaN() }, []string{"spike factor"}},
		{"infinite spike factor", func(c *Config) { c.SpikeFactor = math.Inf(1) }, []string{"spike factor"}},
		{"no baseline days", func(c *Config) { c.BaselineDays = 0 }, []string{"baseline days", "min baseline samples"}},
		{"too many baseline days", func(c *Config) { c.BaselineDays = maxBaselineDays + 1 }, []string{"baseline days"}},
		{"no baseline samples", func(c *Config) { c.MinBaselineSamples = 0 }, []string{"min baseline samples"}},
		{"more samples than days", func(c *Config) { c.MinBaselineSamples = 8 }, []string{"min baseline samples"}},
		{"negative tolerance", func(c *Config) { c.MixTolerance = -0.1 }, []string{"mix tolerance"}},
		{"tolerance above 1", func(c *Config) { c.MixTolerance = 1.5 }, []string{"mix tolerance"}},
		{"NaN tolerance", func(c *Config) { c.MixTolerance = math.NaN() }, []string{"mix tolerance"}},
		{
			"every problem at once",
			func(c *Config) { *c = Config{} },
			[]string{"bucket size", "spike factor", "baseline days", "min baseline samples"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := DefaultConfig()
			tt.change(&c)
			err := c.Validate()
			if len(tt.want) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.want)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("Validate() = %q, want it to contain %q", err, w)
				}
			}
		})
	}
}
