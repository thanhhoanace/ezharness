#!/usr/bin/env bash
set -eu

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_lib.sh
. "$SCRIPT_DIR/_lib.sh"

if root="$(find_workspace_root "${1:-$PWD}")"; then
  printf '%s\n' "$root"
else
  emit_sensor_signal \
    "ezh-workspace-locator" \
    "error" \
    "block" \
    "Could not locate ezHarness workspace" \
    "Run install.sh from the project root or move into a project containing .harness/config.json." \
    "1" \
    "{\"cwd\":\"$PWD\"}" >&2
  exit 1
fi
