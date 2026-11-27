# Scenario fixtures (synthetic)

Each subdirectory (`S1/` through `S8/`) holds a **hand-authored** JSON fixture
for one scenario from [docs/SCENARIOS.md](../../docs/SCENARIOS.md). CI replays
them through `httptest` servers in `cmd/wasitus/scenario_test.go`. They are
**not** byte-for-byte recordings of a live SigNoz session.

They are shaped like the demo lab (multi-service checkout chain, mockpay error
text, 5-minute traffic buckets, alert history) and include cascade spans/logs so
offline verdicts stay honest about class-share dilution. Update a fixture when
live `make scenario ID=…` behaviour changes in a way that would break the
contract.

Run all scenarios:

```
go test ./... -run TestScenario
```

Run a single scenario (as documented in `docs/SCENARIOS.md`):

```
go test ./... -run Scenario/S3
```

All hostnames, services (`storefront`, `orders-service`, `wallet-service`,
`mockpay`), repositories (`alloteyphil/wasitus`), and error messages come from
the local demo lab only (ADR-0002). No real credentials or employer data appear
anywhere in these fixtures.
