#!/usr/bin/env bash
# ez-doctor.sh — read-only check of this machine and of where ezHarness state lives
# (ADR-0015 decision 9, ADR-0016 decision 4).
#
#   bash ez-doctor.sh [--workspace <dir>]...      (in Claude Code: /ez-doctor)
#
# First the machine (ENV lines): bash, git, a working python 3, the plugin's ez-evidence for
# this OS (it must answer `capabilities`), and whether a workspace is configured. No workspace
# is a NOTE, not an error: the plan gate then checks the repo the session stands in.
#
# Then, for each workspace (--workspace, else the configured ones, see _owners.sh), it reads the
# table _state-kinds.tsv next to this script and looks at every `.harness` folder
# (at most 6 levels below the workspace, linked worktrees included):
#
#   host   (parent not in a git work tree)  real files in a registered entry  -> PROBLEM host-holds-state
#                                           symlinks into a git work tree      -> NOTE pointer
#   repo   (top of a work tree)             repo-tracked path ignored/untracked -> PROBLEM shared-not-tracked
#   any    entry with no row in the table                                       -> NOTE unknown-entry
#
# Exit 0: no PROBLEM. Exit 3: at least one PROBLEM (machine or state). Exit 1: usage error.
# It never writes, moves or deletes anything; git is run with optional locks off.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=/dev/null
if [ -f "$HERE/_platform.sh" ]; then
  . "$HERE/_platform.sh"
  HERE="$(cd "$HERE" && pwd -P)"
else
  ezh_has_py() { command -v python3 >/dev/null 2>&1; }
  ezh_py() { python3 "$@"; }
  ezh_os_arch() { return 1; }
  ezh_exe_suffix() { return 0; }
  ezh_plugin_bin() { return 1; }
fi
TABLE="$HERE/_state-kinds.tsv"

if [ ! -f "$TABLE" ]; then
  echo "ez-doctor: state table not found: $TABLE" >&2
  exit 1
fi

WORKSPACES=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --workspace)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        echo "ez-doctor: --workspace needs a folder" >&2
        exit 1
      fi
      WORKSPACES+=("$2")
      shift 2
      ;;
    *)
      echo "ez-doctor: unknown argument: $1 (usage: ez-doctor.sh [--workspace <dir>]...)" >&2
      exit 1
      ;;
  esac
done

if [ "${#WORKSPACES[@]}" -eq 0 ] && [ -f "$HERE/_owners.sh" ]; then
  # shellcheck source=/dev/null
  . "$HERE/_owners.sh"
  WS_LIST="$(ezh_owner_workspaces)"
  while IFS= read -r w; do
    [ -n "$w" ] && WORKSPACES+=("$w")
  done <<EOF_WS
$WS_LIST
EOF_WS
fi

for w in "${WORKSPACES[@]}"; do
  if [ ! -d "$w" ]; then
    echo "ez-doctor: workspace is not a folder: $w" >&2
    exit 1
  fi
done

export GIT_OPTIONAL_LOCKS=0

# --- 1. The machine -------------------------------------------------------------
ENV_PROBLEMS=0
env_line() { # env_line OK|NOTE|PROBLEM <what> <detail>
  [ "$1" = PROBLEM ] && ENV_PROBLEMS=$((ENV_PROBLEMS + 1))
  printf 'ENV %s %s %s\n' "$1" "$2" "$3"
}
env_line OK bash "${BASH_VERSION:-unknown} ($(uname -s 2>/dev/null) $(uname -m 2>/dev/null))"
if command -v git >/dev/null 2>&1; then
  env_line OK git "$(git --version 2>/dev/null)"
else
  env_line PROBLEM git "not found: install git (on Windows: Git for Windows, which also brings bash)"
fi
if ezh_has_py; then
  env_line OK python "${EZH_PY_CMD:-python3} ($(ezh_py -c 'import sys; print(sys.version.split()[0])' 2>/dev/null))"
else
  env_line PROBLEM python "no working python 3 (tried python3, python, py -3): lesson index and lesson route are off"
