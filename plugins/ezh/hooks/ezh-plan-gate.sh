#!/usr/bin/env bash
# ezh-plan-gate.sh — Claude Code UserPromptSubmit hook (v6.6 ADR-0011; lookup
# ADR-0013 point 4 as changed by ADR-0015 decision 5).
#
# When the prompt refers to a plan, run `ez-evidence plan validate` on it and
# inject the result into the model's context. A FAIL result tells the agent to
# refuse executing that plan. Codex and OpenCode have no UserPromptSubmit
# event: there the agent runs the verb by hand (GATE 0 in PLAN-TEMPLATE.md).
#
# One code path for both modes:
#   plugin mode     CLAUDE_PLUGIN_ROOT is set and this script lives under it.
#   installed mode  this script lives in <ws>/.harness/hooks/ (libs may be missing
#                   in old installs: then no workspace scan and no per-turn key).
#
# Which plans: (a) explicit *.md tokens in the prompt that resolve to a file
# under a plans/ directory, (b) slugs named as a whole word, looked up as
# `.harness/plans/<slug>.md` in every work tree of the session's repo, of the
# installed-mode root's repo and of every repo under the configured workspaces
# (_owners.sh), plus the legacy folders <ws>, $CLAUDE_PROJECT_DIR and cwd.
# Equal copies collapse into one validation; different copies BLOCK. The gate
# only reads: it never creates worktrees, writes into a repo or moves a plan.
#
# FAIL-CLOSED policy:
#   - a plan is referenced but no ez-evidence binary is found (next to the plan,
#     in the main checkout, in the plugin) -> BLOCK (exit 2), reason on stderr.
#   - one slug, several different files -> BLOCK (exit 2) listing every path.
#   - the prompt cannot be parsed (no jq and no python3) -> inject a loud
#     "PLAN GATE INOPERATIVE" notice instead of staying quiet.
set -uo pipefail

TAB="$(printf '\t')"
NL='
'

# --- 0. Mode and libraries ----------------------------------------------------
SELF_DIR="$(cd "$(dirname "$0")" 2>/dev/null && pwd -P)"
HAVE_ROUTE=0
HAVE_OWNERS=0
# Platform helpers (ADR-0016). An installed copy may stand alone: then python3 only,
# paths as they come, and no plugin binary.
# shellcheck source=/dev/null
if [ -f "$SELF_DIR/_platform.sh" ]; then
  . "$SELF_DIR/_platform.sh"
else
  ezh_has_py() { command -v python3 >/dev/null 2>&1; }
  ezh_py() { python3 "$@"; }
  ezh_path() { printf '%s\n' "$1"; }
  ezh_plugin_bin() { return 1; }
  ezh_exe_suffix() { return 0; }
  ezh_os_arch() { return 1; }
fi
# shellcheck source=/dev/null
[ -f "$SELF_DIR/_route.sh" ] && . "$SELF_DIR/_route.sh" && HAVE_ROUTE=1
# shellcheck source=/dev/null
[ -f "$SELF_DIR/_owners.sh" ] && . "$SELF_DIR/_owners.sh" && HAVE_OWNERS=1

PLUGIN_MODE=0 PLUGIN_DIR=""
if [ -n "${CLAUDE_PLUGIN_ROOT:-}" ]; then
  pr="$(cd "$(ezh_path "$CLAUDE_PLUGIN_ROOT")" 2>/dev/null && pwd -P)" || pr=""
  case "$SELF_DIR/" in "$pr/"*) [ -n "$pr" ] && PLUGIN_MODE=1 PLUGIN_DIR="$pr" ;; esac
fi
# Installed mode: this script lives in <ws>/.harness/hooks/.
INST_WS=""
if [ "$PLUGIN_MODE" = 0 ] && [ -n "$SELF_DIR" ]; then
  INST_WS="$(dirname "$SELF_DIR")"
  INST_WS="$(dirname "$INST_WS")"
  INST_WS="$(cd "$INST_WS" 2>/dev/null && pwd -P)"
fi

# The plugin hook and a project hook must see each other's per-turn key, but
# _route.sh puts the cache under $CLAUDE_PLUGIN_DATA, which only the plugin has.
[ -z "${EZH_ROUTE_CACHE:-}" ] && export EZH_ROUTE_CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/ezh/sessions"

INPUT="$(cat)"

