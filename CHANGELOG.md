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
