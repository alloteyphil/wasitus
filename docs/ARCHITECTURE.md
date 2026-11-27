# Architecture

## 1. Overview

```
            wasitus timeline --from A --to B
                          │
     ┌────────────────────┼────────────────────┐
     ▼                    ▼                    ▼
┌──────────┐        ┌──────────┐        ┌──────────────┐
│ SigNoz   │        │ GitHub   │        │ Changes file │   1. COLLECT
│ alerts,  │        │ PRs,     │        │ (manual      │      one collector per
│ errors,  │        │ deploys, │        │  notes)      │      source, read-only
│ traffic  │        │ releases │        │              │
└────┬─────┘        └────┬─────┘        └──────┬───────┘
     └───────────────────┼─────────────────────┘
                         ▼
               ┌───────────────────┐
               │ 2. NORMALISE      │  everything becomes an Event (UTC)
               └─────────┬─────────┘
                         ▼
               ┌───────────────────┐
               │ 3. FINGERPRINT    │  group errors, classify each group:
               │                   │  code / dependency / account / limit /
               │                   │  expiry / traffic
               └─────────┬─────────┘
                         ▼
               ┌───────────────────┐
               │ 4. CORRELATE      │  score changes that precede symptoms
               └─────────┬─────────┘
                         ▼
               ┌───────────────────┐
               │ 5. VERDICT        │  our side / external / unknown
               │                   │  + confidence + evidence
               └─────────┬─────────┘
                         ▼
               ┌───────────────────┐
               │ 6. REDACT+RENDER  │  table / Markdown / JSON
               └───────────────────┘
```

Each stage is a pure function of the previous stage's output, except collection.
That keeps stages 2–6 easy to unit-test with no network.

## 2. Event model

Every source is converted into one shape.

```go
type Kind string

const (
    KindChange   Kind = "change"   // something we did: deploy, config, infra
    KindExternal Kind = "external" // something outside us: provider incident, billing notice
    KindSymptom  Kind = "symptom"  // something went wrong: alert, spike, error group
)

type Event struct {
    ID       string            // stable, source-specific (for de-duplication)
    Time     time.Time         // always UTC
    End      *time.Time        // for events with duration (alert fired -> resolved)
    Kind     Kind
    Source   string            // "signoz", "github", "changes-file"
    Service  string            // canonical service name after mapping
    Title    string
    Severity Severity          // info | warning | critical
    Link     string            // deep link back to the source
    Attrs    map[string]string // source-specific detail, used by later stages
}
```

**Service mapping.** Repo names and telemetry service names rarely match
(`org/wallet-api` vs `wallet-service`). The config maps them, and every event is
given its canonical service name during normalisation. Unmapped events keep their
raw name and are flagged in the output.

## 3. Collectors

```go
type Window struct{ From, To time.Time }

type Collector interface {
    Name() string
    // Collect returns events in [w.From - lookback, w.To].
    // Implementations must be read-only and must respect ctx cancellation.
    Collect(ctx context.Context, w Window) ([]event.Event, error)
}
```

- Collectors run concurrently; one failing collector degrades the result (shown as
  a warning) instead of failing the run.
- A **lookback** (default 60 min) before `--from` is always collected, because the
  cause usually comes before the first symptom.

v0.1 collectors:

| Collector | Emits | Notes |
|---|---|---|
| `signoz/alerts` | symptom | alert fired/resolved history |
| `signoz/errors` | symptom (+ samples for fingerprinting) | error spans and error logs, grouped |
| `signoz/traffic` | symptom | request-rate and error-rate buckets for spike detection |
| `github` | change | merged PRs, deployments, releases |
| `changesfile` | change / external | YAML file of manual notes ("02:30 restarted Redis") |

## 4. Fingerprinting

Input: error samples (from spans and logs) with fields such as HTTP status, server
address (who we were calling), whether the span was outbound (client span), and
exception type/message.

Output: error groups, each with a **cause class**, its share of total errors, and
the evidence.

| Class | Typical signals |
|---|---|
| `code` | exceptions raised in our services, 500s on inbound (server) spans |
| `dependency` | outbound (client) spans failing: timeouts, connection refused, 502/503/504 from a third party |
| `account` | `AccessDenied`, `AccountSuspended`, `SubscriptionRequired`, `OptInRequired`, HTTP 402 |
| `limit` | HTTP 429, `ThrottlingException`, quota exceeded |
| `expiry` | TLS/certificate errors, expired token/key errors |
| `traffic` | request rate far above baseline while error types stay unchanged |

