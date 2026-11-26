# Project plan

Owner: `alloteyphil` (personal project, personal GitHub)
Started: 26 Nov 2026
Target: v0.1 public release by end of January 2027

## 1. Why this project exists

**The problem.** During an incident, engineers open SigNoz, GitHub, the cloud
console and Slack in separate tabs and line up timestamps in their heads to answer
three questions: what broke, when, and *was it us?* Teams often blame the last
deploy by default, even when the cause was outside them: a provider outage, an
unpaid cloud bill, a rate limit, an expired certificate.

**The goal for the tool.** Given a time window, produce a single timeline and an
evidence-backed verdict on which side the cause sits.

**The goal for me.** A portfolio project that shows senior-level engineering:

- a real problem with a clear, narrow scope
- deliberate design, with written decisions (ADRs) and honest limitations
- production habits: tests, CI, releases, security and privacy by default
- a demo anyone can reproduce in about 10 minutes
- a write-up that explains the reasoning, not just the code

## 2. Ground rules

1. **No employer data, systems, credentials or time.** All development and testing
   uses the local demo lab (fake company, fake data). See
   [ADR-0002](adr/0002-own-data-only.md).
2. **Read-only and local.** The tool never writes to the systems it reads and never
   sends data anywhere. See [ADR-0003](adr/0003-read-only-local-first.md).
3. **Rules decide, AI only summarises.** See
   [ADR-0004](adr/0004-deterministic-verdict.md).
4. **Every verdict is tested.** Each staged scenario in [SCENARIOS.md](SCENARIOS.md)
   has a recorded fixture and an expected verdict that CI checks.
5. **Scope discipline.** Anything not in the current milestone goes to the backlog.

## 3. Scope

### v0.1 (in scope)
- Command-line tool, single binary (Go)
- Sources: SigNoz (alerts, error traces, error logs, request rate), GitHub (merged
  PRs, deployments, releases), a local "changes file" for manual notes
- Error fingerprinting into causes: code, dependency, account/billing, rate limit,
  expiry, traffic
- Correlation and verdict with confidence and evidence
- Output: terminal table, Markdown report, JSON
- Redaction of personal data (phone numbers, emails, card numbers) in output, on by
  default
- Demo lab: small multi-service app + fake payment provider + local SigNoz, with
  scripts that stage each scenario

### Not in v0.1 (backlog, see section 7)
AWS Health / CloudTrail, provider status-page feeds, synthetic dependency probes,
certificate-expiry scanning, other backends (Prometheus, Grafana, Datadog), AI
summary, Slack output, MCP server, web UI.

## 4. Milestones

Time is in weekends (roughly 8–10 focused hours each). Estimates, not promises.

### M0: Foundations (this weekend: 28–29 Nov)
- [ ] Create GitHub repo `alloteyphil/wasitus` (public), push the skeleton
- [ ] CI: build, `go vet`, `golangci-lint`, `go test -race`
- [ ] `Event` model and `Window` type with unit tests
- [ ] Config loading (YAML + secrets from environment variables) with validation
- [ ] CLI skeleton: `wasitus version`, `wasitus timeline --from --to --config`
- [ ] Conventional commits, CHANGELOG.md, issue labels, GitHub milestones M0–M5

**Done when:** CI is green and `wasitus timeline` runs end-to-end with a fake
in-memory collector.

### M1: The demo lab (weekend 2)
- [ ] `demo/` Docker Compose: SigNoz + 3 small Go services (storefront, orders,
      wallet) instrumented with OpenTelemetry
- [ ] `mockpay`: a fake external payment provider with switchable failure modes
      (healthy, outage, slow, suspended, rate-limited, expired cert)
- [ ] Traffic generator
- [ ] `demo/scenarios/*.sh` to stage and reset each scenario
- [ ] Alert rules in SigNoz for error rate and latency

**Done when:** `make lab-up && make scenario S2` produces a visible incident in the
local SigNoz.

### M2: The timeline (weekend 3)
- [ ] Spike: confirm which SigNoz API endpoints give alert history and query
      traces/logs (record findings in an ADR)
- [ ] Collectors: SigNoz alerts, GitHub (PRs, deployments, releases), changes file
- [ ] Record/replay test harness: real API responses saved to `testdata/`, replayed
      through `httptest` servers in CI
- [ ] Merge, sort, de-duplicate; terminal table output

