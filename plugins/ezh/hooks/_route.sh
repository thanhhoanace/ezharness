#!/usr/bin/env bash
# _route.sh — per-session cache for the ezh plugin hooks (ADR-0013, ADR-0014).
#
# A Claude session may open at a workspace root, in a repo, or deep inside one,
# so hooks keep a small per-session cache instead of trusting the session dir
# (which lesson goes where is decided per lesson, see _owners.sh). The cache is one
# directory per session (start time, vaults touched, turn markers); it is a
# lookup aid, not shared state: nothing reads another session's directory.
#
#   ezh_session_start <sid>         record the start time once
#   ezh_session_started <sid>       print the start time; exit 1 if unknown
#   ezh_session_cwd <sid> [cwd]     record the session's first cwd once; print it
#   ezh_touch <sid> <vault-ws>      remember a vault this session touched
#   ezh_touched <sid>               print the touched vaults, one per line
#   ezh_memory_before <sid> <file>  PreToolUse: record whether the file exists now
#   ezh_memory_was <sid> <file>     print that record (exists|new) and drop it; exit 1 if none
#   ezh_own_memory <sid> <file>     remember a memory file this session created
#   ezh_owns_memory <sid> <file>    exit 0 when the session already owns that file
#   ezh_disown_memory <sid> <file>  forget it (it left memory)
#   ezh_owned_memory <sid>          print the owned files, one per line
#   ezh_once <sid> <event> <key>    exit 0 the first time, 1 after (per turn)
#   ezh_seen <sid> <event> <key>    exit 0 if that key was already recorded
#   ezh_session_end <sid>           drop the session's cache
#
# Cache dir: $EZH_ROUTE_CACHE, else $CLAUDE_PLUGIN_DATA/sessions, else
# ${XDG_CACHE_HOME:-$HOME/.cache}/ezh/sessions. bash 3.2 compatible.

# python through _platform.sh when the caller sourced it (ADR-0016), else python3.
type ezh_py >/dev/null 2>&1 || ezh_py() { python3 "$@"; }

ezh_cache_root() {
  if [ -n "${EZH_ROUTE_CACHE:-}" ]; then
    printf '%s' "$EZH_ROUTE_CACHE"
  elif [ -n "${CLAUDE_PLUGIN_DATA:-}" ]; then
    printf '%s/sessions' "$CLAUDE_PLUGIN_DATA"
  else
    printf '%s/ezh/sessions' "${XDG_CACHE_HOME:-$HOME/.cache}"
  fi
}

