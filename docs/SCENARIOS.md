# Staged incident scenarios

These are the test contract for the verdict engine. Each scenario can be staged in
the demo lab. CI does **not** replay captured lab API dumps: it replays
**synthetic** fixtures in `testdata/scenarios/<id>/scenario.json` (hand-authored
batches of spans, logs, alerts, and traffic shaped like the lab, including
multi-service error cascades). A release ships only if every scenario produces
its expected verdict.

## The lab

```
traffic-gen ─► storefront ─► orders-service ─► wallet-service ─► mockpay (EXTERNAL)
                                                                 fake payment provider,
                                                                 failure mode switchable
all services ──► OpenTelemetry ──► SigNoz (local)
"deploys"     ──► GitHub deployment records on a personal repo + changes file
```

`mockpay` failure modes: `healthy`, `outage` (503 / timeouts), `slow` (p99 > 5s),
`suspended` (402 `AccountSuspended`), `ratelimit` (429 after N calls/min),
`badcert` (serves an expired TLS certificate).

## Scenarios

| ID | Name | How it is staged | Expected verdict |
|---|---|---|---|
| S1 | Bad deploy | Deploy an orders-service version that faults every 5th order (~20%) | `OUR_SIDE / change` → that deploy |
| S2 | Provider outage | `mockpay` → `outage` | `EXTERNAL / dependency` → mockpay |
| S3 | Suspended account | `mockpay` → `suspended` | `EXTERNAL / account` |
| S4 | Rate limited | `mockpay` → `ratelimit`, traffic x3 | `EXTERNAL / limit` |
| S5 | Expired certificate | `mockpay` → `badcert` | `EXTERNAL / expiry` |
| S6 | Traffic surge | traffic x10, no faults | `EXTERNAL / traffic` |
| S7 | Red herring | Harmless deploy at T-5 min, then `mockpay` → `suspended` at T | `EXTERNAL / account` (must **not** blame the deploy) |
| S8 | Unclear | Small mixed errors across services, no change, no dominant class | `UNKNOWN` with top candidates |

S7 and S8 are the important ones. S7 proves the tool does not just blame the most
recent deploy. S8 proves it admits when it does not know.

## Running and staging scenarios

Run the offline synthetic scenario suite (as CI does):

```
go test ./... -run TestScenario
go test ./... -run Scenario/S3
```

Stage a live incident in the local demo lab and run `wasitus` against it (this is
the source of truth for realism; keep the synthetic fixtures honest when live
behaviour changes):

```
make lab-up
make scenario ID=S3                  # stages the fault, waits, and resets
set -a && source demo/.env && set +a
go run ./cmd/wasitus timeline --config demo/wasitus.lab.yaml --from -15m --to now
```
