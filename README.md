# wasitus

**Was it us?** When production breaks, `wasitus` builds the incident timeline from
your observability and change data, then tells you where the evidence points:

- **our side**: a deploy, a config change, an infrastructure change, or
- **outside our control**: a provider outage, a suspended or unpaid account, a rate
  limit, an expired certificate, or a traffic surge.

It is read-only, runs on your own machine, and sends your data nowhere.

> **Status: early development (M1 to M2).** The timeline reads manual notes and
> GitHub (merged PRs, deployments, releases). Error classification, change
> scoring, spike detection and redaction exist as packages; the SigNoz source,
> wiring them into the CLI, and the verdict are next. A demo lab with SigNoz
> runs with one command (`make lab-up`). See [docs/PLAN.md](docs/PLAN.md).

## Try it

```
go run ./cmd/wasitus timeline \
  --config examples/wasitus.example.yaml \
  --from "2026-11-14 03:00" --to "2026-11-14 04:00"
```

```
Window: 2026-11-14 03:00 -> 2026-11-14 04:00 Africa/Accra (plus 60m lookback)

TIME   KIND      SOURCE        SERVICE         EVENT
02:31  change    changes-file  orders-service  Merged PR #12: refactor order validation
02:47  change    changes-file  orders-service  Deployed orders-service v1.4.0 to the lab
03:05  external  changes-file  -               mockpay status page: degraded payments
03:20  change    changes-file  wallet-service  Restarted wallet-service
```

Exit codes: `0` success, `1` error, `2` bad command line, `3` result produced
but some sources failed.

Output formats: `--format table` (default), `--format markdown` (for
post-incident reviews), and `--format json` (for scripts and other tools).
Personal data and secrets (emails, phone numbers, card numbers, tokens) are
redacted by default; pass `--no-redact` to disable redaction locally.

### SigNoz permissions

Give `wasitus` an API key for a SigNoz service account with the `signoz-viewer`
role, stored in the environment variable named by `sources.signoz.api_key_env`
(for example `SIGNOZ_API_KEY`). `wasitus` only makes read calls and never logs
in with a password. Note that in SigNoz v0.149.0 a viewer key can also run raw
ClickHouse SQL queries, so it can read all telemetry in that SigNoz instance
(see [ADR-0007](docs/adr/0007-signoz-api.md)).

## What it will look like

```
$ wasitus timeline --from "2026-11-20 08:00" --to "2026-11-20 12:00"

TIME   KIND      SOURCE   SERVICE         EVENT
08:14  symptom   signoz   wallet-service  Error rate 31% (baseline 0.6%)
08:14  symptom   signoz   wallet-service  Alert FIRED: High 5xx rate
08:15  symptom   traces   wallet-service  92% of failed calls -> api.mockpay.test (402 AccountSuspended)
11:40  symptom   signoz   wallet-service  Alert RESOLVED

OUR CHANGES IN THE 60 MIN BEFORE: none

VERDICT: EXTERNAL / account or billing      confidence: high
  - no deploys or config changes before the first symptom
  - 92% of errors are AccountSuspended responses from one provider
  - errors are on outbound calls, not in our own code
```

(Target output. Illustrative data from the demo lab.)

## How it is different

Most incident tools *list* events. `wasitus` gives a **verdict with its evidence**,
and it answers `UNKNOWN` when the evidence is weak instead of guessing. The core
logic is deterministic rules, not an LLM, so every conclusion can be traced back to
the data. See [docs/PRIOR_ART.md](docs/PRIOR_ART.md).

## Documents

| Doc | What it covers |
|---|---|
| [docs/PLAN.md](docs/PLAN.md) | Goals, milestones, timeline, portfolio deliverables |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | How it works: pipeline, event model, verdict logic |
| [docs/SCENARIOS.md](docs/SCENARIOS.md) | Staged incidents and the verdict each must produce |
| [docs/PRIOR_ART.md](docs/PRIOR_ART.md) | Existing tools and how this one differs |
| [docs/adr/](docs/adr/) | Architecture decision records |

## License

Apache-2.0
