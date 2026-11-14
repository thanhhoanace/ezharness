# ez-evidence verbs

Binary: `.harness/bin/ez-evidence` (a `git gate` alias is configured by the installer).
Authoritative list: `ez-evidence capabilities` (protocol_version 1). Exit `0` = pass, `2` = BLOCK
(or usage error). Every gate invocation stamps one `run_id` on its ledger row, run-report row and
proof filename so the three artifacts of one run can be joined. Optional `EZ_TRACE_ID` /
`EZ_SPAN_ID` / `TRACEPARENT` are recorded when set.

`$EZH` below is the main root of the repository you are changing —
`EZH="$(git rev-parse --git-common-dir)/.."`, run from inside that repo or one of its linked
worktrees (see SKILL.md). Contract and ledger paths are relative to it. Inside a linked worktree
the binary itself resolves `.harness` the same way, so worktree and main root share one evidence
folder.

| Verb | Syntax | Reads | Writes | Role |
|---|---|---|---|---|
| `check` | `check --contract .harness/project-contract.yaml --risk <low\|med\|high> [--repo-root <path>] [--ledger <path>] [--json]` | contract, working tree | worker row → `ledger.jsonl`, proof under `.harness/evidence/`, run-report row | **worker** |
| `verify` | `verify .harness/evidence/ledger.jsonl [--json]` | latest worker proof | verifier verdict row, run-report row | **verifier** (different actor) |
| `replay` | `replay .harness/evidence/ledger.jsonl [--require-verifier] [--expect-tree <sha>] [--changed <p1,p2,…>] [--json]` | ledger | run-report row only (`.harness/run-report.jsonl` + regenerated `run-report.md`) | what the hooks call |
| `attest` | `attest --contract … --risk … [--repo-root <path>] [--ledger …] [--json]` | contract, tree | worker + verifier rows by the SAME actor, run-report row | solo convenience = `git gate` |
| `loopcheck` | `loopcheck --contract … --changed <p1,p2,…> [--run-report <path>] [--json]` | contract `loop:`, run-report, `EZ_TASK_ID` | run-report row | loop brakes (hook runs it) |
| `lesson lookup` | `lesson lookup --changed <p1,p2,…> [--format json] [--dir <vault>] [--repo-root <path>]` | `.harness/lessons/*.md` frontmatter | nothing | retrieval by path |
| `lesson lint` | `lesson lint [--dir <vault>] [--repo-root <path>]` | vault + `_fixtures/` | nothing | fail-closed shape check |
| `plan validate` | `plan validate --file <plan.md> [--env-patterns <file>]` | one plan file | nothing | plankit rules; exit 0 pass / 3 fail |
| `plan save` | `plan save --file <draft.md> [--repo-root <path>]` | draft plan | `.harness/plans/<basename>` | validate + copy |
| `capabilities` | `capabilities` | — | — | machine-readable verb list |

`version` prints the binary version; it is a top-level command, not a capability.
Every verb above except `lesson`, `plan` and `capabilities` appends one row to `.harness/run-report.jsonl`
(and regenerates `run-report.md`) — that is the telemetry, not evidence.

## The standard flow

```bash
git add <files>

EZH="$(git rev-parse --git-common-dir)/.."

# 1. worker — produces proof; alone it never passes the gate
"$EZH/.harness/bin/ez-evidence" check --contract "$EZH/.harness/project-contract.yaml" --risk med \
  --repo-root "$(git rev-parse --show-toplevel)" --json

# 2. verifier — a DIFFERENT session/actor replays the proof and records a verdict
"$EZH/.harness/bin/ez-evidence" verify "$EZH/.harness/evidence/ledger.jsonl" --json

# 3. what pre-commit / pre-push run (you may run it yourself to predict the gate)
"$EZH/.harness/bin/ez-evidence" replay "$EZH/.harness/evidence/ledger.jsonl" --require-verifier --json

git commit -m "…"
```

`replay --require-verifier` passes only when the latest verifier verdict is `pass` **and** covers
the current ledger digest. Any change to the tree after `verify` makes the verdict stale → BLOCK.

## `attest` / `git gate` — know what you are giving up

```bash
git gate            # = attest at the contract's risk_level
git gate low        # override the risk for this run
```

One actor runs check and verify. Fine for a solo, low-risk commit. It does **not** satisfy
independent verification: for high-risk work let a different session run `verify`.

## Linked worktrees — pass `--repo-root`

Evidence *storage* resolves to the main root's `.harness` automatically (v6.4), but `check` and
`attest` bind the proof to the tree under `--repo-root` (default: the contract's `workspace_root`,
i.e. the MAIN checkout). From inside a worktree always pass it, or the proof describes the wrong tree:

```bash
.harness/bin/ez-evidence check --contract .harness/project-contract.yaml --risk med \
  --repo-root "$(git rev-parse --show-toplevel)" --json
```

## Risk

`--risk` selects how strictly the contract is applied. Take it from the contract, never guess:

```bash
awk '$1=="risk_level:"{print $2; exit}' .harness/project-contract.yaml
```

Checking at a lower risk than the contract declares blocks the gate (the hooks use the contract).

## `risk_paths` — when the gate steps aside

If the contract declares `risk_paths`, a change touching none of them is allowed with a warning
(`decision: bypass`). No `risk_paths` block = strict: every change needs evidence.
