#!/usr/bin/env bash
# Usage: scripts/coverage-floor.sh COVERAGE_FILE MIN_PERCENT
#
# Fails when the total statement coverage in COVERAGE_FILE (from
# `go test -coverprofile`) is below MIN_PERCENT. Used by CI and `make check`,
# so the floor is defined in one place per caller and checked the same way.
set -euo pipefail

file="${1:?usage: scripts/coverage-floor.sh COVERAGE_FILE MIN_PERCENT}"
min="${2:?usage: scripts/coverage-floor.sh COVERAGE_FILE MIN_PERCENT}"

total=$(go tool cover -func="$file" | awk '/^total:/ { sub("%", "", $NF); print $NF }')
if [[ -z "$total" ]]; then
  echo "::error::no total coverage found in $file"
  exit 1
fi

if awk -v t="$total" -v m="$min" 'BEGIN { exit !(t + 0 >= m + 0) }'; then
  echo "Coverage ${total}% (floor ${min}%)"
else
  echo "::error::Coverage ${total}% is below the ${min}% floor"
  exit 1
fi