fi
PLUGIN_DIR="$(dirname "$HERE")"
if [ -d "$PLUGIN_DIR/libexec" ]; then
  if BIN="$(ezh_plugin_bin "$PLUGIN_DIR")"; then
    if "$BIN" capabilities >/dev/null 2>&1; then
      env_line OK ez-evidence "$BIN"
    else
      env_line PROBLEM ez-evidence "$BIN does not run (\`capabilities\` failed): the plan gate blocks every plan"
    fi
  else
    env_line PROBLEM ez-evidence "no binary for $(ezh_os_arch 2>/dev/null || echo "$(uname -s) $(uname -m)") in $PLUGIN_DIR/libexec: the plan gate blocks every plan"
  fi
else
  env_line NOTE ez-evidence "no plugin libexec/ next to this script: the plan gate uses .harness/bin or ~/.local/share/ezh/bin"
fi
if [ "${#WORKSPACES[@]}" -eq 0 ]; then
  env_line NOTE workspaces "none configured: the plan gate checks only the repo a session stands in. To cover a folder of repos: mkdir -p ~/.config/ezh && echo /path/to/folder >> ~/.config/ezh/workspaces"
  [ "$ENV_PROBLEMS" -eq 0 ] && { echo "RESULT: OK"; exit 0; }
  echo "RESULT: $ENV_PROBLEMS problem(s)"
  exit 3
fi
if ! ezh_has_py; then
  echo "RESULT: $ENV_PROBLEMS problem(s) (state check skipped: it needs python)"
  exit 3
fi

# --- 2. Where state lives, per workspace -------------------------------------------
EZH_DOCTOR_ENV_PROBLEMS="$ENV_PROBLEMS" ezh_py -I - "$TABLE" "${WORKSPACES[@]}" <<'PY'
import os, subprocess, sys

table_path, workspaces = sys.argv[1], sys.argv[2:]
MAX_DEPTH = 6

rows = []  # (path, group, home)
with open(table_path, encoding="utf-8") as fh:
    for line in fh:
        if line.startswith("#") or not line.strip():
            continue
        cols = line.rstrip("\n").split("\t")
        if len(cols) >= 3:
            rows.append((cols[0], cols[1], cols[2]))


def row_for(rel, is_dir):
    """Longest matching table row for a path relative to the repo/host root."""
    best = None
    probe = rel + "/" if is_dir else rel
    for r in rows:
        p = r[0]
        if p.endswith("/"):
            hit = probe.startswith(p)
        elif p.endswith("*"):
            hit = rel.startswith(p[:-1])
        else:
            hit = rel == p
        if hit and (best is None or len(p) > len(best[0])):
            best = r
    return best


def git(path, *args):
    return subprocess.run(["git", "-C", path, *args], capture_output=True, text=True)


def work_tree_top(path):
    r = git(path, "rev-parse", "--show-toplevel")
    if r.returncode != 0 or not r.stdout.strip():
        return None
    return os.path.realpath(r.stdout.strip())


def in_work_tree(path):
    return work_tree_top(path) is not None


def find_harness(ws):
    out = []
    base_depth = ws.rstrip("/").count("/")
    for cur, dirs, _files in os.walk(ws):
        depth = cur.rstrip("/").count("/") - base_depth + 1  # depth of children
        for skip in ("node_modules", ".git"):
            if skip in dirs:
                dirs.remove(skip)
        if ".harness" in dirs:
            out.append(os.path.join(cur, ".harness"))
            dirs.remove(".harness")
        if depth >= MAX_DEPTH:
            dirs[:] = []
    return out


def files_under(path):
    """Every file or symlink below path (path itself when it is one), no symlink following."""
    if os.path.islink(path) or os.path.isfile(path):
        return [path]
    out = []
    for cur, dirs, files in os.walk(path):
        for d in list(dirs):
            if os.path.islink(os.path.join(cur, d)):
                out.append(os.path.join(cur, d))
                dirs.remove(d)
        out += [os.path.join(cur, f) for f in files if f != ".DS_Store"]   # OS litter is nobody's state
    return sorted(out)


seen = set()
harness_dirs = []
for w in workspaces:
    for h in find_harness(os.path.realpath(w)):
        if h not in seen:
            seen.add(h)
            harness_dirs.append(h)
harness_dirs.sort()

problems = notes = hosts = repos = 0
groups = {g: 0 for g in "ABCDE"}


def emit(kind, name, *rest):
    global problems, notes
    if kind == "PROBLEM":
        problems += 1
    else:
        notes += 1
    print(" ".join(x for x in (kind, name, *rest) if x))


