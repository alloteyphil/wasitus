# wasitus

**Was it us?** When production breaks, `wasitus` builds the incident timeline from
your observability and change data, then tells you where the evidence points:

- **our side**: a deploy, a config change, an infrastructure change, or
- **outside our control**: a provider outage, a suspended or unpaid account, a rate
  limit, an expired certificate, or a traffic surge.

It is read-only, runs on your own machine, and sends your data nowhere.

> **Status: v0.1 feature-complete (M0–M4 done, preparing v0.1.0 release).**
> `wasitus` collects signals from SigNoz (alerts, error spans/logs, request-rate
> traffic), GitHub (merged PRs, deployments, releases), and a local changes
> file, then fingerprints errors, checks for traffic spikes, correlates recent
> changes, and produces a deterministic verdict in table, Markdown, or JSON
> format with PII redaction on by default. All 8/8 synthetic staged scenarios in
> [docs/SCENARIOS.md](docs/SCENARIOS.md) pass in CI. See
> [docs/PLAN.md](docs/PLAN.md).

## Try it

### Offline (no Docker needed)

Run against the bundled example changes file:

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

Run the offline scenario suite (all 8 staged incidents from
[docs/SCENARIOS.md](docs/SCENARIOS.md)):

```
go test -v ./cmd/wasitus -run TestScenario
```

### 10-minute quickstart against the demo lab

Requires Docker with about 6 GB of memory for containers (see
[demo/README.md](demo/README.md)):

```
make lab-up                                    # starts SigNoz, lab services, and mockpay
make scenario ID=S3 MINUTES=4                  # stages suspended payment account (402)
set -a && source demo/.env && set +a           # loads read-only SIGNOZ_API_KEY
go run ./cmd/wasitus timeline \
  --config demo/wasitus.lab.yaml \
  --from -15m --to now
make lab-down
```

Try `--format markdown` for a post-incident review or `--format json` for
scripts.

### Flags, exit codes, and rules

Exit codes: `0` success, `1` error, `2` bad command line, `3` result produced
but some sources failed.

Output formats: `--format table` (default), `--format markdown` (for
post-incident reviews), and `--format json` (for scripts and other tools).
Personal data and secrets (emails, phone numbers, card numbers, tokens) are
redacted by default; pass `--no-redact` to disable redaction locally.

Built-in fingerprint rules cover common code, dependency, account/billing, rate
limit, and certificate/token expiry patterns. Set `rules_file` in your config
YAML to add extra rules for your own providers without modifying `wasitus`.

### SigNoz permissions

Give `wasitus` an API key for a SigNoz service account with the `signoz-viewer`
role, stored in the environment variable named by `sources.signoz.api_key_env`
(for example `SIGNOZ_API_KEY`). `wasitus` only makes read calls and never logs
in with a password. Note that in SigNoz v0.149.0 a viewer key can also run raw
ClickHouse SQL queries, so it can read all telemetry in that SigNoz instance
(see [ADR-0007](docs/adr/0007-signoz-api.md)).

## Example verdict output

Real table output from `wasitus` on an S7-style red-herring incident: a deploy
went out right before the outage, yet 93% of errors are `AccountSuspended`
responses from `mockpay`, so `wasitus` points to `EXTERNAL / account` and
explains why the deploy was not blamed:

```
Window: 2026-11-14 03:00 -> 2026-11-14 04:00 UTC (plus 60m lookback)

TIME   KIND      SOURCE        SERVICE         EVENT
02:31  change    github        orders-service  Merged PR #12: refactor order validation
02:47  change    github        orders-service  Deploy orders-service v1.4.0
03:05  external  changes-file  -               mockpay status page: degraded payments
03:09  symptom   signoz        wallet-service  Alert FIRED: High 5xx rate

ERROR GROUPS
CLASS    SHARE  COUNT  SERVICE         SIGNATURE
account  93%    28     wallet-service  mockpay rejected the charge: HTTP <n> AccountSuspended
code     7%     2      -               unexpected nil | empty body

VERDICT: EXTERNAL / account   confidence: high
  - 93% of errors (28/30) classified as account
  - top error group in wallet-service: mockpay rejected the charge: HTTP <n> AccountSuspended (28 occurrences, rule account-suspended)
  - recent change "Deploy orders-service v1.4.0" (score 0.82) was not blamed because 93% of errors are account failures
```

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
