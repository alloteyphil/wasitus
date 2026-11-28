package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	defaultTimezone = "UTC"
	defaultLookback = 60 * time.Minute
	maxLookback     = 24 * time.Hour
)

// Config is the parsed contents of wasitus.yaml.
type Config struct {
	Timezone  string            `yaml:"timezone"`
	Lookback  time.Duration     `yaml:"lookback"`
	Sources   Sources           `yaml:"sources"`
	Services  map[string]string `yaml:"services"` // repository (owner/name) -> telemetry service name
	RulesFile string            `yaml:"rules_file"`

	location *time.Location
	baseDir  string // directory of the config file; relative paths resolve against it
}

// Sources lists where wasitus reads events from. A nil source is not configured.
type Sources struct {
	SigNoz      *SigNoz `yaml:"signoz"`
	GitHub      *GitHub `yaml:"github"`
	ChangesFile string  `yaml:"changes_file"`
}

// SigNoz is a SigNoz instance to read alerts, errors and traffic from.
type SigNoz struct {
	URL       string `yaml:"url"`
	APIKeyEnv string `yaml:"api_key_env"` // name of the env var holding the API key; optional
}

// GitHub is the set of repositories to read changes (PRs, deployments, releases) from.
type GitHub struct {
	TokenEnv string   `yaml:"token_env"` // name of the env var holding a read-only token
	Repos    []string `yaml:"repos"`
}

var (
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	repoPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data, filepath.Dir(path))
}

// Parse decodes and validates a config. Relative paths inside it are resolved
// against baseDir. Unknown keys are rejected so typos do not pass silently.
func Parse(data []byte, baseDir string) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config is empty")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.baseDir = baseDir
	if c.Timezone == "" {
		c.Timezone = defaultTimezone
	}
	if c.Lookback == 0 {
		c.Lookback = defaultLookback
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// validate reports every problem at once so the user can fix them in one go.
func (c *Config) validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		add("timezone: unknown %q", c.Timezone)
	}
	c.location = loc

	if c.Lookback < 0 || c.Lookback > maxLookback {
		add("lookback must be between 0 and %s, got %s", maxLookback, c.Lookback)
	}

	s := c.Sources
	if s.SigNoz == nil && s.GitHub == nil && s.ChangesFile == "" {
		add("no sources configured: set at least one of sources.signoz, sources.github or sources.changes_file")
	}
	if s.SigNoz != nil {
		if s.SigNoz.URL == "" {
			add("sources.signoz.url is required")
		} else if strings.Contains(s.SigNoz.URL, "@") {
			// Never echo the URL here: it may hold a password, and this
			// message goes to stderr. Credentials belong in api_key_env.
			add("sources.signoz.url must not contain user info (user:password@); put the API key in the variable named by api_key_env")
		} else if u, err := url.Parse(s.SigNoz.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("sources.signoz.url %q must be an http or https URL", s.SigNoz.URL)
		}
		if s.SigNoz.APIKeyEnv != "" && !envNamePattern.MatchString(s.SigNoz.APIKeyEnv) {
			add("sources.signoz.api_key_env %q is not a valid environment variable name", s.SigNoz.APIKeyEnv)
		}
	}
	if s.GitHub != nil {
		switch {
		case s.GitHub.TokenEnv == "":
			add("sources.github.token_env is required")
		case !envNamePattern.MatchString(s.GitHub.TokenEnv):
			add("sources.github.token_env %q is not a valid environment variable name", s.GitHub.TokenEnv)
		}
		if len(s.GitHub.Repos) == 0 {
			add("sources.github.repos must list at least one repository")
		}
		for _, r := range s.GitHub.Repos {
			if !repoPattern.MatchString(r) {
				add("sources.github.repos: %q is not in owner/name form", r)
			}
		}
	}
	for repo, service := range c.Services {
		if !repoPattern.MatchString(repo) {
			add("services: %q is not in owner/name form", repo)
		}
		if service == "" {
			add("services: %q maps to an empty service name", repo)
		}
	}
	return errors.Join(errs...)
}

// Location is the time zone used to read and display times.
func (c *Config) Location() *time.Location { return c.location }

// ChangesFilePath is the changes file path resolved against the config file's
// directory, or "" if no changes file is configured.
func (c *Config) ChangesFilePath() string { return c.resolve(c.Sources.ChangesFile) }

// RulesFilePath is the optional user fingerprint rules file path resolved
// against the config file's directory, or "" if not configured.
func (c *Config) RulesFilePath() string { return c.resolve(c.RulesFile) }

// ServiceFor returns the telemetry service name mapped to a repository, or "".
func (c *Config) ServiceFor(repo string) string { return c.Services[repo] }

func (c *Config) resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.baseDir, p)
}

// Secret reads a secret from the named environment variable using lookup
// (normally os.LookupEnv). Secrets are never stored on Config, so they cannot
// leak through logging or rendering of the config.
func Secret(envName string, lookup func(string) (string, bool)) (string, error) {
	v, ok := lookup(envName)
	if !ok || v == "" {
		return "", fmt.Errorf("environment variable %s is not set", envName)
	}
	return v, nil
}
