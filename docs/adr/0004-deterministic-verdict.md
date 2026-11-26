# ADR-0004: Rules decide the verdict; AI only summarises

Date: 26 Nov 2026 · Status: accepted

## Context
Many recent incident tools let an LLM read the logs and decide the root cause.
Their answers are hard to test, can change between runs, and can invent causes.
During an incident, people need to trust and check the conclusion quickly.

## Decision
- Fingerprinting, correlation and the verdict are deterministic rules with
  documented weights and thresholds.
- Every verdict lists the evidence lines that produced it.
- An optional AI summary (post-v0.1) may only rephrase the evidence and verdict; it
  cannot change them, and it is off by default.

## Consequences
- Verdicts are reproducible and covered by the scenario suite in CI.
- Rules need tuning and will miss unusual cases; `UNKNOWN` is a valid, tested
  outcome.
