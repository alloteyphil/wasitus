# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `internal_hosts` config: extra hosts (or `.domain`s) of our own systems.

### Fixed
- A failed or timed-out source no longer produces a confident verdict as if
  nothing had been found. Example: during a 10x traffic surge with every error
  query failing, wasitus said `EXTERNAL / traffic` (high) with "no new error
  classes appeared". Missing error samples now give `UNKNOWN`; missing
  traffic, baseline errors, or change sources are named in the evidence and
  cap confidence at medium.
- A retry storm after a bad deploy (traffic up, error rate from 0.1% to 16%,
  same error mix) was called `EXTERNAL / traffic`; a pure surge now also needs
  the error rate to stay within 2 points of baseline.
- A near-idle service (0 to 6 requests) no longer counts as a traffic surge;
  a spike needs at least 10 expected requests per bucket.
- A rollback made mid-incident, or a change to a service with only a stray
  error, could be blamed instead of the real deploy. Changes after the
  earliest symptom are no longer candidates, and a service needs at least 10%
  of the window's errors (or an alert) to count as failing.
- Calls between our own services outside docker-compose were read as third
  party, so our own failure came out `EXTERNAL / dependency`. Hosts whose
  first DNS label is a service name (Kubernetes names) and private, loopback
  and link-local IPs now count as ours.
- A third party's 400 / 401 / 422 (it rejected what we sent) now counts as
  code, not dependency. "not yet valid" alone ("coupon not yet valid") no
  longer counts as certificate expiry.
- Markdown reports escape titles, signatures, evidence and warnings, so a PR
  title cannot add images, links, HTML or emphasis to a pasted report (and
  `<n>` placeholders are no longer swallowed as HTML tags). Only http(s)
  links become links.
- Config and changes files with a second YAML document (after `---`) are
  rejected instead of silently dropping it.
- Changes-file titles and services lose control characters (terminal escape
  sequences), and `link:` must be an http(s) URL.
- `config.Parse` now has a fuzz test, as CLAUDE.md requires for parsers.

### Security
- Long messages were cut before redaction, leaving fragments it could not
  recognise ("alice.smith@exampl...", "4111 1111..."). They are now cut at a
  word boundary, dropping trailing number groups.
- Redaction now covers `key => 'value'`, `pwd` / `passphrase` /
  `private_key` keys, PEM private keys, Slack / Stripe / Google keys, Slack
  webhook paths, credentials in URL query strings, bracketed phone numbers
  and MSISDNs without `+` (`00...`, African `2xx` country codes).
- Event IDs and rule IDs are redacted in every output format.
- `sources.signoz.url` may no longer contain `user:password@` (the error
  never echoes it), and alert links never carry URL credentials.

## [0.1.0] - 2026-11-27

### Changed
- Scenario docs and fixtures: stop calling `testdata/scenarios/*` "recorded";
  they are synthetic lab-shaped inputs. External scenarios (S2–S5, S7) now
  include multi-service cascade spans and error logs so offline class share
  matches live dilution (~78% dominant class with wrapper `HTTP 502`
  ClassCode samples). CLAUDE.md: authoring agents must not merge their own PRs.

### Added
- `wasitus timeline --from --to --config --timeout`: prints a timeline table for
  an incident window, including a configurable lookback before `--from`.
- `wasitus version`.
- Event model (change / external / symptom), time window, and time parsing
  (`now`, `-2h`, RFC 3339, `YYYY-MM-DD HH:MM` in the config timezone).
- YAML config with strict validation, secrets read only from environment
  variables, and paths relative to the config file.
- Collector runner: runs sources concurrently and survives a failing or
  panicking source (exit code 3 with warnings).
- Changes-file collector for manual notes.
- CI: formatting, `go mod tidy`, vet, golangci-lint, and tests on Linux, macOS
  and Windows (race detector on Linux and macOS), plus a coverage summary,
  workflow lint and a release dry run on every pull request.
- Release workflow: pushing a `v*` tag publishes a GitHub Release with binaries
  for macOS, Linux and Windows (amd64 and arm64) and checksums.
- Dependabot updates for GitHub Actions and Go modules.
- Demo lab: `mockpay`, a fake external payment provider whose failure mode
  (healthy, outage, slow, suspended, ratelimit) can be switched while it runs.
- Demo lab: `storefront`, `orders-service` and `wallet-service`, traced with
  OpenTelemetry end to end, with a staged code bug in orders-service for the
  bad-deploy scenario (S1).
- Demo lab with Docker: `make lab-up` starts a pinned SigNoz and the lab
  services and completes SigNoz's first-run setup; `make lab-smoke` checks
  that checkouts work and their traces reach SigNoz (also run in CI when the
  lab changes, and weekly).
- Demo lab: SigNoz alert rules for a service's error rate and for checkout
  latency, and a read-only SigNoz API key for wasitus (`SIGNOZ_API_KEY` in
  `demo/.env`), both created by `make lab-up`.
- ADR-0007 records how wasitus reads SigNoz v0.149.0: API key auth, the
  query and alert-history endpoints, their limits and units.
- Demo lab with Docker: steady customer traffic (trafficgen, 2 checkouts/s,
  rate changeable on :8091), and wallet-service now calls mockpay over HTTPS,
  trusting a lab CA mockpay shares through a volume, so mockpay's badcert
  mode stages an expired certificate (S5).
- Demo lab scenario runner (`demo/scenarios/run.sh`, `make scenario ID=S1..S8`,
  `make scenario-reset`) and ready-to-use lab config (`demo/wasitus.lab.yaml`).
- GitHub collector (`internal/collector/github`): merged PRs, deployments, and
  releases with `httptest` record/replay fixtures.
- SigNoz collector (`internal/collector/signoz`): read-only alert history,
  error spans and error logs, and 5-minute request/error traffic buckets with
  7-day baseline chunking (`SigNoz-Api-Key` header auth, verified against
  SigNoz v0.149.0).
- Error fingerprinting engine (`internal/fingerprint`): groups error samples
  and classifies them (`code`, `dependency`, `account`, `limit`, `expiry`)
  using built-in YAML rules plus optional user `rules_file` overrides.
- Traffic spike detection and error-mix stability check (`internal/traffic`):
  compares 5-minute incident buckets against a 7-day baseline to detect
  traffic surges without a new error type.
- Change correlation scorer (`internal/correlate`): ranks preceding changes by
  time proximity, service match, and change type.
- Deterministic verdict engine (`internal/verdict`): combines fingerprint,
  change, and traffic evidence into `OUR_SIDE / change`, `EXTERNAL / <class>`,
  or `UNKNOWN` with confidence levels and evidence lines.
- PII and secret redaction (`internal/redact`): redacts emails, phone numbers,
  card-like numbers, and secrets/tokens from rendered output and warnings by
  default (`--no-redact` to disable locally).
- Output formats (`internal/render`): `--format table` (default),
  `--format markdown`, and `--format json`, with golden-file tests.
- Full CLI verdict pipeline (`wasitus timeline`) and offline end-to-end
  scenario test suite (`testdata/scenarios/S1`–`S8`) verifying 8/8 staged
  incidents in CI.

[Unreleased]: https://github.com/alloteyphil/wasitus/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/alloteyphil/wasitus/releases/tag/v0.1.0
