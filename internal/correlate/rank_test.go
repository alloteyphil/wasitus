package correlate

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/event"
)

// start is when the symptom in these tests was first seen.
var start = time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)

// change returns a change event that happened `before` the symptom start.
func change(id string, before time.Duration, service, changeType string) event.Event {
	e := event.Event{
		ID:       id,
		Time:     start.Add(-before),
		Kind:     event.KindChange,
		Source:   "github",
		Service:  service,
		Title:    "Deploy " + id,
		Severity: event.SeverityInfo,
	}
	if changeType != "" {
		e.Attrs = map[string]string{AttrChangeType: changeType}
	}
	return e
}

func symptom(service string) Symptom { return Symptom{Start: start, Service: service} }

func ids(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Change.ID
	}
	return out
}

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// rankOne ranks a single change and fails the test unless exactly one candidate comes back.
func rankOne(t *testing.T, s Symptom, e event.Event, w Weights) Candidate {
	t.Helper()
	got := Rank(s, []event.Event{e}, w)
	if len(got) != 1 {
		t.Fatalf("Rank returned %d candidates, want 1", len(got))
	}
	return got[0]
}

func TestTimeProximity(t *testing.T) {
	tests := []struct {
		before     time.Duration
		want       float64
		wantReason string
	}{
		{0, 1, "at the same time as the first symptom"},
		{400 * time.Millisecond, math.Exp(-0.4 / 900), "at the same time as the first symptom"},
		{45 * time.Second, math.Exp(-45.0 / 900), "45s before the first symptom"},
		{5 * time.Minute, 0.7165, "5m before the first symptom"}, // documented example
		{15 * time.Minute, 1 / math.E, "15m before the first symptom"},
		{17*time.Minute + 30*time.Second, math.Exp(-17.5 / 15), "17m30s before the first symptom"},
		{60 * time.Minute, 0.0183, "1h before the first symptom"}, // documented example
	}
	for _, tt := range tests {
		t.Run(tt.before.String(), func(t *testing.T) {
			c := rankOne(t, symptom("orders-service"), change("c1", tt.before, "orders-service", "deploy"), DefaultWeights())
			if !approx(c.TimeProximity, tt.want, 5e-5) {
				t.Errorf("TimeProximity = %.6f, want about %.4f", c.TimeProximity, tt.want)
			}
			if c.Before != tt.before {
				t.Errorf("Before = %s, want %s", c.Before, tt.before)
			}
			if c.Reasons[0] != tt.wantReason {
				t.Errorf("time reason = %q, want %q", c.Reasons[0], tt.wantReason)
			}
		})
	}
}

func TestScoreWorkedExample(t *testing.T) {
	// A deploy to the failing service 17 minutes before the first symptom:
	// 0.5*exp(-17/15) + 0.3*1 + 0.2*1.0 ≈ 0.661.
	c := rankOne(t, symptom("orders-service"), change("deploy-42", 17*time.Minute, "orders-service", "deploy"), DefaultWeights())

	want := 0.5*math.Exp(-17.0/15) + 0.3 + 0.2
	if !approx(c.Score, want, 1e-12) || !approx(c.Score, 0.661, 5e-4) {
		t.Errorf("Score = %.6f, want %.6f", c.Score, want)
	}
	if c.SameService != 1 || c.ChangeType != 1 {
		t.Errorf("factors = service %v, type %v; want 1, 1", c.SameService, c.ChangeType)
	}
	wantReasons := []string{
		"17m before the first symptom",
		"same service: orders-service",
		"type deploy (weight 1.0)",
	}
	if !slices.Equal(c.Reasons, wantReasons) {
		t.Errorf("Reasons = %q, want %q", c.Reasons, wantReasons)
	}
	if c.Change.ID != "deploy-42" {
		t.Errorf("Change.ID = %q, want deploy-42", c.Change.ID)
	}
}

