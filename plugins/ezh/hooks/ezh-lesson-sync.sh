#!/usr/bin/env bash
# ezh-lesson-sync.sh — Claude Code Stop hook (v6.5, ADR-0009).
#
# WHY: Claude Code already writes auto memory on its own, but that memory is
# machine-local, and agents never go and read the lessons vault by themselves
# (measured 2026-09-23: 0 vault reads in 215 real sessions). So the machine
# does the part nobody remembers to do: at the end of every turn it MOVES
# project knowledge out of Claude memory into the shared vault and regenerates
# the one-line-per-lesson index that CLAUDE.md imports at session start.
#
# WHAT, per memory file (*.md except MEMORY.md) changed since the last run:
#   type project|reference -> write .harness/lessons/<name>.md (append under a
#                             dated heading when the lesson already exists, so a
#                             team edit is never overwritten), keep a timestamped
#                             copy in .harness/.lesson-sync-moved/, then delete
#                             the memory file and its MEMORY.md line. Move, not
#                             copy: one lesson lives in one place and loads once.
#   type feedback|user     -> stays private; reported once.
#   anything the vault cannot hold as a lesson (no name/description/type, a
#   non-kebab-case or reserved name, a description under 20 characters) stays
#   in memory and is reported.
#   PRIVATE text stays in memory and is reported: built-in patterns (/Volumes/
#   and /Users/<name>/ paths, 192.168.x.x and 172.16-31.x.x addresses) plus every
#   line of .harness/lesson-sync.deny (machine-local, gitignored; plain text or
#   re:<regex>, case-insensitive). Claude's `type: project` means "ongoing work
#   or goal", not "belongs to this repo" — a personal goal is type project too
#   (incident 2026-09-30), so type alone never decides what is shared. A vault
#   lesson that matches is reported as well. An unreadable deny list or a bad
#   regex moves nothing (fail closed).
# Then .harness/lessons/LESSONS.md is regenerated from every lesson's
# frontmatter. Every write is atomic (temp file + rename). A memory file is
# deleted only after its lesson and its backup were written and read back.
#
# CONTRACT: never blocks the turn — always exit 0. Silent when nothing changed.
# Anything it did or could not do is reported as {"systemMessage": ...} JSON on
# stdout, which Claude Code shows to the user.
#
# Memory dir: $EZH_MEMORY_DIR when set; otherwise the Claude project dir
# <config>/projects/<slug>/memory, where <slug> is the git main-worktree root of
# cwd with every non-alphanumeric char turned into "-", then
# <dir of transcript_path>/memory. If autoMemoryDirectory is set anywhere
# (user settings, or the workspace's .claude/settings*.json) and EZH_MEMORY_DIR
# is not, the hook moves NOTHING and says so once: a custom folder may be shared
# by every project, and a committed settings file must never be able to point
# this hook at another project's memory.
#
#   ezh-lesson-sync.sh            Stop hook mode (reads the hook JSON on stdin)
#   ezh-lesson-sync.sh --reindex  regenerate LESSONS.md only (used by install.sh)
set -uo pipefail

HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# Platform helpers (ADR-0016); an installed copy may stand alone with python3 only.
# shellcheck source=/dev/null
if [ -f "$HOOK_DIR/_platform.sh" ]; then
  . "$HOOK_DIR/_platform.sh"
  HOOK_DIR="$(cd "$HOOK_DIR" && pwd -P)"
else
  ezh_has_py() { command -v python3 >/dev/null 2>&1; }
  ezh_py() { python3 "$@"; }
fi
# EZH_WS: the vault to write (the lesson-inbox loop's --apply worktree, or a caller
# that names one); without it the hook serves the workspace it was installed into.
# ezh-route.sh never sets it: the plugin route always writes the inbox (ADR-0015 #6).
WS="${EZH_WS:-$(cd "$HOOK_DIR/../.." && pwd -P)}"
HARNESS="$WS/.harness"
LESSONS="$HARNESS/lessons"
STAMP="$HARNESS/.lesson-sync.stamp"
LOCK="$HARNESS/.lesson-sync.lock"
# EZH_INBOX (ADR-0014): no repo owns the lesson — write it to the machine inbox
# instead of a vault, through the same filters. No index, no vault checks.
if [ -n "${EZH_INBOX:-}" ]; then
  mkdir -p "$EZH_INBOX" 2>/dev/null || true
  HARNESS="$EZH_INBOX"
  LESSONS="$EZH_INBOX"
  STAMP="$EZH_INBOX/.lesson-sync.stamp"
  LOCK="$EZH_INBOX/.lesson-sync.lock"