**Done when:** running against the lab shows the deploy and the alert in one
timeline, and the collector tests run offline.

### M3: Error fingerprinting (weekends 4–5)
- [ ] SigNoz collector for error spans and logs (status, HTTP code, server address,
      exception type and message)
- [ ] Rules engine: data-driven rules file (built-in defaults, user-overridable)
- [ ] Classify errors into causes, with share-of-errors per cause
- [ ] Spike detection: 5-minute buckets compared with a 7-day baseline
- [ ] PII redaction

**Done when:** each lab scenario's errors land in the expected cause class.

### M4: Correlation and verdict (weekend 6)
- [ ] Candidate-cause scoring for changes (time proximity, same service, change type)
- [ ] Verdict engine combining change candidates and fingerprint evidence
- [ ] `UNKNOWN` when evidence is weak; confidence levels
- [ ] Markdown and JSON output; golden-file tests
- [ ] Scenario test suite: all scenarios in SCENARIOS.md pass from recorded fixtures

**Done when:** 8/8 scenarios produce the expected verdict in CI.

### M5: Ship v0.1 (weekend 7)
- [ ] Releases with GoReleaser (macOS, Linux, Windows) + Homebrew tap
- [ ] README with demo GIF and 10-minute quickstart against the lab
- [ ] Design write-up / blog post: "Was it us? Telling our failures from someone
      else's"
- [ ] Add the project to the portfolio site (`../portfolio`)
- [ ] Share: LinkedIn post, SigNoz and OpenTelemetry community channels

**Done when:** a stranger can install it, run the lab, and reproduce a verdict
without asking me anything.

## 5. Timeline at a glance

| Weekend | Dates (approx.) | Milestone |
|---|---|---|
| 1 | 28–29 Nov | M0 Foundations |
| 2 | 5–6 Dec | M1 Demo lab |
| 3 | 12–13 Dec | M2 Timeline |
| 4–5 | 19 Dec – 10 Jan | M3 Fingerprinting (holiday slack built in) |
| 6 | 17 Jan | M4 Verdict |
| 7 | 24–31 Jan | M5 Ship v0.1 |

## 6. How I'll work (senior habits, visible in the repo)

- **Small PRs, even solo.** One feature per branch, PR description explains why.
  The history becomes part of the portfolio.
- **Decisions written down.** Any choice with trade-offs gets an ADR in `docs/adr/`.
- **Tests first for the logic.** Fingerprinting and verdict rules are table-driven
  tests; renderers use golden files; collectors use record/replay.
- **Definition of done** for each task: tests, docs updated, CHANGELOG entry, CI
  green.
- **Weekly note** in `docs/devlog/` (5 lines: what shipped, what I learned, what's
  next). This feeds the blog post later.

## 7. Backlog (after v0.1)

| Item | Why it matters |
|---|---|
| Synthetic dependency probes | Catches provider outages when the provider has no status page |
| Status-page feeds (Statuspage-style JSON) | Cheap external evidence |
| Certificate, domain and token expiry checks | A common, embarrassing cause |
| AWS Health + CloudTrail collectors | Cloud outages and config changes |
| Prometheus/Grafana and Datadog backends | Users beyond SigNoz |
| AI-written incident summary (opt-in) | Readable post-incident draft; cites evidence only |
| Slack output | Teams live there during incidents |
| MCP server mode | Lets AI agents call `wasitus` during investigations |

## 8. Risks

| Risk | Mitigation |
|---|---|
| Scope creep (it's a tempting problem) | Milestone exit criteria; backlog for everything else |
| SigNoz APIs change or are under-documented | Spike in M2; isolate behind the collector interface |
| Lab is heavy on RAM (SigNoz + services) | Keep lab services tiny; document minimum resources |
| Verdict looks smart on staged data only | Publish the scoring rules openly; include tricky scenarios (S7, S8) |
| Motivation dips mid-project | M2 already gives a working, demoable timeline |
| Employer IP questions | No employer data/time/systems (ADR-0002); optional conversation later |

## 9. Portfolio outcomes (fill in only when true)

CV bullet draft, to complete after v0.1:

> Designed and built **wasitus**, an open-source incident analysis CLI (Go) that
> combines OpenTelemetry/SigNoz signals with change history to tell
> self-inflicted outages apart from external ones; [N]/[N] staged incident types
> classified correctly; [stars / users / talks].
