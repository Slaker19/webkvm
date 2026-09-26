#!/usr/bin/env bash
# check-undefined-components — verifies that every component referenced
# in a Svelte template is actually imported or declared in that file.
#
# Svelte 5 compiles an unresolved capitalised tag into a direct call to
# a bare identifier. Nothing warns about it: the build succeeds,
# svelte-check stays quiet, and the page ships. The ReferenceError only
# fires the first time that branch renders, so a control can sit in the
# UI looking perfectly normal and do nothing at all when clicked.
#
# Fails (exit 1) when a tag names a component that exists in the project
# but is not in scope in the file using it. CI calls this script.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if ! command -v node >/dev/null 2>&1; then
  echo "check-undefined-components: node is required" >&2
  exit 1
fi

node "$ROOT/scripts/check-undefined-components.mjs" "$@"
