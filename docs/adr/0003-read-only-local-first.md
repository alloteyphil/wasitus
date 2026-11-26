# ADR-0003: Read-only and local-first

Date: 26 Nov 2026 · Status: accepted

## Context
Incident data is sensitive. Teams in regulated sectors (fintech, banking) often
cannot send telemetry to a third-party service. A tool used during an incident must
also never make things worse.

## Decision
- `wasitus` only reads from its sources. No writes, no acknowledgements, no
  annotations in v0.1.
- It runs where the user runs it (laptop or CI). No hosted service, no telemetry,
  no phone-home.
- Secrets come only from environment variables.

## Consequences
- Easy to approve inside security-conscious organisations.
- No hosted product to monetise in v0.1; a hosted mode could be reconsidered later
  as a separate decision.