Rules are **data, not code**: a built-in rules file ships with the binary, and users
add their own (for their providers' error formats) in their config. That is also how
company-specific knowledge stays out of this public repo.

```yaml
# rules.yaml (shape)
- id: aws-account-suspended
  class: account
  match:
    any:
      - message_contains: "AccountSuspended"
      - message_contains: "OptInRequired"
      - http_status: 402
```

## 5. Correlation

For each symptom group, every change in the lookback window gets a score in [0, 1]:

```
score = 0.5 * time_proximity      # exp(-Δt / 15min): 5 min before ≈ 0.72, 60 min ≈ 0.02
      + 0.3 * same_service        # 1 if the change touched the failing service
      + 0.2 * change_weight       # prod deploy 1.0, config 0.8, infra 0.8, docs/test 0.1
```

Weights are config, documented, and covered by tests. They are deliberately simple
so a reader can check any result by hand.

## 6. Verdict

The verdict combines two kinds of evidence:

1. **Fingerprint evidence**: what share of errors falls in each cause class.
2. **Change evidence**: the best change candidate's score.

| Situation | Verdict |
|---|---|
| ≥ 60% of errors are `code` and a change scores ≥ 0.6 | `OUR_SIDE / change` |
| ≥ 60% of errors are `dependency` / `account` / `limit` / `expiry` | `EXTERNAL / <class>`, even if a deploy happened |
| traffic far above baseline, error mix unchanged | `EXTERNAL / traffic` |
| no class dominates, or evidence conflicts | `UNKNOWN` + the top candidates |

Confidence (`high` / `medium` / `low`) comes from how dominant the class is and how
much data supports it. Every verdict lists the evidence lines that produced it.

The thresholds above are starting points; the scenario suite (SCENARIOS.md) is what
tunes them.

## 7. Redaction and output

- Before rendering, free-text fields pass through a redactor (phone numbers, emails,
  card-like numbers, tokens). On by default; `--no-redact` to disable locally.
- Renderers: `table` (default, terminal), `markdown` (post-incident review), `json`
  (for other tools). Renderers are tested with golden files.
- Exit codes: `0` verdict produced, `1` error, `2` bad command line, `3` ran
  but some collectors failed.

## 8. Configuration

```yaml
# wasitus.yaml
timezone: Africa/Accra          # display only; storage is UTC
lookback: 60m

sources:
  signoz:
    url: http://localhost:8080
    api_key_env: SIGNOZ_API_KEY # secrets only ever come from env vars
  github:
    token_env: GITHUB_TOKEN     # read-only, fine-grained token
    repos: [alloteyphil/wasitus]
  changes_file: ./changes.yaml

services:                        # repo -> telemetry service name
  alloteyphil/wasitus: orders-service

rules_file: ./rules.local.yaml   # optional extra fingerprint rules
```

## 9. Code layout

```
wasitus/
├── cmd/wasitus/          # main: flags, wiring, exit codes
├── internal/
│   ├── config/           # load + validate YAML, resolve env secrets
│   ├── event/            # Event, Kind, Severity, Window
│   ├── collector/        # Collector interface + runner (concurrency, lookback)
│   │   ├── signoz/
│   │   ├── github/
│   │   └── changesfile/
│   ├── fingerprint/      # rules engine + built-in rules
│   ├── traffic/          # 5-minute traffic spike and error-mix analysis
│   ├── correlate/        # change scoring
│   ├── verdict/          # verdict engine
│   ├── redact/           # PII redaction
│   ├── render/           # table, markdown, json
│   └── golden/           # golden-file test helper
├── demo/                 # the lab: compose file, services, mockpay, scenarios
├── testdata/             # synthetic scenario fixtures (S1-S8)
└── docs/                 # plan, architecture, scenarios, ADRs
```

`internal/` keeps the API private until it is worth stabilising. Packages depend
"downwards" only: `cmd -> verdict -> correlate/fingerprint/traffic -> event`.
Collectors depend on `event` only.

## 10. Testing strategy

| Layer | Approach |
|---|---|
| Fingerprint, traffic, correlate, verdict | table-driven unit tests |
| Collectors | httptest fixtures shaped like SigNoz/GitHub API responses |
| Renderers | golden files (`UPDATE_GOLDEN=1 go test ./...` to refresh) |
| End to end | scenario suite: synthetic lab-shaped incident in, expected verdict out (CI) |
| Live | `make lab-smoke` and `make scenario ID=S2` against the running lab |

## 11. Security and privacy

- Read-only tokens only; documented minimum scopes.
- Secrets come only from environment variables, never from files or flags, and are
  never logged or rendered.
- No telemetry or phone-home. Nothing leaves the machine except the read calls to
  the configured sources.
- Redaction on by default (section 7).