func TestSameService(t *testing.T) {
	tests := []struct {
		name       string
		symptom    string
		change     string
		want       float64
		wantReason string
	}{
		{"same", "orders-service", "orders-service", 1, "same service: orders-service"},
		{"same, different case", "orders-service", "Orders-Service", 1, "same service: Orders-Service"},
		{"same, padded", " orders-service ", "orders-service\t", 1, "same service: orders-service"},
		{"different", "orders-service", "wallet-service", 0, "different service: wallet-service (symptom in orders-service)"},
		{"change has no service", "orders-service", "", 0, "no service on the change"},
		{"change service is blank", "orders-service", "   ", 0, "no service on the change"},
		{"symptom has no service", "", "orders-service", 0, "no service on the symptom"},
		{"neither has a service", "", "", 0, "no service on the change"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := DefaultWeights()
			c := rankOne(t, symptom(tt.symptom), change("c1", 5*time.Minute, tt.change, "deploy"), w)
			if c.SameService != tt.want {
				t.Errorf("SameService = %v, want %v", c.SameService, tt.want)
			}
			if c.Reasons[1] != tt.wantReason {
				t.Errorf("service reason = %q, want %q", c.Reasons[1], tt.wantReason)
			}
			want := w.TimeProximity*math.Exp(-5.0/15) + w.SameService*tt.want + w.ChangeType*1.0
			if !approx(c.Score, want, 1e-12) {
				t.Errorf("Score = %.6f, want %.6f", c.Score, want)
			}
		})
	}
}

func TestChangeTypeWeights(t *testing.T) {
	tests := []struct {
		name       string
		attrs      map[string]string
		want       float64
		wantReason string
	}{
		{"deploy", map[string]string{AttrChangeType: "deploy"}, 1.0, "type deploy (weight 1.0)"},
		{"release", map[string]string{AttrChangeType: "release"}, 0.9, "type release (weight 0.9)"},
		{"config", map[string]string{AttrChangeType: "config"}, 0.8, "type config (weight 0.8)"},
		{"infra", map[string]string{AttrChangeType: "infra"}, 0.8, "type infra (weight 0.8)"},
		{"merge", map[string]string{AttrChangeType: "merge"}, 0.4, "type merge (weight 0.4)"},
		{"docs", map[string]string{AttrChangeType: "docs"}, 0.1, "type docs (weight 0.1)"},
		{"test", map[string]string{AttrChangeType: "test"}, 0.1, "type test (weight 0.1)"},
		{"uppercase", map[string]string{AttrChangeType: " DEPLOY "}, 1.0, "type deploy (weight 1.0)"},
		{"unknown", map[string]string{AttrChangeType: "rollback"}, 0.5, `unknown type "rollback" (weight 0.5)`},
		{"empty", map[string]string{AttrChangeType: ""}, 0.5, "type not set (weight 0.5)"},
		{"attribute missing", map[string]string{"pr": "42"}, 0.5, "type not set (weight 0.5)"},
		{"no attributes", nil, 0.5, "type not set (weight 0.5)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := DefaultWeights()
			e := change("c1", 10*time.Minute, "wallet-service", "")
			e.Attrs = tt.attrs
			c := rankOne(t, symptom("orders-service"), e, w)
			if c.ChangeType != tt.want {
				t.Errorf("ChangeType = %v, want %v", c.ChangeType, tt.want)
			}
			if c.Reasons[2] != tt.wantReason {
				t.Errorf("type reason = %q, want %q", c.Reasons[2], tt.wantReason)
			}
			want := w.TimeProximity*math.Exp(-10.0/15) + w.ChangeType*tt.want
			if !approx(c.Score, want, 1e-12) {
				t.Errorf("Score = %.6f, want %.6f", c.Score, want)
			}
		})
	}
}

func TestCustomTypeWeights(t *testing.T) {
	w := DefaultWeights()
	w.ChangeTypes = map[string]float64{"feature-flag": 0.85}
	c := rankOne(t, symptom(""), change("c1", time.Minute, "", "feature-flag"), w)
	if c.ChangeType != 0.85 || c.Reasons[2] != "type feature-flag (weight 0.85)" {
		t.Errorf("ChangeType = %v, reason %q; want 0.85 and the custom type", c.ChangeType, c.Reasons[2])
	}
	// "deploy" is no longer listed, so it is unknown.
	c = rankOne(t, symptom(""), change("c2", time.Minute, "", "deploy"), w)
	if c.ChangeType != UnknownTypeWeight {
		t.Errorf("unlisted deploy weighs %v, want %v", c.ChangeType, UnknownTypeWeight)
	}
}

