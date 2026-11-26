# Demo lab

A small fake company used to develop, test and demonstrate `wasitus`. Built in
milestone M1 (see ../docs/PLAN.md). All data here is fictional.

The lab is its own Go module, so the tool's dependencies stay small
(../docs/adr/0006-demo-separate-module.md). Run its tests from this folder:
`go test -race ./...`.

## Run the lab with Docker

Needs Docker with about 6 GB of memory for containers. From the repository root:

```
make lab-up      # build and start SigNoz + the lab, wait until healthy
make lab-ps      # what is running
make lab-logs    # follow the logs
make lab-down    # stop (data kept)
make lab-reset   # stop and delete all lab data
```

| What | Where |
|---|---|
| Checkout (the customers' way in) | `curl -X POST localhost:8000/checkout -d '{"customer_id":"cus_42","amount":2500,"currency":"GHS"}'` |
| Switch mockpay's failure mode | `curl -X PUT localhost:8090/admin/mode -d '{"mode":"suspended"}'` |
| SigNoz (traces, logs) | http://localhost:8080 |
| A staged bad deploy (S1) | `ORDERS_VERSION=1.4.0 ORDERS_BUG_EVERY=5 docker compose -f demo/compose.yaml up -d orders-service` |

A fresh SigNoz stores nothing until its first admin account exists, so
`make lab-up` creates a lab-only admin. Its login is generated into
`demo/.env` (`LAB_SIGNOZ_EMAIL`, `LAB_SIGNOZ_PASSWORD`), which git ignores.
SigNoz's own files and where they come from: [signoz/README.md](signoz/README.md).

```
demo/
├── compose.yaml         # the lab: SigNoz + lab services        (done)
├── Dockerfile           # one image recipe for every program    (done)
├── signoz/              # SigNoz, pinned (from Foundry)         (done)
├── internal/lab/        # shared: OpenTelemetry, HTTP, errors   (done)
├── services/            # the lab's own services                (done)
│   ├── storefront/
│   ├── orders/
│   └── wallet/
├── mockpay/             # fake external payment provider        (done)
├── trafficgen/                                                 (coming)
└── scenarios/           # S1..S8 stage/reset scripts            (coming)
```

## Lab services

```
customer ─► storefront ─► orders-service ─► wallet-service ─► mockpay (external)
  :8080        :8081           :8082            :8090
```

| Service | Does | Settings (besides `LAB_ADDR`, `APP_VERSION`) |
|---|---|---|
| `storefront` | `POST /checkout`; places the order | `ORDERS_URL`, `STOREFRONT_ORDERS_TIMEOUT` (8s) |
| `orders-service` | `POST /orders`; pays through the wallet | `WALLET_URL`, `ORDERS_WALLET_TIMEOUT` (5s), `ORDERS_BUG_EVERY` (0) |
| `wallet-service` | `POST /payments`; charges mockpay | `MOCKPAY_URL`, `WALLET_PROVIDER_TIMEOUT` (3s) |

Each service sends traces and logs to SigNoz when `OTEL_EXPORTER_OTLP_ENDPOINT`
is set, and always logs JSON to stdout. A checkout is one trace across all
three services. `APP_VERSION` becomes `service.version`, so a staged deploy is
visible in the traces.

The two kinds of failure look different, which is what wasitus will judge:

| Staged | Customer sees | Our telemetry shows |
|---|---|---|
| mockpay `suspended` (external) | 502 `CheckoutFailed` | wallet-service: `payment.provider.error_code=AccountSuspended`, mockpay answered 402 |
| orders `ORDERS_BUG_EVERY=2` (our side, S1) | 502 on every 2nd order | orders-service: a `runtime.boundsError` exception ("index out of range") on the new version |

Run the chain without Docker (one terminal each):

```
go run ./mockpay
go run ./services/wallet
go run ./services/orders
go run ./services/storefront

curl -X POST localhost:8080/checkout \
  -d '{"customer_id": "cus_42", "amount": 2500, "currency": "GHS"}'
```

## mockpay

A fake payment provider: the lab's one dependency on a company it does not
control. Its failure mode can be switched while it runs, to stage incidents that
are **not** the lab's fault.

mockpay sends no telemetry to SigNoz, on purpose. A real provider is a black
box, so wasitus has to recognise these failures only from what the lab's own
services see when they call it.

| Mode | What callers see | Scenario |
|---|---|---|
| `healthy` | `201` and a charge | |
| `outage` | `503 ServiceUnavailable`; every 5th call hangs until the caller times out | S2 |
| `slow` | `201`, but only after 6 seconds | |
| `suspended` | `402 AccountSuspended` | S3, S7 |
| `ratelimit` | `429 RateLimited` with `Retry-After` after 60 calls a minute | S4 |
| `badcert` | expired TLS certificate | S5 (coming in its own PR) |

Run it and switch modes:

```
go run ./mockpay                       # listens on :8090, starts healthy

curl -X POST localhost:8090/v1/charges \
  -d '{"amount": 2500, "currency": "GHS", "reference": "order-1001"}'

curl -X PUT localhost:8090/admin/mode -d '{"mode": "suspended"}'
curl localhost:8090/admin/mode
```

Settings: `MOCKPAY_ADDR` (default `:8090`), `MOCKPAY_MODE` (default `healthy`),
`MOCKPAY_SLOW_DELAY` (default `6s`), `MOCKPAY_RATE_LIMIT` (charges per minute,
default `60`). Switching mode resets the rate-limit window and the outage
counter, so every staged scenario starts from the same state. `/healthz`
answers `ok` in every mode, so Docker never restarts mockpay mid-scenario.
