#!/usr/bin/env bash
# Decides whether this commit has to be deployed: writes "deploy=true" or "deploy=false" to $GITHUB_OUTPUT.
# One script for every image the pipeline deploys (the Go server, the speech recognition service): what differs
# is the image and the paths whose change makes a new image necessary.
#
# It compares the commit with the one that is actually deployed, not with the previous push. The previous push is
# a bad base: a run that was cancelled (GitHub keeps only the newest run of those waiting, so two merges a few
# seconds apart cancel the first) or whose deployment failed takes its backend change with it, and the next
# docs-only push then sees "nothing changed" and skips the deploy for good. The deployed commit is recorded in the
# image itself: every image carries its commit in the label org.opencontainers.image.revision, and once Coolify
# has confirmed a deployment the workflow tags that image `deployed`.
#
# Env: DEPLOYED_IMAGE (the image with the `deployed` tag; the caller has logged in to its registry), CHANGE_PATHS
# (the paths, separated by spaces, whose change needs a new image, for example "backend .github/scripts"), EVENT_NAME
# and GITHUB_SHA; WHAT names the image in the messages (default: image). Run it in a checkout with the full history.
# Whatever it cannot establish means "deploy": a deploy that was not needed costs a few minutes, one that was skipped
# leaves the server stale without anyone noticing.

set -uo pipefail

# An empty path list would compare nothing and never deploy: that is a mistake of the caller, not a decision.
: "${DEPLOYED_IMAGE:?DEPLOYED_IMAGE is not set}" "${CHANGE_PATHS:?CHANGE_PATHS is not set}"
: "${EVENT_NAME:?EVENT_NAME is not set}" "${GITHUB_SHA:?GITHUB_SHA is not set}"
output="${GITHUB_OUTPUT:-/dev/stdout}"
what="${WHAT:-image}"
read -r -a paths <<< "$CHANGE_PATHS"

decide() { # true|false, reason
  echo "deploy=$1" >> "$output"
  echo "Deploy the $what: $1 ($2)"
  exit 0
}

# A manual run ("Run workflow") always deploys.
[ "$EVENT_NAME" = push ] || decide true "not a push: $EVENT_NAME"

docker pull -q "$DEPLOYED_IMAGE" > /dev/null 2>&1 || decide true "nothing is tagged as deployed yet, or it could not be pulled"

deployed=$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$DEPLOYED_IMAGE" 2> /dev/null) || deployed=""
[ -n "$deployed" ] || decide true "the deployed image has no revision label"
git cat-file -e "$deployed^{commit}" 2> /dev/null || decide true "the deployed commit $deployed is not in this checkout"

git diff --quiet "$deployed" "$GITHUB_SHA" -- "${paths[@]}"
case $? in
  0) decide false "$what unchanged ($CHANGE_PATHS) since the deployed commit ${deployed:0:7}" ;;
  1) decide true "$what changed ($CHANGE_PATHS) since the deployed commit ${deployed:0:7}" ;;
  *) decide true "could not compare with the deployed commit ${deployed:0:7}" ;;
esac
