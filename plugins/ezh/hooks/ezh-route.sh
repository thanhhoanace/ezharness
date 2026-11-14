#!/usr/bin/env bash
# ezh-route.sh — one entry for the Claude hook events of the `ezh` plugin (ADR-0013).
#
# WHY: Claude Code reads hook settings only from the folder a session opened in,
# so hooks installed in a repo never fire for a session opened at the workspace
# root, and the reverse. The plugin brings the hooks to every session; this
# script decides which lesson vault a session belongs to by the path it touched.
#
#   ezh-route.sh session-start   SessionStart: remember when the session began; inside a
#                                workspace print the lesson index as context (ADR-0015 #7)
#   ezh-route.sh pre-tool        PreToolUse:   record whether a memory file exists before a write
#   ezh-route.sh post-tool       PostToolUse:  own a memory file the session created; note the repo
#   ezh-route.sh stop            Stop:         move the owned memory files to the inbox
#   ezh-route.sh session-end     SessionEnd:   drop the session cache
#
# Stop moves ONLY the memory files this session itself CREATED (ADR-0015 #6): a
# Write/Edit on a file under <config>/projects/*/memory/ whose PreToolUse saw no file
# there. A memory that existed before the edit stays, and so does one written with
# no PreToolUse record (plugin enabled mid-session, old hooks.json): when in doubt a
# memory stays where it is. Sessions in one folder share one memory folder, so time
# alone cannot tell whose memory is whose; the folder comes from the files, never the cwd.
# Every moved memory goes to the machine inbox (${XDG_DATA_HOME:-~/.local/share}/ezh/inbox)
# with hints: repos the session edited, its cwd, the repos whose
# `.harness/lesson-owners.yaml` matched, and `assigned` = the one repo when exactly one
# matched (ADR-0014 rule C). The weekly lesson-inbox loop proposes, the owner approves by
# hash, then a draft MR carries it. This hook never writes into a git repo.
# Stop moves nothing unless the machine turned the route on (ezh_lesson_route_on in
# _owners.sh; default off), because only the loop brings a moved memory back.
# Never blocks: always exit 0. bash 3.2 compatible.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=_platform.sh
. "$HERE/_platform.sh"
HERE="$(cd "$HERE" && pwd -P)"   # Windows: C:/ form once _platform.sh is in
# shellcheck source=_route.sh
. "$HERE/_route.sh"
# shellcheck source=_owners.sh
. "$HERE/_owners.sh"

case "${EZH_ROUTE:-on}" in off|OFF|0|false|no) exit 0 ;; esac
event="${1:-}"
if ! ezh_has_py; then
  # Never silent (ADR-0016): say it once, at session start; other events stay quiet.
  if [ "$event" = session-start ]; then
    printf '%s\n' '{"systemMessage":"ezh: no working python 3 (tried python3, python, py -3). Lesson index and lesson route are off; the plan gate still runs. Install Python 3, then run /ez-doctor."}'
  fi
  exit 0
fi

input="$(cat 2>/dev/null || true)"

fields="$(printf '%s' "$input" | ezh_py -c '
import json, os, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
ti = d.get("tool_input") or {}
path = ti.get("file_path") or ti.get("notebook_path") or ""
tp = d.get("transcript_path") or ""
size = os.path.getsize(tp) if tp and os.path.isfile(tp) else -1
for v in (d.get("session_id") or "", d.get("cwd") or "", path, size):
    print(str(v).replace("\n", " "))
' 2>/dev/null)"
[ -n "$fields" ] || exit 0
sid="$(printf '%s\n' "$fields" | sed -n 1p)"
cwd="$(printf '%s\n' "$fields" | sed -n 2p)"
path="$(printf '%s\n' "$fields" | sed -n 3p)"
[ -n "$cwd" ] && cwd="$(ezh_path "$cwd")"
[ -n "$path" ] && path="$(ezh_path "$path")"
tsize="$(printf '%s\n' "$fields" | sed -n 4p)"
[ -n "$sid" ] || exit 0

emit() {
  ezh_py -c 'import json,sys; print(json.dumps({"systemMessage": sys.argv[1]}))' "$1" 2>/dev/null || true
}

