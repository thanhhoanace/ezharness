#!/usr/bin/env bash
set -eu

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_lib.sh
. "$SCRIPT_DIR/_lib.sh"

hook_name="${1:-}"
if [ "$hook_name" != "pre-commit" ] && [ "$hook_name" != "pre-push" ]; then
  emit_sensor_signal \
    "ezh-evidence-gate" \
    "error" \
    "block" \
    "Invalid evidence gate invocation" \
    "Invoke ezh-evidence-gate.sh from pre-commit or pre-push." \
    "2" \
    "{\"hook\":\"$hook_name\"}" >&2
  exit 2
fi

contract_risk() {
  contract_path="$1"
  python3 - "$contract_path" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
if not path.exists():
    print("high")
    raise SystemExit(0)

text = path.read_text(errors="replace").strip()
if text.startswith("{"):
    try:
        risk = json.loads(text).get("risk_level", "")
        print(risk if risk in {"low", "med", "high"} else "high")
    except Exception:
        print("high")
    raise SystemExit(0)

for raw in text.splitlines():
    if raw.startswith((" ", "\t")):
        continue
    stripped = raw.strip()
    if stripped.startswith("#") or ":" not in stripped:
        continue
    key, value = stripped.split(":", 1)
    if key.strip() == "risk_level":
        risk = value.strip().strip("\"'")
        print(risk if risk in {"low", "med", "high"} else "high")
        raise SystemExit(0)

print("high")
PY
}

resolve_ezh_evidence() {
  workspace_root="$1"
  if command -v ezh-evidence >/dev/null 2>&1; then
    command -v ezh-evidence
    return 0
  fi
  if [ -x "$workspace_root/.harness/bin/ezh-evidence" ]; then
    printf '%s\n' "$workspace_root/.harness/bin/ezh-evidence"
    return 0
  fi
  return 1
}

# Fail-closed: the gate hook only runs because core.hooksPath points at this
# repo's .harness/hooks, so a gate IS expected. If we cannot locate the
# workspace (e.g. an out-of-tree worktree whose PWD never walks up to a
# .harness/config.json), we must BLOCK rather than silently allow an ungated
# commit/push. First try walking up from PWD; then fall back to the hook's own
# location ($workspace/.harness/hooks/<this>); only if both fail do we block.
workspace_root="$(find_workspace_root "$PWD" 2>/dev/null || true)"
if [ -z "$workspace_root" ]; then
  candidate="$(cd "$SCRIPT_DIR/../.." 2>/dev/null && pwd || true)"
  if [ -n "$candidate" ] && [ -f "$candidate/.harness/config.json" ]; then
    workspace_root="$candidate"
  fi
fi
if [ -z "$workspace_root" ]; then
  emit_sensor_signal \
    "ezh-evidence-gate" \
    "error" \
    "block" \
    "Evidence gate blocked: workspace root not found (fail-closed)" \
    "The gate hook fired but no .harness/config.json was found from PWD or the hook location. Run install.sh for this repo/worktree, or set core.hooksPath correctly. Refusing to allow an ungated operation." \
    "2" \
    "{\"hook\":\"$hook_name\",\"pwd\":\"$PWD\",\"script_dir\":\"$SCRIPT_DIR\"}" >&2
  exit 2
fi

git_root="$(current_git_root)"
if [ -z "$git_root" ]; then
  emit_sensor_signal \
    "ezh-evidence-gate" \
    "error" \
    "block" \
    "Evidence gate blocked: not inside a git work tree (fail-closed)" \
    "The gate hook fired outside a resolvable git repository. Investigate the git environment before retrying." \
    "2" \
    "{\"hook\":\"$hook_name\",\"pwd\":\"$PWD\"}" >&2
  exit 2
fi
branch="$(current_branch)"
repo_rel="$(repo_relpath "$workspace_root" "$git_root")"
contract_path="$workspace_root/.harness/project-contract.yaml"
ledger_path="$workspace_root/.harness/evidence/ledger.jsonl"
risk="$(contract_risk "$contract_path")"

if is_protected_branch "$branch"; then
  if [ "$hook_name" = "pre-commit" ]; then
    files="$(staged_files_json)"
    evidence="$(python3 - "$repo_rel" "$branch" "$files" <<'PY'
import json
import sys

repo, branch, files_raw = sys.argv[1:]
try:
    files = json.loads(files_raw)
except Exception:
    files = []