fi

emit() {
  # emit <message> — the only way this hook talks: one systemMessage JSON line.
  ezh_py -c 'import json,sys; print(json.dumps({"systemMessage": sys.argv[1]}))' "$1" 2>/dev/null || true
}

if ! ezh_has_py; then
  printf '{"systemMessage":"ezHarness lesson sync failed: no working python 3 (tried python3, python, py -3)"}\n'
  exit 0
fi

mode="sync"
if [ "${1:-}" = "--reindex" ]; then
  mode="reindex"
fi

input=""
if [ "$mode" = "sync" ] && [ ! -t 0 ]; then
  input="$(cat)"
fi

json_field() {
  printf '%s' "$input" | ezh_py -c 'import json,sys
try: print(json.load(sys.stdin).get(sys.argv[1]) or "")
except Exception: print("")' "$1" 2>/dev/null
}

resolve_memory_dir() {
  local config_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
  local cwd transcript candidate common root slug
  if [ -n "${EZH_MEMORY_DIR:-}" ]; then
    printf '%s\n' "$EZH_MEMORY_DIR"
    return 0
  fi
  candidate="$(ezh_py - "$config_dir/settings.json" "$WS/.claude/settings.local.json" "$WS/.claude/settings.json" <<'PY' 2>/dev/null
import json, sys
for path in sys.argv[1:]:
    try:
        if json.load(open(path)).get("autoMemoryDirectory"):
            print(path)
            break
    except Exception:
        pass
PY
)"
  if [ -n "$candidate" ]; then
    printf 'CUSTOM:%s\n' "$candidate"
    return 0
  fi
  cwd="$(json_field cwd)"
  transcript="$(json_field transcript_path)"
  if type ezh_path >/dev/null 2>&1; then
    [ -n "$cwd" ] && cwd="$(ezh_path "$cwd")"
    [ -n "$transcript" ] && transcript="$(ezh_path "$transcript")"
  fi
  [ -n "$cwd" ] || cwd="${CLAUDE_PROJECT_DIR:-$WS}"
  root="$cwd"
  common="$(git -C "$cwd" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
  if [ -n "$common" ]; then
    root="$(cd "$common/.." 2>/dev/null && pwd -P || printf '%s' "$cwd")"
  fi
  for candidate in "$root" "$cwd"; do
    slug="$(printf '%s' "$candidate" | sed 's/[^A-Za-z0-9]/-/g')"
    if [ -d "$config_dir/projects/$slug/memory" ]; then
      printf '%s\n' "$config_dir/projects/$slug/memory"
      return 0
    fi
  done
  if [ -n "$transcript" ] && [ -d "$(dirname "$transcript")/memory" ]; then
    printf '%s\n' "$(dirname "$transcript")/memory"
    return 0
  fi
  return 1
}

memdir=""
if [ "$mode" = "sync" ]; then
  memdir="$(resolve_memory_dir)" || exit 0
  case "$memdir" in
    CUSTOM:*)
      notice="$HARNESS/.lesson-sync.custom-dir-reported"
      if [ -d "$HARNESS" ] && [ ! -e "$notice" ]; then
        : > "$notice" 2>/dev/null || true
        emit "ezHarness lesson sync is OFF: autoMemoryDirectory is set in ${memdir#CUSTOM:}. A custom memory folder may hold other projects' memories, so nothing is moved. Set EZH_MEMORY_DIR (settings.json \"env\") to this project's memory folder to turn it on."
      fi
      exit 0
      ;;
  esac
  [ -d "$memdir" ] || exit 0
  # Fast path: no memory file changed since the last successful run. Not on the
  # plugin route: one vault there serves many memory folders (one per session
  # folder), so the vault stamp says nothing about this session's folder.
  if [ -z "${EZH_SINCE:-}" ] && [ -f "$STAMP" ] && [ -z "$(find "$memdir" -maxdepth 1 -type f -name '*.md' ! -name 'MEMORY.md' -newer "$STAMP" -print 2>/dev/null | head -n 1)" ]; then
    exit 0
  fi
