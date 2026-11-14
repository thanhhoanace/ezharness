---
name: ezh
description: ezHarness evidence gate for this repo — prove work is done (worker check → independent verify → git gate), look up and lint lessons, validate and save plans, and recover when a commit or push is BLOCKED. Use before committing or closing a task, when the gate blocks, when a symptom repeats, or when saving a plan.
---

# ezh — ezHarness evidence gate

## First, locate the gate, then ask the binary — never read source or docs to discover features

Your session may be open at a **workspace root** (not gated) or inside a **linked worktree**. The
gate belongs to the repository you are changing, so resolve it from there:

```bash
cd <the repo or worktree you are changing>
EZH="$(git rev-parse --git-common-dir)/.."       # main root of that repo — also from a linked worktree
test -f "$EZH/.harness/project-contract.yaml" || echo "this repository is not gated"   # a root may hold the binary for the plan gate only
"$EZH/.harness/bin/ez-evidence" capabilities     # JSON: exact verbs this install has (10 in v6.4)
"$EZH/.harness/bin/ez-evidence" --help           # exact syntax for every verb
```

Every command below is written relative to `$EZH`. The binary is the authority on *what exists*;
this file is only the *when* and *why*.

## What ezHarness is, in three lines

- A **gate at the git boundary**: `pre-commit` / `pre-push` refuse until an **independent verifier**
  has replayed evidence that this repo's real verify command passes on the current tree.
- **Not** a planner, runner, worktree orchestrator or knowledge base. Your host (Claude Code, Codex,
  OpenCode) owns planning and execution; ezHarness only makes "done" provable.
- Policy lives in `.harness/project-contract.yaml` (project-owned: `build.verify`, `risk_level`,
  `risk_paths`, `env_rules`, optional `loop:`). Evidence lives in `.harness/evidence/ledger.jsonl`.

## When to do what

| Situation | Do this | Detail |
|---|---|---|
| Implementation ready, about to commit | `git add …` → worker `check` → independent `verify` → commit | [reference/verbs.md](reference/verbs.md) |
| Solo / low-risk fast path | `git gate [risk]` (check + verify by the SAME actor — not independent) | verbs.md §attest |
| Commit or push was **BLOCKED** | Read the JSON reason on stderr, fix the cause, never bypass | [reference/troubleshooting.md](reference/troubleshooting.md) |
| Same error or symptom **twice** | `lesson lookup --changed <paths>` before the next attempt; `rg -i "<error>" .harness/lessons/` | [reference/lessons.md](reference/lessons.md) |
| Found a durable fix worth keeping | Write a lesson file → `lesson lint` → propose via PR (never self-merge) | lessons.md |
| Writing, updating or executing a plan | copy `.harness/plankit/PLAN-TEMPLATE.md`; `plan validate --file` must say `RESULT: PASS` before you execute (Claude Code checks it on every prompt) | [reference/plans.md](reference/plans.md) |
| Contract has `loop:` | Work on the task branch (its name is the task id) or export ONE stable `EZ_TASK_ID`; the hook runs `loopcheck` itself | troubleshooting.md §loop |
| `build.verify` is `EZH_VERIFY_UNDEFINED` | Define a real `make verify` from CI; never a placeholder that always passes | troubleshooting.md §verify |
| Working inside a linked worktree | Evidence lands in `$EZH/.harness` automatically, but pass `--repo-root "$(git rev-parse --show-toplevel)"` to `check`/`attest` so the proof binds to the worktree's tree | troubleshooting.md §worktree |
| Session open at the workspace root | `cd` into the repo/worktree first (above); the root itself has no gate, only this skill | — |

## Rules that never bend

1. The actor who ran `check` must not be the one who runs `verify` on high-risk work. No self-approval.
2. Exit code `2` from any verb means BLOCK. Fix the underlying cause; do not edit the ledger, skip
   hooks (`--no-verify`) or weaken the contract to get through.
3. `forbidden_paths` and exhausted loop budgets have **no unlock**. Escalate to the human.
4. Lessons and plans are human-reviewed Git changes. Propose; never auto-approve.
5. Never invent a verify command. `build.verify` defines "done" — it must match what CI runs.

## Reference (load only what you need)

- [reference/verbs.md](reference/verbs.md) — every verb: syntax, what it reads/writes, exit codes
- [reference/lessons.md](reference/lessons.md) — vault layout, lookup, lint, propose flow
- [reference/plans.md](reference/plans.md) — plankit rules, env-patterns, validate/save, the Claude plan gate hook
- [reference/troubleshooting.md](reference/troubleshooting.md) — BLOCK reasons → actions
