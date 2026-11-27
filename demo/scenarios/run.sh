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
changes=${CHANGES_FILE:-scenarios/changes.yaml}
normal_version=1.3.0 # orders-service's version when nothing is staged

say() { printf '%s  %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

set_mode() {
  curl -fsS -o /dev/null -X PUT "$mockpay/admin/mode" -d "{\"mode\":\"$1\"}" || return
  say "mockpay: $1"
}

set_rate() {
  curl -fsS -o /dev/null -X PUT "$traffic/admin/rate" -d "{\"multiplier\":$1}" || return
  say "traffic: x$1"
}

# deploy VERSION BUG_EVERY: recreate orders-service, as a real deploy would.
# BUG_EVERY=N makes every Nth order hit the staged bug (0: off).
deploy() {
  local out status=0
  out=$(ORDERS_VERSION=$1 ORDERS_BUG_EVERY=$2 \
    docker compose -f compose.yaml up -d --wait orders-service 2>&1) || status=$?
  if ((status != 0)); then
    printf '%s\n' "$out" >&2
    return "$status"
  fi
  say "orders-service: deployed $1 (bug every $2 orders; 0 means off)"
}

# orders_env NAME: an environment variable of the running orders-service.
orders_env() {
  local cid
  cid=$(docker compose -f compose.yaml ps -q orders-service)
  [[ -n "$cid" ]] || return 1
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$cid" | sed -n "s/^$1=//p"
}

# init_changes ensures the changes file exists so `wasitus timeline` against
# demo/wasitus.lab.yaml succeeds even after scenarios that make no deploys.
init_changes() {
  if [[ ! -f "$changes" ]]; then
    printf '# What "people did" during staged incidents. Written by demo/scenarios/run.sh.\n' >"$changes"
  fi
}

# note SERVICE TITLE [CHANGE_TYPE]: a change, as a person would write it down.
note() {
  init_changes
  printf -- '- time: "%s"\n  kind: change\n  service: %s\n  title: "%s"\n' "$(now)" "$1" "$2" >>"$changes"
  if [[ -n "${3:-}" ]]; then
    printf -- '  change_type: %s\n' "$3" >>"$changes"
  fi
}

hold() {
  say "holding for $1 minute(s); Ctrl-C ends early and resets"
  sleep $(($1 * 60))
}

# reset_lab tries every reset step even if one fails, so a transient error in
# one service never leaves another service stuck in a broken state.
reset_lab() {
  local rc=0
  set_mode healthy || rc=1
  set_rate 1 || rc=1
  local ver bug
  if ver=$(orders_env APP_VERSION) && bug=$(orders_env ORDERS_BUG_EVERY); then
    if [[ "$ver" != "$normal_version" || "$bug" != 0 ]]; then
      if deploy "$normal_version" 0; then
        note orders-service "Rolled back orders-service to v$normal_version" deploy
      else
        rc=1
      fi
    fi
  else
    say "orders-service: not running; skipping rollback check"
    rc=1
  fi
  return "$rc"
}

id=$(tr '[:lower:]' '[:upper:]' <<<"${1:-}")
minutes=${2:-6}
if [[ -z "$id" ]]; then
  echo "usage: $0 S1..S8 [minutes] | reset" >&2
  exit 2
fi
if [[ "$id" == RESET ]]; then
  reset_lab
  exit $?
fi
case "$id" in
S1 | S2 | S3 | S4 | S5 | S6 | S7 | S8) ;;
*)
  echo "unknown scenario '$1': use S1 to S8 or reset" >&2
  exit 2
  ;;
esac
if ! [[ "$minutes" =~ ^[1-9][0-9]*$ ]]; then
  echo "minutes must be a whole number of at least 1, got '$minutes'" >&2
  exit 2
fi
curl -fs -o /dev/null "$mockpay/healthz" || {
  echo "The lab is not running (mockpay does not answer). Start it: make lab-up" >&2
  exit 1
}
curl -fs -o /dev/null "$traffic/healthz" || {
  echo "The lab is not running (trafficgen does not answer). Start it: make lab-up" >&2
  exit 1
}

init_changes
trap 'say "resetting the lab"; reset_lab || true' EXIT
start=$(now)
say "scenario $id: starting"

case "$id" in
S1) # Bad deploy: a new orders-service version faults every 5th order (~20%).
  note orders-service "Deployed orders-service v1.4.0 (refactor order validation)" deploy
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
  note orders-service "Deployed orders-service v1.3.1 (log wording only)" deploy
  deploy 1.3.1 0
  hold 5
  start=$(now) # the incident starts now; the deploy is before it
  set_mode suspended
  hold "$minutes"
  ;;
S8) # Unclear: small, mixed provider errors and no change. No cause dominates.
  # At 2 req/s, mockpay's 60/min rate limit takes 30s to exhaust, so 40s in
  # ratelimit produces ~20 errors (429), matching 10s of outage and 10s of
  # suspended (~20 errors each) so all three classes stay roughly even.
  end=$(($(date +%s) + minutes * 60))
  while (($(date +%s) < end)); do
    for step in "ratelimit 40" "healthy 30" "outage 10" "healthy 30" "suspended 10" "healthy 30"; do
      rem=$((end - $(date +%s)))
      ((rem <= 0)) && break
      read -r m secs <<<"$step"
      ((secs > rem)) && secs=$rem
      set_mode "$m"
      sleep "$secs"
    done
  done
  ;;
esac

end_time=$(now)
say "scenario $id: done. Incident window (UTC): $start to $end_time"
say "see it: wasitus timeline --config demo/wasitus.lab.yaml --from $start --to $end_time"