func TestRankWindow(t *testing.T) {
	external := change("provider-incident", 5*time.Minute, "orders-service", "")
	external.Kind = event.KindExternal
	alert := change("alert-1", 5*time.Minute, "orders-service", "")
	alert.Kind = event.KindSymptom
	unknownKind := change("weird", 5*time.Minute, "orders-service", "deploy")
	unknownKind.Kind = "deploy"

	events := []event.Event{
		change("after-symptom", -time.Minute, "orders-service", "deploy"),
		change("just-after", -time.Nanosecond, "orders-service", "deploy"),
		change("at-start", 0, "orders-service", "deploy"),
		change("at-lookback-edge", 60*time.Minute, "orders-service", "deploy"),
		change("just-outside-lookback", 60*time.Minute+time.Nanosecond, "orders-service", "deploy"),
		change("two-hours-before", 2*time.Hour, "orders-service", "deploy"),
		external,
		alert,
		unknownKind,
	}
	got := Rank(symptom("orders-service"), events, DefaultWeights())
	want := []string{"at-start", "at-lookback-edge"}
	if !slices.Equal(ids(got), want) {
		t.Fatalf("Rank kept %q, want %q", ids(got), want)
	}
}

func TestRankRespectsCustomLookbackAndTau(t *testing.T) {
	w := DefaultWeights()
	w.Lookback = 2 * time.Hour
	w.Tau = time.Hour
	got := Rank(symptom("orders-service"), []event.Event{
		change("c-90m", 90*time.Minute, "orders-service", "deploy"),
		change("c-3h", 3*time.Hour, "orders-service", "deploy"),
	}, w)
	if !slices.Equal(ids(got), []string{"c-90m"}) {
		t.Fatalf("Rank kept %q, want [c-90m]", ids(got))
	}
	if want := math.Exp(-1.5); !approx(got[0].TimeProximity, want, 1e-12) {
		t.Errorf("TimeProximity = %v, want %v", got[0].TimeProximity, want)
	}
	if got[0].Reasons[0] != "1h30m before the first symptom" {
		t.Errorf("time reason = %q", got[0].Reasons[0])
	}
}

func TestRankOrder(t *testing.T) {
	events := []event.Event{
		change("docs-old", 50*time.Minute, "wallet-service", "docs"),
		change("deploy-same-recent", 3*time.Minute, "orders-service", "deploy"),
		change("config-other", 2*time.Minute, "wallet-service", "config"),
		change("deploy-same-old", 40*time.Minute, "orders-service", "deploy"),
		change("merge-same", 10*time.Minute, "orders-service", "merge"),
	}
	got := Rank(symptom("orders-service"), events, DefaultWeights())
	want := []string{"deploy-same-recent", "merge-same", "config-other", "deploy-same-old", "docs-old"}
	if !slices.Equal(ids(got), want) {
		t.Fatalf("order = %q, want %q", ids(got), want)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Errorf("candidate %d (%.4f) scores above candidate %d (%.4f)", i, got[i].Score, i-1, got[i-1].Score)
		}
	}
}

func TestRankTies(t *testing.T) {
	t.Run("same score and time: by ID", func(t *testing.T) {
		got := Rank(symptom("orders-service"), []event.Event{
			change("c", 5*time.Minute, "orders-service", "deploy"),
			change("a", 5*time.Minute, "orders-service", "deploy"),
			change("b", 5*time.Minute, "Orders-Service", "deploy"),
		}, DefaultWeights())
		if want := []string{"a", "b", "c"}; !slices.Equal(ids(got), want) {
			t.Fatalf("order = %q, want %q", ids(got), want)
		}
	})

	t.Run("same score: closer change first", func(t *testing.T) {
		// With no weight on time, changes far apart can score the same.
		w := DefaultWeights()
		w.TimeProximity, w.SameService, w.ChangeType = 0, 0.5, 0.5
		got := Rank(symptom("orders-service"), []event.Event{
			change("a-far", 30*time.Minute, "orders-service", "deploy"),
			change("z-near", 2*time.Minute, "orders-service", "deploy"),
		}, w)
		if got[0].Score != got[1].Score {
			t.Fatalf("scores differ (%v, %v); the test needs a tie", got[0].Score, got[1].Score)
		}
		if want := []string{"z-near", "a-far"}; !slices.Equal(ids(got), want) {
			t.Fatalf("order = %q, want %q", ids(got), want)
		}
	})

	t.Run("same ID from two sources: by source", func(t *testing.T) {
		fromFile := change("1", 5*time.Minute, "orders-service", "deploy")
		fromFile.Source = "changes-file"
		fromGitHub := change("1", 5*time.Minute, "orders-service", "deploy")
		got := Rank(symptom("orders-service"), []event.Event{fromGitHub, fromFile}, DefaultWeights())
		if got[0].Change.Source != "changes-file" || got[1].Change.Source != "github" {
			t.Fatalf("sources = %q, %q; want changes-file, github", got[0].Change.Source, got[1].Change.Source)
		}
	})

	t.Run("input order does not matter", func(t *testing.T) {
		events := []event.Event{
			change("a", 5*time.Minute, "orders-service", "deploy"),
			change("b", 5*time.Minute, "orders-service", "deploy"),
			change("c", 5*time.Minute, "wallet-service", "config"),
			change("d", 20*time.Minute, "", ""),
			change("e", 20*time.Minute, "", ""),
			change("f", 0, "orders-service", "test"),
		}
		want := ids(Rank(symptom("orders-service"), events, DefaultWeights()))
		rng := rand.New(rand.NewPCG(1, 2)) // fixed seed: the test is repeatable
		for range 50 {
			shuffled := slices.Clone(events)
			rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
			if got := ids(Rank(symptom("orders-service"), shuffled, DefaultWeights())); !slices.Equal(got, want) {
				t.Fatalf("order after shuffle = %q, want %q", got, want)
			}
		}
	})
}

