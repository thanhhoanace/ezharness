#!/usr/bin/env bash
# Shared helpers for ezHarness hooks/scripts.

set -u

emit_sensor_signal() {
  # Args:
  #   sensor_id severity verdict summary suggested_action exit_code evidence_json
  sensor_id="$1"
  severity="$2"
  verdict="$3"
  summary="$4"
  suggested_action="$5"
  exit_code="$6"
  if [ "$#" -ge 7 ]; then
    evidence_json="$7"
  else
    evidence_json="{}"
  fi

  python3 - "$sensor_id" "$severity" "$verdict" "$summary" "$suggested_action" "$exit_code" "$evidence_json" <<'PY'
import json
import sys
sensor_id, severity, verdict, summary, suggested_action, exit_code, evidence_raw = sys.argv[1:]
try:
    evidence = json.loads(evidence_raw)
except Exception:
    evidence = {"raw": evidence_raw}
print(json.dumps({
    "schema_version": 1,
    "sensor_id": sensor_id,
    "severity": severity,
    "verdict": verdict,
    "summary": summary,
    "evidence": evidence,
    "suggested_action": suggested_action,
    "exit_code": int(exit_code),
}, ensure_ascii=False, separators=(",", ":")))
PY
}

find_workspace_root() {
  dir="${1:-$PWD}"
  dir="$(cd "$dir" 2>/dev/null && pwd)" || return 1
  while [ "$dir" != "/" ]; do
    if [ -f "$dir/.harness/config.json" ]; then
      printf '%s\n' "$dir"
      return 0
    fi
    dir="$(dirname "$dir")"
  done
  return 1
}

current_git_root() {
  git rev-parse --show-toplevel 2>/dev/null || return 1
}

current_branch() {
  branch="$(git symbolic-ref --quiet --short HEAD 2>/dev/null || true)"
  if [ -z "$branch" ]; then
    branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
  fi
  if [ -z "$branch" ] || [ "$branch" = "HEAD" ]; then
    printf 'unknown\n'
    return 0
  fi
  printf '%s\n' "$branch"
}

is_protected_branch() {
  branch="$1"
  [ "$branch" = "main" ] || [ "$branch" = "master" ]
}

repo_relpath() {
  workspace_root="$1"
  git_root="$2"
  python3 - "$workspace_root" "$git_root" <<'PY'
import os, sys
workspace, repo = sys.argv[1:]
workspace = os.path.realpath(workspace)
repo = os.path.realpath(repo)
print(os.path.relpath(repo, workspace))
PY
}

repo_type_from_config() {
  workspace_root="$1"
  repo_rel="$2"
  config="$workspace_root/.harness/config.json"
  if [ "$repo_rel" = "." ]; then
    printf 'context\n'
    return 0
  fi
  python3 - "$config" "$repo_rel" <<'PY'
import json, sys
config_path, repo_rel = sys.argv[1:]
try:
    with open(config_path) as f:
        cfg = json.load(f)
except Exception:
    print("code")
    raise SystemExit(0)
for repo in cfg.get("repos", []):
    if repo.get("path") == repo_rel:
        print(repo.get("repo_type", "code"))
        break
else:
    # Fail-safe: unlisted nested repos are treated as code repos.
    print("code")
PY
}

staged_files_json() {
  python3 - <<'PY'
import json, subprocess
try:
    out = subprocess.check_output(["git", "diff", "--cached", "--name-only"], text=True)
    files = [line for line in out.splitlines() if line]
except Exception:
    files = []
print(json.dumps(files))
PY
}

changed_files_for_hook() {
  # Args: hook_name
  # Emits a JSON array of repo-relative changed file paths for the hook.
  # pre-commit: staged files. pre-push: union of files changed across the pushed
  # ref ranges parsed from EZH_HOOK_STDIN (set by the pre-push wrapper).
  hook="$1"
  if [ "$hook" = "pre-commit" ]; then
    staged_files_json
    return 0
  fi
  printf '%s' "${EZH_HOOK_STDIN:-}" | python3 - <<'PY'
import sys, json, subprocess
files = set()
for line in sys.stdin.read().splitlines():
    parts = line.split()
    if len(parts) < 4:
        continue
    local_sha, remote_sha = parts[1], parts[3]
    if set(local_sha) == {"0"}:           # branch deletion
        continue
    if set(remote_sha) == {"0"}:          # new branch: commits not on any remote
        cmd = ["git", "log", "--name-only", "--pretty=format:", local_sha, "--not", "--remotes"]
    else:
        cmd = ["git", "diff", "--name-only", f"{remote_sha}..{local_sha}"]
    try:
        out = subprocess.check_output(cmd, text=True)
    except Exception:
        out = ""
    for f in out.splitlines():
        f = f.strip()
        if f:
            files.add(f)
print(json.dumps(sorted(files)))
PY
}
