# Lessons vault — `.harness/lessons/`

The one Git-tracked folder under `.harness/`. A lesson is a reviewed, durable fix for a symptom
that has already cost someone time. Frontmatter **is** the index; there is no separate index file.

```text
.harness/lessons/
├── README.md              human guide (installer-seeded, never overwritten)
├── <name>.md              one lesson = one file
└── _fixtures/<case>.log   REAL error logs used as retrieval test cases
```

## When to look

- A failure or symptom **repeats** — before trying another fix, not on every task.
- The gate BLOCKs — the hook already prints the lessons matching the changed files.
- You are about to edit a path a lesson declares under `paths:` — the hook reminds you.

```bash
# by changed path (what the hooks do)
.harness/bin/ez-evidence lesson lookup --changed "src/a.go,src/b.go" --format json

# by symptom text (the primary manual path)
rg -i "<exact error string>" .harness/lessons/
```

Read the matched file: what the error is, the fix, the trap to avoid, how to verify.

## Lesson file shape (skills-compatible frontmatter)

```markdown
---
name: <kebab-case, equals the filename stem>
description: <one line containing the symptom VERBATIM — this is the retrieval key>
paths:                      # optional; repo-relative globs the lesson applies to
  - "src/payments/**"
---

## Symptom
## Root cause
## Fix
## Verify
```

## Lessons from Claude Code memory (v6.5)

With lesson sync installed, you do not write lessons by hand in Claude Code: save the fact to
auto memory as usual (`type: project` or `reference`). At the end of the turn the Stop hook moves
it into `.harness/lessons/<name>.md` and lists it in `.harness/lessons/LESSONS.md`, which CLAUDE.md
loads at session start. `feedback`/`user` memories stay private. The human reviews the move in
`git diff` before committing; the original text stays in `.harness/.lesson-sync-moved/`.

## Proposing a lesson (propose-only, human merges)

1. Write `.harness/lessons/<name>.md`; paste the real log into `_fixtures/<name>.log`.
2. `.harness/bin/ez-evidence lesson lint` — must print `lessons index OK`; exit `2` lists the
   problems (missing fields, name ≠ filename, vault gitignored, fixture without a matching lesson).
3. Commit on a branch and open a PR. **Never** merge your own lesson, never bulk-generate lessons.

`lesson lint` FAILS when the vault is gitignored — a lesson nobody can commit is not knowledge.
If a machine-wide `~/.gitignore_global` carries a bare `.harness` line it silently wins over the
repo's `!.harness/lessons/`; fix with a per-repo `core.excludesFile` or remove the global line.

## What the metrics mean

`lesson-test.sh` (in `.harness/hooks/`) feeds every `_fixtures/*.log` through lookup and reports
`index_conformance` (fixture ↔ frontmatter agreement) and `noise`. This measures the **index
structure**, not whether an agent actually recalls lessons — that claim needs a cold-start A/B.