emit() { # emit <context-text>  -> hook JSON on stdout
  if ezh_has_py; then
    CTX="$1" ezh_py -c 'import json,os;print(json.dumps({"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":os.environ["CTX"]}}))'
  else
    jq -n --arg ctx "$1" '{hookSpecificOutput:{hookEventName:"UserPromptSubmit",additionalContext:$ctx}}'
  fi
}

# --- 1. Read the prompt, session id and cwd ------------------------------------
PROMPT="" SID="" CWD="" TRANSCRIPT=""
if command -v jq >/dev/null 2>&1; then
  PROMPT="$(printf '%s' "$INPUT" | jq -r '.prompt // empty' 2>/dev/null)"
  SID="$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null)"
  CWD="$(printf '%s' "$INPUT" | jq -r '.cwd // empty' 2>/dev/null)"
  TRANSCRIPT="$(printf '%s' "$INPUT" | jq -r '.transcript_path // empty' 2>/dev/null)"
elif ezh_has_py; then
  field() { printf '%s' "$INPUT" | ezh_py -c 'import json,sys;print(json.load(sys.stdin).get(sys.argv[1]) or "")' "$1" 2>/dev/null; }
  PROMPT="$(field prompt)"; SID="$(field session_id)"; CWD="$(field cwd)"; TRANSCRIPT="$(field transcript_path)"
else
  # Cannot inspect the prompt at all: say so loudly instead of failing open.
  printf '%s' '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"[PLAN GATE INOPERATIVE] Neither jq nor python (python3, python, py -3) is available, so the plan gate could not inspect this prompt. Do NOT execute any plan in this session until the gate is working again. Tell the user."}}'
  exit 0
fi
[ -z "$PROMPT" ] && exit 0
[ -n "$CWD" ] && CWD="$(ezh_path "$CWD")"
[ -n "$TRANSCRIPT" ] && TRANSCRIPT="$(ezh_path "$TRANSCRIPT")"
{ [ -n "$CWD" ] && [ -d "$CWD" ]; } || CWD="$(pwd)"

# --- 2. Lookup set: plans folders of every work tree we can see ---------------
# DIRS lines: <plans-dir> TAB <main checkout or empty>. Order matters for equal
# copies: the session's work tree first, then its main checkout, then the rest.
DIRS=""
# An empty field is written as "-": tab is IFS whitespace, so `read` would collapse it.
add_dir() { [ -d "$1" ] && DIRS="${DIRS}$1${TAB}${2:--}${NL}"; return 0; }

add_repo() { # add_repo <absolute git common dir>: main checkout + linked worktrees
  local common="$1" main gd line
  case "$common" in */.git) main="${common%/.git}" ;; *) return 0 ;; esac
  add_dir "$main/.harness/plans" "$main"
  for gd in "$common"/worktrees/*/gitdir; do
    [ -f "$gd" ] || continue
    line=""
    read -r line < "$gd" || [ -n "$line" ] || continue
    case "$line" in /*|[A-Za-z]:/*) ;; *) line="${gd%/gitdir}/$line" ;; esac   # relative gitdir (git >= 2.48)
    line="${line%/.git}"
    [ -d "$line" ] && add_dir "$line/.harness/plans" "$main"
  done
}

add_repo_of() { # add_repo_of <folder>: the work tree it sits in first, then its repo
  local out top common
  top="$(git -C "$1" rev-parse --path-format=absolute --show-toplevel 2>/dev/null)" || return 0
  common="$(git -C "$1" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || return 0
  [ -n "$top" ] && [ -n "$common" ] || return 0
  REPO_TOP="$top" REPO_MAIN=""
  case "$common" in */.git) REPO_MAIN="${common%/.git}" ;; esac
  add_dir "$top/.harness/plans" "$REPO_MAIN"
  add_repo "$common"
}

REPO_TOP="" REPO_MAIN=""
add_repo_of "$CWD"                                       # (a) the session's repo
SESS_TOP="$REPO_TOP" SESS_MAIN="$REPO_MAIN"
[ -n "$INST_WS" ] && add_repo_of "$INST_WS"              # installed-mode root's repo