func TestRankEmpty(t *testing.T) {
	onlySymptoms := change("alert-1", time.Minute, "orders-service", "")
	onlySymptoms.Kind = event.KindSymptom
	for name, events := range map[string][]event.Event{
		"nil":           nil,
		"empty":         {},
		"no changes":    {onlySymptoms},
		"all too early": {change("old", 3*time.Hour, "orders-service", "deploy")},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Rank(symptom("orders-service"), events, DefaultWeights()); got != nil {
				t.Fatalf("Rank = %v, want nil", got)
			}
		})
	}
}

func TestRankDoesNotModifyInput(t *testing.T) {
	events := []event.Event{
		change("b", 30*time.Minute, "wallet-service", "docs"),
		change("a", time.Minute, "orders-service", "deploy"),
	}
	before := slices.Clone(events)
	Rank(symptom("orders-service"), events, DefaultWeights())
	for i := range events {
		if events[i].ID != before[i].ID || !events[i].Time.Equal(before[i].Time) {
			t.Fatalf("input changed at %d: %+v, was %+v", i, events[i], before[i])
		}
	}
}

func TestRankWithInvalidWeightsDoesNotPanic(t *testing.T) {
	events := []event.Event{
		change("at-start", 0, "orders-service", "deploy"),
		change("before", 5*time.Minute, "orders-service", "deploy"),
	}
	for name, w := range map[string]Weights{
		"zero value":   {},
		"zero tau":     {TimeProximity: 1, Lookback: time.Hour},
		"NaN weights":  {TimeProximity: math.NaN(), SameService: math.NaN(), Tau: time.Minute, Lookback: time.Hour},
		"huge weights": {TimeProximity: math.Inf(1), ChangeType: 1e300, Tau: time.Minute, Lookback: time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			if w.Validate() == nil {
				t.Fatal("weights are valid; this test needs invalid ones")
			}
			for _, c := range Rank(symptom("orders-service"), events, w) {
				if len(c.Reasons) != 3 {
					t.Errorf("candidate %q has %d reasons, want 3", c.Change.ID, len(c.Reasons))
				}
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                       "0s",
		300 * time.Millisecond:                  "0s",
		700 * time.Millisecond:                  "1s",
		45 * time.Second:                        "45s",
		17 * time.Minute:                        "17m",
		17*time.Minute + 30*time.Second:         "17m30s",
		time.Hour:                               "1h",
		time.Hour + 5*time.Minute:               "1h5m",
		2*time.Hour + 7*time.Second:             "2h7s",
		59*time.Minute + 59600*time.Millisecond: "1h", // rounds up to a whole hour
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestFormatWeight(t *testing.T) {
	for v, want := range map[float64]string{
		0:           "0.0",
		1:           "1.0",
		0.8:         "0.8",
		0.85:        "0.85",
		0.1:         "0.1",
		2:           "2.0",
		math.Inf(1): "+Inf",
	} {
		if got := formatWeight(v); got != want {
			t.Errorf("formatWeight(%v) = %q, want %q", v, got, want)
		}
	}
	if got := formatWeight(math.NaN()); got != "NaN" {
		t.Errorf("formatWeight(NaN) = %q, want NaN", got)
	}
}
