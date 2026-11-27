#!/usr/bin/env bash
# Stages one incident from docs/SCENARIOS.md in the running lab, holds it, and
# puts the lab back to normal.
#
#   demo/scenarios/run.sh S2        # or: make scenario ID=S2
#   demo/scenarios/run.sh S2 10     # hold the fault for 10 minutes (default 6)
#   demo/scenarios/run.sh reset     # or: make scenario-reset
#
# The fault is held long enough for SigNoz's alert to fire, about 3 minutes
# after it starts (docs/adr/0007-signoz-api.md). However the run ends (done,
# Ctrl-C or an error), the lab is reset: mockpay healthy, traffic x1,
# orders-service back on its normal version. Deploys and rollbacks are written
# to demo/scenarios/changes.yaml, the changes file wasitus reads
# (demo/wasitus.lab.yaml), as the notes a person would keep.
#
# Needs the lab running (make lab-up), curl and docker.
set -euo pipefail

cd "$(dirname "$0")/.." # demo/
mockpay=${MOCKPAY_URL:-http://localhost:8090}
traffic=${TRAFFIC_URL:-http://localhost:8091}
changes=scenarios/changes.yaml
normal_version=1.3.0 # orders-service's version when nothing is staged

say() { printf '%s  %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

set_mode() {
  curl -fsS -o /dev/null -X PUT "$mockpay/admin/mode" -d "{\"mode\":\"$1\"}"
  say "mockpay: $1"
}

set_rate() {
  curl -fsS -o /dev/null -X PUT "$traffic/admin/rate" -d "{\"multiplier\":$1}"
  say "traffic: x$1"
}

# deploy VERSION BUG_EVERY: recreate orders-service, as a real deploy would.
# BUG_EVERY=N makes every Nth order hit the staged bug (0: off).
deploy() {
  ORDERS_VERSION=$1 ORDERS_BUG_EVERY=$2 \
    docker compose -f compose.yaml up -d --wait orders-service >/dev/null 2>&1
  say "orders-service: deployed $1 (bug every $2 orders; 0 means off)"
}

# orders_env NAME: an environment variable of the running orders-service.
orders_env() {
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' \
    "$(docker compose -f compose.yaml ps -q orders-service)" | sed -n "s/^$1=//p"
}

# note SERVICE TITLE: a change, as a person would write it down.
note() {
  if [[ ! -f "$changes" ]]; then
    printf '# What "people did" during staged incidents. Written by demo/scenarios/run.sh.\n' >"$changes"
  fi
  printf -- '- time: "%s"\n  kind: change\n  service: %s\n  title: "%s"\n' "$(now)" "$1" "$2" >>"$changes"
}

hold() {
  say "holding for $1 minute(s); Ctrl-C ends early and resets"
  sleep $(($1 * 60))
}

reset_lab() {
  set_mode healthy
  set_rate 1
  if [[ "$(orders_env APP_VERSION)" != "$normal_version" || "$(orders_env ORDERS_BUG_EVERY)" != 0 ]]; then
    note orders-service "Rolled back orders-service to v$normal_version"
    deploy "$normal_version" 0
  fi
}

id=$(tr '[:lower:]' '[:upper:]' <<<"${1:-}")
minutes=${2:-6}
if [[ -z "$id" ]]; then
  echo "usage: $0 S1..S8 [minutes] | reset" >&2
  exit 2
fi
if [[ "$id" == RESET ]]; then
  reset_lab
  exit 0
fi
if ! [[ "$minutes" =~ ^[1-9][0-9]*$ ]]; then
  echo "minutes must be a whole number of at least 1, got '$minutes'" >&2
  exit 2
fi
curl -fsS -o /dev/null "$mockpay/healthz" || {
  echo "The lab is not running (mockpay does not answer). Start it: make lab-up" >&2
  exit 1
}

trap 'say "resetting the lab"; reset_lab' EXIT
start=$(now)
say "scenario $id: starting"

case "$id" in
S1) # Bad deploy: a new orders-service version breaks 20% of orders.
  note orders-service "Deployed orders-service v1.4.0 (refactor order validation)"
  deploy 1.4.0 5
  hold "$minutes"
  ;;
S2) # Provider outage.
  set_mode outage
  hold "$minutes"
  ;;
S3) # Our account with the provider is suspended.
  set_mode suspended
  hold "$minutes"
  ;;
S4) # Rate limited: three times the usual traffic hits the provider's quota.
  set_mode ratelimit
  set_rate 3
  hold "$minutes"
  ;;
S5) # The provider serves an expired TLS certificate.
  set_mode badcert
  hold "$minutes"
  ;;
S6) # Traffic surge, no fault anywhere.
  set_rate 10
  hold "$minutes"
  ;;
S7) # Red herring: a harmless deploy, then 5 minutes later the provider
  # suspends the account. The verdict must not blame the deploy.
  note orders-service "Deployed orders-service v1.3.1 (log wording only)"
  deploy 1.3.1 0
  hold 5
  start=$(now) # the incident starts now; the deploy is before it
  set_mode suspended
  hold "$minutes"
  ;;
S8) # Unclear: small, mixed provider errors and no change. No cause dominates.
  end=$(($(date +%s) + minutes * 60))
  while (($(date +%s) < end)); do
    for step in "ratelimit 20" "healthy 40" "outage 10" "healthy 40" "suspended 10" "healthy 40"; do
      read -r m secs <<<"$step"
      set_mode "$m"
      sleep "$secs"
    done
  done
  ;;
*)
  echo "unknown scenario '$1': use S1 to S8 or reset" >&2
  trap - EXIT
  exit 2
  ;;
esac

say "scenario $id: done. Incident window (UTC): $start to $(now)"
say "see it: wasitus timeline --config demo/wasitus.lab.yaml --from $start --to $(now)"
