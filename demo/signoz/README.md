# SigNoz for the demo lab

A local SigNoz (UI on http://localhost:8080, OTLP on 4317/4318) that the lab's
services send traces and logs to, and that wasitus reads from.

## Where these files come from

SigNoz installs through [Foundry](https://github.com/SigNoz/foundry) since its
Docker Compose files left the main repository. Foundry turns `casting.yaml`
into the files in this folder. They are copied from Foundry **v0.5.0**,
`docs/examples/docker/compose/pours/deployment/`, so nobody needs Foundry
installed to run the lab.

Changes from that output (everything else is as generated):

| File | Change | Why |
|---|---|---|
| `compose.yaml` | `signoz/signoz:latest` → `v0.149.0`, `signoz/signoz-otel-collector:latest` → `v0.144.15` (the collector release before that SigNoz release) | `latest` changes under you; the lab must be reproducible |
| `compose.yaml` | Postgres uses `POSTGRES_HOST_AUTH_METHOD=trust` and SigNoz connects without a password, instead of the literal password `signoz` | No password to store or leak: the database has no published port and is reachable only inside Docker's network |
| `compose.yaml` | `LOW_CARDINAL_EXCEPTION_GROUPING=false` set on the ingester | `ingester.yaml` reads it, but the generated file never sets it |
| `compose.yaml` | Published ports bound to `127.0.0.1` (8080, 4317, 4318) instead of all interfaces | A fresh SigNoz lets anyone register as admin until `setup.sh` runs; nobody else on your network should reach it |
| `compose.yaml` | Header comment | Points here |

Only the UI (8080) and OTLP (4317, 4318) are published, and only on
127.0.0.1, so they are reachable from your machine alone. ClickHouse and
Postgres are reachable only inside Docker's network.

## What `make lab-up` sets up in SigNoz

`setup.sh` runs after the containers are healthy. Every step is skipped when
it is already done, so running it again changes nothing.

1. **The first admin account.** A fresh SigNoz ingests nothing until one
   exists. The login is generated into `demo/.env` (`LAB_SIGNOZ_EMAIL`,
   `LAB_SIGNOZ_PASSWORD`).
2. **Alert rules**, one file each in `alerts/` (milestone M1): a lab service's
   error rate above 5% over 5 minutes, and storefront's checkout p99 above 3s.
   Rules are matched by name, so a rule edited in SigNoz's UI is not
   overwritten. Expect a rule to fire 2 to 4 minutes after a failure starts:
   SigNoz evaluates data with a 2-minute delay (docs/adr/0007-signoz-api.md).
3. **A read-only API key for wasitus**: a service account `wasitus-reader`
   with SigNoz's viewer role, its key saved as `SIGNOZ_API_KEY` in
   `demo/.env`. SigNoz refuses any change made with it.

All three are made with the API calls recorded in ADR-0007.

## Upgrading

1. Pick a SigNoz release at least two weeks old and the signoz-otel-collector
   release just before it.
2. Compare Foundry's latest `docs/examples/docker/compose` output with these
   files, apply the changes in the table above again, and update the table.
3. `make lab-reset && make lab-up`, then check traces still arrive.
