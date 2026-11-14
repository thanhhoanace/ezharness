#!/usr/bin/env bash
# _owners.sh — which git repo owns a lesson (ADR-0014, rule C).
#
# Each gated repo may commit `.harness/lesson-owners.yaml`:
#
#   keywords: [cluster, argocd]      # whole word, case-insensitive, on name + description
#   paths:                           # repo-relative fnmatch globs (`*` also crosses `/`;
#                                    #   `dir/**` = anything under dir), matched against paths the
#     - apps/**                      #   lesson BODY names (never the frontmatter
#                                    #   `paths:` field: ADR-0005 scope does not pick a repo)
#
# Tables are found under the workspaces listed in $EZH_WORKSPACES (colon-separated),
# else in ${XDG_CONFIG_HOME:-~/.config}/ezh/workspaces (one absolute folder per line),
# at most 4 folders deep. Only a table at the top of a MAIN work tree counts (R12):
# a linked worktree carries a copy of the table but is the same repo, so it never
# gives a second match. A malformed table counts as no match and is named on stderr.
#
#   ezh_owner_repos <lesson-file>   print the matching repo roots, one per line
#   ezh_owner_workspaces            print the workspaces that are scanned
#   ezh_in_workspace <path>         exit 0 when <path> is inside one of them
#   ezh_lesson_route_on             exit 0 when this machine moves new memories (default off)
#   ezh_is_main_worktree <dir>      exit 0 when <dir> is the top of a main work tree
#   ezh_workspace_find <suffix>     print paths under the workspaces ending in /<suffix>

# python through _platform.sh when the caller sourced it (ADR-0016), else python3.
type ezh_py >/dev/null 2>&1 || ezh_py() { python3 "$@"; }

ezh_owner_workspaces() {
  local conf="${XDG_CONFIG_HOME:-$HOME/.config}/ezh/workspaces"
  if [ -n "${EZH_WORKSPACES:-}" ]; then
    printf '%s\n' "$EZH_WORKSPACES" | tr ':' '\n' | grep -v '^$'
  elif [ -f "$conf" ]; then
    grep -v '^[[:space:]]*\(#\|$\)' "$conf"
  fi
  return 0
}

ezh_lesson_route_on() {
  # ezh_lesson_route_on: exit 0 when this machine moves new memories to the inbox.
  # OFF unless turned on (EZH_LESSON_ROUTE=on, or "on" in
  # ${XDG_CONFIG_HOME:-~/.config}/ezh/lesson-route): a memory moved to the inbox
  # leaves the shared memory folder, and only the lesson-inbox loop brings it
  # back to a repo. Turn it on once the loop can run on this machine.
  case "${EZH_LESSON_ROUTE:-}" in
    on|ON|1|true|yes) return 0 ;;
    off|OFF|0|false|no) return 1 ;;
  esac
  local conf="${XDG_CONFIG_HOME:-$HOME/.config}/ezh/lesson-route"
  [ -f "$conf" ] || return 1
  grep -v '^[[:space:]]*#' "$conf" | tr -d '[:space:]' | grep -qix 'on'
}

ezh_in_workspace() {
  # ezh_in_workspace <path>: exit 0 when <path> is inside a configured workspace.
  local p ws
  p="$(cd "$1" 2>/dev/null && pwd -P)" || return 1
  while IFS= read -r ws; do
    [ -d "$ws" ] || continue
    ws="$(cd "$ws" && pwd -P)"
    case "$p/" in "$ws/"*) return 0 ;; esac
  done <<EOF_WS
$(ezh_owner_workspaces)
EOF_WS
  return 1
}

ezh_is_main_worktree() {
  # The top of a work tree whose git dir is the common dir: a linked worktree's
  # git dir is <common>/worktrees/<name>. Paths may print relative to <dir>.
  local d top gd cd out
  d="$(cd "$1" 2>/dev/null && pwd -P)" || return 1
  out="$(git -C "$d" rev-parse --show-toplevel --git-dir --git-common-dir 2>/dev/null)" || return 1
  top="$(printf '%s\n' "$out" | sed -n 1p)"
  gd="$(printf '%s\n' "$out" | sed -n 2p)"
  cd="$(printf '%s\n' "$out" | sed -n 3p)"
  [ -n "$top" ] && [ -n "$gd" ] && [ -n "$cd" ] || return 1
  [ "$(cd "$top" 2>/dev/null && pwd -P)" = "$d" ] || return 1
  gd="$(cd "$d" && cd "$gd" 2>/dev/null && pwd -P)" || return 1
  cd="$(cd "$d" && cd "$cd" 2>/dev/null && pwd -P)" || return 1
  [ "$gd" = "$cd" ]
}