print(json.dumps({"repo": repo, "branch": branch, "hook": "pre-commit", "files": files}, separators=(",", ":")))
PY
)"
    emit_sensor_signal \
      "ezh-main-branch-write-guard" \
      "error" \
      "block" \
      "Attempted commit on protected branch" \
      "Create or switch to the plan worktree/branch before committing code." \
      "1" \
      "$evidence" >&2
  else
    evidence="$(python3 - "$repo_rel" "$branch" "${EZH_HOOK_REMOTE_NAME:-origin}" "${EZH_HOOK_REMOTE_URL:-unknown}" "${EZH_HOOK_STDIN:-}" <<'PY'
import json
import sys

repo, branch, remote_name, remote_url, payload = sys.argv[1:]
print(json.dumps({
    "repo": repo,
    "branch": branch,
    "hook": "pre-push",
    "remote": remote_name,
    "remote_url": remote_url,
    "refs": payload.splitlines(),
}, separators=(",", ":")))
PY
)"
    emit_sensor_signal \
      "ezh-main-branch-write-guard" \
      "error" \
      "block" \
      "Attempted push from protected branch" \
      "Push from a plan branch/worktree, not from main/master." \
      "1" \
      "$evidence" >&2
  fi
  exit 1
fi

if ! evidence_cli="$(resolve_ezh_evidence "$workspace_root")"; then
  evidence="$(python3 - "$hook_name" "$repo_rel" "$branch" "$contract_path" <<'PY'
import json
import sys

hook, repo, branch, contract = sys.argv[1:]
print(json.dumps({
    "hook": hook,
    "repo": repo,
    "branch": branch,
    "contract": contract,
    "reason": "ezh-evidence binary not found in PATH or .harness/bin/ezh-evidence",
}, separators=(",", ":")))
PY
)"
  emit_sensor_signal \
    "ezh-evidence-gate" \
    "error" \
    "block" \
    "Evidence gate blocked: ezh-evidence binary not found" \
    "Run install.sh from ezHarness source with Go available, or place ezh-evidence at .harness/bin/ezh-evidence." \
    "2" \
    "$evidence" >&2
  exit 2
fi

if [ ! -f "$contract_path" ]; then
  evidence="$(python3 - "$hook_name" "$repo_rel" "$branch" "$contract_path" "$ledger_path" <<'PY'
import json
import sys

hook, repo, branch, contract, ledger = sys.argv[1:]
print(json.dumps({
    "hook": hook,
    "repo": repo,
    "branch": branch,
    "contract": contract,
    "ledger": ledger,
    "reason": "project contract not found",
}, separators=(",", ":")))
PY
)"
  emit_sensor_signal \
    "ezh-evidence-gate" \
    "error" \
    "block" \
    "Evidence gate blocked: project contract not found" \
    "Create .harness/project-contract.yaml, run worker evidence check, then run the independent verifier." \
    "2" \
    "$evidence" >&2
  exit 2
fi

# --- Path-based risk gating ---------------------------------------------------
# Evidence is only required when the change touches a declared risk path. The
# contract's `risk_paths:` (list of globs, repo-relative) marks load-bearing
# paths. If risk_paths is set and the change touches none of them, the operation
# is allowed with a warning (no evidence dance for trivial/docs changes). If
# risk_paths is empty/unset, the gate stays strict (evidence required for all).
changed_json="$(changed_files_for_hook "$hook_name")"
gate_decision="$(python3 - "$contract_path" "$changed_json" <<'PY'
import json, sys, fnmatch
from pathlib import Path

contract_path, changed_raw = sys.argv[1:]
try:
    changed = json.loads(changed_raw)
except Exception:
    changed = []

risk_paths = []
p = Path(contract_path)
text = p.read_text(errors="replace") if p.exists() else ""
if text.strip().startswith("{"):
    try:
        risk_paths = json.loads(text).get("risk_paths", []) or []
    except Exception:
        risk_paths = []
else:
    in_block = False
    for raw in text.splitlines():
        if not raw.startswith((" ", "\t")):
            in_block = raw.strip() == "risk_paths:"
            continue
        s = raw.strip()
        if in_block and s.startswith("- "):
            risk_paths.append(s[2:].strip().strip("\"'"))

def is_risky(f):
    for pat in risk_paths:
        base = pat.rstrip("/*")
        if fnmatch.fnmatch(f, pat) or f == pat or (base and (f == base or f.startswith(base + "/"))):
            return True
    return False

if not risk_paths:
    print(json.dumps({"decision": "require_evidence", "reason": "no risk_paths declared; strict gate", "changed": changed}, separators=(",", ":")))
else:
    risky = [f for f in changed if is_risky(f)]
    if risky:
        print(json.dumps({"decision": "require_evidence", "reason": "change touches risk paths", "risk_paths": risk_paths, "risky": risky}, separators=(",", ":")))
    else:
        print(json.dumps({"decision": "bypass", "reason": "no risk path touched", "risk_paths": risk_paths, "changed": changed}, separators=(",", ":")))
