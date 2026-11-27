# ADR-0007: How wasitus reads SigNoz

Date: 27 Nov 2026 · Status: accepted

## Context
wasitus needs four things from SigNoz: alert history (fired and resolved), error
spans and error logs with their attributes, and request counts per service for
spike detection. SigNoz's HTTP API is only partly documented and has several
generations side by side, so the M2 spike checked SigNoz **v0.149.0** (the lab's
pinned version) against its source and a live lab. What it found:

- **Auth.** Logging in (`POST /api/v2/sessions/email_password`) returns an opaque
  bearer token (not a JWT) that expires after 30 minutes. Long-lived API keys
  now belong to *service accounts*: an admin creates the account, gives it a
  role (`signoz-viewer`), and creates a key, which is shown once. The key is
  sent as `SigNoz-Api-Key: <key>`; sent as a bearer token it is rejected.
- **Query API.** `POST /api/v5/query_range` is the only query endpoint in
  SigNoz's OpenAPI spec. `/api/v3/query_range` and `/api/v4/query_range` still answer but are
  legacy (values as strings, unsorted). A viewer key can run every query wasitus
  needs, and also raw ClickHouse SQL.
- **Alert history** exists only per rule:
  `GET /api/v2/rules/{id}/history/timeline`. There is no endpoint across rules.
  Rules are listed with `GET /api/v3/rules` (`GET /api/v2/rules` is deprecated).
- **Rules see data late.** Threshold rules evaluate a window that ends 2 minutes
  before the evaluation time (`ruler.eval_delay`, default 2m). In the lab, a
  rule fired 2 min 53 s after the failure began. Resolving lags more: after a
  4-minute staged failure ended, the error-rate rule went back to `inactive`
  7 min 36 s later (its 5-minute window has to empty, plus the 2-minute delay
  and up to one 1-minute step). Both were read through the viewer key. (In
  that second run the rule fired only 36 s after the failure began, because
  errors from the smoke test minutes earlier were still inside its window; it
  is not a measure of detection delay.)
- **Limits.** Raw queries return at most 10,000 rows. The raw `nextCursor` is the
  last row's millisecond, and the next page starts *before* that millisecond, so
  rows sharing it are skipped (32 of 100 spans were lost in the lab with small
  pages). Time-series queries over traces and logs are held to at most 1,500
  points per series, so 5-minute buckets fit about 125 hours, not 7 days (a
  7-day request is silently widened to 400-second buckets, with a warning).
- **Errors.** Exception type and message are span *events*, not attributes.
  `status_message` is often empty (otelhttp overwrites it on 5xx). An unknown
  field in a filter is not an error: the response is 200 with a warning.

## Decision
- **Auth:** wasitus uses one API key of a service account with the
  `signoz-viewer` role, read from the environment variable named in the config
  (`api_key_env`, default `SIGNOZ_API_KEY`), sent as `SigNoz-Api-Key`. wasitus
  never logs in with a password and never needs more than viewer rights. The
  lab's setup (`demo/signoz/setup.sh`, run by `make lab-up`) logs in as the
  lab admin to create that key and the lab's alert rules; nothing else does.
  Checked in the lab: the viewer key reads everything below, and an attempt to
  create a rule with it is refused (403 "only editors/admins can access this
  resource"), so ADR-0003's read-only promise is enforced by SigNoz itself.
- **Endpoints** (all viewer):

  | Need | Endpoint |
  |---|---|
  | Health and version | `GET /api/v1/version` (no auth) |
  | Check the key, `wasitus doctor` | `GET /api/v1/service_accounts/me` |
  | Alert rules | `GET /api/v3/rules?limit=200&offset=N` |
  | Alert fired/resolved | `GET /api/v2/rules/{id}/history/timeline?start&end` (ms), one call per rule, pages via its offset `nextCursor` |
  | Error spans | `POST /api/v5/query_range`, `requestType: raw`, `signal: traces`, filter `has_error = true`, no `selectFields` (returns all attributes, resource and events) |
  | Error logs | same, `signal: logs`, filter `severity_text = 'ERROR'` (or `severity_number >= 17`) |
  | Request counts | same endpoint, `requestType: time_series`, `count()` of `kind_string = 'Server'` spans grouped by `service.name`, `stepInterval: 300`, `formatOptions.fillGaps: true` |

- **Pagination:** wasitus does not follow the raw `nextCursor`. It asks for up
  to 10,000 rows per request; when a page is full it splits the time range
  itself, with inclusive boundaries, and removes duplicates by `span_id` (spans)
  or `id` (logs).
- **Baseline:** the 7-day baseline is fetched separately from the incident
  window, in chunks of at most 120 hours, so every bucket is 5 minutes.
- **Times:** wasitus sends Unix milliseconds and parses each field in its own
  unit (see Consequences). Every `data.warning` in a response becomes a visible
  warning in wasitus's output.
- **Isolation:** all of this lives behind the SigNoz collector interface, with
  recorded responses (scrubbed) replayed in tests, so an API change touches one
  package.

## Consequences
- A team can give wasitus read-only access without sharing a person's login.
  But in v0.149.0 a viewer key can also run raw ClickHouse SQL, so it can read
  all telemetry in that SigNoz. demo/signoz/README.md says so for the lab; the
  user-facing README must say it too when the SigNoz source ships (M2).
- Alert history costs one request per rule, because the `states` filter of
  `/api/v3/rules` is about the current state, not the past. That is fine for
  tens of rules; with hundreds, wasitus will need a concurrency limit.
- Alert times are evaluation times. A "fired" event lags the first bad data by
  the rule's evaluation delay plus up to one frequency step (about 2 to 4
  minutes with defaults), and a "resolved" event lags the recovery by the
  rule's window as well (about 7 to 8 minutes for a 5-minute window). The
  timeline must take the start and end of a symptom from the error data, and
  show alerts as what they are: late, rounded signals. Errors still inside a
  rule's window from an earlier problem can make it fire early.
- Units differ per field: request `start`/`end` and series timestamps are ms;
  raw span `timestamp` is an RFC 3339 string; raw log `data.timestamp` is an
  integer in ns; `events[].timeUnixNano` and `duration_nano` are ns; history
  `unixMilli` is ms; API key `expiresAt` and session `expiresIn` are seconds.
- Not available, so not used: alert history across all rules in one call
  (except through raw SQL on SigNoz's internal tables, which wasitus will not
  depend on); 5-minute buckets over 7 days in one query; exception fields as
  span columns.
- Legacy endpoints (`/api/v3/query_range` and `/api/v4/query_range`, `/api/v1/rules`,
  `/api/v1/listErrors`) are not used, so their removal will not break wasitus.
- Other SigNoz versions need their own check; wasitus reads `/api/v1/version`
  and warns when it is not a version it was tested with.
