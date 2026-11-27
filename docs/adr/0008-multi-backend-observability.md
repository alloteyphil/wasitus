# ADR-0008: Multi-backend observability (accessibility without diluting the verdict)

Date: 27 Nov 2026 · Status: accepted

## Context

v0.1 only speaks SigNoz for symptoms (alerts, errors, traffic). That proves the
verdict thesis, but most developers live in Prometheus/Grafana, Datadog, or a
generic OpenTelemetry stack. Asking them to adopt SigNoz first caps adoption.

The pipeline after collection is already backend-agnostic: every source becomes
an `Event`, then fingerprint → correlate → verdict. The blocker is collection
coverage, not scoring.

## Decision

1. **Keep one `Collector` interface.** New backends are new packages under
   `internal/collector/<name>` that emit the same `Event` shapes. Verdict code
   does not import backend SDKs.
2. **Backends declare capabilities**, not “full SigNoz clones”:

   | Capability | Feeds | Needed for |
   |---|---|---|
   | `alerts` | symptom events | timeline richness |
   | `errors` | samples for fingerprinting | EXTERNAL class (account/limit/…) |
   | `traffic` | request/error rate buckets | EXTERNAL / traffic, spike evidence |

   A backend may ship with a subset. Config validation states clearly what
   verdicts are weakened when `errors` or `traffic` is missing.
3. **Grow backends by developer reach**, one major path per minor version, with
   httptest fixtures and synthetic scenarios before any live dependency.
4. **SigNoz remains the reference lab** (ADR-0005). Alternate backends get
   fixture-first CI; optional Compose profiles come later, not as a gate.
5. **Hard rules unchanged:** read-only, secrets from env, own-data only,
   deterministic verdict (ADR-0002–0004).

## Consequences

- More YAML surface (`sources.prometheus`, `sources.datadog`, …) and more ADRs
  for each API spike (like ADR-0007 for SigNoz).
- Partial backends are honest: e.g. Prometheus-only may support traffic + alerts
  well but need Loki/OTLP later for rich error classes.
- Release notes must say which capabilities each source provides so strangers
  know what they get without SigNoz.
