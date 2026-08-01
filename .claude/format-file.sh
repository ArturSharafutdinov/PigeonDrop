#!/bin/bash
# format-file.sh
# Runs eslint --fix + prettier on the file Claude just edited.
# Works for both the Zepp OS mini-program client (.js) and the
# Node/TS server (.ts) as long as each has its own eslint/prettier
# config in the respective package.json.

INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty')

# Only act on JS/TS/JSON files
case "$FILE_PATH" in
  *.js|*.ts|*.json) ;;
  *) exit 0 ;;
esac

[ -f "$FILE_PATH" ] || exit 0

# Run from whichever package the file belongs to, so it picks up
# that package's local eslint/prettier config and binaries.
DIR=$(dirname "$FILE_PATH")
PKG_DIR=$(cd "$DIR" && npm root 2>/dev/null | xargs dirname 2>/dev/null)
cd "${PKG_DIR:-$DIR}" 2>/dev/null || cd "$DIR"

npx --no-install eslint --fix "$FILE_PATH" >/dev/null 2>&1
npx --no-install prettier --write "$FILE_PATH" >/dev/null 2>&1

exit 0
