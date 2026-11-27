# Was it us? Telling our failures from someone else's

Philip Allotey · November 2026 · [wasitus v0.1.0](https://github.com/alloteyphil/wasitus/releases/tag/v0.1.0)

## The problem

When production breaks, the first useful question is rarely "what is the root
cause?" It is simpler and more political: **was it us?**

Engineers open SigNoz (or Datadog, or Grafana), GitHub, the cloud console, and
Slack. They line up timestamps by eye. Someone remembers a deploy. Someone else
points at a third-party payment provider. The room splits into camps before the
evidence is written down.

Teams often blame the last change by default. That habit is wrong often enough
to matter: unpaid accounts, rate limits, expired certificates, and provider
outages all look like "our API is failing" from the outside. The opposite habit —
always blaming the vendor — is no better.

I wanted a tool that, given a time window, builds one timeline and returns an
**evidence-backed verdict** on which side the cause sits: our change, or outside
our control — or honestly `UNKNOWN`.

## What wasitus does

`wasitus` is a read-only CLI. It runs on your machine. It does not write to
SigNoz, GitHub, or anywhere else, and it does not ship your telemetry to a SaaS.

For a window (`--from` / `--to`) it:

1. Collects change events (GitHub merges, deployments, releases; optional local
   changes file) and symptoms (SigNoz alerts, error spans/logs, request rates).
2. Fingerprints errors into cause classes: code, dependency, account/billing,
   rate limit, expiry, traffic.
3. Detects traffic spikes against a baseline.
4. Scores recent changes against the symptoms.
5. Emits a deterministic verdict with confidence and evidence lines, as a table,
   Markdown, or JSON. Personal data in the output is redacted by default.

Install and a ten-minute lab quickstart are in the
[README](../../README.md).

## Design choices that mattered

### Rules decide; models do not

Many incident tools let an LLM read the logs and name a cause. Those answers are
hard to test, can drift between runs, and can invent detail. During an incident,
people need to check the conclusion quickly.

wasitus keeps fingerprinting, correlation, and the verdict as **deterministic
rules** with documented weights ([ADR-0004](../adr/0004-deterministic-verdict.md)).
Every verdict lists the evidence lines that produced it. An optional AI summary
is backlog only, and even then it may only rephrase — never change — the verdict.

### Own data only, own lab

I will not put employer hosts, incidents, or credentials in this repo
([ADR-0002](../adr/0002-own-data-only.md)). Development and demos use a fake
company lab: storefront → orders → wallet → `mockpay`, all traced into a local
SigNoz ([ADR-0005](../adr/0005-own-lab-app.md)).

### Read-only and local-first

The collectors only call read APIs. Secrets come from environment variables, not
from committed files ([ADR-0003](../adr/0003-read-only-local-first.md)).

### Honest fixtures

CI does not replay captured production dumps. It runs **synthetic** scenario
fixtures shaped like the lab, including multi-service error cascades, so the
offline class share matches what a live cascade looks like. If the lab and the
fixtures disagree, the fixtures must change — not the other way around.

## The scenario contract

Eight staged incidents are the test contract ([SCENARIOS.md](../SCENARIOS.md)):

| ID | Story | Expected verdict |
|---|---|---|
| S1 | Bad deploy (~20% faults) | `OUR_SIDE / change` |
| S2 | Provider outage | `EXTERNAL / dependency` |
| S3 | Suspended payment account | `EXTERNAL / account` |
| S4 | Rate limited under load | `EXTERNAL / limit` |
| S5 | Expired TLS cert on the provider | `EXTERNAL / expiry` |
| S6 | Traffic surge, no new error type | `EXTERNAL / traffic` |
| S7 | Harmless deploy, then account suspension | `EXTERNAL / account` (must not blame the deploy) |
| S8 | Weak, mixed signal | `UNKNOWN` |

S7 and S8 are the ones that justify the project. S7 proves the tool does not
rubber-stamp the most recent deploy. S8 proves it can admit ignorance.

## What v0.1 deliberately does not do

- No AWS Health / CloudTrail, status-page feeds, or certificate scanners yet
  (backlog).
- No Slack bot, MCP server, or web UI.
- No AI-written narrative in the critical path.
- SigNoz is the first observability backend; others are backlog.
- Verdicts are only as good as the rules and the data you give them. Unusual
  failures will land in `UNKNOWN` until someone writes a rule.

## Try it

```
brew install alloteyphil/wasitus/wasitus   # or: go install ...@v0.1.0
wasitus version

# Offline example (no Docker):
wasitus timeline \
  --config examples/wasitus.example.yaml \
  --from "2026-11-14 03:00" --to "2026-11-14 04:00"

# Full lab (≈6 GB Docker memory):
make lab-up && make scenario ID=S7
```

Source, ADRs, and architecture notes live in the
[repository](https://github.com/alloteyphil/wasitus).
