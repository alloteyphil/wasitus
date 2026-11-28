package main

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/alloteyphil/wasitus/internal/collector/signoz"
	"github.com/alloteyphil/wasitus/internal/config"
	"github.com/alloteyphil/wasitus/internal/correlate"
	"github.com/alloteyphil/wasitus/internal/event"
	"github.com/alloteyphil/wasitus/internal/fingerprint"
	"github.com/alloteyphil/wasitus/internal/render"
	"github.com/alloteyphil/wasitus/internal/traffic"
	"github.com/alloteyphil/wasitus/internal/verdict"
)

// analysisResult holds the outputs of stages 3–5 (fingerprint, traffic,
// correlate, verdict) for one CLI run.
type analysisResult struct {
	ErrorGroups []render.ErrorGroup
	Verdict     *render.Verdict
	Warnings    []string
	Failures    []error
}

// loadUserRules reads and validates the optional user fingerprint rules file
// upfront so a bad rules file fails fast before any network calls.
func loadUserRules(cfg *config.Config) ([]byte, error) {
	path := cfg.RulesFilePath()
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules_file: %w", err)
	}
	if _, err := fingerprint.LoadRules(data, fingerprint.Options{}); err != nil {
		return nil, fmt.Errorf("load rules_file %s: %w", path, err)
	}
	return data, nil
}

// analyzeIncident runs stages 3–5 (fingerprinting, traffic spike detection,
// change correlation, and verdict) using telemetry from sz. Non-fatal query
// failures are returned in Failures so the timeline still renders with
// exitPartial, and each one is passed to the verdict as a missing input so it
// is never read as "nothing found". changesMissing reports that a change
// source (GitHub, changes file) failed during collection.
func analyzeIncident(
	ctx context.Context,
	sz *signoz.Collector,
	cfg *config.Config,
	userRules []byte,
	win event.Window,
	events []event.Event,
	changesMissing bool,
) analysisResult {
	var out analysisResult
	client := sz.Client()
	missing := verdict.Missing{Changes: changesMissing}

	samples, w1, err := client.ErrorSamples(ctx, win)
	appendPrefixed(&out.Warnings, "signoz: ", w1)
	if err != nil {
		out.Failures = append(out.Failures, fmt.Errorf("signoz: error samples: %w", err))
		missing.Errors = true
	}

	tcfg := traffic.DefaultConfig()
	trafficWin := alignTrafficWindow(win, tcfg.BucketSize)
	winBuckets, w2, err := client.TrafficBuckets(ctx, trafficWin, tcfg.BucketSize)
	appendPrefixed(&out.Warnings, "signoz: ", w2)
	if err != nil {
		out.Failures = append(out.Failures, fmt.Errorf("signoz: window traffic: %w", err))
		missing.Traffic = true
	}

	var baseBuckets map[string][]signoz.TrafficBucket
	if len(winBuckets) > 0 {
		// Include one extra bucket before day BaselineDays so traffic.Analyze
		// has the n = -1 neighbor slot on the oldest baseline day.
		baseWin := event.Window{
			From: trafficWin.From.Add(-time.Duration(tcfg.BaselineDays)*24*time.Hour - tcfg.BucketSize),
			To:   trafficWin.From,
		}
		var w3 []string
		baseBuckets, w3, err = client.TrafficBuckets(ctx, baseWin, tcfg.BucketSize)
		appendPrefixed(&out.Warnings, "signoz: ", w3)
		if err != nil {
			out.Failures = append(out.Failures, fmt.Errorf("signoz: baseline traffic: %w", err))
			missing.Traffic = true
		}
	}

	hosts := collectInternalHosts(cfg, events, samples, winBuckets, baseBuckets)
	rs, err := fingerprint.LoadRules(userRules, fingerprint.Options{InternalHosts: hosts})
	if err != nil {
		// userRules was already validated by loadUserRules, so this should not happen.
		out.Failures = append(out.Failures, fmt.Errorf("fingerprint rules: %w", err))
		return out
	}

	fpReport := rs.Group(toFingerprintSamples(samples))
	trafficRes := analyzeTraffic(winBuckets, baseBuckets, tcfg)

	var baselineErrors map[fingerprint.Class]int
	if trafficRes != nil && trafficRes.IsSpike && fpReport.Total > 0 && cfg.Lookback > 0 {
		lbFrom := win.From.Add(-cfg.Lookback)
		lbTo := win.From.Add(-time.Millisecond)
		if lbTo.After(lbFrom) {
			lbWin := event.Window{From: lbFrom, To: lbTo}
			baseSamples, w4, err := client.ErrorSamples(ctx, lbWin)
			appendPrefixed(&out.Warnings, "signoz: ", w4)
			if err != nil {
				out.Failures = append(out.Failures, fmt.Errorf("signoz: baseline errors: %w", err))
				missing.BaselineErrors = true
			}
			// Keep partial samples (e.g., spans succeeded while logs failed) and
			// exclude anything at or after win.From so window errors never leak into baseline.
			filtered := filterBefore(baseSamples, win.From)
			if len(filtered) > 0 {
				baselineErrors = classCounts(rs, filtered)
			}
		}
	}

	candidates := rankChanges(events, fpReport, win, cfg.Lookback)
	v := verdict.Decide(verdict.Input{
		Errors:         fpReport,
		Candidates:     candidates,
		Traffic:        trafficRes,
		BaselineErrors: baselineErrors,
		Missing:        missing,
	}, verdict.DefaultConfig())

	out.ErrorGroups = toRenderErrorGroups(fpReport.Groups)
	out.Verdict = toRenderVerdict(v)
	return out
}