WORKSPACES="" SCAN=""
[ "$HAVE_OWNERS" = 1 ] && WORKSPACES="$(ezh_owner_workspaces)"
# Only a session that stands inside a workspace looks at the workspace's plans:
# a prompt in an unrelated folder must never be checked, or blocked, by them.
[ "$HAVE_OWNERS" = 1 ] && [ -n "$WORKSPACES" ] && ezh_in_workspace "$CWD" && SCAN="$WORKSPACES"
scan_ws() { # scan_ws <workspace>: add every repo below it (b)
  local gits g
  gits="$(find "$1" -maxdepth 5 \( -name node_modules -o -name .worktrees \) -prune -o -name .git -type d -print -prune 2>/dev/null | sort)"
  while IFS= read -r g; do
    [ -n "$g" ] && add_repo "$g"
  done <<EOF_G
$gits
EOF_G
}
while IFS= read -r ws; do                               # (b) repos under the workspaces
  [ -n "$ws" ] && [ -d "$ws" ] && scan_ws "$ws"
done <<EOF_WS
$SCAN
EOF_WS

[ -n "$INST_WS" ] && add_dir "$INST_WS/.harness/plans" ""        # (c) legacy folders
[ -n "${CLAUDE_PROJECT_DIR:-}" ] && add_dir "$CLAUDE_PROJECT_DIR/.harness/plans" ""
add_dir "$CWD/.harness/plans" ""

# Deduplicate by real path (a symlink and its target are one folder).
UDIRS="" SEEN=""
while IFS="$TAB" read -r d main; do
  [ -n "$d" ] || continue
  rd="$(cd "$d" 2>/dev/null && pwd -P)" || continue
  case "$NL$SEEN" in *"$NL$rd$NL"*) continue ;; esac
  SEEN="$SEEN$rd$NL"
  UDIRS="${UDIRS}${rd}${TAB}${main}${NL}"
done <<EOF_D
$DIRS
EOF_D

# --- 3. Explicit *.md paths in the prompt -------------------------------------
# FILES lines: <file> TAB <main checkout or empty> TAB <other equal copies, |-joined>
FILES="" EXPLICIT=""
add_file() { # add_file <file> <main> <also>
  case "$NL$FILES" in *"$NL$1$TAB"*) return 0 ;; esac
  FILES="${FILES}$1${TAB}${2:--}${TAB}${3:--}${NL}"
}
TOKENS="$(printf '%s' "$PROMPT" | grep -oE '[A-Za-z0-9._/-]+\.md' | sort -u || true)"
while IFS= read -r p; do
  [ -n "$p" ] || continue
  f=""
  case "$p" in
    /*) [ -f "$p" ] && f="$p" ;;
    *)
      # Only against the session's own folders: resolving against any work tree
      # would pick one of several different copies and skip the ambiguity check.
      for root in "$CWD" "$SESS_TOP" "$SESS_MAIN" "$INST_WS"; do
        [ -n "$root" ] && [ -f "$root/$p" ] && { f="$root/$p"; break; }
      done ;;
  esac
  [ -n "$f" ] || continue                                 # unresolved: the slug lookup sees it
  case "$f" in */plans/*) ;; *) continue ;; esac          # only plan files
  # The plugin runs in every session: there only the canonical home counts, so a
  # file under some other tool's plans/ folder is never linted as a plankit plan.
  if [ "$PLUGIN_MODE" = 1 ]; then case "$f" in */.harness/plans/*) ;; *) continue ;; esac; fi
  fbase="$(basename "$f")"
  case "$fbase" in PLAN-TEMPLATE.md) continue ;; esac
  fdir="$(dirname "$f")"
  fdir="$(cd "$fdir" && pwd -P)"
  f="$fdir/$fbase"
  add_file "$f" "" ""
  EXPLICIT="$EXPLICIT${fbase%.md}$NL"
done <<EOF_T
$TOKENS
EOF_T

# --- 4. Slugs named as a whole word -------------------------------------------
# Whole word: no [A-Za-z0-9_-] on either side, so slug `state` does not match
# `state-placement` and a short slug does not block an ordinary prompt.
HITS=""
while IFS="$TAB" read -r d main; do
  [ -n "$d" ] || continue
  for f in "$d"/*.md; do
    [ -e "$f" ] || continue
    slug="${f##*/}"; slug="${slug%.md}"
    [ "$slug" = "PLAN-TEMPLATE" ] && continue
    case "$NL$EXPLICIT" in *"$NL$slug$NL"*) continue ;; esac   # explicit path wins
    case "$slug" in
      *[!A-Za-z0-9._-]*) printf '%s' "$PROMPT" | grep -qF -- "$slug" || continue ;;
      *)
        re="(^|[^A-Za-z0-9_-])${slug//./\\.}([^A-Za-z0-9_-]|$)"
        [[ $PROMPT =~ $re ]] || continue ;;
    esac
    HITS="${HITS}${slug}${TAB}${f}${TAB}${main}${NL}"
  done
