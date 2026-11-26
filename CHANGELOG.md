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
- CI: formatting, `go mod tidy`, vet, race-enabled tests, golangci-lint.
