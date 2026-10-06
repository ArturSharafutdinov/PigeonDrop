#!/usr/bin/env bash
# Runs .github/scripts/coolify-deploy.sh against fake-coolify.mjs (a stand-in for the Coolify API) in every mode
# and checks the outcome. Needs node, curl and jq.
here="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$here/../coolify-deploy.sh"
cd "$here" || exit 1
fails=0
check() { if [ "$2" = "$3" ]; then echo "  ok   $1"; else echo "  FAIL $1 (wanted $3, got $2)"; fails=$((fails+1)); fi; }

run() { # mode, expected exit, expected output fragment, [extra env...]
  local mode="$1" want="$2" fragment="$3"; shift 3
  local port=$((39000 + RANDOM % 900))
  node fake-coolify.mjs "$port" "$mode" & local pid=$!
  sleep 0.6
  local out; out=$(env COOLIFY_URL="http://127.0.0.1:$port/" COOLIFY_TOKEN=secret-token COOLIFY_APP_UUID=app-uuid POLL_SECONDS=0.2 POLL_ATTEMPTS=12 "$@" "$SCRIPT" 2>&1); local code=$?
  local seen; seen=$(curl -s "http://127.0.0.1:$port/__requests" | jq -r '.[]' | tr '\n' ';')
  kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
  check "$mode: exit code" "$code" "$want"
  case "$out" in *"$fragment"*) check "$mode: says '$fragment'" ok ok ;; *) check "$mode: says '$fragment'" "no: $(echo "$out" | tr '\n' '|' | cut -c1-200)" ok ;; esac
  LAST_REQUESTS="$seen"
}

outfile="$(mktemp)"; trap 'rm -f "$outfile"' EXIT
marked() { if grep -qx 'deployed=true' "$outfile"; then echo yes; else echo no; fi; }

echo "success (queued, in progress, finished)"
: > "$outfile"; run ok 0 "Deployed." GITHUB_OUTPUT="$outfile"
check "ok: marks the deployment as deployed (for the image tag)" "$(marked)" yes
case "$LAST_REQUESTS" in *"POST /api/v1/deploy?uuid=app-uuid&force=false auth=Bearer secret-token"*) check "ok: POST with the uuid, force=false and the bearer token" ok ok ;; *) check "ok: POST with the uuid, force=false and the bearer token" "$LAST_REQUESTS" ok ;; esac
echo "older Coolify: POST refused with 405, GET works"
: > "$outfile"; run get-only 0 "Deployed." GITHUB_OUTPUT="$outfile"
check "get-only: marks the deployment as deployed" "$(marked)" yes
case "$LAST_REQUESTS" in *"GET /api/v1/deploy?uuid=app-uuid"*) check "get-only: falls back to GET" ok ok ;; *) check "get-only: falls back to GET" "$LAST_REQUESTS" ok ;; esac
echo "failures are failures"
: > "$outfile"; run fail 1 "ended as 'failed'" GITHUB_OUTPUT="$outfile"
check "fail: not marked as deployed" "$(marked)" no
run cancelled 1 "ended as 'cancelled-by-user'" GITHUB_OUTPUT="$outfile"
run http500 1 "Coolify answered HTTP 500" GITHUB_OUTPUT="$outfile"
run no-id 1 "did not return a deployment id" GITHUB_OUTPUT="$outfile"
echo "wrong token"
run ok 1 "Coolify answered HTTP 401" COOLIFY_TOKEN=wrong GITHUB_OUTPUT="$outfile"
check "wrong token: not marked as deployed" "$(marked)" no
echo "a deployment that never finishes is a warning, not a failure, and does not count as deployed"
run slow 0 "had not finished" GITHUB_OUTPUT="$outfile"
check "slow: not marked as deployed" "$(marked)" no
echo "no secrets: says so and exits 0"
: > "$outfile"; out=$(env -u COOLIFY_URL -u COOLIFY_TOKEN -u COOLIFY_APP_UUID GITHUB_OUTPUT="$outfile" "$SCRIPT" 2>&1); check "no secrets: exit code" "$?" 0
case "$out" in *"secrets are not set"*) check "no secrets: warns" ok ok ;; *) check "no secrets: warns" "$out" ok ;; esac
check "no secrets: not marked as deployed" "$(marked)" no
out=$(env COOLIFY_URL=http://127.0.0.1:1 COOLIFY_TOKEN=x COOLIFY_APP_UUID= "$SCRIPT" 2>&1); check "one secret empty: exit code" "$?" 0
echo "unreachable Coolify fails the job"
if env COOLIFY_URL=http://127.0.0.1:1 COOLIFY_TOKEN=x COOLIFY_APP_UUID=u "$SCRIPT" >/dev/null 2>&1; then got=zero; else got=nonzero; fi
check "unreachable: exit code is not 0" "$got" nonzero

echo; [ "$fails" = 0 ] && echo "ALL CHECKS PASS" || echo "$fails FAILURE(S)"
exit "$fails"
