---
name: ezh
description: Run ezHarness evidence checks — worker proof (check) and independent verifier (verify/replay) — and explain the git evidence gate. Use before committing or closing a task.
---

# ezh — evidence gate

ezHarness enforces "done means done" at the git boundary. A commit/push is blocked until an
**independent verifier** verdict covers the current contract evidence. Read
`.harness/project-contract.yaml` for this repo's `build.verify` and `env_rules`.

## Fast path: one command

`git gate [risk]` runs the worker check then the independent verifier in one step (resolves the
repo's `.harness` even from inside a linked worktree). Stage your change first:

```bash
git add <files>
git gate            # or: git gate med
git commit -m "..."
```

This collapses worker+verifier into a single actor — fine for a solo/fast commit. For true
independence on high-risk work, run `check` and `verify` as separate actors/sessions (below).

## Worker: produce proof

Run after implementation, before asking to close/commit:

```bash
.harness/bin/ezh-evidence check --contract .harness/project-contract.yaml --risk <risk> --json
```

Runs the contract's real verify commands + executable `env_rules` asserts and appends a worker
proof (`actor_role: worker`) to `.harness/evidence/ledger.jsonl`. Worker evidence alone does NOT
pass the gate.

## Verifier: independent replay

Must run as a different actor/session than the worker (no self-approval):

```bash
.harness/bin/ezh-evidence verify .harness/evidence/ledger.jsonl --json
.harness/bin/ezh-evidence replay .harness/evidence/ledger.jsonl --require-verifier --json
```

`verify` replays the worker proof and records a verdict (`actor_role: verifier`). `replay
--require-verifier` is exactly what `pre-commit`/`pre-push` call: it passes only when the latest
verifier verdict is `pass` and covers the current ledger digest.

## When the gate blocks

Missing contract, missing binary, worker-only ledger, proof mismatch, or a stale verifier verdict
all exit `2`. Fix the underlying check or run the missing verifier pass — do not bypass the gate.

## Define verify (if undefined)

If `build.verify` is `EZH_VERIFY_UNDEFINED`, ezHarness could not auto-detect a real verify for this
repo and the gate will BLOCK risk-path commits until one is defined. Do NOT write a placeholder that
always passes. Instead:

1. Read this repo's CI (`.gitlab-ci.yml`, pipeline configs) and build files (`Makefile`, `go.mod`,
   `Cargo.toml`, `package.json`) — the CI is the source of truth for what "valid" means here.
2. Define a canonical `verify` target in the `Makefile` running the repo's real validation
   (lint/build/test/render), runnable OFFLINE, matching what CI runs.
3. Set `build.verify: "make verify"` in `.harness/project-contract.yaml`.
4. Run `make verify` on a clean tree — it must pass before you rely on it.
5. Keep the Makefile target, CI, and the gate pointing at the SAME command (single source of truth).
6. Request human review — `verify` defines "done"; it must be correct, not invented.
