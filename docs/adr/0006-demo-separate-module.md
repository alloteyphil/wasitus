# ADR-0006: The demo lab is a separate Go module

Date: 26 Nov 2026 · Status: accepted

## Context
The lab's services need the OpenTelemetry SDK and exporters, which bring many
dependencies. wasitus is a read-only tool that people install and trust with
access to their observability data, so its dependency list is part of that
trust: every module in `go.mod` is code someone has to review and keep patched.
If the lab lived in the root module, its dependencies would appear in wasitus's
`go.mod` even though the binary never uses them.

## Decision
`demo/` has its own `go.mod` (module `github.com/alloteyphil/wasitus/demo`). The
lab does not import wasitus packages, and wasitus does not import the lab.

## Consequences
- wasitus's `go.mod` stays small, and reviewers and Dependabot see the tool's
  and the lab's dependencies separately.
- `go test ./...` at the root skips `demo/`; CI checks the lab in its own job,
  and Dependabot watches both directories.
- Working on both at once needs two commands (or a local, uncommitted
  `go.work`).