// collectInternalHosts gathers the names of our own services so outbound calls
// between internal services stay classified as ClassCode rather than third-party
// dependencies. External dependencies (like mockpay) never emit telemetry to our
// SigNoz instance or appear in cfg.Services.
func collectInternalHosts(
	cfg *config.Config,
	events []event.Event,
	samples []signoz.ErrorSample,
	winBuckets, baseBuckets map[string][]signoz.TrafficBucket,
) []string {
	seen := map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
	}
	add := func(s string) {
		s = strings.TrimSpace(s)
		// Reject empty names and leading-dot domain wildcards from untrusted event data.
		if s == "" || strings.HasPrefix(s, ".") {
			return
		}
		seen[s] = true
	}
	for _, svc := range cfg.Services {
		add(svc)
	}
	// internal_hosts comes from the operator's own config, so unlike event
	// data it may name a whole domain (".corp.example").
	for _, h := range cfg.InternalHosts {
		if h = strings.TrimSpace(h); h != "" {
			seen[h] = true
		}
	}
	for _, e := range events {
		if e.Kind == event.KindExternal {
			continue
		}
		add(e.Service)
	}
	for _, s := range samples {
		add(s.Service)
	}
	for svc := range winBuckets {
		add(svc)
	}
	for svc := range baseBuckets {
		add(svc)
	}
	return slices.Sorted(maps.Keys(seen))
}

func filterBefore(in []signoz.ErrorSample, cutoff time.Time) []signoz.ErrorSample {
	if len(in) == 0 {
		return nil
	}
	out := make([]signoz.ErrorSample, 0, len(in))
	for _, s := range in {
		if s.Time.Before(cutoff) {
			out = append(out, s)
		}
	}
	return out
}

// alignTrafficWindow snaps w to whole step-wide UTC boundaries so partial edge
// buckets do not dilute Factor when compared against full baseline slots.
func alignTrafficWindow(w event.Window, step time.Duration) event.Window {
	from := w.From.UTC().Truncate(step)
	to := w.To.UTC().Truncate(step)
	if !to.After(from) {
		to = from.Add(step)
	}
	return event.Window{From: from, To: to}
}

func toFingerprintSamples(in []signoz.ErrorSample) []fingerprint.Sample {
	if len(in) == 0 {
		return nil
	}
	out := make([]fingerprint.Sample, len(in))
	for i, s := range in {
		out[i] = fingerprint.Sample{
			Time:          s.Time,
			Service:       s.Service,
			Outbound:      s.Outbound,
			HTTPStatus:    s.HTTPStatus,
			ServerAddress: s.ServerAddress,
			ExceptionType: s.ExceptionType,
			Message:       s.Message,
			Attrs:         s.Attrs,
		}
	}
	return out
}

func classCounts(rs *fingerprint.RuleSet, samples []signoz.ErrorSample) map[fingerprint.Class]int {
	counts := make(map[fingerprint.Class]int)
	for _, s := range toFingerprintSamples(samples) {
		class, _ := rs.Classify(s)
		counts[class]++
	}
	return counts
}

// minSpikeExpectedPerBucket is the least baseline traffic, in requests per
// bucket, a service needs before its spike can count as a surge.
const minSpikeExpectedPerBucket = 10

// analyzeTraffic runs traffic.Analyze per service (so a spike on one entry
// service is not diluted by quiet services) and returns the strongest spike,
// or the highest-factor non-spike result when traffic was observed.
func analyzeTraffic(
	winBuckets, baseBuckets map[string][]signoz.TrafficBucket,
	cfg traffic.Config,
) *traffic.Result {
	if len(winBuckets) == 0 {
		return nil
	}
	services := slices.Sorted(maps.Keys(winBuckets))

	var bestSpike *traffic.Result
	var bestSpikeSvc string
	var bestAny *traffic.Result

	for _, svc := range services {
		res := traffic.Analyze(toTrafficBuckets(winBuckets[svc]), toTrafficBuckets(baseBuckets[svc]), cfg)
		if res.IsSpike && res.ExpectedRequests < minSpikeExpectedPerBucket*res.WindowBuckets {
			// A near-idle service (say 6 requests against 0 expected) gives a
			// large factor from tiny numbers; it is not a surge.
			res.IsSpike = false
			res.Reason = fmt.Sprintf("%s (ignored: under %d expected requests per bucket)", res.Reason, minSpikeExpectedPerBucket)
		}
		if res.IsSpike {
			if bestSpike == nil || res.Factor > bestSpike.Factor {
				cp := res
				bestSpike = &cp
				bestSpikeSvc = svc
			}
		}
		if bestAny == nil || res.Factor > bestAny.Factor {
			cp := res
			bestAny = &cp
		}
	}

	if bestSpike != nil {
		if len(services) > 1 && bestSpikeSvc != "" {
			bestSpike.Reason = bestSpikeSvc + ": " + bestSpike.Reason
		}
		return bestSpike
	}
	return bestAny
}