fi

if [ ! -d "$HARNESS" ]; then
  emit "ezHarness lesson sync failed: $HARNESS does not exist (reinstall with install.sh)"
  exit 0
fi

# One run at a time; a lock older than 2 minutes is a crashed run.
if [ -d "$LOCK" ] && [ -n "$(find "$LOCK" -maxdepth 0 -mmin +2 2>/dev/null)" ]; then
  rmdir "$LOCK" 2>/dev/null || true
fi
if ! mkdir "$LOCK" 2>/dev/null; then
  # Another sync holds this vault or inbox: say so, so the router retries next turn.
  emit "ezHarness lesson sync failed, memory kept for the next turn: $LESSONS is busy (another sync runs)"
  exit 0
fi
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT

# The stamp marks the START of the last successful sync run. --reindex never
# touches it, so memories older than the install are still moved on the first Stop.
started="$STAMP.new"
if [ "$mode" = "sync" ]; then
  : > "$started" 2>/dev/null || true
else
  rm -f "$started" 2>/dev/null || true
fi

result="$(ezh_py - "$mode" "$memdir" "$LESSONS" <<'PY'
import datetime
import json
import os
import re
import sys
from pathlib import Path

mode, memdir_raw, lessons_raw = sys.argv[1:4]
lessons = Path(lessons_raw)
inbox_mode = bool(os.environ.get("EZH_INBOX"))
moved_dir = (lessons / ".moved") if inbox_mode else (lessons.parent / ".lesson-sync-moved")
NAME_RE = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
SHARED_TYPES = {"project", "reference"}
PRIVATE_TYPES = {"feedback", "user"}
# Vault files that are not lessons. Compared case-insensitively: on a
# case-insensitive disk a memory named "lessons" IS the index file.
RESERVED = {"readme", "lessons"}
MIN_DESCRIPTION = 20  # same floor as `ez-evidence lesson lint`
INLINE_BEGIN = "<!-- ezh-lessons:begin (generated by ezHarness lesson sync; do not edit) -->"
INLINE_END = "<!-- ezh-lessons:end -->"
# The inbox belongs to no repo: it uses the machine's own deny list.
DENY_FILE = (Path(os.environ.get("XDG_CONFIG_HOME") or Path.home() / ".config") / "ezh" / "lesson-sync.deny") \
    if inbox_mode else (lessons.parent / "lesson-sync.deny")
BUILTIN_PRIVATE = [
    ("a /Volumes/ path", r"/Volumes/"),
    ("a /Users/<name>/ path", r"/Users/[^/\s]+/"),
    ("a 192.168.x.x address", r"\b192\.168\.\d{1,3}\.\d{1,3}\b"),
    ("a 172.16-31.x.x address", r"\b172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}\b"),
]


def load_private_patterns():
    """Built-ins plus the workspace deny list. Raises ValueError on a bad line."""
    patterns = [(label, re.compile(rx, re.IGNORECASE)) for label, rx in BUILTIN_PRIVATE]
    if DENY_FILE.exists():
        for number, raw in enumerate(DENY_FILE.read_text().splitlines(), start=1):
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            if line.startswith("re:"):
                try:
                    patterns.append((f"deny line {number}", re.compile(line[3:], re.IGNORECASE)))
                except re.error as exc:
                    raise ValueError(f"lesson-sync.deny line {number}: bad regex ({exc})")
            else:
                patterns.append((f"deny line {number}", re.compile(re.escape(line), re.IGNORECASE)))
    return patterns


def private_match(text, patterns):
    for label, rx in patterns:
        if rx.search(text):
            return label
    return ""


