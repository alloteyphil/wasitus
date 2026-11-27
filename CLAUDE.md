# wasitus: notes for AI assistants

Personal open-source project. GitHub account: `alloteyphil` (personal), never the
work account. These rules apply to every agent working here, including parallel
agents and reviewers.

## Hard rules
- No employer data, hostnames, service names, incidents or credentials anywhere in
  this repo (see docs/adr/0002-own-data-only.md). Use the demo lab and fake data.
- No secrets in the repo, tests or fixtures. Tokens come from environment
  variables only; recorded API responses are scrubbed before they are committed.
- The tool is read-only against every source it queries.
- Verdict logic stays deterministic (docs/adr/0004-deterministic-verdict.md).
- Never push to `main`, never force-push a shared branch, never merge your own PR.

## How work gets done
1. **One task, one branch, one pull request.** Branch from an up-to-date `main`.
   Change only the files your task needs. Work only on the scope you were given
   (docs/PLAN.md lists the milestones).
2. **`make check` passes before every push.** It runs what CI runs: formatting,
   `go mod tidy`, vet, race-enabled shuffled tests with the coverage floor, the
   demo lab's tests, and golangci-lint when installed.
3. **Definition of done** for every pull request:
   - Tests: table-driven for logic; golden files for rendered output; record and
     replay through `httptest` for anything that calls an API. Unit tests never
     touch the network or the real clock (inject `now`).
   - Coverage: CI fails the tool module below 85%. New packages under `internal/`
     aim for 90% or more.
   - Code that parses outside input (config, changes file, API responses) has a
     fuzz test with a seed corpus, and never panics on bad input.
   - Errors carry context (`fmt.Errorf("load config: %w", err)`); no ignored errors.
   - Docs match the code: package comment, README or ARCHITECTURE when behaviour
     changes, an ADR for any decision with trade-offs (docs/adr/).
   - Small conventional commits (`feat:`, `fix:`, `test:`, `docs:`, `ci:`,
     `chore:`); every commit builds and passes tests on its own.
   - The PR description says why, what, and how it was tested.
4. **Dependencies.** Prefer the standard library. A new dependency in the tool's
   `go.mod` needs an ADR. Lab-only dependencies go in `demo/go.mod` (ADR-0006).
   Pin exact versions released at least two weeks ago, except to fix a
   vulnerability reported by `govulncheck` (take the fixed version at once).
5. **Shared files.** When several agents work in parallel, they do not edit
   `CHANGELOG.md`, `docs/PLAN.md` or `.github/`. The integrator updates those
   when merging, to avoid conflicts.

## Merging (integrator only)
- A PR is merged only when `scripts/pr-green.sh <PR>` passes (every check green,
  `ci ok` and GitGuardian present, branch up to date with `main`) **and** an
  independent reviewer found no blocking problem.
- The agent or person who authored a PR must not merge it. A subagent review
  launched by that same authoring agent does not count as independent; a
  separate human (or a separate session that did not write the PR) must approve,
  then the owner or another integrator merges.
- "No result" is never "green". Pending, skipped, cancelled or missing checks
  block the merge.
- Merge with a merge commit (`gh pr merge --merge`) so each PR's commits stay in
  the history.
- `main` is protected on GitHub: the `ci ok` and GitGuardian checks must pass and
  the branch must be up to date before anyone can merge.

## Where things are
- Plan and milestones: docs/PLAN.md
- Design: docs/ARCHITECTURE.md
- Test contract: docs/SCENARIOS.md
- CI/CD and its rules: docs/CI_CD.md
- Demo lab: demo/README.md

## Commands
- Everything CI checks: `make check`
- Build: `go build ./...`
- Test: `go test -race -shuffle=on ./...` (lab: `cd demo && go test -race ./...`)
- Run: `go run ./cmd/wasitus version`
- Is a PR safe to merge: `scripts/pr-green.sh <PR>`
