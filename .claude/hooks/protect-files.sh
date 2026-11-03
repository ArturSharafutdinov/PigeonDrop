#!/bin/bash
# PreToolUse hook for Edit and Write: refuses to touch secrets, lock files and git internals.
# The agent can still be told to change one of these by hand if that is really intended.
# Needs jq; without it the hook allows everything.

INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty')
[ -n "$FILE_PATH" ] || exit 0

block() {
  echo "Blocked: $FILE_PATH is protected ($1). Edit it manually if this is intentional." >&2
  exit 2
}

case "$(basename "$FILE_PATH")" in
  .env.example | .env.sample) ;; # templates are meant to be edited
  .env | .env.*) block "environment file" ;;
  *.pem | *.key) block "key material" ;;
  credentials* | secrets*) block "credentials" ;;
  package-lock.json | yarn.lock | pnpm-lock.yaml) block "lock file; regenerate it with the package manager" ;;
esac

case "$FILE_PATH" in
  */.git/* | */node_modules/*) block "git internals or installed dependencies" ;;
esac

exit 0
