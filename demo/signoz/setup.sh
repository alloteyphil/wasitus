#!/usr/bin/env bash
# Finishes SigNoz's first-run setup, so the lab works with one command.
#
# A fresh SigNoz accepts no telemetry until its first admin account exists:
# until then the SigNoz server gives its ingester a do-nothing ("nop")
# pipeline, and the OTLP ports stay closed. This script creates a lab-only
# admin (credentials in demo/.env, which git ignores) and waits until the
# ingester accepts OTLP. Safe to run again: it skips signup once SigNoz is set up.
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
elif [[ "$just_generated" == true ]]; then
  echo "Warning: SigNoz was already set up, but demo/.env had no login, so the one just" >&2
  echo "written there does not work. Run 'make lab-reset && make lab-up' for a fresh lab." >&2
fi

# After signup the server sends the ingester its real pipeline config and the
# ingester restarts, which can take a little while.
echo "Waiting for SigNoz's ingester to accept OTLP on $otlp ..."
for _ in $(seq 1 60); do
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$otlp/v1/traces" \
    -H 'Content-Type: application/json' -d '{}' || true)
  if [[ "$code" == 2* ]]; then
    echo "SigNoz is ready. Log in at $signoz with the LAB_SIGNOZ_* values in demo/.env"
    exit 0
  fi
  sleep 3
done
echo "The ingester did not open OTLP within 3 minutes. Check: make lab-logs" >&2
exit 1
