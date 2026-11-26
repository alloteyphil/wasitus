# ADR-0005: Build a small lab app instead of using the OpenTelemetry Demo

Date: 26 Nov 2026 · Status: accepted

## Context
The OpenTelemetry Demo (a multi-service online shop) is a ready-made source of
telemetry with built-in failure flags. However:
- it is heavy (many services) to run next to SigNoz on a laptop;
- its services ship as prebuilt images, so a "bad deploy" of my own code is
  awkward to stage;
- it has no clearly external dependency whose failure modes I control.

## Decision
Build a minimal lab in `demo/`: three small Go services, a fake external payment
provider (`mockpay`) with switchable failure modes, a traffic generator, and local
SigNoz.

## Consequences
- Every scenario in SCENARIOS.md can be staged with one command and has a known
  cause.
- More work in M1 (about one weekend).
- The lab doubles as the public demo and quickstart.