done <<EOF_U
$UDIRS
EOF_U

# Copies of one plan that differ are not always a conflict: once a plan is on the
# default branch every work tree carries a copy, and only the one being worked
# on changes. A work tree "changed" a plan when the file differs from the
# merge-base with the default branch (or was never there, or we cannot tell).
default_ref() { # default_ref <dir> -> a ref of the repo's default branch
  local r
  r="$(git -C "$1" symbolic-ref -q --short refs/remotes/origin/HEAD 2>/dev/null)" && [ -n "$r" ] && { printf '%s' "$r"; return 0; }
  for r in origin/main origin/master main master; do
    git -C "$1" rev-parse -q --verify "$r^{commit}" >/dev/null 2>&1 && { printf '%s' "$r"; return 0; }
  done
  return 1
}
plan_changed_here() { # exit 0: this work tree changed the plan, or we cannot tell
  local f="$1" d="${1%/*}" ref mb rel
  ref="$(default_ref "$d")" || return 0
  rel="$(git -C "$d" rev-parse --show-prefix 2>/dev/null)" || return 0
  rel="$rel${f##*/}"
  mb="$(git -C "$d" merge-base HEAD "$ref" 2>/dev/null)" || return 0
  git -C "$d" cat-file -e "$mb:$rel" 2>/dev/null || return 0
  git -C "$d" diff --quiet "$mb" -- "${f##*/}" 2>/dev/null && return 1
  return 0
}
plan_is_default_tip() { # exit 0: the file equals the default branch's version
  local f="$1" d="${1%/*}" ref rel
  ref="$(default_ref "$d")" || return 1
  rel="$(git -C "$d" rev-parse --show-prefix 2>/dev/null)" || return 1
  [ "$(git -C "$d" show "$ref:$rel${f##*/}" 2>/dev/null | cksum)" = "$(cksum < "$f")" ]
}

AMBIGUOUS="" OLDER="" MINE=""
note_mine() { # note_mine <checked file> <group>: the session's own copy differs
  local chosen="$1" csum f _s main
  [ -n "$SESS_TOP" ] || return 0
  csum="$(cksum < "$chosen")"
  while IFS="$TAB" read -r _s f main; do
    [ -n "$f" ] || continue
    case "$f" in "$SESS_TOP/.harness/plans/"*) ;; *) continue ;; esac   # not a nested work tree
    [ "$f" = "$chosen" ] && continue
    [ "$(cksum < "$f")" = "$csum" ] || MINE="${MINE}${chosen}${TAB}${f}${NL}"
  done <<EOF_M
$2
EOF_M
}
if [ -n "$HITS" ]; then
  SLUGS="$(printf '%s' "$HITS" | cut -f1 | sort -u)"
  while IFS= read -r slug; do
    [ -n "$slug" ] || continue
    group="$(printf '%s' "$HITS" | awk -F'\t' -v s="$slug" '$1==s')"
    first="" first_main="" also="" sums="" paths=""
    while IFS="$TAB" read -r _s f main; do
      sum="$(cksum < "$f" | awk '{print $1"-"$2}')"
      case " $sums " in *" $sum "*) ;; *) sums="$sums $sum" ;; esac
      paths="$paths    $f$NL"
      if [ -z "$first" ]; then first="$f"; first_main="$main"
      else also="${also:+$also|}$f"; fi
    done <<EOF_GR
$group
EOF_GR
    set -- $sums
    if [ "$#" -gt 1 ]; then
      # Keep only the copies their work tree changed.
      c_first="" c_main="" c_also="" c_sums="" c_paths="" tip="" tip_main="" unchanged=0
      while IFS="$TAB" read -r _s f main; do
        if plan_changed_here "$f"; then
          sum="$(cksum < "$f" | awk '{print $1"-"$2}')"
          case " $c_sums " in *" $sum "*) ;; *) c_sums="$c_sums $sum" ;; esac
          c_paths="$c_paths    $f$NL"
          if [ -z "$c_first" ]; then c_first="$f"; c_main="$main"
          else c_also="${c_also:+$c_also|}$f"; fi
        else
          unchanged=$((unchanged + 1))
          if [ -z "$tip" ] && plan_is_default_tip "$f"; then tip="$f"; tip_main="$main"; fi
        fi
      done <<EOF_CH