ezh_workspace_find() {
  # ezh_workspace_find <suffix>: paths ending in /<suffix> at most 5 levels under a
  # workspace (a repo at most 3 deep); never inside .git, node_modules or .worktrees.
  local ws
  while IFS= read -r ws; do
    [ -d "$ws" ] || continue
    find "$ws" -maxdepth 5 \( -name .git -o -name node_modules -o -name .worktrees \) -prune \
      -o -path "*/$1" -print 2>/dev/null
  done <<EOF_WS
$(ezh_owner_workspaces)
EOF_WS
  return 0
}

ezh_owner_repos() {
  local lesson="$1"
  [ -f "$lesson" ] || return 0
  ezh_workspace_find .harness/lesson-owners.yaml | while IFS= read -r t; do
    [ -n "$t" ] && [ -f "$t" ] || continue
    repo="$(cd "$(dirname "$t")/.." && pwd -P)"
    ezh_is_main_worktree "$repo" && printf '%s\t%s\n' "$repo" "$t"
  done | ezh_py -c '
import fnmatch, re, sys

def parse_table(text):
    """keywords/paths: inline [a, b] or block "- a". Anything else is malformed."""
    out, key = {"keywords": [], "paths": []}, None
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].rstrip()
        if not line.strip():
            continue
        m = re.match(r"^(keywords|paths):\s*(.*)$", line)
        if m:
            key, rest = m.group(1), m.group(2).strip()
            if rest:
                if not (rest.startswith("[") and rest.endswith("]")):
                    raise ValueError(f"{key}: expected [a, b] or a block list")
                items = [i.strip().strip("\"\x27") for i in rest[1:-1].split(",")]
                if any(not i or ":" in i for i in items):
                    raise ValueError(f"{key}: bad item")
                out[key] += items
                key = None
            continue
        m = re.match(r"^\s+-\s+(.+)$", line)
        if m and key:
            out[key].append(m.group(1).strip().strip("\"\x27"))
            continue
        raise ValueError(f"cannot read line: {raw.strip()[:60]}")
    return out

def split(text):
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        return {}, text
    fields = {}
    for i, raw in enumerate(lines[1:], start=1):
        if raw.strip() == "---":
            return fields, "\n".join(lines[i + 1:])
        m = re.match(r"^(name|description):\s*(.*)$", raw)
        if m:
            fields[m.group(1)] = m.group(2).strip().strip("\"\x27")
    return {}, text

lesson = open(sys.argv[1], errors="replace").read()
fields, body = split(lesson)
words = (fields.get("name", "") + " " + fields.get("description", "")).replace("-", " ").replace("_", " ")
body_paths = {re.sub(r"^\./", "", p.lstrip("`\"\x27([").rstrip("`\"\x27)],.;:"))
              for p in re.findall(r"[\w./*-]*/[\w./*-]+", body)}
for row in sys.stdin.read().splitlines():
    repo, table = row.split("\t", 1)
    try:
        t = parse_table(open(table, errors="replace").read())
    except Exception as exc:
        print(f"ezh: lesson-owners table ignored in {repo}: {exc}", file=sys.stderr)
        continue
    norm = lambda k: k.replace("-", " ").replace("_", " ")
    hit = any(re.search(r"(?<![\w-])" + re.escape(norm(k)) + r"(?![\w-])", words, re.I) for k in t["keywords"])
    if not hit:
        for g in t["paths"]:
            pref = g[:-3] if g.endswith("/**") else None
            if any(fnmatch.fnmatch(p, g) or (pref and (p == pref or p.startswith(pref + "/"))) for p in body_paths):
                hit = True
                break
    if hit:
        print(repo)
' "$lesson" | sort -u
}
