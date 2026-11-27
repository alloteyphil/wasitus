package verdict

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/correlate"
	"github.com/alloteyphil/wasitus/internal/event"
	"github.com/alloteyphil/wasitus/internal/fingerprint"
	"github.com/alloteyphil/wasitus/internal/traffic"
)

var t0 = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

func labRules(t *testing.T) *fingerprint.RuleSet {
	t.Helper()
	rs, err := fingerprint.LoadRules(nil, fingerprint.Options{
		InternalHosts: []string{"storefront", "orders-service", "wallet-service", "localhost"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func deployEvent(id, service, title string, at time.Time) event.Event {
	return event.Event{
		ID:       id,
		Time:     at,
		Kind:     event.KindChange,
		Source:   "github",
		Service:  service,
		Title:    title,
		Severity: event.SeverityInfo,
		Attrs:    map[string]string{correlate.AttrChangeType: "deploy"},
	}
}

func repeatSamples(n int, s fingerprint.Sample) []fingerprint.Sample {
	out := make([]fingerprint.Sample, n)
	for i := range n {
		cp := s
		cp.Time = s.Time.Add(time.Duration(i) * time.Second)
		out[i] = cp
	}
	return out
}

func makeTrafficResult(t *testing.T, multiplier float64) *traffic.Result {
	t.Helper()
	cfg := traffic.DefaultConfig()
	var history []traffic.Bucket
	for d := 1; d <= 7; d++ {
		history = append(history, traffic.Bucket{
			Start:    t0.AddDate(0, 0, -d),
			Requests: 600,
			Errors:   3,
		})
	}
	win := []traffic.Bucket{{
		Start:    t0,
		Requests: int(math.Round(600 * multiplier)),
		Errors:   int(math.Round(3 * multiplier)),
	}}
	res := traffic.Analyze(win, history, cfg)
	return &res
}

// TestScenariosS1ToS8 exercises the full deterministic contract from
// docs/SCENARIOS.md using real fingerprint.Group, correlate.Rank and
// traffic.Analyze outputs.
func TestScenariosS1ToS8(t *testing.T) {
	rs := labRules(t)
	cfg := DefaultConfig()
	weights := correlate.DefaultWeights()

	tests := []struct {
		name         string
		in           Input
		wantSide     Side
		wantCause    Cause
		wantConf     Confidence
		wantLabel    string
		wantChangeID string
		wantEvidence []string
	}{
		{
			name: "S1 bad deploy",
			in: Input{
				Errors: rs.Group(repeatSamples(20, fingerprint.Sample{
					Time:          t0,
					Service:       "orders-service",
					HTTPStatus:    500,
					ExceptionType: "runtime.boundsError",
					Message:       "runtime error: index out of range [3] with length 3",
				})),
				Candidates: correlate.Rank(
					correlate.Symptom{Start: t0, Service: "orders-service"},
					[]event.Event{deployEvent("dep-1", "orders-service", "Deployed orders-service v1.4.0", t0.Add(-2*time.Minute))},
					weights,
				),
			},
			wantSide:     SideOurSide,
			wantCause:    CauseChange,
			wantConf:     ConfidenceHigh,
			wantLabel:    "OUR_SIDE / change",
			wantChangeID: "dep-1",
			wantEvidence: []string{"100% of errors", "orders-service", "Deployed orders-service v1.4.0"},
		},
		{
			name: "S2 provider outage",
			in: Input{
				Errors: rs.Group(repeatSamples(25, fingerprint.Sample{
					Time:          t0,
					Service:       "wallet-service",
					Outbound:      true,
					HTTPStatus:    503,
					ServerAddress: "mockpay",
					Message:       "HTTP 503 ServiceUnavailable: Payments are temporarily unavailable.",
				})),
			},
			wantSide:     SideExternal,
			wantCause:    CauseDependency,
			wantConf:     ConfidenceHigh,
			wantLabel:    "EXTERNAL / dependency",
			wantEvidence: []string{"100% of errors", "dependency", "wallet-service"},
		},
		{
			name: "S3 suspended account",
			in: Input{
				Errors: rs.Group(repeatSamples(30, fingerprint.Sample{
					Time:          t0,
					Service:       "wallet-service",
					Outbound:      true,
					HTTPStatus:    402,
					ServerAddress: "mockpay",
					Message:       "mockpay rejected the charge: HTTP 402 AccountSuspended",
				})),
			},
			wantSide:     SideExternal,
			wantCause:    CauseAccount,
			wantConf:     ConfidenceHigh,
			wantLabel:    "EXTERNAL / account",
			wantEvidence: []string{"100% of errors", "account", "AccountSuspended"},
		},
		{
			name: "S4 rate limited with 3x traffic",
			in: Input{
				Errors: rs.Group(repeatSamples(18, fingerprint.Sample{
					Time:          t0,
					Service:       "wallet-service",
					Outbound:      true,
					HTTPStatus:    429,
					ServerAddress: "mockpay",
					Message:       "HTTP 429 RateLimited: Too many requests.",
				})),
				Traffic:        makeTrafficResult(t, 3.5),
				BaselineErrors: map[fingerprint.Class]int{fingerprint.ClassCode: 2},
			},
			wantSide:     SideExternal,
			wantCause:    CauseLimit,
			wantConf:     ConfidenceHigh,
			wantLabel:    "EXTERNAL / limit",
			wantEvidence: []string{"100% of errors", "limit", "traffic also spiked"},
		},
		{
			name: "S5 expired certificate",
			in: Input{
				Errors: rs.Group(repeatSamples(15, fingerprint.Sample{
					Time:          t0,
					Service:       "wallet-service",
					Outbound:      true,
					ServerAddress: "mockpay",
					Message:       `Post "https://mockpay:8443/v1/charges": tls: failed to verify certificate: x509: certificate has expired or is not yet valid`,
				})),
			},
			wantSide:     SideExternal,
			wantCause:    CauseExpiry,
			wantConf:     ConfidenceHigh,
			wantLabel:    "EXTERNAL / expiry",
			wantEvidence: []string{"100% of errors", "expiry", "certificate has expired"},
		},
		{
			name: "S6 traffic surge with no faults",
			in: Input{
				Errors:  rs.Group(nil),
				Traffic: makeTrafficResult(t, 10.0),
			},
			wantSide:     SideExternal,
			wantCause:    CauseTraffic,
			wantConf:     ConfidenceHigh,
			wantLabel:    "EXTERNAL / traffic",
			wantEvidence: []string{"10x", "no new error classes"},
		},
		{
			name: "S6b traffic surge with unchanged background error mix",
			in: Input{
				Errors: rs.Group(repeatSamples(20, fingerprint.Sample{
					Time:          t0,
					Service:       "wallet-service",
					Outbound:      true,
					HTTPStatus:    503,
					ServerAddress: "mockpay",
					Message:       "HTTP 503 ServiceUnavailable",
				})),
				Traffic:        makeTrafficResult(t, 10.0),
				BaselineErrors: map[fingerprint.Class]int{fingerprint.ClassDependency: 2},
			},
			wantSide:     SideExternal,
			wantCause:    CauseTraffic,
			wantConf:     ConfidenceHigh,
			wantLabel:    "EXTERNAL / traffic",
			wantEvidence: []string{"10x", "error mix stayed within"},
		},
		{
			name: "S7 red herring deploy must not be blamed",
			in: Input{
				Errors: rs.Group(repeatSamples(25, fingerprint.Sample{
					Time:          t0,
					Service:       "wallet-service",
					Outbound:      true,
					HTTPStatus:    402,
					ServerAddress: "mockpay",
					Message:       "HTTP 402 AccountSuspended",
				})),
				Candidates: correlate.Rank(
					correlate.Symptom{Start: t0, Service: "orders-service"},
					[]event.Event{deployEvent("dep-harmless", "orders-service", "Deployed orders-service v1.3.1 (log wording only)", t0.Add(-5*time.Minute))},
					weights,
				),
			},
			wantSide:     SideExternal,
			wantCause:    CauseAccount,
			wantConf:     ConfidenceHigh,
			wantLabel:    "EXTERNAL / account",
			wantEvidence: []string{"100% of errors", "account", "was not blamed"},
		},
		{
			name: "S8 unclear mixed errors and no change",
			in: Input{
				Errors: rs.Group(append(
					append(
						repeatSamples(6, fingerprint.Sample{Time: t0, Service: "wallet-service", Outbound: true, HTTPStatus: 429, ServerAddress: "mockpay", Message: "HTTP 429 RateLimited"}),
						repeatSamples(6, fingerprint.Sample{Time: t0, Service: "wallet-service", Outbound: true, HTTPStatus: 503, ServerAddress: "mockpay", Message: "HTTP 503 ServiceUnavailable"})...,
					),
					repeatSamples(6, fingerprint.Sample{Time: t0, Service: "wallet-service", Outbound: true, HTTPStatus: 402, ServerAddress: "mockpay", Message: "HTTP 402 AccountSuspended"})...,
				)),
			},
			wantSide:     SideUnknown,
			wantCause:    CauseNone,
			wantConf:     ConfidenceLow,
			wantLabel:    "UNKNOWN",
			wantEvidence: []string{"no error class reached 60%", "no changes recorded"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.in, cfg)
			if got.Side != tt.wantSide || got.Cause != tt.wantCause || got.Confidence != tt.wantConf {
				t.Fatalf("Decide() = (%s, %s, %s), want (%s, %s, %s)\nevidence: %v",
					got.Side, got.Cause, got.Confidence, tt.wantSide, tt.wantCause, tt.wantConf, got.Evidence)
			}
			if got.Label() != tt.wantLabel {
				t.Errorf("Label() = %q, want %q", got.Label(), tt.wantLabel)
			}
			if tt.wantChangeID != "" {
				if got.Change == nil || got.Change.Change.ID != tt.wantChangeID {
					t.Errorf("Change = %+v, want ID %q", got.Change, tt.wantChangeID)
				}
			} else if got.Change != nil {
				t.Errorf("Change = %+v, want nil", got.Change)
			}
			joined := strings.Join(got.Evidence, "\n")
			for _, sub := range tt.wantEvidence {
				if !strings.Contains(joined, sub) {
					t.Errorf("evidence missing %q:\n%s", sub, joined)
				}
			}
		})
	}
}

func TestEdgeCasesAndConfidenceTiers(t *testing.T) {
	rs := labRules(t)
	cfg := DefaultConfig()
	weights := correlate.DefaultWeights()

	t.Run("code dominant with no change gives UNKNOWN", func(t *testing.T) {
		got := Decide(Input{
			Errors: rs.Group(repeatSamples(12, fingerprint.Sample{
				Time: t0, Service: "orders-service", HTTPStatus: 500, ExceptionType: "runtime.boundsError",
			})),
		}, cfg)
		if got.Side != SideUnknown || got.Confidence != ConfidenceLow {
			t.Errorf("got (%s, %s), want (UNKNOWN, low)", got.Side, got.Confidence)
		}
		if !strings.Contains(strings.Join(got.Evidence, "\n"), "no changes in the lookback window") {
			t.Errorf("unexpected evidence: %v", got.Evidence)
		}
	})

	t.Run("code dominant with weak change below 0.60 gives UNKNOWN", func(t *testing.T) {
		weak := correlate.Rank(
			correlate.Symptom{Start: t0, Service: "orders-service"},
			[]event.Event{{
				ID: "docs-pr", Time: t0.Add(-40 * time.Minute), Kind: event.KindChange,
				Source: "github", Service: "wallet-service", Title: "docs: update README",
				Attrs: map[string]string{correlate.AttrChangeType: "docs"},
			}},
			weights,
		)
		got := Decide(Input{
			Errors: rs.Group(repeatSamples(12, fingerprint.Sample{
				Time: t0, Service: "orders-service", HTTPStatus: 500, ExceptionType: "runtime.boundsError",
			})),
			Candidates: weak,
		}, cfg)
		if got.Side != SideUnknown || len(got.Candidates) != 1 {
			t.Errorf("got (%s, candidates=%d), want UNKNOWN with 1 candidate", got.Side, len(got.Candidates))
		}
		if !strings.Contains(strings.Join(got.Evidence, "\n"), "below the 0.60 threshold") {
			t.Errorf("unexpected evidence: %v", got.Evidence)
		}
	})

	t.Run("medium confidence when sample count below high threshold", func(t *testing.T) {
		got := Decide(Input{
			Errors: rs.Group(repeatSamples(5, fingerprint.Sample{
				Time: t0, Service: "wallet-service", Outbound: true, HTTPStatus: 402, ServerAddress: "mockpay", Message: "AccountSuspended",
			})),
		}, cfg)
		if got.Side != SideExternal || got.Cause != CauseAccount || got.Confidence != ConfidenceMedium {
			t.Errorf("got (%s, %s, %s), want (EXTERNAL, account, medium)", got.Side, got.Cause, got.Confidence)
		}
	})

	t.Run("medium confidence traffic surge between 3x and 5x with red herring change", func(t *testing.T) {
		cands := correlate.Rank(
			correlate.Symptom{Start: t0, Service: "storefront"},
			[]event.Event{deployEvent("dep-s", "storefront", "Deployed storefront v1.0.1", t0.Add(-3*time.Minute))},
			weights,
		)
		got := Decide(Input{
			Traffic:    makeTrafficResult(t, 3.5),
			Candidates: cands,
		}, cfg)
		if got.Side != SideExternal || got.Cause != CauseTraffic || got.Confidence != ConfidenceMedium {
			t.Errorf("got (%s, %s, %s), want (EXTERNAL, traffic, medium)", got.Side, got.Cause, got.Confidence)
		}
		if !strings.Contains(strings.Join(got.Evidence, "\n"), "was not blamed") {
			t.Errorf("expected not-blamed note in evidence: %v", got.Evidence)
		}
	})

	t.Run("too few error samples yields UNKNOWN", func(t *testing.T) {
		got := Decide(Input{
			Errors: rs.Group(repeatSamples(2, fingerprint.Sample{
				Time: t0, Service: "wallet-service", Outbound: true, HTTPStatus: 402, ServerAddress: "mockpay", Message: "AccountSuspended",
			})),
		}, cfg)
		if got.Side != SideUnknown || !strings.Contains(strings.Join(got.Evidence, "\n"), "need at least 3") {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("empty input with non-spike traffic yields UNKNOWN", func(t *testing.T) {
		got := Decide(Input{
			Traffic: makeTrafficResult(t, 1.1),
			Candidates: correlate.Rank(
				correlate.Symptom{Start: t0, Service: "storefront"},
				[]event.Event{deployEvent("dep-1", "storefront", "Deployed storefront", t0.Add(-10*time.Minute))},
				weights,
			),
		}, cfg)
		if got.Side != SideUnknown || len(got.Candidates) != 1 {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("invalid config yields UNKNOWN without panicking", func(t *testing.T) {
		got := Decide(Input{}, Config{})
		if got.Side != SideUnknown || got.Confidence != ConfidenceLow || !strings.Contains(got.Evidence[0], "invalid verdict configuration") {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("unsorted candidates are sorted deterministically", func(t *testing.T) {
		low := correlate.Candidate{Change: deployEvent("low", "orders-service", "Old deploy", t0.Add(-45*time.Minute)), Score: 0.62, Before: 45 * time.Minute}
		high := correlate.Candidate{Change: deployEvent("high", "orders-service", "Fresh deploy", t0.Add(-1*time.Minute)), Score: 0.95, Before: time.Minute}
		got := Decide(Input{
			Errors: rs.Group(repeatSamples(12, fingerprint.Sample{
				Time: t0, Service: "orders-service", HTTPStatus: 500, ExceptionType: "runtime.boundsError",
			})),
			Candidates: []correlate.Candidate{low, high},
		}, cfg)
		if got.Side != SideOurSide || got.Change == nil || got.Change.Change.ID != "high" {
			t.Errorf("expected highest-scoring candidate to win, got %+v", got)
		}
	})

	t.Run("shares-only report without groups still works", func(t *testing.T) {
		got := Decide(Input{
			Errors: fingerprint.Report{
				Total:  10,
				Shares: map[fingerprint.Class]float64{fingerprint.ClassExpiry: 0.9},
			},
		}, cfg)
		if got.Side != SideExternal || got.Cause != CauseExpiry || got.Confidence != ConfidenceHigh {
			t.Errorf("got %+v", got)
		}
	})
}

func TestConfigValidate(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("DefaultConfig().Validate() = %v", err)
	}
	bad := Config{
		DominanceShare:              0.5, // must be > 0.5
		MinChangeScore:              0,
		MinErrorSamples:             0,
		MixTolerance:                -0.1,
		HighConfidenceShare:         0.4,
		HighConfidenceSamples:       0,
		HighConfidenceChangeScore:   1.5,
		HighConfidenceTrafficFactor: 1.0,
	}
	err := bad.Validate()
	if err == nil {
		t.Fatal("Validate(bad) = nil, want errors")
	}
	for _, want := range []string{
		"dominance share",
		"min change score",
		"min error samples",
		"mix tolerance",
		"high confidence share",
		"high confidence change score",
		"high confidence traffic factor",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error missing %q: %v", want, err)
		}
	}
}
