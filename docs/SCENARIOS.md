# Staged incident scenarios

These are the test contract for the verdict engine. Each scenario is staged in the
demo lab, recorded into `testdata/scenarios/<id>/`, and replayed in CI. A release
ships only if every scenario produces its expected verdict.

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
| S1 | Bad deploy | Deploy an orders-service version that throws on 20% of requests | `OUR_SIDE / change` → that deploy |
| S2 | Provider outage | `mockpay` → `outage` | `EXTERNAL / dependency` → mockpay |
| S3 | Suspended account | `mockpay` → `suspended` | `EXTERNAL / account` |
| S4 | Rate limited | `mockpay` → `ratelimit`, traffic x3 | `EXTERNAL / limit` |
| S5 | Expired certificate | `mockpay` → `badcert` | `EXTERNAL / expiry` |
| S6 | Traffic surge | traffic x10, no faults | `EXTERNAL / traffic` |
| S7 | Red herring | Harmless deploy at T-5 min, then `mockpay` → `suspended` at T | `EXTERNAL / account` (must **not** blame the deploy) |
| S8 | Unclear | Small mixed errors across services, no change, no dominant class | `UNKNOWN` with top candidates |

S7 and S8 are the important ones. S7 proves the tool does not just blame the most
recent deploy. S8 proves it admits when it does not know.

## Recording a scenario

```
make lab-up
make scenario ID=S3          # stages the fault, waits for alerts, resets
make record ID=S3            # captures SigNoz + GitHub responses into testdata/
go test ./... -run Scenario/S3
```
