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
| `compose.yaml` | Header comment | Points here |

Only the UI (8080) and OTLP (4317, 4318) are published to your machine.
ClickHouse and Postgres are reachable only inside Docker's network.

## Upgrading

1. Pick a SigNoz release at least two weeks old and the signoz-otel-collector
   release just before it.
2. Compare Foundry's latest `docs/examples/docker/compose` output with these
   files, apply the changes in the table above again, and update the table.
3. `make lab-reset && make lab-up`, then check traces still arrive.
