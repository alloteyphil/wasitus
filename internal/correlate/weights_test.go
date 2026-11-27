package correlate

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestDefaultWeights(t *testing.T) {
	w := DefaultWeights()
	if err := w.Validate(); err != nil {
		t.Fatalf("DefaultWeights().Validate() = %v, want nil", err)
	}
	if w.TimeProximity != 0.5 || w.SameService != 0.3 || w.ChangeType != 0.2 {
		t.Errorf("top-level weights = %v/%v/%v, want 0.5/0.3/0.2", w.TimeProximity, w.SameService, w.ChangeType)
	}
	if w.Tau != 15*time.Minute {
		t.Errorf("Tau = %s, want 15m", w.Tau)
	}
	if w.Lookback != 60*time.Minute {
		t.Errorf("Lookback = %s, want 1h", w.Lookback)
	}
	want := map[string]float64{
		"deploy": 1.0, "release": 0.9, "config": 0.8, "infra": 0.8,
		"merge": 0.4, "docs": 0.1, "test": 0.1,
	}
	if len(w.ChangeTypes) != len(want) {
		t.Errorf("ChangeTypes has %d entries, want %d: %v", len(w.ChangeTypes), len(want), w.ChangeTypes)
	}
	for typ, v := range want {
		if got, ok := w.ChangeTypes[typ]; !ok || got != v {
			t.Errorf("ChangeTypes[%q] = %v (present %v), want %v", typ, got, ok, v)
		}
	}
}

