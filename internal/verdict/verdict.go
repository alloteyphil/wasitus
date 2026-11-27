package verdict

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/alloteyphil/wasitus/internal/correlate"
	"github.com/alloteyphil/wasitus/internal/fingerprint"
	"github.com/alloteyphil/wasitus/internal/traffic"
)

// Side is which side of the system boundary the evidence points to.
type Side string

const (
	SideOurSide  Side = "OUR_SIDE"
	SideExternal Side = "EXTERNAL"
	SideUnknown  Side = "UNKNOWN"
)

// Cause narrows Side to a specific cause category.
type Cause string

const (
	CauseNone       Cause = ""
	CauseChange     Cause = "change"
	CauseDependency Cause = "dependency"
	CauseAccount    Cause = "account"
	CauseLimit      Cause = "limit"
	CauseExpiry     Cause = "expiry"
	CauseTraffic    Cause = "traffic"
)

// Confidence reflects how strongly the evidence supports the verdict.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Input bundles the evidence produced by earlier pipeline stages.
type Input struct {
	// Errors is the grouped error fingerprint report for the incident window.
	Errors fingerprint.Report

	// Candidates are the changes correlated against the first symptom. They
	// need not be pre-sorted; Decide orders a copy best-first.
	Candidates []correlate.Candidate

	// Traffic is the optional spike-detection result for the window.
	Traffic *traffic.Result

	// BaselineErrors is the optional error count per cause class from the
	// baseline period, used with traffic.ErrorMixStable to distinguish a pure
	// traffic surge (S6) from a spike that triggered new failure modes (S4).
	BaselineErrors map[fingerprint.Class]int
}

// ClassShare is one cause class's share of total error samples.
type ClassShare struct {
	Class fingerprint.Class `json:"class"`
	Share float64           `json:"share"` // 0..1
	Count int               `json:"count"`
}

// Result is the deterministic verdict with confidence, supporting evidence,
// and top candidates for human inspection.
type Result struct {
	Side       Side                  `json:"side"`
	Cause      Cause                 `json:"cause,omitempty"`
	Confidence Confidence            `json:"confidence"`
	Evidence   []string              `json:"evidence"`
	Change     *correlate.Candidate  `json:"change,omitempty"`     // blamed change for OUR_SIDE / change
	Candidates []correlate.Candidate `json:"candidates,omitempty"` // top candidates (especially on UNKNOWN)
	TopClasses []ClassShare          `json:"top_classes,omitempty"`
}

// Label returns the human-readable verdict heading, such as
// "OUR_SIDE / change", "EXTERNAL / account", or "UNKNOWN".
func (r Result) Label() string {
	if r.Cause == CauseNone {
		return string(r.Side)
	}
	return fmt.Sprintf("%s / %s", r.Side, r.Cause)
}

const (
	shareEpsilon  = 1e-9
	maxCandidates = 3
)

// Decide evaluates in against cfg and returns a deterministic verdict. It
// never fails or panics: an invalid cfg yields UNKNOWN with the config error
// in Evidence.
func Decide(in Input, cfg Config) Result {
	if err := cfg.Validate(); err != nil {
		return Result{
			Side:       SideUnknown,
			Confidence: ConfidenceLow,
			Evidence:   []string{"invalid verdict configuration: " + strings.ReplaceAll(err.Error(), "\n", "; ")},
		}
	}

	candidates := sortedCandidates(in.Candidates)
	classes := rankClasses(in.Errors)

	// 1. Traffic surge: confirmed spike with unchanged error mix (S6).
	// Checked before error classes so steady background errors scaled up by
	// traffic do not mask the surge, while new failure modes (S4 rate limits)
	// fail ErrorMixStable and fall through to the external class rules below.
	if in.Traffic != nil && in.Traffic.IsSpike && errorMixStable(in, cfg.MixTolerance) {
		return decideTrafficSurge(in, candidates, classes, cfg)
	}

	// 2. Dominant error class (requires at least MinErrorSamples).
	if in.Errors.Total >= cfg.MinErrorSamples && len(classes) > 0 && classes[0].Share+shareEpsilon >= cfg.DominanceShare {
		top := classes[0]
		if cause, external := externalCause(top.Class); external {
			return decideExternalClass(in, top, cause, candidates, classes, cfg)
		}
		if top.Class == fingerprint.ClassCode {
			return decideCodeClass(in, top, candidates, classes, cfg)
		}
	}

	// 3. Otherwise: evidence is missing, too thin, or split (S8).
	return decideUnknown(in, candidates, classes, cfg)
}

