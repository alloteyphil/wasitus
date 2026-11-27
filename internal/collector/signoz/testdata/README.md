# SigNoz collector fixtures

Response bodies replayed by `httptest` servers in the SigNoz collector tests.
Every file uses fictional demo-lab data (storefront, orders-service,
wallet-service, mockpay) shaped directly from SigNoz v0.149.0's Go API types
(see `docs/adr/0007-signoz-api.md`). No real tokens, credentials, or external
data appear here.

| File | Endpoint | What it covers |
|---|---|---|
| `version.json` | `GET /api/v1/version` | Tested version `v0.149.0` with setup complete |
| `service_account_me.json` | `GET /api/v1/service_accounts/me` | Viewer service account identity (`wasitus-reader`) |
| `rules.json` | `GET /api/v3/rules` | The two lab alert rules with severity labels |
| `rule_history_page1.json`, `rule_history_page2.json` | `GET /api/v2/rules/{id}/history/timeline` | `pending`, `firing`, repeated `stateChanged: false`, `nextCursor` pagination, and `inactive` (resolved) |
| `error_spans.json` | `POST /api/v5/query_range` (`raw`, `traces`) | Outbound client span (402 to mockpay), inbound server span with `AccountSuspended` exception event, and `orders-service` `runtime.boundsError` panic |
| `error_logs.json` | `POST /api/v5/query_range` (`raw`, `logs`) | Log record whose timestamp arrives only in `data.timestamp` as Unix nanoseconds |
| `traffic_series.json` | `POST /api/v5/query_range` (`time_series`, `traces`) | Queries `A` (total server spans) and `B` (error server spans), `partial: true` point skipped, and `data.warning` surfaced |
