#!/usr/bin/env bash
# Asks Coolify to deploy one application and waits until the deployment has finished.
#
# Needs COOLIFY_URL, COOLIFY_TOKEN and COOLIFY_APP_UUID in the environment (GitHub secrets, see
# backend/DEPLOY.md). Without all three it says so and exits 0, so that the workflow is safe to push
# before Coolify is set up. A deployment that fails, or that Coolify does not acknowledge, fails the job.
# When Coolify reports the deployment as finished it also writes `deployed=true` to $GITHUB_OUTPUT; no
# other outcome does, including the exits with a warning.
#
# POLL_SECONDS and POLL_ATTEMPTS only exist so that the script can be tested quickly against a fake Coolify.

set -euo pipefail

if [ -z "${COOLIFY_URL:-}" ] || [ -z "${COOLIFY_TOKEN:-}" ] || [ -z "${COOLIFY_APP_UUID:-}" ]; then
  echo "::warning::The Coolify secrets are not set, so nothing was deployed (see backend/DEPLOY.md)"
  exit 0
fi

poll_seconds="${POLL_SECONDS:-10}"
poll_attempts="${POLL_ATTEMPTS:-90}" # 90 x 10 s = 15 minutes

base="${COOLIFY_URL%/}"
auth="Authorization: Bearer $COOLIFY_TOKEN"
response="$(mktemp)"
trap 'rm -f "$response"' EXIT

trigger() {
  curl -sS -o "$response" -w '%{http_code}' -X "$1" -H "$auth" -H 'Accept: application/json' \
    "$base/api/v1/deploy?uuid=$COOLIFY_APP_UUID&force=false"
}

code=$(trigger POST)
# Older Coolify versions only accept GET on this route.
if [ "$code" = 405 ]; then code=$(trigger GET); fi
if [ "$code" != 200 ]; then
  echo "::error::Coolify answered HTTP $code"
  cat "$response"
  exit 1
fi

deployment=$(jq -r '.deployments[0].deployment_uuid // empty' "$response")
if [ -z "$deployment" ]; then
  echo "::error::Coolify did not return a deployment id"
  cat "$response"
  exit 1
fi
echo "Deployment $deployment queued"

# A failed deployment fails this job, so a broken release is visible.
for attempt in $(seq 1 "$poll_attempts"); do
  status=$(curl -sS -H "$auth" "$base/api/v1/deployments/$deployment" | jq -r '.status // "unknown"' || echo unknown)
  echo "[$attempt] $status"
  case "$status" in
    finished)
      echo "Deployed."
      # Only this counts as deployed (not the warnings, which also exit 0): the workflow then marks the image.
      echo "deployed=true" >> "${GITHUB_OUTPUT:-/dev/null}"
      exit 0
      ;;
    failed | error | cancelled*)
      echo "::error::The deployment ended as '$status' - see its log in Coolify"
      exit 1
      ;;
  esac
  sleep "$poll_seconds"
done
minutes=$(awk -v attempts="$poll_attempts" -v seconds="$poll_seconds" 'BEGIN { printf "%.0f", attempts * seconds / 60 }')
echo "::warning::The deployment had not finished after about $minutes minutes - check Coolify"
