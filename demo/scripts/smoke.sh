#!/usr/bin/env bash
# Smoke test for a running lab (start it with `make lab-up`).
#
# Proves the whole path works, not just that containers are "healthy":
# checkouts succeed, a staged provider failure (mockpay suspended) gives the
# customer a 502, and the traces of both reach SigNoz with the evidence
# wasitus needs. A healthy-looking lab once stored nothing at all because
# SigNoz's first-run setup was missing; this test would have caught it.
#
# Needs curl and docker. Reads SigNoz's ClickHouse directly, so it is tied to
# the pinned SigNoz version (see demo/signoz/README.md).
set -euo pipefail

store=${STOREFRONT_URL:-http://localhost:8000}
mockpay=${MOCKPAY_URL:-http://localhost:8090}
body='{"customer_id":"cus_42","amount":2500,"currency":"GHS"}'

checkout() { curl -s -o /dev/null -w '%{http_code}' -X POST "$store/checkout" -d "$body"; }
set_mode() { curl -fsS -o /dev/null -X PUT "$mockpay/admin/mode" -d "{\"mode\":\"$1\"}"; }
ch() { docker exec signoz-telemetrystore-clickhouse-0-0 clickhouse-client -q "$1"; }
fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

trap 'set_mode healthy || true' EXIT
start=$(date -u +%s)

for i in 1 2 3 4 5; do
  code=$(checkout)
  [[ "$code" == 201 ]] || fail "healthy checkout $i returned $code, want 201"
done

set_mode suspended
for i in 1 2 3; do
  code=$(checkout)
  [[ "$code" == 502 ]] || fail "checkout $i with mockpay suspended returned $code, want 502"
done
set_mode healthy

echo "Checkouts behave. Waiting for their traces in SigNoz..."
services=0 suspended=0
for _ in $(seq 1 30); do
  services=$(ch "SELECT uniqExact(serviceName) FROM signoz_traces.distributed_signoz_index_v3
                 WHERE timestamp >= toDateTime($start)")
  suspended=$(ch "SELECT count() FROM signoz_traces.distributed_signoz_index_v3
                  WHERE timestamp >= toDateTime($start) AND serviceName = 'wallet-service'
                    AND attributes_string['payment.provider.error_code'] = 'AccountSuspended'")
  if [[ "$services" -ge 3 && "$suspended" -ge 3 ]]; then
    echo "SMOKE OK: spans from $services services; $suspended wallet-service spans carry AccountSuspended"
    exit 0
  fi
  sleep 3
done
fail "after 90s SigNoz has spans from $services services and $suspended AccountSuspended spans (want at least 3 and 3)"
