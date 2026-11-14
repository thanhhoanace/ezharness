# _platform.sh — what differs between macOS, Linux and Windows/Git Bash (ADR-0016).
# Sourced by the hooks; bash 3.2 compatible; defines functions and a few variables only.
#
#   ezh_os_arch            print <os>-<arch> (darwin|linux|windows x amd64|arm64); exit 1 if unknown
#   ezh_exe_suffix         print ".exe" on Windows, nothing elsewhere
#   ezh_has_py             exit 0 when a working python 3 was found
#   ezh_py ARGS...         run that python: the first of python3, python, `py -3` that runs
#                          (Windows ships a python3 stub that only opens the Store, so `command -v`
#                          is not enough); exit 127 with a message when there is none
#   ezh_path P             print P in the one form bash, python and native tools all read:
#                          C:/x/y on Windows (from C:\x\y or /c/x/y), unchanged elsewhere
#   ezh_plugin_bin ROOT    print a runnable ez-evidence for this machine from ROOT/libexec; when the
#                          copy lost its exec bit, copy it to the plugin data folder and chmod it
#
# On Windows, sourcing this file also makes `pwd` print C:/ paths and HOME use that form, so every
# path a hook builds can be handed to python or ez-evidence.exe unchanged.
# Tests may set EZH_UNAME_S / EZH_UNAME_M to pretend another machine.

EZH_UNAME_S="${EZH_UNAME_S:-$(uname -s 2>/dev/null || echo unknown)}"
EZH_UNAME_M="${EZH_UNAME_M:-$(uname -m 2>/dev/null || echo unknown)}"
case "$EZH_UNAME_S" in
  Darwin) EZH_OS=darwin ;;
  Linux) EZH_OS=linux ;;
  MINGW*|MSYS*|CYGWIN*|Windows_NT) EZH_OS=windows ;;
  *) EZH_OS="" ;;
esac
case "$EZH_UNAME_M" in
  x86_64|amd64|AMD64) EZH_ARCH=amd64 ;;
  arm64|aarch64|ARM64) EZH_ARCH=arm64 ;;
  *) EZH_ARCH="" ;;
esac

ezh_os_arch() {
  [ -n "$EZH_OS" ] && [ -n "$EZH_ARCH" ] || return 1
  printf '%s-%s\n' "$EZH_OS" "$EZH_ARCH"
}

ezh_exe_suffix() {
  [ "$EZH_OS" = windows ] && printf '.exe'
  return 0
}

ezh_path() {
  case "$1" in
    [A-Za-z]:\\*|[A-Za-z]:/*) ;;
    *) [ "$EZH_OS" = windows ] || { printf '%s\n' "$1"; return 0; } ;;
  esac
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -m "$1"
  else
    printf '%s\n' "$1" | tr '\\' '/'
  fi
}

ezh__py_pick() {
  # Probe once per process; the result is inherited by subshells.
  [ "${EZH_PY_PICKED:-}" = 1 ] && return 0
  EZH_PY_PICKED=1
  EZH_PY_CMD=""
  local c
  for c in python3 python "py -3"; do
    command -v "${c%% *}" >/dev/null 2>&1 || continue
    # shellcheck disable=SC2086 # "py -3" is two words on purpose
    if $c -c 'import sys; sys.exit(0 if sys.version_info >= (3, 6) else 1)' </dev/null >/dev/null 2>&1; then
      EZH_PY_CMD="$c"
      return 0
    fi
  done
  return 0
}

ezh_has_py() {
  ezh__py_pick
  [ -n "$EZH_PY_CMD" ]
}

ezh_py() {
  ezh__py_pick
  if [ -z "$EZH_PY_CMD" ]; then
    echo "ezh: no working python 3 found (tried python3, python, py -3)" >&2
    return 127
  fi
  # shellcheck disable=SC2086
  $EZH_PY_CMD "$@"
}

ezh_plugin_bin() {
  local root="$1" plat name src data dst tmp
  [ -n "$root" ] || return 1
  plat="$(ezh_os_arch)" || return 1
  name="ez-evidence-$plat$(ezh_exe_suffix)"
  src="$root/libexec/$name"
  [ -f "$src" ] || return 1
  if [ -x "$src" ]; then
    printf '%s\n' "$src"
    return 0
  fi
  data="${CLAUDE_PLUGIN_DATA:-${XDG_DATA_HOME:-$HOME/.local/share}/ezh}"
  dst="$data/bin/$name"
  if [ -x "$dst" ] && cmp -s "$src" "$dst"; then
    printf '%s\n' "$dst"
    return 0
  fi
  tmp="$dst.tmp.$$"
  if mkdir -p "$data/bin" && cp "$src" "$tmp" && chmod +x "$tmp" && mv -f "$tmp" "$dst"; then
    printf '%s\n' "$dst"
    return 0
  fi
  rm -f "$tmp"
  echo "ezh: cannot make $src runnable (copy to $dst failed)" >&2
  return 1
}

if [ "$EZH_OS" = windows ] && command -v cygpath >/dev/null 2>&1; then
  pwd() { cygpath -m "$(builtin pwd "$@")"; }
  HOME="$(cygpath -m "$HOME")"
  export HOME
  [ -n "${XDG_DATA_HOME:-}" ] && XDG_DATA_HOME="$(cygpath -m "$XDG_DATA_HOME")" && export XDG_DATA_HOME
  [ -n "${XDG_CONFIG_HOME:-}" ] && XDG_CONFIG_HOME="$(cygpath -m "$XDG_CONFIG_HOME")" && export XDG_CONFIG_HOME
  [ -n "${CLAUDE_PLUGIN_DATA:-}" ] && CLAUDE_PLUGIN_DATA="$(cygpath -m "$CLAUDE_PLUGIN_DATA")" && export CLAUDE_PLUGIN_DATA
fi
ezh__py_pick