lesson_index() {
  # One JSON SessionStart context: name + description of every lesson in the main work
  # trees under the workspaces, at most 60 lines. Prints nothing when there is none.
  # Same discovery as ezh_owner_repos; the main-work-tree test (R12) runs here in one
  # process (git only when .git is a file) to stay far under the hook timeout.
  ezh_workspace_find .harness/lessons | EZH_IDX_WS="$(ezh_owner_workspaces)" ezh_py -c '
import json, os, re, subprocess, sys

def main_worktree(top):
    """The top of a work tree whose git dir is the common dir (not a linked worktree)."""
    dotgit = os.path.join(top, ".git")
    if not os.path.lexists(dotgit):
        return False                      # not the top of any work tree
    if os.path.isdir(dotgit) and not os.path.islink(dotgit) and os.path.isfile(os.path.join(dotgit, "HEAD")):
        return True                       # a .git folder is its own common dir; no git call needed
    try:
        out = subprocess.run(["git", "-C", top, "rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir"],
                             capture_output=True, text=True, timeout=5)
    except Exception:
        return False
    lines = out.stdout.splitlines()
    if out.returncode != 0 or len(lines) != 3:
        return False
    t, gd, cd = (os.path.realpath(os.path.join(top, x)) for x in lines)
    return t == top and gd == cd

repos = set()
for d in sys.stdin.read().splitlines():
    if "/_fixtures/" in d + "/" or not os.path.isdir(d):
        continue
    top = os.path.realpath(os.path.dirname(os.path.dirname(os.path.realpath(d))))
    if top not in repos and main_worktree(top):
        repos.add(top)
wss = sorted({os.path.realpath(w) for w in os.environ["EZH_IDX_WS"].splitlines() if os.path.isdir(w)},
             key=len, reverse=True)
rows = []
for repo in sorted(repos):
    ws = next((w for w in wss if repo == w or repo.startswith(w + "/")), None)
    if not ws:
        continue
    vault = os.path.join(repo, ".harness", "lessons")
    for n in sorted(os.listdir(vault)):
        if not n.endswith(".md") or n in ("LESSONS.md", "README.md") or n[0] in "._":
            continue
        try:
            lines = open(os.path.join(vault, n), errors="replace").read(8192).splitlines()
        except OSError:
            continue
        if not lines or lines[0].strip() != "---":
            continue
        f = {}
        for raw in lines[1:]:
            if raw.strip() == "---":
                break
            m = re.match(r"^(name|description):\s*(.*)$", raw)
            if m:
                f[m.group(1)] = m.group(2).strip().strip("\"\x27")
        else:
            continue
        if f.get("name") and f.get("description"):
            rows.append(("- %s — %s" % (os.path.relpath(os.path.join(vault, n), ws), f["description"]), ws))
if not rows:
    sys.exit(0)
rows.sort()
cap = 60
text = ["Lessons in this workspace (ezHarness index; open the file when the work touches the topic):"]
text += [r for r, _ in rows[:cap]]
if len(rows) > cap:
    more = sorted({w for _, w in rows[cap:]})
    text.append("… and %d more: run rg in %s" % (len(rows) - cap, " and ".join(w + "/**/.harness/lessons/" for w in more)))
print(json.dumps({"hookSpecificOutput": {"hookEventName": "SessionStart", "additionalContext": "\n".join(text)}}))
' 2>/dev/null
}

case "$event" in
  session-start)
    ezh_session_start "$sid"
    ezh_session_cwd "$sid" "$cwd" >/dev/null
    # Read from everywhere (ADR-0015 #7): the index only, never lesson text, no file written.
    if [ -n "$cwd" ] && ezh_in_workspace "$cwd"; then lesson_index; fi
    ;;
  pre-tool)
    # Silent on purpose: PreToolUse output can be read as a permission decision.
    if [ -n "$path" ] && ezh_is_memory_file "$path"; then ezh_memory_before "$sid" "$path"; fi
    ;;
  post-tool)
    [ -n "$path" ] || exit 0
    if ezh_is_memory_file "$path"; then
      # Owned only when created in this session: PreToolUse saw no file, or an
      # earlier write of this session created it. No record → it stays (R21).
      was="$(ezh_memory_was "$sid" "$path")" || was=""
      if [ "$was" = new ] || ezh_owns_memory "$sid" "$path"; then ezh_own_memory "$sid" "$path"; fi
    else
      # Hint only: the git repo of the edited file.
      d="$path"; while [ ! -d "$d" ] && [ "$d" != "/" ]; do d="$(dirname "$d")"; done
      repo="$(git -C "$d" rev-parse --show-toplevel 2>/dev/null)" && ezh_touch "$sid" "$repo"
    fi
    ;;
  stop)
    # The lesson route is a separate switch (default off): the plan gate and the
    # index stay on while new memories stay in the shared memory folder.
    ezh_lesson_route_on || exit 0
    if ! start="$(ezh_session_started "$sid")"; then
      # No SessionStart seen (plugin enabled mid-session): start counting now,
      # move nothing, so no memory older than this moment can leave.
      ezh_session_start "$sid"
      exit 0
    fi
    key="$(printf '%s|%s' "$tsize" "$input" | cksum | tr -d ' ')"
    ezh_once "$sid" stop "$key" || exit 0
    # Only memory files this session wrote and that still sit in memory.
    owned="" listed="$(ezh_owned_memory "$sid")"
    while IFS= read -r f; do
      [ -n "$f" ] || continue
      # Gone (moved, or deleted by hand): a file created later at that path is not ours.
      if [ -f "$f" ]; then owned="$owned$f