$group
EOF_CH
      set -- $c_sums
      if [ "$#" -eq 1 ]; then
        add_file "$c_first" "$c_main" "$c_also"
        OLDER="${OLDER}${c_first}${TAB}${unchanged}${NL}"
        note_mine "$c_first" "$group"
      elif [ "$#" -eq 0 ] && [ -n "$tip" ]; then
        add_file "$tip" "$tip_main" ""
        behind=$((unchanged - 1))
        OLDER="${OLDER}${tip}${TAB}${behind}${NL}"
        note_mine "$tip" "$group"
      elif [ "$#" -eq 0 ]; then
        AMBIGUOUS="${AMBIGUOUS}  slug '$slug' names different plans, none on the default branch:$NL$paths"
      else
        AMBIGUOUS="${AMBIGUOUS}  slug '$slug' is being changed in $# places:$NL$c_paths"
      fi
    else
      add_file "$first" "$first_main" "$also"
    fi
  done <<EOF_S
$SLUGS
EOF_S
fi

# --- 5. Nothing to check -------------------------------------------------------
if [ -z "$FILES" ] && [ -z "$AMBIGUOUS" ]; then
  # Plugin mode with no workspace and no plans folder in sight: say it once per
  # session, never block for it.
  if [ "$PLUGIN_MODE" = 1 ] && [ -z "$WORKSPACES" ] && [ -z "$UDIRS" ] \
     && [ "$HAVE_ROUTE" = 1 ] && [ -n "$SID" ] && ezh_once "$SID" plan-gate-noworkspace x; then
    emit "ezh plan gate: no .harness/plans in this repo and no workspace configured (~/.config/ezh/workspaces), so plans named in prompts are not checked here. Run /ez-doctor for setup."
  fi
  exit 0
fi

# --- 6. Ambiguous names block ------------------------------------------------
if [ -n "$AMBIGUOUS" ]; then
  {
    echo "PLAN GATE BLOCKED: one plan name matches different files."
    printf '%s' "$AMBIGUOUS"
    echo "Name the path of the plan you mean in the prompt (for example repo/.harness/plans/<slug>.md)."
  } >&2
  exit 2
fi

