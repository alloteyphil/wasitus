package verdict

import (
	"strings"
	"testing"

	"github.com/alloteyphil/wasitus/internal/fingerprint"
)

// TestMissingInputs checks that a failed source is never read as "nothing
// found": missing error samples give UNKNOWN, and any other missing input is
// named in the evidence and caps confidence at medium.
func TestMissingInputs(t *testing.T) {
	rs := labRules(t)
	cfg := DefaultConfig()

	account := func(n int) fingerprint.Report {
		return rs.Group(repeatSamples(n, fingerprint.Sample{
			Time: t0, Service: "wallet-service", Outbound: true, HTTPStatus: 402,
			ServerAddress: "mockpay", Message: "mockpay rejected the charge: HTTP 402 AccountSuspended",
		}))
	}
	code := func(n int) fingerprint.Report {
		return rs.Group(repeatSamples(n, fingerprint.Sample{
			Time: t0, Service: "orders-service", HTTPStatus: 500, ExceptionType: "runtime.boundsError",
		}))
	}

	tests := []struct {
		name      string
		in        Input
		wantSide  Side
		wantCause Cause
		wantConf  Confidence
		want      []string // evidence substrings that must appear
		notWant   []string // evidence substrings that must not appear
	}{
		{
			// Regression: a 10x surge while every error query failed used to
			// give EXTERNAL / traffic with high confidence.
			name:     "surge with error samples missing gives UNKNOWN",
			in:       Input{Traffic: makeTrafficResult(t, 10), Missing: Missing{Errors: true}},
			wantSide: SideUnknown,
			wantConf: ConfidenceLow,
			want:     []string{"error samples could not be read completely", "no changes recorded"},
			notWant:  []string{"no new error classes"},
		},
		{
			name:     "partial error samples are shown but not decided on",
			in:       Input{Errors: account(30), Missing: Missing{Errors: true}},
			wantSide: SideUnknown,
			wantConf: ConfidenceLow,
			want:     []string{"partial data only: 100% of 30 readable error sample(s) classified as account"},
		},
		{
			name:      "missing traffic caps an external verdict at medium",
			in:        Input{Errors: account(30), Missing: Missing{Traffic: true}},
			wantSide:  SideExternal,
			wantCause: CauseAccount,
			wantConf:  ConfidenceMedium,
			want:      []string{"100% of errors", "traffic could not be read"},
		},
		{
			name:      "missing baseline errors during a spike is named",
			in:        Input{Errors: account(30), Traffic: makeTrafficResult(t, 10), Missing: Missing{BaselineErrors: true}},
			wantSide:  SideExternal,
			wantCause: CauseAccount,
			wantConf:  ConfidenceMedium,
			want:      []string{"pure traffic surge could not be confirmed"},
		},
		{
			name:      "missing changes never claims there were none (external)",
			in:        Input{Errors: account(30), Missing: Missing{Changes: true}},
			wantSide:  SideExternal,
			wantCause: CauseAccount,
			wantConf:  ConfidenceMedium,
			want:      []string{"a change source could not be read"},
			notWant:   []string{"no changes recorded"},
		},
		{
			name:     "missing changes never claims there were none (code)",
			in:       Input{Errors: code(12), Missing: Missing{Changes: true}},
			wantSide: SideUnknown,
			wantConf: ConfidenceLow,
			want:     []string{"no readable change to attribute", "a change source could not be read"},
			notWant:  []string{"no changes in the lookback window"},
		},
		{
			name:     "missing changes and errors together",
			in:       Input{Missing: Missing{Errors: true, Changes: true}},
			wantSide: SideUnknown,
			wantConf: ConfidenceLow,
			want:     []string{"error samples could not be read completely", "a change source could not be read"},
			notWant:  []string{"no changes recorded"},
		},
		{
			name:     "missing changes in a split-evidence UNKNOWN",
			in:       Input{Missing: Missing{Changes: true}},
			wantSide: SideUnknown,
			wantConf: ConfidenceLow,
			want:     []string{"no error samples observed", "a change source could not be read"},
			notWant:  []string{"no changes recorded"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.in, cfg)
			if got.Side != tc.wantSide || got.Cause != tc.wantCause || got.Confidence != tc.wantConf {
				t.Errorf("got (%s, %q, %s), want (%s, %q, %s)\nevidence: %v",
					got.Side, got.Cause, got.Confidence, tc.wantSide, tc.wantCause, tc.wantConf, got.Evidence)
			}
			ev := strings.Join(got.Evidence, "\n")
			for _, s := range tc.want {
				if !strings.Contains(ev, s) {
					t.Errorf("evidence missing %q:\n%s", s, ev)
				}
			}
			for _, s := range tc.notWant {
				if strings.Contains(ev, s) {
					t.Errorf("evidence should not contain %q:\n%s", s, ev)
				}
			}
		})
	}
}

func TestMissingAny(t *testing.T) {
	if (Missing{}).Any() {
		t.Error("zero Missing reports Any() = true")
	}
	for _, m := range []Missing{{Errors: true}, {Traffic: true}, {BaselineErrors: true}, {Changes: true}} {
		if !m.Any() {
			t.Errorf("%+v reports Any() = false", m)
		}
	}
}
