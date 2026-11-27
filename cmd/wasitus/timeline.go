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
	"github.com/alloteyphil/wasitus/internal/render"
)

func runTimeline(args []string, stdout, stderr io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("wasitus timeline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "wasitus.yaml", "path to the config file")
	fromFlag := fs.String("from", "", "start of the incident window (required)")
	toFlag := fs.String("to", "now", "end of the incident window")
	timeout := fs.Duration("timeout", time.Minute, "maximum time to spend collecting")
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

	collectors, err := buildCollectors(cfg, os.LookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "wasitus: %v\n", err)
		return exitError
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	res := collector.Run(ctx, win.WithLookback(cfg.Lookback), collectors)

	err = render.Table(stdout, render.Timeline{
		Window:   win,
		Lookback: cfg.Lookback,
		Location: cfg.Location(),
		Events:   res.Events,
	})
	if err != nil {
		fmt.Fprintf(stderr, "wasitus: write output: %v\n", err)
		return exitError
	}
	for _, c := range collectors {
		if wr, ok := c.(warningReporter); ok {
			for _, w := range wr.Warnings() {
				fmt.Fprintf(stderr, "warning: %s: %s\n", c.Name(), w)
			}
		}
	}
	for _, f := range res.Failures {
		fmt.Fprintf(stderr, "warning: %v\n", f)
	}
	if len(res.Failures) > 0 {
		return exitPartial
	}
	return exitOK
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
func buildCollectors(cfg *config.Config, lookupEnv func(string) (string, bool)) (cs []collector.Collector, err error) {
	if p := cfg.ChangesFilePath(); p != "" {
		cs = append(cs, changesfile.New(p, cfg.Location()))
	}
	if sz := cfg.Sources.SigNoz; sz != nil {
		c, err := newSigNozCollector(sz, lookupEnv)
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	if gh := cfg.Sources.GitHub; gh != nil {
		c, err := newGitHubCollector(gh, cfg.ServiceFor, lookupEnv)
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	if len(cs) == 0 {
		return nil, errors.New("no collectors configured for the enabled sources")
	}
	return cs, nil
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