def unquote(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] == '"':
        return value[1:-1].replace('\\"', '"').replace("\\\\", "\\")
    if len(value) >= 2 and value[0] == value[-1] == "'":
        return value[1:-1].replace("''", "'")
    return value


def split_frontmatter(text):
    """Return (fields, body). name/description/type are read at top level;
    a type nested under metadata: (how Claude Code writes it) is the fallback."""
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        return None, text
    fields, nested_type = {}, ""
    for index, raw in enumerate(lines[1:], start=1):
        if raw.strip() == "---":
            if "type" not in fields and nested_type:
                fields["type"] = nested_type
            return fields, "\n".join(lines[index + 1:])
        if not raw.strip() or raw.lstrip().startswith("#") or ":" not in raw:
            continue
        key, value = raw.split(":", 1)
        key = key.strip()
        if raw[:1] in (" ", "\t"):
            if key == "type" and not nested_type:
                nested_type = unquote(value).lower()
            continue
        if key in ("name", "description"):
            fields[key] = unquote(value)
        elif key == "type":
            fields["type"] = unquote(value).lower()
    return None, text


def atomic_write(path, text):
    tmp = path.with_name(f".{path.name}.tmp")
    tmp.write_text(text)
    os.replace(tmp, path)
    if path.read_text(errors="replace") != text:
        raise OSError(f"read-back mismatch on {path.name}")


def regenerate_index():
    entries = []
    for path in sorted(lessons.glob("*.md")):
        if path.stem.lower() in RESERVED or path.name.startswith(("_", ".")):
            continue
        fields, _ = split_frontmatter(path.read_text(errors="replace"))
        fields = fields or {}
        name = fields.get("name") or path.stem
        description = " ".join((fields.get("description") or "").split())
        line = f"- [{name}]({path.name})"
        if description:
            line += f" — {description}"
        entries.append(line)
    header = [
        "# Project lessons",
        "",
        "<!-- Generated by ezHarness (.harness/hooks/ezh-lesson-sync.sh) from the",
        "     frontmatter of every .harness/lessons/*.md. Do not edit: edit the lesson",
        "     file instead; this index is rewritten at the end of the next Claude turn. -->",
        "",
        "What this team learned the hard way. One line per lesson. Open",
        "`.harness/lessons/<name>.md` when the work touches that topic.",
        "",
    ]
    text = "\n".join(header + (entries or ["(no lessons yet)"])) + "\n"
    index = lessons / "LESSONS.md"
    if not index.exists() or index.read_text(errors="replace") != text:
        atomic_write(index, text)
    # Plugin route (ADR-0013): a session opened in a sub-folder reads this
    # folder's CLAUDE.md but not what it @-imports from outside its own folder,
    # so the index is also written inline between the ezh-lessons markers.
    # Claude reads CLAUDE.md, Codex reads AGENTS.md (root → cwd, no imports):
    # the same block goes into each root file that carries the markers (ADR-0014).
    for claude_md in (lessons.parent.parent / "CLAUDE.md", lessons.parent.parent / "AGENTS.md"):
        if not claude_md.is_file():
            continue
        current = claude_md.read_text(errors="replace")
        begin, end = INLINE_BEGIN, INLINE_END
        if begin in current and end in current.split(begin, 1)[1]:
            head, rest = current.split(begin, 1)
            tail = rest.split(end, 1)[1]
            body = ["", "## Lessons (ezHarness)", "",
                    "Lesson files live in `.harness/lessons/` next to this file. "
                    "Open `.harness/lessons/<name>.md` when the work touches that topic.", ""]
            body += [e.replace("](", "](.harness/lessons/", 1) for e in entries] or ["(no lessons yet)"]
            updated = head + begin + "\n".join(body) + "\n" + end + tail
            if updated != current:
                atomic_write(claude_md, updated)