func TestDefaultWeightsReturnsAFreshMap(t *testing.T) {
	a := DefaultWeights()
	a.ChangeTypes["deploy"] = 0
	if got := DefaultWeights().ChangeTypes["deploy"]; got != 1.0 {
		t.Fatalf("changing one DefaultWeights() map changed the next: deploy = %v", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Weights)
		wantErr []string // every substring must appear; nil means valid
	}{
		{"defaults", func(*Weights) {}, nil},
		{"nil change types", func(w *Weights) { w.ChangeTypes = nil }, nil},
		{"one top-level weight of 1", func(w *Weights) { w.TimeProximity, w.SameService, w.ChangeType = 1, 0, 0 }, nil},
		{"sum within tolerance", func(w *Weights) { w.TimeProximity, w.SameService, w.ChangeType = 0.1, 0.2, 0.7 }, nil},
		{"type weights at the bounds", func(w *Weights) { w.ChangeTypes = map[string]float64{"deploy": 1, "docs": 0} }, nil},
		{
			"negative time weight",
			func(w *Weights) { w.TimeProximity, w.SameService = -0.1, 0.9 },
			[]string{"time proximity weight must be >= 0, got -0.1"},
		},
		{
			"negative service weight",
			func(w *Weights) { w.SameService, w.TimeProximity = -0.2, 1.0 },
			[]string{"same service weight must be >= 0, got -0.2"},
		},
		{
			"negative type weight",
			func(w *Weights) { w.ChangeType, w.TimeProximity = -0.3, 1.0 },
			[]string{"change type weight must be >= 0, got -0.3"},
		},
		{
			"NaN weight",
			func(w *Weights) { w.TimeProximity = math.NaN() },
			[]string{"time proximity weight must be >= 0, got NaN", "must sum to 1, got NaN"},
		},
		{
			"sum below 1",
			func(w *Weights) { w.TimeProximity = 0.4 },
			[]string{"must sum to 1, got 0.9"},
		},
		{
			"sum above 1",
			func(w *Weights) { w.SameService = 0.4 },
			[]string{"must sum to 1, got 1.1"},
		},
		{
			"sum just outside tolerance",
			func(w *Weights) { w.TimeProximity += 1e-8 },
			[]string{"must sum to 1"},
		},
		{"zero tau", func(w *Weights) { w.Tau = 0 }, []string{"tau must be > 0, got 0s"}},
		{"negative tau", func(w *Weights) { w.Tau = -time.Minute }, []string{"tau must be > 0, got -1m0s"}},
		{"zero lookback", func(w *Weights) { w.Lookback = 0 }, []string{"lookback must be > 0, got 0s"}},
		{"negative lookback", func(w *Weights) { w.Lookback = -time.Hour }, []string{"lookback must be > 0, got -1h0m0s"}},
		{
			"negative change type weight",
			func(w *Weights) { w.ChangeTypes["docs"] = -0.1 },
			[]string{`change type "docs" weight must be between 0 and 1, got -0.1`},
		},
		{
			"change type weight above 1",
			func(w *Weights) { w.ChangeTypes["deploy"] = 1.5 },
			[]string{`change type "deploy" weight must be between 0 and 1, got 1.5`},
		},
		{
			"NaN change type weight",
			func(w *Weights) { w.ChangeTypes["merge"] = math.NaN() },
			[]string{`change type "merge" weight must be between 0 and 1, got NaN`},
		},
		{
			"empty change type name",
			func(w *Weights) { w.ChangeTypes[""] = 0.5 },
			[]string{"change type name is empty"},
		},
		{
			"uppercase change type name",
			func(w *Weights) { w.ChangeTypes["Deploy"] = 1 },
			[]string{`change type "Deploy" must be lowercase with no surrounding spaces`},
		},
		{
			"padded change type name",
			func(w *Weights) { w.ChangeTypes[" deploy"] = 1 },
			[]string{`change type " deploy" must be lowercase with no surrounding spaces`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := DefaultWeights()
			tt.mutate(&w)
			err := w.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want errors %q", tt.wantErr)
			}
			if !strings.HasPrefix(err.Error(), "correlation weights: ") {
				t.Errorf("error %q does not start with the context prefix", err)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	w := Weights{
		TimeProximity: -1,
		Tau:           0,
		Lookback:      0,
		ChangeTypes:   map[string]float64{"Infra": 2},
	}
	err := w.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for weights with many problems")
	}
	for _, want := range []string{
		"time proximity weight must be >= 0",
		"must sum to 1",
		"tau must be > 0",
		"lookback must be > 0",
		`change type "Infra" must be lowercase`,
		`change type "Infra" weight must be between 0 and 1`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestValidateOrdersChangeTypeErrors(t *testing.T) {
	w := DefaultWeights()
	w.ChangeTypes = map[string]float64{"zeta": -1, "alpha": -1, "mid": -1}
	err := w.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want errors")
	}
	msg := err.Error()
	a, m, z := strings.Index(msg, `"alpha"`), strings.Index(msg, `"mid"`), strings.Index(msg, `"zeta"`)
	if a < 0 || m < 0 || z < 0 || a >= m || m >= z {
		t.Fatalf("change type errors are not in name order: %q", msg)
	}
}

func TestTypeWeight(t *testing.T) {
	w := DefaultWeights()
	tests := []struct {
		typ        string
		want       float64
		wantListed bool
	}{
		{"deploy", 1.0, true},
		{"release", 0.9, true},
		{"config", 0.8, true},
		{"infra", 0.8, true},
		{"merge", 0.4, true},
		{"docs", 0.1, true},
		{"test", 0.1, true},
		{"", UnknownTypeWeight, false},
		{"rollback", UnknownTypeWeight, false},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			got, listed := w.typeWeight(tt.typ)
			if got != tt.want || listed != tt.wantListed {
				t.Errorf("typeWeight(%q) = %v, %v; want %v, %v", tt.typ, got, listed, tt.want, tt.wantListed)
			}
		})
	}
}

func TestTypeWeightEmptyNameIsUnknownEvenWhenListed(t *testing.T) {
	w := DefaultWeights()
	w.ChangeTypes[""] = 1 // invalid, but typeWeight must still treat a missing type as unknown
	if got, listed := w.typeWeight(""); got != UnknownTypeWeight || listed {
		t.Fatalf(`typeWeight("") = %v, %v; want %v, false`, got, listed, UnknownTypeWeight)
	}
}

func TestNormalizeType(t *testing.T) {
	for in, want := range map[string]string{
		"deploy":     "deploy",
		"Deploy":     "deploy",
		"  CONFIG\t": "config",
		"":           "",
		"   ":        "",
	} {
		if got := normalizeType(in); got != want {
			t.Errorf("normalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}
