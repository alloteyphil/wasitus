package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/alloteyphil/wasitus/internal/collector"
	"github.com/alloteyphil/wasitus/internal/collector/changesfile"
	"github.com/alloteyphil/wasitus/internal/collector/github"
	"github.com/alloteyphil/wasitus/internal/collector/signoz"
	"github.com/alloteyphil/wasitus/internal/config"
	"github.com/alloteyphil/wasitus/internal/event"
	"github.com/alloteyphil/wasitus/internal/redact"
	"github.com/alloteyphil/wasitus/internal/render"
)

func runTimeline(args []string, stdout, stderr io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("wasitus timeline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "wasitus.yaml", "path to the config file")
	fromFlag := fs.String("from", "", "start of the incident window (required)")
	toFlag := fs.String("to", "now", "end of the incident window")
	timeout := fs.Duration("timeout", time.Minute, "maximum time to spend collecting")
	formatFlag := fs.String("format", string(render.FormatTable), "output format: table, markdown, or json")
	noRedact := fs.Bool("no-redact", false, "disable PII and secret redaction in output")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wasitus timeline: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	if *fromFlag == "" {
		fmt.Fprintln(stderr, "wasitus timeline: --from is required")
		return exitUsage
	}
	format, err := render.ParseFormat(*formatFlag)
	if err != nil {
		fmt.Fprintf(stderr, "wasitus timeline: --format: %v\n", err)
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "wasitus: %v\n", err)
		return exitError
	}

	from, err := event.ParseTime(*fromFlag, cfg.Location(), now)
	if err != nil {
		fmt.Fprintf(stderr, "wasitus timeline: --from: %v\n", err)
		return exitUsage
	}
	to, err := event.ParseTime(*toFlag, cfg.Location(), now)
	if err != nil {
		fmt.Fprintf(stderr, "wasitus timeline: --to: %v\n", err)
		return exitUsage
	}
	win, err := event.NewWindow(from, to)
	if err != nil {
		fmt.Fprintf(stderr, "wasitus timeline: %v\n", err)
		return exitUsage
	}

	userRules, err := loadUserRules(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "wasitus: %v\n", err)
		return exitError
	}

	collectors, sz, err := buildCollectors(cfg, os.LookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "wasitus: %v\n", err)
		return exitError
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	res := collector.Run(ctx, win.WithLookback(cfg.Lookback), collectors)

	var analysis analysisResult
	if sz != nil && !hasFatalSigNozFailure(res.Failures) && ctx.Err() == nil {
		analysis = analyzeIncident(ctx, sz, cfg, userRules, win, res.Events)
	}

	var warnings []string
	for _, c := range collectors {
		if wr, ok := c.(warningReporter); ok {
			appendPrefixed(&warnings, c.Name()+": ", wr.Warnings())
		}
	}
	warnings = append(warnings, analysis.Warnings...)
	warnings = dedupeStrings(warnings)

	failures := make([]error, 0, len(res.Failures)+len(analysis.Failures))
	for _, f := range res.Failures {
		failures = append(failures, f)
	}
	failures = append(failures, analysis.Failures...)

	var redactor *redact.Redactor
	if !*noRedact {
		redactor = redact.New()
	}

	err = render.Render(stdout, format, render.Timeline{
		Window:      win,
		Lookback:    cfg.Lookback,
		Location:    cfg.Location(),
		Events:      res.Events,
		ErrorGroups: analysis.ErrorGroups,
		Verdict:     analysis.Verdict,
		Warnings:    warnings,
		Redactor:    redactor,
	})
	if err != nil {
		fmt.Fprintf(stderr, "wasitus: write output: %v\n", err)
		return exitError
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "warning: %s\n", redactor.String(w))
	}
	for _, f := range failures {
		fmt.Fprintf(stderr, "warning: %s\n", redactor.String(f.Error()))
	}
	if len(failures) > 0 {
		return exitPartial
	}
	return exitOK
}

// hasFatalSigNozFailure reports whether the SigNoz collector already failed
// with an auth error, so analyzeIncident does not repeat doomed requests.
func hasFatalSigNozFailure(failures []collector.Failure) bool {
	for _, f := range failures {
		if f.Collector == signoz.Source && (errors.Is(f.Err, signoz.ErrUnauthorized) || errors.Is(f.Err, signoz.ErrForbidden)) {
			return true
		}
	}
	return false
}

// warningReporter is implemented by collectors that record non-fatal warnings
// (for example, an untested SigNoz server version or a query warning).
type warningReporter interface {
	Warnings() []string
}

// githubAPI is the GitHub REST API root. Tests point it at a local server.
var githubAPI = github.DefaultBaseURL

// httpRequestTimeout bounds each individual API request; --timeout bounds the run.
const httpRequestTimeout = 30 * time.Second

// buildCollectors creates a collector for each configured source. Secrets are
// read with lookupEnv (normally os.LookupEnv).
func buildCollectors(cfg *config.Config, lookupEnv func(string) (string, bool)) (cs []collector.Collector, szCollector *signoz.Collector, err error) {
	if p := cfg.ChangesFilePath(); p != "" {
		cs = append(cs, changesfile.New(p, cfg.Location()))
	}
	if sz := cfg.Sources.SigNoz; sz != nil {
		c, err := newSigNozCollector(sz, lookupEnv)
		if err != nil {
			return nil, nil, err
		}
		cs = append(cs, c)
		szCollector = c
	}
	if gh := cfg.Sources.GitHub; gh != nil {
		c, err := newGitHubCollector(gh, cfg.ServiceFor, lookupEnv)
		if err != nil {
			return nil, nil, err
		}
		cs = append(cs, c)
	}
	if len(cs) == 0 {
		return nil, nil, errors.New("no collectors configured for the enabled sources")
	}
	return cs, szCollector, nil
}

// newSigNozCollector reads the API key from the environment variable named in
// the config. When api_key_env is set, a missing or empty variable is an error
// so a typo never silently sends unauthenticated requests.
func newSigNozCollector(sz *config.SigNoz, lookupEnv func(string) (string, bool)) (*signoz.Collector, error) {
	var apiKey string
	if sz.APIKeyEnv != "" {
		k, err := config.Secret(sz.APIKeyEnv, lookupEnv)
		if err != nil {
			return nil, fmt.Errorf("sources.signoz: %w; it must hold a read-only SigNoz API key", err)
		}
		apiKey = k
	}
	return signoz.New(sz.URL, apiKey, &http.Client{Timeout: httpRequestTimeout})
}

// newGitHubCollector reads the token from the environment variable named in
// the config. A variable that is named but missing or empty is an error, so a
// typo never silently falls back to anonymous, rate-limited access.
func newGitHubCollector(gh *config.GitHub, serviceFor func(string) string, lookupEnv func(string) (string, bool)) (*github.Collector, error) {
	var token string
	if gh.TokenEnv != "" {
		t, err := config.Secret(gh.TokenEnv, lookupEnv)
		if err != nil {
			return nil, fmt.Errorf("sources.github: %w; it must hold a read-only GitHub token", err)
		}
		token = t
	}
	return github.New(gh.Repos, token, githubAPI, &http.Client{Timeout: httpRequestTimeout}, serviceFor)
}
