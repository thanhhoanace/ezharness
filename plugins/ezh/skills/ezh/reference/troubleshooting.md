# When the gate BLOCKs

The hook prints one JSON line on stderr with `hook`, `repo`, `branch`, `reason` (and
`lessons_matched` when the vault has a hit). Read `reason` first; every case below maps to it.
Exit code `2` is always BLOCK. Never `git commit --no-verify`, never edit the ledger.

| `reason` says | Meaning | Action |
|---|---|---|
| `project contract not found` | `.harness/project-contract.yaml` missing | Re-run the installer (`install.sh --refresh --target <repo>`) — the contract is never overwritten, only restored |
| binary not found | `.harness/bin/ez-evidence` missing or not executable | Re-run the installer; check `.harness/bin/.ezharness-bin.json` |
| worker-only ledger / no verifier verdict | `check` ran, `verify` did not | Run `verify` from a different session/actor |
| stale verifier verdict / digest mismatch | Tree changed after `verify` | Re-run `check` then `verify` on the current tree |
| proof mismatch | Ledger row does not match its proof file | Re-run `check`; if it persists, evidence was edited — stop and ask a human |
| `build.verify is undefined for this repo` | Contract carries `EZH_VERIFY_UNDEFINED` | Define a real verify (below) |
| `loop brake` | `EZ_TASK_ID` unset, budget exhausted, or a `forbidden_paths` hit | See §loop |
| a real verify command failed | The repo's own tests/lint fail | Fix the code. This is the gate doing its job |

## §verify — defining `build.verify` when it is undefined

Do **not** write a placeholder that always passes.

1. Read this repo's CI (`.gitlab-ci.yml`, workflow files) and build files (`Makefile`, `go.mod`,
   `Cargo.toml`, `package.json`). CI is the source of truth for what "valid" means here.
2. Add a `verify` target in the `Makefile` that runs the same lint/build/test **offline**.
3. Set `build.verify: "make verify"` in `.harness/project-contract.yaml`.
4. Run `make verify` on a clean tree — it must pass before you rely on it.
5. Keep Makefile, CI and the contract pointing at the SAME command; ask a human to review — the
   verify command defines "done".

The install state `installed_needs_verify` in `.harness/install-manifest.json` means exactly this
situation; it becomes `installed_active` after a refresh with a real verify.

## §loop — contracts with `loop:`

- The task id is `EZ_TASK_ID`, else the **branch name** (one task = one branch). On `main`,
  `master` or a detached HEAD with no `EZ_TASK_ID` → BLOCK (fail-closed). Keep ONE id for the whole
  task: stay on the task branch, or `export EZ_TASK_ID=<plan task_id>`; never mint one per attempt.
- `budget.max_iterations` counts gate rounds per task id. Exhausted → BLOCK with no unlock; a
  human decides whether to raise the budget in the contract.
- `forbidden_paths` block even with valid evidence and even at `--risk low`. A human makes such
  changes outside the loop. There is no environment-variable bypass by design.
- Telemetry per round lands in `.harness/run-report.jsonl` (`task_id`, `run_id`, decision).

## §worktree — linked worktrees

Since v6.4 the binary resolves evidence paths through `git rev-parse --git-common-dir`, so a linked
worktree writes to the **main root's** `.harness` (one ledger, one run-report). That covers *where*
evidence is stored — not *which tree* it describes: `check`/`attest` default `--repo-root` to the
contract's `workspace_root` (the main checkout). Inside a worktree always pass
`--repo-root "$(git rev-parse --show-toplevel)"`, otherwise the proof binds the main checkout's tree
and the gate on the worktree commit is stale or wrong. Older installs
created a `.harness` symlink in the worktree; `git add` refuses to add lesson files "beyond a
symbolic link" — materialise `.harness/lessons/` as a real directory in the worktree before
committing a lesson from there.

## §refresh — after upgrading the bundle

`install.sh --refresh` overwrites installer-owned files (binary, hooks, `.harness/AGENTS.md`, this
skill, host policies) and preserves `project-contract.yaml`, human `AGENTS.md` content, evidence and
lessons. To see the exact list before touching anything:

```bash
<bundle>/install.sh --plan --target <repo>
```

## §placement — a file sits in the wrong place (ADR-0015)

Every path ezHarness writes has one row in `_state-kinds.tsv` (next to the hooks): its group and
where it lives. Team knowledge (contract, lessons, plans) lives in a git repo the team clones. A
folder that is not a git repo holds nothing. To see what is misplaced, run the read-only check:

```bash
bash .harness/hooks/ez-doctor.sh --workspace <folder>      # or the copy in the ezh plugin
```

| Line | Meaning | What to do |
|---|---|---|
| `PROBLEM host-holds-state` | a folder without git holds ezHarness files | move plans and lessons into a git repo; the tools go away with `uninstall.sh` |
| `PROBLEM shared-not-tracked` | a team file exists but git ignores it or it was never added | refresh the install (`.gitignore` follows the table), then commit the file |
| `NOTE pointer` | a symlink into a git work tree | fine while a move is in progress |
| `NOTE unknown-entry` | not an ezHarness path | yours to keep or move |

Exit 0 means no problem, 3 means at least one. The check never writes.

## Where to look

| Question | Path |
|---|---|
| Which verbs exist here | `.harness/bin/ez-evidence capabilities` |
| Which binary/commit is installed | `.harness/bin/.ezharness-bin.json`, `.harness/hooks/.ezharness-hooks.json` |
| Install state and ownership | `.harness/install-manifest.json` |
| Policy | `.harness/project-contract.yaml` |
| Evidence | `.harness/evidence/ledger.jsonl` (append-only) |
| Loop telemetry | `.harness/run-report.jsonl` |
