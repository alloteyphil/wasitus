#!/usr/bin/env bash
# Finishes SigNoz's setup for the lab, so the lab works with one command.
#
# A fresh SigNoz accepts no telemetry until its first admin account exists:
# until then the SigNoz server gives its ingester a do-nothing ("nop")
# pipeline, and the OTLP ports stay closed. This script:
#   1. creates a lab-only admin (login in demo/.env, which git ignores) and
#      waits until the ingester accepts OTLP;
#   2. creates the alert rules in signoz/alerts/*.json;
#   3. creates a read-only API key for wasitus (SIGNOZ_API_KEY in demo/.env)
#      and checks that it works.
# Safe to run again: every step is skipped once done (signoz/README.md).
#
# Run by `make lab-up`. Needs curl, jq and openssl.
set -euo pipefail

cd "$(dirname "$0")/.." # demo/
env_file=.env
signoz=${SIGNOZ_URL:-http://localhost:8080}
otlp=${OTLP_HTTP_URL:-http://localhost:4318}

umask 077
touch "$env_file"
chmod 600 "$env_file" # readable by you only, even if it already existed
get() { sed -n "s/^$1=//p" "$env_file" | tail -n 1; }

# Lab-only admin credentials, created once and never committed. The random
# part comes from openssl; the fixed part guarantees the upper case, lower
# case, digit and symbol that SigNoz's password rules ask for.
just_generated=false
if [[ -z "$(get LAB_SIGNOZ_PASSWORD)" ]]; then
  just_generated=true
  generated="Lab-Aa1-$(openssl rand -hex 12)"
  # Start on a new line even if the file does not end with one.
  if [[ -s "$env_file" && -n "$(tail -c 1 "$env_file")" ]]; then echo >>"$env_file"; fi
  {
    echo "LAB_SIGNOZ_EMAIL=admin@wasitus-lab.local"
    echo "LAB_SIGNOZ_PASSWORD=$generated"
  } >>"$env_file"
fi
email=$(get LAB_SIGNOZ_EMAIL)
password=$(get LAB_SIGNOZ_PASSWORD)

setup_done=$(curl -fsS "$signoz/api/v1/version" | jq -r '.setupCompleted')
if [[ "$setup_done" != "true" ]]; then
  echo "SigNoz: creating the lab admin account ($email)"
  # The password reaches jq through the environment, not the command line,
  # so it never shows up in `ps`.
  LAB_EMAIL="$email" LAB_PASSWORD="$password" jq -n \
    '{name: "Lab Admin", email: env.LAB_EMAIL, password: env.LAB_PASSWORD, orgDisplayName: "wasitus lab", orgName: "wasitus-lab"}' |
    curl -fsS -X POST "$signoz/api/v1/register" -H 'Content-Type: application/json' --data @- >/dev/null
  # A brand-new SigNoz (for example after make lab-reset) cannot know an API
  # key from an earlier one, so forget it; a new one is created below.
  # grep exits 1 when no line is left (fine) and 2 on a real error (stop,
  # leaving .env as it was). The temp file holds secrets: .gitignore covers
  # .env.* so an interrupted run can never get it committed.
  grep -v '^SIGNOZ_API_KEY=' "$env_file" >"$env_file.tmp" || [[ $? -eq 1 ]]
  mv "$env_file.tmp" "$env_file"
elif [[ "$just_generated" == true ]]; then
  echo "Warning: SigNoz was already set up, but demo/.env had no login, so the one just" >&2
  echo "written there does not work. Run 'make lab-reset && make lab-up' for a fresh lab." >&2
fi

# After signup the server sends the ingester its real pipeline config and the
# ingester restarts, which can take a little while.
echo "Waiting for SigNoz's ingester to accept OTLP on $otlp ..."
ready=false
for _ in $(seq 1 60); do
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$otlp/v1/traces" \
    -H 'Content-Type: application/json' -d '{}' || true)
  if [[ "$code" == 2* ]]; then
    ready=true
    break
  fi
  sleep 3
done
if [[ "$ready" != true ]]; then
  echo "The ingester did not open OTLP within 3 minutes. Check: make lab-logs" >&2
  exit 1
fi

if [[ "$just_generated" == true && "$setup_done" == "true" ]]; then
  exit 0 # the warning above applies: we cannot log in, so skip the lab content
fi

# ---- Lab content: alert rules, and a read-only key for wasitus ---------------
# docs/adr/0007-signoz-api.md explains these endpoints.

# Log in as the lab admin. The session token stays in this shell: curl gets
# it through a file descriptor, never on its command line.
org=$(curl -fsS "$signoz/api/v2/sessions/context?email=$(jq -rn --arg e "$email" '$e|@uri')&ref=$signoz" |
  jq -r '.data.orgs[0].id')
# `|| true`: a refused login must reach the explanation below, not stop at a
# bare curl error.
session=$(LAB_EMAIL="$email" LAB_PASSWORD="$password" ORG_ID="$org" jq -n \
  '{email: env.LAB_EMAIL, password: env.LAB_PASSWORD, orgId: env.ORG_ID}' |
  curl -fs -X POST "$signoz/api/v2/sessions/email_password" -H 'Content-Type: application/json' --data @- |
  jq -r '.data.accessToken') || true
if [[ -z "$session" || "$session" == null ]]; then
  echo "Could not log in to SigNoz with the LAB_SIGNOZ_* values in demo/.env." >&2
  echo "If demo/.env was deleted while the lab kept its data, start fresh:" >&2
  echo "  make lab-reset && make lab-up" >&2
  exit 1
fi

# api METHOD PATH [body]: an authenticated call; with a third argument, the
# request body is read from stdin. On an HTTP error it names the endpoint and
# prints SigNoz's error message, then fails.
api() {
  local out
  if ! out=$(curl -sS --fail-with-body -X "$1" "$signoz$2" -H 'Content-Type: application/json' \
    -H @<(printf 'Authorization: Bearer %s\n' "$session") ${3:+--data @-}); then
    echo "SigNoz API call failed: $1 $2: $(jq -r '.error.message? // .' <<<"$out" 2>/dev/null)" >&2
    return 1
  fi
  printf '%s\n' "$out"
}

# Alert rules (milestone M1): one file per rule in signoz/alerts/, created
# once, matched by name so editing a rule in SigNoz's UI is not overwritten.
existing=$(api GET /api/v3/rules | jq -r '.data.rules[]?.alert')
for rule in signoz/alerts/*.json; do
  name=$(jq -r .alert "$rule")
  if ! grep -Fxq "$name" <<<"$existing"; then
    api POST /api/v2/rules body <"$rule" >/dev/null
    echo "SigNoz: created alert rule \"$name\""
  fi
done

# The key wasitus reads SigNoz with: a service account with the viewer role
# only, so wasitus can never change anything. Stored as SIGNOZ_API_KEY.
if [[ -z "$(get SIGNOZ_API_KEY)" ]]; then
  account=$(api GET /api/v1/service_accounts |
    jq -r '[.data[]? | select(.name == "wasitus-reader")][0].id // empty')
  if [[ -z "$account" ]]; then
    account=$(jq -n '{name: "wasitus-reader"}' | api POST /api/v1/service_accounts body | jq -r .data.id)
  fi
  viewer=$(api GET /api/v1/roles | jq -r '.data[] | select(.name == "signoz-viewer") | .id')
  has_role=$(api GET "/api/v1/service_accounts/$account" |
    jq --arg r "$viewer" '[.data.serviceAccountRoles[]? | select(.roleId == $r)] | length')
  if [[ "$has_role" == 0 ]]; then
    jq -n --arg a "$account" --arg r "$viewer" '{serviceAccountId: $a, roleId: $r}' |
      api POST /api/v1/service_account_roles body >/dev/null
  fi
  # A key is shown only once, so a lost one is replaced by a new, unique name
  # (SigNoz allows lower-case letters, digits and '-' only).
  key=$(jq -n --arg n "wasitus-$(date -u +%Y%m%d-%H%M%S)" --argjson exp "$(($(date +%s) + 90 * 86400))" \
    '{name: $n, expiresAt: $exp}' | api POST "/api/v1/service_accounts/$account/keys" body | jq -r .data.key)
  if [[ -z "$key" || "$key" == null ]]; then
    echo "SigNoz did not return an API key" >&2
    exit 1
  fi
  if [[ -s "$env_file" && -n "$(tail -c 1 "$env_file")" ]]; then echo >>"$env_file"; fi
  echo "SIGNOZ_API_KEY=$key" >>"$env_file"
  echo "SigNoz: created a read-only API key for wasitus (SIGNOZ_API_KEY in demo/.env)"
fi

# Prove the key works and is the read-only account.
# (A key expires after 90 days; `|| true` lets an expired or unknown key reach
# the explanation instead of a bare curl error.)
who=$(curl -fs "$signoz/api/v1/service_accounts/me" \
  -H @<(printf 'SigNoz-Api-Key: %s\n' "$(get SIGNOZ_API_KEY)") | jq -r .data.name) || true
if [[ "$who" != wasitus-reader ]]; then
  echo "SIGNOZ_API_KEY in demo/.env does not work (expired after 90 days, or from an" >&2
  echo "earlier lab). Delete that line from demo/.env and run make lab-up again." >&2
  exit 1
fi

echo "SigNoz is ready. Log in at $signoz with the LAB_SIGNOZ_* values in demo/.env"