ezh_session_dir() {
  # Session ids come from Claude Code; refuse anything that could leave the cache.
  case "$1" in
    ''|*/*|.|.[.]) return 1 ;;
  esac
  printf '%s/%s' "$(ezh_cache_root)" "$1"
}

ezh_session_start() {
  local dir
  dir="$(ezh_session_dir "$1")" || return 0
  mkdir -p "$dir" 2>/dev/null || return 0
  [ -f "$dir/start" ] || ezh_py -c 'import time; print(time.time())' > "$dir/start" 2>/dev/null
  # Sessions that never sent SessionEnd: drop caches older than a day.
  find "$(ezh_cache_root)" -mindepth 1 -maxdepth 1 -type d -mtime +0 -exec rm -rf {} + 2>/dev/null || true
}

ezh_session_cwd() {
  # ezh_session_cwd <sid> [cwd]: record the cwd once (at SessionStart); print it.
  local dir
  dir="$(ezh_session_dir "$1")" || return 1
  if [ -n "${2:-}" ] && [ ! -s "$dir/cwd" ]; then
    mkdir -p "$dir" 2>/dev/null && printf '%s\n' "$2" > "$dir/cwd"
  fi
  [ -s "$dir/cwd" ] && cat "$dir/cwd"
}

ezh_session_started() {
  local dir
  dir="$(ezh_session_dir "$1")" || return 1
  [ -s "$dir/start" ] || return 1
  cat "$dir/start"
}

ezh_touch() {
  local dir
  dir="$(ezh_session_dir "$1")" || return 0
  mkdir -p "$dir" 2>/dev/null || return 0
  touch "$dir"   # the day-old sweep looks at the folder's mtime
  grep -qxF -- "$2" "$dir/touched" 2>/dev/null || printf '%s\n' "$2" >> "$dir/touched"
}

ezh_own_memory() {
  local dir
  dir="$(ezh_session_dir "$1")" || return 0
  mkdir -p "$dir" 2>/dev/null || return 0
  touch "$dir"
  grep -qxF -- "$2" "$dir/memory" 2>/dev/null || printf '%s\n' "$2" >> "$dir/memory"
}

ezh_memory_before() {
  # Latest record wins: a Write that was denied never reaches PostToolUse, so an
  # older "new" must not outlive the next edit of that path.
  local dir state=new
  dir="$(ezh_session_dir "$1")" || return 0
  mkdir -p "$dir" 2>/dev/null || return 0
  touch "$dir"
  [ -e "$2" ] && state=exists
  { [ -f "$dir/pre-memory" ] && P="$2" awk -F '\t' '$2 != ENVIRON["P"]' "$dir/pre-memory"
    printf '%s\t%s\n' "$state" "$2"; } > "$dir/pre-memory.$$" && mv "$dir/pre-memory.$$" "$dir/pre-memory"
}

ezh_memory_was() {
  # One PreToolUse record serves one PostToolUse: it is dropped once read.
  local dir state
  dir="$(ezh_session_dir "$1")" || return 1
  [ -f "$dir/pre-memory" ] || return 1
  state="$(P="$2" awk -F '\t' '$2 == ENVIRON["P"] { s = $1 } END { print s }' "$dir/pre-memory")"
  P="$2" awk -F '\t' '$2 != ENVIRON["P"]' "$dir/pre-memory" > "$dir/pre-memory.$$" && mv "$dir/pre-memory.$$" "$dir/pre-memory"
  [ -n "$state" ] || return 1
  printf '%s\n' "$state"
}

ezh_owns_memory() {
  local dir
  dir="$(ezh_session_dir "$1")" || return 1
  grep -qxF -- "$2" "$dir/memory" 2>/dev/null
}

ezh_disown_memory() {
  local dir
  dir="$(ezh_session_dir "$1")" || return 0
  [ -f "$dir/memory" ] || return 0
  grep -vxF -- "$2" "$dir/memory" > "$dir/memory.$$"
  mv "$dir/memory.$$" "$dir/memory"
}

ezh_owned_memory() {
  local dir
  dir="$(ezh_session_dir "$1")" || return 0
  [ -f "$dir/memory" ] && cat "$dir/memory"
  return 0
}

ezh_is_memory_file() {
  # A Claude auto-memory file: <config>/projects/<slug>/memory/<name>.md
  local config="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
  case "$1" in
    "$config"/projects/*/memory/MEMORY.md) return 1 ;;
    "$config"/projects/*/memory/*.md) return 0 ;;
  esac
  return 1
}

ezh_touched() {
  local dir
  dir="$(ezh_session_dir "$1")" || return 0
  [ -f "$dir/touched" ] && cat "$dir/touched"
  return 0
}

ezh_once() {
  local dir mark
  dir="$(ezh_session_dir "$1")" || return 0
  mkdir -p "$dir" 2>/dev/null || return 0
  touch "$dir"
  mark="$2:$3"
  grep -qxF -- "$mark" "$dir/once" 2>/dev/null && return 1
  printf '%s\n' "$mark" >> "$dir/once"
  return 0
}

ezh_seen() {
  # ezh_seen <sid> <event> <key>: exit 0 if ezh_once already recorded it.
  local dir
  dir="$(ezh_session_dir "$1")" || return 1
  grep -qxF -- "$2:$3" "$dir/once" 2>/dev/null
}

ezh_session_end() {
  local dir shared
  dir="$(ezh_session_dir "$1")" || return 0
  rm -rf "$dir"
  # The plan gate keeps its per-turn keys under the XDG cache, so that the plugin
  # hook and a project hook see the same keys; clean that folder too.
  shared="${XDG_CACHE_HOME:-$HOME/.cache}/ezh/sessions/$1"
  [ "$shared" != "$dir" ] && rm -rf "$shared"
  return 0
}