func decideTrafficSurge(in Input, candidates []correlate.Candidate, classes []ClassShare, cfg Config) Result {
	conf := ConfidenceMedium
	if in.Traffic.Factor >= cfg.HighConfidenceTrafficFactor {
		conf = ConfidenceHigh
	}
	var ev []string
	if in.Traffic.Reason != "" {
		ev = append(ev, in.Traffic.Reason)
	} else {
		ev = append(ev, fmt.Sprintf("traffic %.1fx baseline (%d vs %d expected)",
			in.Traffic.Factor, in.Traffic.WindowRequests, in.Traffic.ExpectedRequests))
	}
	if in.Errors.Total == 0 {
		ev = append(ev, "no new error classes appeared during the surge")
	} else {
		ev = append(ev, fmt.Sprintf("error mix stayed within %s of baseline across %d error sample(s)",
			pct(cfg.MixTolerance), in.Errors.Total))
	}
	if len(candidates) > 0 && candidates[0].Score+shareEpsilon >= cfg.MinChangeScore {
		ev = append(ev, notBlamedChangeLine(candidates[0], "traffic surged with an unchanged error mix"))
	}
	return Result{
		Side:       SideExternal,
		Cause:      CauseTraffic,
		Confidence: conf,
		Evidence:   ev,
		Candidates: topN(candidates, maxCandidates),
		TopClasses: classes,
	}
}

func decideExternalClass(
	in Input,
	top ClassShare,
	cause Cause,
	candidates []correlate.Candidate,
	classes []ClassShare,
	cfg Config,
) Result {
	conf := ConfidenceMedium
	if top.Share+shareEpsilon >= cfg.HighConfidenceShare && in.Errors.Total >= cfg.HighConfidenceSamples {
		conf = ConfidenceHigh
	}

	ev := []string{
		fmt.Sprintf("%s of errors (%d/%d) classified as %s", pct(top.Share), top.Count, in.Errors.Total, top.Class),
	}
	if g, ok := topGroupForClass(in.Errors.Groups, top.Class); ok {
		ev = append(ev, formatGroupEvidence(g))
	}
	if in.Traffic != nil && in.Traffic.IsSpike && in.Traffic.Reason != "" {
		ev = append(ev, "traffic also spiked: "+in.Traffic.Reason)
	}
	if len(candidates) > 0 && candidates[0].Score+shareEpsilon >= cfg.MinChangeScore {
		ev = append(ev, notBlamedChangeLine(candidates[0],
			fmt.Sprintf("%s of errors are %s failures", pct(top.Share), top.Class)))
	} else if len(candidates) == 0 {
		ev = append(ev, "no changes recorded in the lookback window")
	}

	return Result{
		Side:       SideExternal,
		Cause:      cause,
		Confidence: conf,
		Evidence:   ev,
		Candidates: topN(candidates, maxCandidates),
		TopClasses: classes,
	}
}

func decideCodeClass(
	in Input,
	top ClassShare,
	candidates []correlate.Candidate,
	classes []ClassShare,
	cfg Config,
) Result {
	ev := []string{
		fmt.Sprintf("%s of errors (%d/%d) classified as code", pct(top.Share), top.Count, in.Errors.Total),
	}
	if g, ok := topGroupForClass(in.Errors.Groups, fingerprint.ClassCode); ok {
		ev = append(ev, formatGroupEvidence(g))
	}

	if len(candidates) > 0 && candidates[0].Score+shareEpsilon >= cfg.MinChangeScore {
		best := candidates[0]
		conf := ConfidenceMedium
		if top.Share+shareEpsilon >= cfg.HighConfidenceShare &&
			in.Errors.Total >= cfg.HighConfidenceSamples &&
			best.Score+shareEpsilon >= cfg.HighConfidenceChangeScore {
			conf = ConfidenceHigh
		}
		ev = append(ev, formatChangeEvidence(best))
		return Result{
			Side:       SideOurSide,
			Cause:      CauseChange,
			Confidence: conf,
			Evidence:   ev,
			Change:     &best,
			Candidates: topN(candidates, maxCandidates),
			TopClasses: classes,
		}
	}

	if len(candidates) == 0 {
		ev = append(ev, "no changes in the lookback window to attribute the code errors to")
	} else {
		ev = append(ev, fmt.Sprintf("best change candidate %q scored %.2f, below the %.2f threshold",
			candidates[0].Change.Title, candidates[0].Score, cfg.MinChangeScore))
	}
	return Result{
		Side:       SideUnknown,
		Confidence: ConfidenceLow,
		Evidence:   ev,
		Candidates: topN(candidates, maxCandidates),
		TopClasses: classes,
	}
}

func decideUnknown(in Input, candidates []correlate.Candidate, classes []ClassShare, cfg Config) Result {
	var ev []string
	switch {
	case in.Errors.Total == 0:
		ev = append(ev, "no error samples observed in the window")
		if in.Traffic != nil && in.Traffic.Reason != "" {
			ev = append(ev, in.Traffic.Reason)
		}
	case in.Errors.Total < cfg.MinErrorSamples:
		ev = append(ev, fmt.Sprintf("only %d error sample(s) observed (need at least %d to decide)",
			in.Errors.Total, cfg.MinErrorSamples))
	case len(classes) > 0:
		parts := make([]string, 0, min(len(classes), 3))
		for _, c := range classes[:min(len(classes), 3)] {
			parts = append(parts, fmt.Sprintf("%s %s", c.Class, pct(c.Share)))
		}
		ev = append(ev, fmt.Sprintf("no error class reached %s (%s across %d samples)",
			pct(cfg.DominanceShare), strings.Join(parts, ", "), in.Errors.Total))
	}

	if len(candidates) == 0 {
		ev = append(ev, "no changes recorded in the lookback window")
	} else {
		ev = append(ev, fmt.Sprintf("top change candidate: %s (score %.2f)",
			candidates[0].Change.Title, candidates[0].Score))
	}

	return Result{
		Side:       SideUnknown,
		Confidence: ConfidenceLow,
		Evidence:   ev,
		Candidates: topN(candidates, maxCandidates),
		TopClasses: classes,
	}
}

