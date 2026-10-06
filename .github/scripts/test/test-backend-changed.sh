#!/usr/bin/env bash
# Runs .github/scripts/backend-changed.sh in a throwaway git repository with a fake `docker` (the image that is
# "deployed" is whatever the test says) and checks the decision for every situation that matters, above all the one
# that went wrong in production: a backend change whose run was cancelled, followed by a push that only touched docs.
# Needs git.
here="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$here/../backend-changed.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fails=0
check() { if [ "$2" = "$3" ]; then echo "  ok   $1"; else echo "  FAIL $1 (wanted $3, got $2)"; fails=$((fails+1)); fi; }

# The fake docker: FAKE_PULL=ok|missing decides whether `docker pull` works, FAKE_LABEL is what the label holds.
mkdir "$work/bin"
cat > "$work/bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "$*" >> "$FAKE_DOCKER_LOG"
case "$1" in
  pull) [ "$FAKE_PULL" = ok ] || { echo "manifest unknown" >&2; exit 1; } ;;
  image) printf '%s\n' "$FAKE_LABEL" ;;
esac
EOF
chmod +x "$work/bin/docker"

# A history shaped like the real one: A (start), B (backend change), C (docs), D (deploy script change), E (docs).
repo="$work/repo"
mkdir "$repo" && cd "$repo" || exit 1
git init -q && git config user.name test && git config user.email test@example.com
mkdir backend docs .github .github/scripts
commit() { echo "$2" >> "$1"; git add -A; git commit -q -m "$2"; git rev-parse HEAD; }
A=$(commit backend/main.go a)
B=$(commit backend/main.go b)
C=$(commit docs/readme.md c)
D=$(commit .github/scripts/deploy.sh d)
E=$(commit docs/readme.md e)
CI_ONLY=$(commit .github/workflows-note.md f) # touches .github, but not .github/scripts

decide() { # name, expected deploy value, expected fragment of the message, then env assignments
  local name="$1" want="$2" fragment="$3"; shift 3
  : > "$work/output"; : > "$work/log"
  local out; out=$(env PATH="$work/bin:$PATH" FAKE_DOCKER_LOG="$work/log" GITHUB_OUTPUT="$work/output" \
    DEPLOYED_IMAGE=ghcr.io/owner/app-backend:deployed "$@" "$SCRIPT" 2>&1)
  local code=$?
  check "$name: exit code" "$code" 0
  check "$name: deploy" "$(sed -n 's/^deploy=//p' "$work/output")" "$want"
  case "$out" in *"$fragment"*) check "$name: says '$fragment'" ok ok ;; *) check "$name: says '$fragment'" "no: $(echo "$out" | tr '\n' '|' | cut -c1-200)" ok ;; esac
}

echo "the real failure: the deployed commit is before a backend change whose run was cancelled"
decide "B was never deployed, HEAD is docs only (C)" true "backend changed" EVENT_NAME=push GITHUB_SHA="$C" FAKE_PULL=ok FAKE_LABEL="$A"
decide "same, several docs commits later (E)" true "backend changed" EVENT_NAME=push GITHUB_SHA="$E" FAKE_PULL=ok FAKE_LABEL="$A"
echo "nothing to deploy"
decide "backend is deployed, HEAD only adds docs" false "backend unchanged" EVENT_NAME=push GITHUB_SHA="$C" FAKE_PULL=ok FAKE_LABEL="$B"
decide "HEAD is the deployed commit" false "backend unchanged" EVENT_NAME=push GITHUB_SHA="$C" FAKE_PULL=ok FAKE_LABEL="$C"
decide "only .github outside scripts changed" false "backend unchanged" EVENT_NAME=push GITHUB_SHA="$CI_ONLY" FAKE_PULL=ok FAKE_LABEL="$E"
echo "the deploy script counts as backend"
decide "deploy script changed" true "backend changed" EVENT_NAME=push GITHUB_SHA="$D" FAKE_PULL=ok FAKE_LABEL="$C"
echo "a manual run always deploys, and does not even look at the image"
decide "workflow_dispatch" true "not a push" EVENT_NAME=workflow_dispatch GITHUB_SHA="$C" FAKE_PULL=missing FAKE_LABEL=
check "workflow_dispatch: docker was not called" "$(wc -c < "$work/log" | tr -d ' ')" 0
echo "what cannot be established means deploy"
decide "no image is tagged deployed" true "nothing is tagged as deployed" EVENT_NAME=push GITHUB_SHA="$C" FAKE_PULL=missing FAKE_LABEL=
decide "image without the revision label" true "no revision label" EVENT_NAME=push GITHUB_SHA="$C" FAKE_PULL=ok FAKE_LABEL=
decide "deployed commit unknown to this checkout" true "is not in this checkout" EVENT_NAME=push GITHUB_SHA="$C" FAKE_PULL=ok FAKE_LABEL=0000000000000000000000000000000000000001
echo "it asks for the right image and the right label"
decide "pull and inspect" false "backend unchanged" EVENT_NAME=push GITHUB_SHA="$C" FAKE_PULL=ok FAKE_LABEL="$B"
case "$(cat "$work/log")" in *"pull -q ghcr.io/owner/app-backend:deployed"*) check "pulls the deployed tag" ok ok ;; *) check "pulls the deployed tag" "$(cat "$work/log")" ok ;; esac
case "$(cat "$work/log")" in *'org.opencontainers.image.revision'*'ghcr.io/owner/app-backend:deployed'*) check "reads the revision label of that image" ok ok ;; *) check "reads the revision label of that image" "$(cat "$work/log")" ok ;; esac
echo "missing settings are an error, not a silent deploy"
if env -u DEPLOYED_IMAGE EVENT_NAME=push GITHUB_SHA="$C" PATH="$work/bin:$PATH" "$SCRIPT" > /dev/null 2>&1; then got=zero; else got=nonzero; fi
check "no DEPLOYED_IMAGE: exit code is not 0" "$got" nonzero

echo; [ "$fails" = 0 ] && echo "ALL CHECKS PASS" || echo "$fails FAILURE(S)"
exit "$fails"
