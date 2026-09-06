#!/usr/bin/env bash
# check-i18n — verifies that the en/es/ca message dictionaries in
# src/lib/i18n.svelte.js are key-symmetric (V12-FE-03).
#
# Fails (exit 1) when any key is missing from one of the three
# languages. Runs the pure comparison helper; CI calls this script.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if ! command -v node >/dev/null 2>&1; then
  echo "check-i18n: node is required" >&2
  exit 1
fi

node "$ROOT/scripts/check-i18n.mjs" "$@"