"; else ezh_disown_memory "$sid" "$f"; fi
    done <<EOF_OWNED
$listed
EOF_OWNED
    [ -n "$owned" ] || exit 0
    # Only inside a configured workspace: elsewhere a project memory is not ours.
    # The cwd seen at SessionStart decides; the hook's cwd follows `cd`.
    gate_cwd="$(ezh_session_cwd "$sid")" || gate_cwd="$cwd"
    ezh_in_workspace "${gate_cwd:-$cwd}" || exit 0
    memdirs="$(printf '%s' "$owned" | while IFS= read -r f; do [ -n "$f" ] && dirname "$f"; done | sort -u)"
    inbox="${XDG_DATA_HOME:-$HOME/.local/share}/ezh/inbox"
    touched="$(ezh_touched "$sid")"
    messages=""
    while IFS= read -r memdir; do
      [ -n "$memdir" ] || continue
      # One sync per memory folder at a time: MEMORY.md is rewritten in place.
      # A lock older than 2 minutes is a run that was killed (Stop timeout).
      lock="$memdir/.ezh-lesson-sync.lock"
      if [ -d "$lock" ] && [ -n "$(find "$lock" -maxdepth 0 -mmin +2 2>/dev/null)" ]; then
        rmdir "$lock" 2>/dev/null || true
      fi
      tries=0
      busy=""
      until mkdir "$lock" 2>/dev/null; do
        tries=$((tries + 1))
        [ "$tries" -ge 40 ] && { busy=1; break; }
        sleep 0.25
      done
      if [ -n "$busy" ]; then
        messages="$messages$memdir is busy (another sync runs); memory kept for the next turn.
"
        continue
      fi
      trap 'rmdir "$lock" 2>/dev/null || true' EXIT INT TERM
      while IFS= read -r f; do
        [ -n "$f" ] && [ "$(dirname "$f")" = "$memdir" ] && [ -f "$f" ] || continue
        n="$(basename "$f")"
        # Each file once per content: a kept-private memory is reported once.
        ezh_seen "$sid" memory "$n|$(cksum < "$f" | tr -d ' ')" && continue
        errf="$(ezh_session_dir "$sid")/owners.err"
        repos="$(ezh_owner_repos "$f" 2>"$errf")"
        # R11: a broken ownership table is said once per session, not hidden.
        if [ -s "$errf" ] && ezh_once "$sid" table-error "$(cksum < "$errf" | tr -d ' ')"; then
          messages="$messages$(tr '\n' ' ' < "$errf")
"
        fi
        # Always the inbox (ADR-0015 #6): one match is only pre-assigned, never written
        # into that repo's checkout; the loop proposes it and the owner approves.
        hints="$(ezh_py -c 'import json,sys
m = [l for l in sys.argv[4].splitlines() if l]
print(json.dumps({"session": sys.argv[1], "cwd": sys.argv[2],
  "touched": [l for l in sys.argv[3].splitlines() if l],
  "matches": m, "assigned": m[0] if len(m) == 1 else None}))' "$sid" "$cwd" "$touched" "$repos")"
        result="$(printf '%s' "$input" | EZH_INBOX="$inbox" EZH_INBOX_HINTS="$hints" EZH_SINCE="$start" \
          EZH_MEMORY_DIR="$memdir" EZH_ONLY_FILES="$n" bash "$HERE/ezh-lesson-sync.sh")"
        msg="$(printf '%s' "$result" | ezh_py -c 'import json,sys
try: print(json.load(sys.stdin).get("systemMessage",""))
except Exception: pass' 2>/dev/null)"
        [ -n "$msg" ] && messages="$messages$msg
"
        # Mark done only after a run that did not fail (a failed one retries).
        case "$result" in
          *"failed, memory kept"*|*"lesson sync failed"*) ;;
          *) [ -f "$f" ] && ezh_once "$sid" memory "$n|$(cksum < "$f" | tr -d ' ')" >/dev/null ;;
        esac
        # Gone from memory: a file another session later creates at this path is not ours.
        [ -f "$f" ] || ezh_disown_memory "$sid" "$f"
      done <<EOF_FILES
$owned
EOF_FILES
      rmdir "$lock" 2>/dev/null || true
      trap - EXIT INT TERM
    done <<EOF_DIRS
$memdirs
EOF_DIRS
    # One short line per lesson.
    [ -n "$messages" ] && emit "$(printf '%s' "$messages" | sed '/^$/d')"
    ;;
  session-end)
    ezh_session_end "$sid"
    ;;
esac
exit 0
