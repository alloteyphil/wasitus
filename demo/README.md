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
make lab-smoke   # prove it works: checkouts, a staged failure, traces in SigNoz
make lab-ps      # what is running
make lab-logs    # follow the logs
make lab-down    # stop (data kept)
make lab-reset   # stop and delete all lab data
```

All ports are bound to 127.0.0.1, so only your machine can reach the lab. If
you ran an earlier version of the lab, start with `make lab-reset`: its
database was created with a password the lab no longer uses.

| What | Where |
|---|---|
| Checkout (the customers' way in) | `curl -X POST localhost:8000/checkout -d '{"customer_id":"cus_42","amount":2500,"currency":"GHS"}'` |
| Switch mockpay's failure mode | `curl -X PUT localhost:8090/admin/mode -d '{"mode":"suspended"}'` |
| Change the customers' rate (S4 x3, S6 x10) | `curl -X PUT localhost:8091/admin/rate -d '{"multiplier": 10}'` |
| SigNoz (traces, logs) | http://localhost:8080 |
| A staged bad deploy (S1) | `ORDERS_VERSION=1.4.0 ORDERS_BUG_EVERY=5 docker compose -f demo/compose.yaml up -d orders-service` |

A fresh SigNoz stores nothing until its first admin account exists, so
`make lab-up` creates a lab-only admin, the lab's alert rules (error rate and
checkout latency), and a read-only API key for wasitus. The login and the key
are generated into `demo/.env` (`LAB_SIGNOZ_EMAIL`, `LAB_SIGNOZ_PASSWORD`,
`SIGNOZ_API_KEY`), which git ignores. Details, and where SigNoz's own files
come from: [signoz/README.md](signoz/README.md).

```
demo/
├── compose.yaml         # the lab: SigNoz + lab services        (done)
├── Dockerfile           # one image recipe for every program    (done)
├── signoz/              # SigNoz, pinned (from Foundry)         (done)
├── scripts/smoke.sh     # end-to-end check of a running lab     (done)
├── internal/lab/        # shared: OpenTelemetry, HTTP, errors   (done)
├── services/            # the lab's own services                (done)
│   ├── storefront/
│   ├── orders/
│   └── wallet/
├── mockpay/             # fake external payment provider        (done)
├── trafficgen/          # the customers: steady checkouts         (done)
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
| `wallet-service` | `POST /payments`; charges mockpay | `MOCKPAY_URL`, `MOCKPAY_CA_FILE` (a CA to trust for https), `WALLET_PROVIDER_TIMEOUT` (3s) |
| `trafficgen` | the customers: `POST /checkout` at a steady rate (2/s in Docker) | `TRAFFIC_TARGET`, `TRAFFIC_RPS`, `TRAFFIC_ADMIN_ADDR` (:8091), `TRAFFIC_SEED`, `TRAFFIC_MAX_IN_FLIGHT`, `TRAFFIC_TIMEOUT` |

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
| `badcert` | over HTTPS, an expired certificate: `x509: certificate has expired` (plain HTTP is unaffected) | S5 |

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
default `60`), and for HTTPS `MOCKPAY_TLS_ADDR` (default `:8443`),
`MOCKPAY_TLS_HOSTS` (default `localhost,127.0.0.1,mockpay`) and
`MOCKPAY_CA_OUT` (where to write the lab CA certificate). mockpay creates a
new lab CA at every start; in Docker it shares the certificate (public part
only) with wallet-service through the `lab-ca` volume, and wallet-service
calls `https://mockpay:8443`. Private keys never leave mockpay's memory. Switching mode resets the rate-limit window and the outage
counter, so every staged scenario starts from the same state. `/healthz`
answers `ok` in every mode, so Docker never restarts mockpay mid-scenario.
