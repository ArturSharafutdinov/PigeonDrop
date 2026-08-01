#!/bin/bash
# protect-files.sh
# Blocks edits to secrets, lockfiles, and generated/vendor dirs.
# Relevant for PigeonDrop: the Node/TS server holds keys/credentials
# for the private endpoint that receives voice recordings.

INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty')

PROTECTED_PATTERNS=(
  ".env"
  ".env."
  "package-lock.json"
  "yarn.lock"
  "pnpm-lock.yaml"
  ".git/"
  "node_modules/"
  ".pem"
  ".key"
  "credentials"
  "secrets"
)

for pattern in "${PROTECTED_PATTERNS[@]}"; do
  if [[ "$FILE_PATH" == *"$pattern"* ]]; then
    echo "Blocked: $FILE_PATH matches protected pattern '$pattern'. Edit it manually if this is intentional." >&2
    exit 2
  fi
done

exit 0