PY
)"
decision="$(printf '%s' "$gate_decision" | python3 -c 'import sys, json
try:
    print(json.load(sys.stdin).get("decision", "require_evidence"))
except Exception:
    print("require_evidence")')"
if [ "$decision" = "bypass" ]; then
  emit_sensor_signal \
    "ezh-evidence-gate" \
    "info" \
    "warn" \
    "Evidence gate skipped: change touches no contract risk_path" \
    "Allowed without evidence. Touch a risk_path (e.g. api/auth, gitops, infra, migrations) to require worker+verifier evidence." \
    "0" \
    "$gate_decision" >&2
  exit 0
fi

stdout_file="$(mktemp)"
stderr_file="$(mktemp)"
cleanup() {
  rm -f "$stdout_file" "$stderr_file"
}
trap cleanup EXIT

if [ "$hook_name" = "pre-commit" ]; then
  expect_tree="$(git -C "$git_root" write-tree 2>/dev/null || true)"
else
  expect_tree="$(git -C "$git_root" rev-parse 'HEAD^{tree}' 2>/dev/null || true)"
fi

set +e
if [ -n "$expect_tree" ]; then
  "$evidence_cli" replay "$ledger_path" --require-verifier --expect-tree "$expect_tree" --json >"$stdout_file" 2>"$stderr_file"
else
  "$evidence_cli" replay "$ledger_path" --require-verifier --json >"$stdout_file" 2>"$stderr_file"
fi
cli_status=$?
set -e

if [ "$cli_status" -eq 0 ]; then
  exit 0
fi

hook_context="$(python3 - "$hook_name" "$repo_rel" "$branch" "${EZH_HOOK_REMOTE_NAME:-}" "${EZH_HOOK_REMOTE_URL:-}" "${EZH_HOOK_STDIN:-}" <<'PY'
import json
import sys

hook, repo, branch, remote_name, remote_url, stdin_payload = sys.argv[1:]
context = {
    "hook": hook,
    "repo": repo,
    "branch": branch,
}
if hook == "pre-commit":
    try:
        context["files"] = json.loads(sys.stdin.read() or "[]")
    except Exception:
        context["files"] = []
if hook == "pre-push":
    context["remote"] = remote_name
    context["remote_url"] = remote_url
    context["refs"] = [line for line in stdin_payload.splitlines() if line]
print(json.dumps(context, separators=(",", ":")))
PY
)"

if [ "$hook_name" = "pre-commit" ]; then
  staged_files="$(staged_files_json)"
else
  staged_files="[]"
fi

evidence="$(python3 - "$hook_context" "$staged_files" "$contract_path" "$ledger_path" "$risk" "$cli_status" "$stdout_file" "$stderr_file" <<'PY'
import json
import sys
from pathlib import Path

context_raw, staged_raw, contract, ledger, risk, status_raw, stdout_path, stderr_path = sys.argv[1:]
try:
    evidence = json.loads(context_raw)
except Exception:
    evidence = {"hook_context_raw": context_raw}
if evidence.get("hook") == "pre-commit":
    try:
        evidence["files"] = json.loads(staged_raw)
    except Exception:
        evidence["files"] = []

stdout = Path(stdout_path).read_text(errors="replace").strip()
stderr = Path(stderr_path).read_text(errors="replace").strip()
reason = stderr
cli_result = None
if stdout:
    try:
        cli_result = json.loads(stdout)
        reason = cli_result.get("reason") or reason
    except Exception:
        reason = reason or stdout
if not reason:
    reason = f"ezh-evidence exited {status_raw}"

evidence.update({
    "contract": contract,
    "ledger": ledger,
    "risk": risk,
    "cli_exit_code": int(status_raw),
    "reason": reason,
})
if cli_result is not None:
    evidence["cli_result"] = cli_result
elif stdout:
    evidence["cli_stdout"] = stdout
if stderr:
    evidence["cli_stderr"] = stderr
print(json.dumps(evidence, separators=(",", ":")))
PY
)"

reason="$(python3 - "$evidence" <<'PY'
import json
import sys
try:
    print(json.loads(sys.argv[1]).get("reason", "ezh-evidence replay failed"))
except Exception:
    print("ezh-evidence replay failed")
PY
)"

emit_sensor_signal \
  "ezh-evidence-gate" \
  "error" \
  "block" \
  "Evidence gate blocked: $reason" \
  "Run ezh-evidence check as the worker, then run ezh-evidence verify as an independent verifier before retrying the Git operation." \
  "2" \
  "$evidence" >&2

exit 2