func toTrafficBuckets(in []signoz.TrafficBucket) []traffic.Bucket {
	if len(in) == 0 {
		return nil
	}
	out := make([]traffic.Bucket, len(in))
	for i, b := range in {
		out[i] = traffic.Bucket{
			Start:    b.Start,
			Requests: b.Requests,
			Errors:   b.Errors,
		}
	}
	return out
}

// rankChanges ranks changes against each failing service's earliest symptom in
// win, keeping each change's highest score so upstream error propagation does
// not hide a same-service deploy.
func rankChanges(
	events []event.Event,
	fp fingerprint.Report,
	win event.Window,
	lookback time.Duration,
) []correlate.Candidate {
	w := correlate.DefaultWeights()
	if lookback > 0 {
		w.Lookback = lookback
	}

	targets := symptomTargets(events, fp, win)
	// A change made after the incident had already started (a responder's
	// rollback or hotfix) cannot have caused it, even if it came before a
	// later cascaded symptom in another service.
	start := targets[0].Start
	for _, sym := range targets[1:] {
		if sym.Start.Before(start) {
			start = sym.Start
		}
	}
	events = slices.DeleteFunc(slices.Clone(events), func(e event.Event) bool {
		return e.Kind == event.KindChange && e.Time.After(start)
	})

	type changeKey struct {
		id     string
		source string
	}
	best := make(map[changeKey]correlate.Candidate)
	for _, sym := range targets {
		for _, cand := range correlate.Rank(sym, events, w) {
			k := changeKey{id: cand.Change.ID, source: cand.Change.Source}
			if prev, ok := best[k]; !ok || cand.Score > prev.Score ||
				(cand.Score == prev.Score && cand.Before < prev.Before) {
				best[k] = cand
			}
		}
	}
	if len(best) == 0 {
		return nil
	}
	out := make([]correlate.Candidate, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
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

// minSymptomShare is the least share of the window's errors a service needs
// for its first error to count as a symptom when ranking changes.
const minSymptomShare = 0.10

// symptomTargets returns the earliest symptom time per failing service within
// win (falling back to the earliest unattributed symptom or win.From).
func symptomTargets(events []event.Event, fp fingerprint.Report, win event.Window) []correlate.Symptom {
	earliestBySvc := make(map[string]time.Time)
	record := func(svc string, t time.Time) {
		if t.IsZero() || t.Before(win.From) || t.After(win.To) {
			return
		}
		if prev, ok := earliestBySvc[svc]; !ok || t.Before(prev) {
			earliestBySvc[svc] = t
		}
	}

	for _, e := range events {
		if e.Kind == event.KindSymptom {
			record(e.Service, e.Time)
		}
	}
	// A service with only a stray error or two is not a failing service, so
	// it must not pull in a change made to it (for example one wallet error
	// right after a wallet config change, while orders is panicking).
	perSvc := make(map[string]int)
	for _, g := range fp.Groups {
		perSvc[g.Service] += g.Count
	}
	for _, g := range fp.Groups {
		if fp.Total > 0 && float64(perSvc[g.Service]) < minSymptomShare*float64(fp.Total) {
			continue
		}
		record(g.Service, g.First)
	}

	if len(earliestBySvc) == 0 {
		return []correlate.Symptom{{Start: win.From}}
	}
	// If we have service-attributed symptoms, drop the empty-service bucket so
	// unattributed alerts do not dilute service matching.
	if len(earliestBySvc) > 1 {
		delete(earliestBySvc, "")
	}
	svcs := slices.Sorted(maps.Keys(earliestBySvc))
	out := make([]correlate.Symptom, 0, len(svcs))
	for _, svc := range svcs {
		out = append(out, correlate.Symptom{Start: earliestBySvc[svc], Service: svc})
	}
	return out
}

func toRenderErrorGroups(groups []fingerprint.Group) []render.ErrorGroup {
	if len(groups) == 0 {
		return nil
	}
	out := make([]render.ErrorGroup, len(groups))
	for i, g := range groups {
		out[i] = render.ErrorGroup{
			Class:     string(g.Class),
			Service:   g.Service,
			Signature: g.Signature,
			RuleID:    g.RuleID,
			Count:     g.Count,
			Share:     g.Share,
		}
	}
	return out
}

func toRenderVerdict(v verdict.Result) *render.Verdict {
	return &render.Verdict{
		Label:      v.Label(),
		Side:       string(v.Side),
		Cause:      string(v.Cause),
		Confidence: string(v.Confidence),
		Evidence:   v.Evidence,
	}
}

func appendPrefixed(dst *[]string, prefix string, items []string) {
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		*dst = append(*dst, prefix+s)
	}
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
