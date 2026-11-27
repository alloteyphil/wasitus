# Recorded scenario fixtures

Each subdirectory (`S1/` through `S8/`) holds the recorded inputs for one
scenario from [docs/SCENARIOS.md](../../docs/SCENARIOS.md) and is replayed
offline through `httptest` servers by `cmd/wasitus/scenario_test.go`.

Run all scenarios:

```
go test ./... -run TestScenarios
```

Run a single scenario (as documented in `docs/SCENARIOS.md`):

```
go test ./... -run Scenario/S3
```

All hostnames, services (`storefront`, `orders-service`, `wallet-service`,
`mockpay`), repositories (`alloteyphil/wasitus`), and error messages come from
the local demo lab only (ADR-0002). No real credentials or employer data appear
anywhere in these fixtures.
