# Plans — `.harness/plans/<slug>.md`

One canonical home for a task's plan. ezHarness checks **shape only** (the plankit rules, ADR-0011)
— it never runs a plan, changes its status, opens a worktree or touches git.

## Verbs

```bash
.harness/bin/ez-evidence plan validate --file <plan.md> [--env-patterns <file>]  # FAIL lines + RESULT; exit 0 pass, 3 fail, 1 usage
.harness/bin/ez-evidence plan save --file <draft.md> [--repo-root .]            # validate, then copy to .harness/plans/<basename>
.harness/plankit/plan-audit.sh [--deep]                                          # report only: status, validate result, idle days
```

A plan with `RESULT: FAIL` must not be executed. Fix the plan, not the gate.

## Start a plan

Copy `.harness/plankit/PLAN-TEMPLATE.md` to `.harness/plans/<slug>.md` and fill every bracket.
The template and `env-patterns` are seeded once and belong to the project; refresh never
overwrites them.

## Rules (all must pass)

| Rule | Passes when |
|---|---|
| Sections | headings for Environment Scope, Multi-Repo Impact, Test Plan, Tasks, Rollback, Plan Quality Gate |
| Target | a `Target environment` line says `prod` or `non-prod` (one only) |
| Placeholders | no `[One sentence`, `[Explicitly`, `[Exactly`, `exact/path`, `[...]`, `YYYY-MM-DD` left |
| Environment | from `## 2.` on, a prod plan names no non-prod resource and vice versa; `env-ok: <reason>` on a line allows it |
| Workspace | an `Execution workspace` line, then one `- ` line per repo the plan writes to |
| L1 / L3 | every `### Repo:` block has a `DoD:` line, and every `DoD:` line has a command in backticks |
| L2 | `### Repo:` blocks == Execution workspace lines; `no-worktree: <reason>` excludes a read-only repo |
| Quality gate | no `- [ ]` left under `## Plan Quality Gate` |
| Status | `**Status:** approved`, `in-progress` or `done`; `draft`, `blocked`, `stale` or anything else fails |

## Environment patterns are project-owned

`.harness/plankit/env-patterns` holds `prod: <regex>` and `nonprod: <regex>` (Go RE2,
case-insensitive). Put account ids, GitOps branches and hostnames there — never in ezHarness. A
missing key keeps the generic default (`overlays/prod`; `nonprod|non-prod|overlays/(dev|uat|sit)`).
A malformed file fails every plan closed. The file is found next to the plan's `.harness/`, else
through `git rev-parse --git-common-dir`, so a worktree uses the workspace's file.

## The automatic gate (Claude Code only)

`.harness/hooks/ezh-plan-gate.sh` runs on every Claude Code prompt (UserPromptSubmit). It validates
each plan the prompt names (a `*.md` path or a bare slug of `.harness/plans/`) and injects the
result. A missing binary blocks the prompt. **Codex and OpenCode have no such hook:** run
`plan validate` by hand before executing any plan (GATE 0 in the template).

## What stays on the host side

Creating a branch/worktree for a plan (1 slug = 1 branch = 1 worktree) is a host skill (`ezplan`
on Claude Code), not an ezHarness verb. `plan save` never opens a worktree.

## Binding evidence to a plan

Work on the plan's branch (its name becomes the task id) or export `EZ_TASK_ID` before running
`check`/`attest` (the variable wins); the ledger and run-report rows then carry it, and every run
also carries its own `run_id`.
