#!/usr/bin/env bash
# Prepares the sources the zeppos skill reads, in a cache directory outside any project:
#   - the official documentation (zepp-health/zeppos-docs, only its docs/ folder),
#   - the official sample apps (zepp-health/zeppos-samples),
#   - the API declarations (@zeppos/device-types) and the index built from them.
#
#   setup.sh             prepare what is missing (quick when everything is already there)
#   setup.sh --update    also pull the newest documentation and samples, and rebuild the index
#
# Prints KEY=value lines (DOCS, SAMPLES, DTS, API_INDEX) at the end. Where things go, and what to point at
# instead of downloading:
#   ZEPPOS_SKILL_CACHE   cache directory (default ~/.cache/zeppos-skill)
#   ZEPPOS_DOCS_DIR      an existing checkout of zeppos-docs to use instead of cloning one
#   ZEPPOS_SAMPLES_DIR   an existing checkout of zeppos-samples
#   ZEPPOS_DEVICE_TYPES  an existing index.d.ts of @zeppos/device-types
# Needs git; npm only when the declarations are not found in a node_modules above the current directory.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cache="${ZEPPOS_SKILL_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/zeppos-skill}"
update=0
[ "${1:-}" = --update ] && update=1
mkdir -p "$cache"

log() { echo "setup.sh: $*" >&2; }

# ---- documentation: only docs/ (the repository is 400+ MB, mostly images and old versions) ---------------

docs="${ZEPPOS_DOCS_DIR:-$cache/zeppos-docs}"
if [ ! -d "$docs/docs" ]; then
  log "cloning the documentation (docs/ only) into $docs"
  rm -rf "$docs"
  if git clone --quiet --depth 1 --filter=blob:none --sparse https://github.com/zepp-health/zeppos-docs "$docs" 2> /dev/null; then
    git -C "$docs" sparse-checkout set docs # files in the root (LICENSE.txt, NOTICE.txt) come with it
  else
    # A server that does not do partial clones: a plain shallow clone works too, only bigger and slower.
    log "partial clone is not available, cloning in full (400+ MB)"
    rm -rf "$docs"
    git clone --quiet --depth 1 https://github.com/zepp-health/zeppos-docs "$docs"
  fi
elif [ "$update" = 1 ] && [ -z "${ZEPPOS_DOCS_DIR:-}" ]; then
  log "updating the documentation"
  git -C "$docs" pull --quiet --ff-only
fi

# ---- samples -----------------------------------------------------------------------------------------

samples="${ZEPPOS_SAMPLES_DIR:-$cache/zeppos-samples}"
if [ ! -d "$samples/application" ]; then
  log "cloning the samples into $samples"
  rm -rf "$samples"
  git clone --quiet --depth 1 https://github.com/zepp-health/zeppos-samples "$samples"
elif [ "$update" = 1 ] && [ -z "${ZEPPOS_SAMPLES_DIR:-}" ]; then
  log "updating the samples"
  git -C "$samples" pull --quiet --ff-only
fi

# ---- API declarations ---------------------------------------------------------------------------------

dts="${ZEPPOS_DEVICE_TYPES:-}"
if [ -z "$dts" ]; then
  # The project's own copy first (in a node_modules of this directory, of one level below it, or of a parent): it is
  # the version the project builds against.
  dir="$PWD"
  while [ -z "$dts" ] && [ "$dir" != / ]; do
    for modules in "$dir/node_modules" "$dir"/*/node_modules; do
      if [ -f "$modules/@zeppos/device-types/dist/index.d.ts" ]; then
        dts="$modules/@zeppos/device-types/dist/index.d.ts"
        break
      fi
    done
    dir="$(dirname "$dir")"
  done
fi
if [ -z "$dts" ]; then
  dts="$cache/device-types/package/dist/index.d.ts"
  if [ ! -f "$dts" ] || [ "$update" = 1 ]; then
    log "downloading @zeppos/device-types"
    rm -rf "$cache/device-types"
    mkdir -p "$cache/device-types"
    (cd "$cache/device-types" && tarball="$(npm pack --silent @zeppos/device-types)" && tar -xzf "$tarball")
  fi
fi
[ -f "$dts" ] || { log "no index.d.ts at $dts"; exit 1; }

# The index is rebuilt when it was made from another file (a project with its own copy of the declarations, after one
# without) or when that file is newer than the index.
index="$cache/api-index.md"
if [ ! -f "$index" ] || [ "$update" = 1 ] || [ "$dts" -nt "$index" ] || [ "$(cat "$index.src" 2> /dev/null)" != "$dts" ]; then
  log "building the API index"
  node "$here/build-api-index.mjs" "$dts" "$index"
  echo "$dts" > "$index.src"
fi

echo "DOCS=$docs/docs"
echo "SAMPLES=$samples"
echo "DTS=$dts"
echo "API_INDEX=$index"
if [ -d "$docs/.git" ]; then echo "DOCS_COMMIT=$(git -C "$docs" log -1 --format='%h %cs')"; fi