for h in harness_dirs:
    parent = os.path.dirname(h)
    top = work_tree_top(parent)
    if top is None:
        mode = "host"
        hosts += 1
    elif top == os.path.realpath(parent):
        mode = "repo"
        repos += 1
    else:
        mode = "inner"
    try:
        entries = sorted(os.listdir(h))
    except OSError as exc:
        print(f"NOTE unreadable {parent} .harness ({exc.strerror})")
        notes += 1
        continue

    for ent in entries:
        full = os.path.join(h, ent)
        is_dir = os.path.isdir(full) and not os.path.islink(full)
        label = ent + "/" if is_dir else ent
        files = files_under(full)
        if is_dir and not files:
            continue  # empty folder
        rel = ".harness/" + ent
        best = row_for(rel, is_dir)
        if best is None:  # a row may cover only files inside the folder
            for f in files:
                best = row_for(os.path.relpath(f, parent), False)
                if best:
                    break
        if best is None:
            emit("NOTE", "unknown-entry", parent, label)
            continue
        groups[best[1]] = groups.get(best[1], 0) + 1
        if mode != "host":
            continue
        real = [f for f in files if not os.path.islink(f)]
        links = [f for f in files if os.path.islink(f)]
        if real:
            emit("PROBLEM", "host-holds-state", parent, label, f"group={best[1]}", f"files={len(real)}")
        elif links:
            inside = [l for l in links if in_work_tree(os.path.dirname(os.path.realpath(l)))
                      and os.path.exists(l)]
            if len(inside) == len(links):
                emit("NOTE", "pointer", parent, label, "->", f"{len(links)} symlinks into git work trees")
            else:
                emit("NOTE", "pointer-outside-git", parent, label, "->", f"{len(links) - len(inside)} of {len(links)} symlinks do not end in a git work tree")

    if mode != "repo":
        continue
    if os.path.islink(h):
        emit("NOTE", "harness-symlink", parent, ".harness")
        continue
    tracked = set(git(parent, "ls-files", "-z").stdout.split("\0"))
    for p, _g, home in rows:
        if home != "repo-tracked" or not p.startswith(".harness/"):
            continue
        target = os.path.join(parent, p.rstrip("*"))
        if p.endswith("*"):
            cands = []
            d = os.path.dirname(target)
            if os.path.isdir(d):
                cands = [os.path.join(d, n) for n in os.listdir(d)
                         if os.path.join(d, n).startswith(target)]
            files = sorted(x for c in cands for x in files_under(c))
        elif os.path.lexists(target):
            files = files_under(target)
        else:
            continue
        # a deeper row may give a sub-path another home; keep only repo-tracked files
        files = [f for f in files if (row_for(os.path.relpath(f, parent), False) or ("", "", ""))[2] == "repo-tracked"]
        # The lessons skeleton the installer writes (README.md, _fixtures/) is
        # installer output, not team knowledge: alone it is nothing to share.
        if p == ".harness/lessons/":
            files = [f for f in files
                     if os.path.basename(f) != "README.md" and "/_fixtures/" not in f.replace(os.sep, "/")]
        if not files:
            continue
        rels = [os.path.relpath(f, parent) for f in files]
        untracked = [r for r in rels if r not in tracked]
        if not untracked:
            continue
        # An ignored file can never reach the team, even next to tracked ones.
        ignored = [r for r in untracked if git(parent, "check-ignore", "-q", "--", r).returncode == 0]
        if ignored:
            emit("PROBLEM", "shared-not-tracked", parent, p, "ignored",
                 f"files={len(ignored)}" if len(ignored) > 1 else "", f"first={ignored[0]}")
        elif len(untracked) == len(rels):
            emit("PROBLEM", "shared-not-tracked", parent, p, "untracked",
                 f"files={len(untracked)}" if len(untracked) > 1 else "")
        else:
            emit("NOTE", "not-committed-yet", parent, p, f"files={len(untracked)}")

print(f"SUMMARY harness-folders={len(harness_dirs)} hosts={hosts} repos={repos} problems={problems} notes={notes}")
print("SUMMARY entries-per-group " + " ".join(f"{g}={groups.get(g, 0)}" for g in "ABCDE"))
problems += int(os.environ.get("EZH_DOCTOR_ENV_PROBLEMS", "0") or 0)
print("RESULT: OK" if problems == 0 else f"RESULT: {problems} problem(s)")
sys.exit(0 if problems == 0 else 3)
PY
