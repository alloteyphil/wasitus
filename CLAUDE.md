# wasitus: notes for AI assistants

Personal open-source project. GitHub account: `alloteyphil` (personal), never the
work account.

## Hard rules
- No employer data, hostnames, service names, incidents or credentials anywhere in
  this repo (see docs/adr/0002-own-data-only.md). Use the demo lab and fake data.
- The tool is read-only against every source it queries.
- Verdict logic stays deterministic (docs/adr/0004-deterministic-verdict.md).
- Show diffs before committing; small, focused commits using conventional commit
  messages.

## Where things are
- Plan and milestones: docs/PLAN.md (work only on the current milestone)
- Design: docs/ARCHITECTURE.md
- Test contract: docs/SCENARIOS.md

## Commands
- Build: `go build ./...`
- Test: `go test -race ./...`
- Run: `go run ./cmd/wasitus version`
