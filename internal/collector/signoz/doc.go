// Package signoz reads telemetry from SigNoz (tested against v0.149.0):
// alert rule history, error spans, error logs, and per-service request/error
// counts.
//
// It uses only the standard library and is strictly read-only, calling only:
//
//	GET  /api/v1/version                                    (no auth)
//	GET  /api/v1/service_accounts/me                        (auth check)
//	GET  /api/v3/rules?limit=200&offset=N                   (alert rules)
//	GET  /api/v2/rules/{id}/history/timeline?start&end      (alert transitions)
//	POST /api/v5/query_range                                (raw traces/logs, time-series counts)
//
// Authentication uses a read-only service account key (signoz-viewer role)
// sent in the SigNoz-Api-Key header; it is never sent as a bearer token and
// never appears in errors or events. See docs/adr/0007-signoz-api.md for the
// endpoint choices, pagination limits, and timestamp units.
//
// Like every collector in wasitus, this package depends only on internal/event
// (not internal/fingerprint or internal/traffic); callers convert ErrorSample
// and TrafficBucket into downstream types.
package signoz