def promote(text, name, body, stamp_now):
    """Write the lesson and its backup; raise before memory is touched."""
    dest = lessons / f"{name}.md"
    if dest.exists():
        current = dest.read_text(errors="replace")
        new_body = body.strip()
        if new_body and new_body not in current:
            today = datetime.date.today().isoformat()
            atomic_write(dest, current.rstrip("\n") + f"\n\n## Update {today} (from Claude memory)\n\n{new_body}\n")
    else:
        atomic_write(dest, text if text.endswith("\n") else text + "\n")
    # Safety net for the human review: if the lesson is deleted in `git diff`,
    # the original memory text is still here (gitignored, never overwritten).
    moved_dir.mkdir(parents=True, exist_ok=True)
    atomic_write(moved_dir / f"{name}.{stamp_now}.md", text if text.endswith("\n") else text + "\n")


def promote_inbox(text, name, stamp_now):
    """Inbox copy + hints + backup, each read back; raise before memory is touched.
    An existing name gets a numbered suffix: the inbox never merges two lessons."""
    data = text if text.endswith("\n") else text + "\n"
    target, n = lessons / f"{name}.md", 2
    while target.exists() or target.with_suffix(".json").exists():
        target, n = lessons / f"{name}-{n}.md", n + 1
    atomic_write(target, data)
    hints = os.environ.get("EZH_INBOX_HINTS") or "{}"
    atomic_write(target.with_suffix(".json"), hints if hints.endswith("\n") else hints + "\n")
    moved_dir.mkdir(parents=True, exist_ok=True)
    atomic_write(moved_dir / f"{name}.{stamp_now}.md", data)
    return target.stem


def main():
    lessons.mkdir(parents=True, exist_ok=True)
    if mode == "reindex":
        regenerate_index()
        return {}
    memdir = Path(memdir_raw)
    stamp = lessons.parent / ".lesson-sync.stamp"
    since = stamp.stat().st_mtime if stamp.exists() else None
    # EZH_SINCE (plugin route): only memories changed during this session move.
    try:
        session_since = float(os.environ.get("EZH_SINCE", ""))
    except ValueError:
        session_since = None
    # EZH_ONLY_FILES (plugin route): the memory files this session wrote.
    only_raw = os.environ.get("EZH_ONLY_FILES")
    only_files = None if only_raw is None else {n for n in only_raw.splitlines() if n}
    if session_since is not None:
        # Plugin route: the session start alone decides. Moved memories leave the
        # folder, so nothing moves twice without the vault stamp.
        since = session_since
    stamp_now = datetime.datetime.now().strftime("%Y%m%dT%H%M%S%f")
    moved, private, skipped, failed, removed_files = [], [], [], [], []
    blocked, vault_flags = [], []
    try:
        patterns = load_private_patterns()
    except (OSError, ValueError) as exc:
        # Fail closed: without a working filter nothing may leave memory.
        return {"failed": [f"private-word filter unusable, nothing moved: {exc}"]}
    for path in sorted(memdir.glob("*.md")):
        try:
            if path.name == "MEMORY.md" or not path.is_file():
                continue
            if since is not None and path.stat().st_mtime <= since:
                continue
            if only_files is not None and path.name not in only_files:
                continue
            text = path.read_text(errors="replace")
            fields, body = split_frontmatter(text)
            fields = fields or {}
            name, kind = fields.get("name", ""), fields.get("type", "")
            description = (fields.get("description") or "").strip()
            if not name or not description or not kind:
                skipped.append(f"{path.name} (missing name/description/type)")
                continue
            if kind in PRIVATE_TYPES:
                private.append(name)
                continue
            if kind not in SHARED_TYPES:
                skipped.append(f"{name} (unknown type {kind!r})")
                continue
            if not NAME_RE.match(name) or name.lower() in RESERVED:
                skipped.append(f"{name} (name must be kebab-case and not 'lessons'/'readme')")
                continue
            if len(description) < MIN_DESCRIPTION:
                skipped.append(f"{name} (description under {MIN_DESCRIPTION} characters)")
                continue
            hit = private_match(text, patterns)
            if hit:
                blocked.append(f"{name} ({hit})")
                continue
            if inbox_mode:
                moved.append(promote_inbox(text, name, stamp_now))
            else:
                promote(text, name, body, stamp_now)
                moved.append(name)
            path.unlink()
            removed_files.append(path.name)
        except Exception as exc:  # one bad file never loses track of the others
            failed.append(f"{path.name} ({getattr(exc, 'strerror', None) or exc})")
    if removed_files:
        index = memdir / "MEMORY.md"
        try:
            if index.exists():
                lines = index.read_text(errors="replace").splitlines()
                kept = [l for l in lines if not any(f"]({f})" in l for f in removed_files)]
                if kept != lines:
                    atomic_write(index, "\n".join(kept) + ("\n" if kept else ""))
        except Exception as exc:
            failed.append(f"MEMORY.md ({getattr(exc, 'strerror', None) or exc}); remove the lines for "
                          + ", ".join(removed_files) + " by hand")
    if inbox_mode:
        return {"moved": moved, "private": private, "blocked": blocked, "skipped": skipped,
                "failed": failed, "vault_flags": [], "inbox": True}
    try:
        regenerate_index()
    except Exception as exc:
        failed.append(f"LESSONS.md ({getattr(exc, 'strerror', None) or exc})")
    for lesson in sorted(lessons.glob("*.md")):
        if lesson.stem.lower() in RESERVED or lesson.name.startswith(("_", ".")):
            continue
        try:
            hit = private_match(lesson.read_text(errors="replace"), patterns)
        except OSError:
            continue
        if hit:
            vault_flags.append(f"{lesson.stem} ({hit})")
    return {"moved": moved, "private": private, "blocked": blocked, "skipped": skipped,
            "failed": failed, "vault_flags": vault_flags}