func errorMixStable(in Input, tolerance float64) bool {
	winCounts := make(map[string]int, len(in.Errors.Groups))
	for _, g := range in.Errors.Groups {
		if g.Count > 0 {
			winCounts[string(g.Class)] += g.Count
		}
	}
	if len(winCounts) == 0 && in.Errors.Total > 0 {
		for class, share := range in.Errors.Shares {
			if n := int(math.Round(share * float64(in.Errors.Total))); n > 0 {
				winCounts[string(class)] = n
			}
		}
	}
	baseCounts := make(map[string]int, len(in.BaselineErrors))
	for class, n := range in.BaselineErrors {
		if n > 0 {
			baseCounts[string(class)] = n
		}
	}
	return traffic.ErrorMixStable(baseCounts, winCounts, tolerance)
}

func externalCause(c fingerprint.Class) (Cause, bool) {
	switch c {
	case fingerprint.ClassDependency:
		return CauseDependency, true
	case fingerprint.ClassAccount:
		return CauseAccount, true
	case fingerprint.ClassLimit:
		return CauseLimit, true
	case fingerprint.ClassExpiry:
		return CauseExpiry, true
	default:
		return CauseNone, false
	}
}

func rankClasses(rep fingerprint.Report) []ClassShare {
	if rep.Total <= 0 {
		return nil
	}
	counts := make(map[fingerprint.Class]int)
	for _, g := range rep.Groups {
		if g.Count > 0 {
			counts[g.Class] += g.Count
		}
	}
	if len(counts) == 0 {
		for class, share := range rep.Shares {
			if share > 0 {
				counts[class] = int(math.Round(share * float64(rep.Total)))
			}
		}
	}
	out := make([]ClassShare, 0, len(counts))
	for class, n := range counts {
		share := rep.Shares[class]
		if share == 0 && n > 0 {
			share = float64(n) / float64(rep.Total)
		}
		out = append(out, ClassShare{Class: class, Share: share, Count: n})
	}
	slices.SortFunc(out, func(a, b ClassShare) int {
		return cmp.Or(
			cmp.Compare(b.Share, a.Share),
			cmp.Compare(b.Count, a.Count),
			cmp.Compare(a.Class, b.Class),
		)
	})
	return out
}

func sortedCandidates(in []correlate.Candidate) []correlate.Candidate {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.SortStableFunc(out, func(a, b correlate.Candidate) int {
		return cmp.Or(
			cmp.Compare(b.Score, a.Score),
			cmp.Compare(a.Before, b.Before),
			cmp.Compare(a.Change.ID, b.Change.ID),
			cmp.Compare(a.Change.Source, b.Change.Source),
		)
	})
	return out
}

func topGroupForClass(groups []fingerprint.Group, class fingerprint.Class) (fingerprint.Group, bool) {
	var best fingerprint.Group
	found := false
	for _, g := range groups {
		if g.Class != class {
			continue
		}
		if !found || g.Count > best.Count ||
			(g.Count == best.Count && cmp.Or(cmp.Compare(g.Service, best.Service), cmp.Compare(g.Signature, best.Signature)) < 0) {
			best, found = g, true
		}
	}
	return best, found
}

func formatGroupEvidence(g fingerprint.Group) string {
	svc := g.Service
	if svc == "" {
		svc = "unknown service"
	}
	return fmt.Sprintf("top error group in %s: %s (%d occurrences, rule %s)", svc, g.Signature, g.Count, g.RuleID)
}

func formatChangeEvidence(c correlate.Candidate) string {
	if len(c.Reasons) == 0 {
		return fmt.Sprintf("correlated change: %s (score %.2f)", c.Change.Title, c.Score)
	}
	return fmt.Sprintf("correlated change: %s (score %.2f: %s)", c.Change.Title, c.Score, strings.Join(c.Reasons, "; "))
}

func notBlamedChangeLine(c correlate.Candidate, because string) string {
	return fmt.Sprintf("recent change %q (score %.2f) was not blamed because %s", c.Change.Title, c.Score, because)
}

func topN(in []correlate.Candidate, n int) []correlate.Candidate {
	if len(in) == 0 {
		return nil
	}
	return in[:min(len(in), n)]
}

func pct(v float64) string {
	return fmt.Sprintf("%.0f%%", math.Round(v*100))
}
