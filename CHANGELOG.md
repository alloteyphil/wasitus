# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

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