try:
    print(json.dumps(main()))
except Exception as exc:  # the hook must never break the turn
    print(json.dumps({"failed": [f"{type(exc).__name__}: {exc}"]}))
PY
)"

ezh_py - "$result" "$started" "$STAMP" <<'PY'
import json
import os
import sys

raw, started, stamp = sys.argv[1:4]
try:
    r = json.loads(raw or "{}")
except Exception:
    r = {"failed": [f"unreadable sync result: {raw[:200]}"]}
failed = r.get("failed") or []
# Advance the stamp only when nothing failed, so a failed memory is retried.
if not failed and os.path.exists(started):
    os.replace(started, stamp)
elif os.path.exists(started):
    os.remove(started)
parts = []
if r.get("moved") and r.get("inbox"):
    # Plain words: where it went and, when one table matched, the repo it is proposed for.
    try:
        assigned = json.loads(os.environ.get("EZH_INBOX_HINTS") or "{}").get("assigned")
    except Exception:
        assigned = None
    where = f"moved {', '.join(r['moved'])} to the lesson inbox ({os.environ.get('EZH_INBOX', '')})"
    if assigned:
        parts.append(f"{where}, proposed for {assigned} by its lesson-owners table; "
                     f"it reaches the repo only after you approve the weekly lesson-inbox proposal")
    else:
        parts.append(f"{where}; no single repo's lesson-owners table matched, "
                     f"the weekly lesson-inbox loop proposes a home for you to approve")
elif r.get("moved"):
    parts.append(f"moved {len(r['moved'])} Claude memories into .harness/lessons/ "
                 f"({', '.join(r['moved'])}) — review them in git diff before you commit; "
                 f"originals are in .harness/.lesson-sync-moved/")
if r.get("private"):
    parts.append(f"kept private (type feedback/user): {', '.join(r['private'])}")
if r.get("blocked"):
    parts.append(f"kept private (private words — see .harness/lesson-sync.deny): {'; '.join(r['blocked'])}")
if r.get("vault_flags"):
    parts.append(f"ALREADY SHARED lessons that look private, remove or redact them before the next push: "
                 f"{'; '.join(r['vault_flags'])}")
if r.get("skipped"):
    parts.append(f"left in memory: {'; '.join(r['skipped'])}")
if failed:
    parts.append(f"failed, memory kept for the next turn: {'; '.join(failed)}")
if parts:
    print(json.dumps({"systemMessage": "ezHarness lesson sync: " + ". ".join(parts) + "."}))
PY
exit 0