# --- 7. Binary for each plan, fail closed --------------------------------------
# Order (R18): .harness/bin next to the plan, the main checkout of the plan's
# repo, the plugin's own binary for this machine (libexec/, ADR-0016), the plugin
# data folder, the user data folder. Never PATH. On Windows each name may end in .exe.
find_bin() { # find_bin <plan-file> <main> -> sets BIN and SEARCHED
  local pd="${1%/*}" main="$2" c common
  [ "$main" = - ] && main=""
  if [ -z "$main" ]; then
    common="$(git -C "$pd" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || common=""
    case "$common" in */.git) main="${common%/.git}" ;; esac
  fi
  SEARCHED="${pd%/*}/bin/ez-evidence"
  [ -n "$main" ] && SEARCHED="$SEARCHED$NL$main/.harness/bin/ez-evidence"
  if [ -n "$PLUGIN_DIR" ]; then
    plat="$(ezh_os_arch 2>/dev/null)" || plat="unknown-os"
    sfx="$(ezh_exe_suffix)"
    SEARCHED="$SEARCHED$NL$PLUGIN_DIR/libexec/ez-evidence-$plat$sfx"
  fi
  [ -n "${CLAUDE_PLUGIN_DATA:-}" ] && SEARCHED="$SEARCHED$NL$CLAUDE_PLUGIN_DATA/bin/ez-evidence"
  SEARCHED="$SEARCHED$NL${XDG_DATA_HOME:-$HOME/.local/share}/ezh/bin/ez-evidence"
  BIN=""
  local x
  x="$(ezh_exe_suffix)"
  while IFS= read -r c; do
    case "$c" in
      "$PLUGIN_DIR"/libexec/*)
        # The cache copy may have lost its exec bit: ezh_plugin_bin fixes that.
        if [ -n "$PLUGIN_DIR" ] && BIN="$(ezh_plugin_bin "$PLUGIN_DIR")"; then return 0; fi
        BIN=""; continue ;;
    esac
    [ -x "$c" ] && [ -f "$c" ] && { BIN="$c"; return 0; }
    [ -n "$x" ] && [ -x "$c$x" ] && [ -f "$c$x" ] && { BIN="$c$x"; return 0; }
  done <<EOF_B
$SEARCHED
EOF_B
  return 1
}

PLAN_BINS="" MISSING=""
while IFS="$TAB" read -r f main also; do
  [ -n "$f" ] || continue
  if find_bin "$f" "$main"; then
    PLAN_BINS="${PLAN_BINS}${f}${TAB}${BIN}${TAB}${also}${NL}"
  else
    searched="$(printf '%s\n' "$SEARCHED" | sed 's/^/    searched: /')"
    MISSING="${MISSING}  plan: $f${NL}${searched}${NL}"
  fi
done <<EOF_F
$FILES
EOF_F

if [ -n "$MISSING" ]; then
  {
    echo "PLAN GATE BLOCKED: a plan file was referenced but the validator is missing."
    printf '%s' "$MISSING"
    echo "Install the ezh plugin, or re-run the ezHarness installer (install.sh --refresh, or --context-only at a workspace root), before executing any plan."
  } >&2
  exit 2
fi

# --- 8. One result per turn ----------------------------------------------------
# The plugin and a project hook may both fire for one prompt; only the first
# prints (key: session id + prompt checksum + transcript size + plan contents,
# so the same prompt in a later turn, or after an edit, is checked again). A BLOCK never takes the key, so a
# retyped blocked prompt is checked again.
if [ "$HAVE_ROUTE" = 1 ] && [ -n "$SID" ]; then
  tsize=0
  [ -n "$TRANSCRIPT" ] && [ -f "$TRANSCRIPT" ] && tsize="$(wc -c < "$TRANSCRIPT" | tr -d ' ')"
  plan_sums() { printf '%s' "$FILES" | cut -f1 | while IFS= read -r pf; do [ -n "$pf" ] && cksum < "$pf"; done; }
  psum="$(plan_sums | cksum | awk '{print $1}')"
  psig="$(printf '%s' "$PROMPT" | cksum | awk '{print $1"-"$2}')"
  key="$psig-$tsize-$psum"
  ezh_once "$SID" plan-gate "$key" || exit 0
fi

# --- 9. Validate every referenced plan ----------------------------------------
OUT=""
while IFS="$TAB" read -r f bin also; do
  [ -n "$f" ] || continue
  pd="${f%/*}"
  # Run inside the plan's work tree: ez-evidence finds env-patterns through the
  # process cwd's git common dir too.
  RESULT="$(cd "$pd" && "$bin" plan validate --file "$f" 2>&1)"
  STATUS=$?
  NOTES=""
  if [ "$also" != - ]; then
    alsolist="$(printf '%s\n' "$also" | tr '|' '\n' | sed 's/^/  /')"
    NOTES="same content, also at:$NL$alsolist$NL"
  fi
  mine="$(printf '%s' "$MINE" | awk -F'\t' -v f="$f" '$1==f {print $2; exit}')"
  if [ -n "$mine" ]; then
    NOTES="${NOTES}NOTE: the copy in this session's work tree is different and was NOT checked: $mine$NL"
  fi
  older="$(printf '%s' "$OLDER" | awk -F'\t' -v f="$f" '$1==f {print $2; exit}')"
  if [ -n "$older" ] && [ "$older" -gt 0 ] 2>/dev/null; then
    NOTES="${NOTES}NOTE: $older other work tree(s) hold a copy they did not change; this is the copy being worked on.$NL"
  fi
  if [ "$(git -C "$pd" rev-parse --is-inside-work-tree 2>/dev/null)" != "true" ]; then
    NOTES="${NOTES}NOTE: this plan lives outside git; ADR-0015 says a plan belongs in a git repo the team can clone.$NL"
  fi
  OUT="${OUT}
=== plan validate: ${f} (exit ${STATUS}) ===
${NOTES}${RESULT}"
done <<EOF_V
$PLAN_BINS
EOF_V

emit "[PLAN GATE — automatic plan validate result]${OUT}

If any result above shows 'RESULT: FAIL' (or a non-zero exit), you MUST NOT execute that plan (no ralph/goal/task execution). Report the failures to the user and stop. Only a plan with 'RESULT: PASS' may be executed."
