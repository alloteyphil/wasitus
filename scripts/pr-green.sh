#!/usr/bin/env bash
# Usage: scripts/pr-green.sh PR_NUMBER
#
# Exits 0 only when a pull request is safe to merge beyond doubt:
#   1. GitHub returned its checks (an error or an empty answer is NOT green);
#   2. the required checks ("ci ok" and "GitGuardian Security Checks") exist;
#   3. every check passed: none failing, pending, skipped or cancelled;
#   4. GitHub reports the PR as mergeable and up to date with main (CLEAN).
# No news is never good news: anything unexpected exits 1.
#
# Needs gh (logged in) and jq.
set -euo pipefail

pr="${1:?usage: scripts/pr-green.sh PR_NUMBER}"
required=("ci ok" "GitGuardian Security Checks")

# gh exits non-zero while checks fail or are pending, so judge the JSON it
# prints, never its exit code.
checks=$(gh pr checks "$pr" --json name,bucket 2>/dev/null || true)
if ! jq -e 'type == "array" and length > 0' >/dev/null 2>&1 <<<"$checks"; then
  echo "NOT GREEN: no check results for PR #$pr"
  exit 1
fi

status=0
for name in "${required[@]}"; do
  if ! jq -e --arg n "$name" 'any(.[]; .name == $n)' >/dev/null <<<"$checks"; then
    echo "NOT GREEN: required check missing: $name"
    status=1
  fi
done

bad=$(jq -r '.[] | select(.bucket != "pass") | "  \(.name): \(.bucket)"' <<<"$checks")
if [[ -n "$bad" ]]; then
  echo "NOT GREEN: checks not passing:"
  echo "$bad"
  status=1
fi

state=$(gh pr view "$pr" --json mergeStateStatus -q .mergeStateStatus 2>/dev/null || true)
if [[ "$state" != "CLEAN" ]]; then
  echo "NOT GREEN: merge state is '${state:-unknown}', want CLEAN (mergeable and up to date)"
  status=1
fi

if [[ $status -eq 0 ]]; then
  echo "GREEN: PR #$pr has $(jq length <<<"$checks") checks, all passed, and is up to date with main."
fi
exit "$status"
