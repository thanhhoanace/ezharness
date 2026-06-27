# ezHarness

**A per-repo evidence gate for git — "done means done" at the commit boundary.**

ezHarness blocks a commit or push until an **independent verifier** has replayed
recorded evidence proving the repository's contract checks pass. It is an
evidence/policy gate, not a planner: planning and task tracking stay with your
host agent (Claude Code, Codex, etc.); ezHarness only makes "done" *provable*.

The whole tool ships as a single static binary, `ezh`, that embeds its hooks and
templates. One `ezh install` wires a repo's git hooks, project contract, and the
`git gate` convenience alias — no external files required.

## Install

### curl | sh

```sh
curl -fsSL https://raw.githubusercontent.com/thanhhoanace/ezharness/main/scripts/install.sh | sh
```

### Homebrew

```sh
brew install thanhhoanace/tap/ezharness
```

### go install

```sh
go install github.com/thanhhoanace/ezharness/cmd/ezh@latest
```

## Quick start

```sh
cd <your repo>
ezh install            # wire hooks + contract into this repo's .harness/
```

`ezh install` creates `.harness/` with:

- `hooks/` — the pre-commit / pre-push gate scripts (`core.hooksPath` is set to
  `.harness/hooks`).
- `project-contract.yaml` — the contract. The verify command is auto-detected
  (a `verify:` Makefile target → `make verify`, a `go.mod` → `go test ./... &&
  go build ./...`, …); otherwise the `EZH_VERIFY_UNDEFINED` sentinel is written
  for you to fill in. Edit `owners` and `risk_paths` before relying on the gate.
- `AGENTS.md` — the workflow rules for host agents.
- `bin/ezh-evidence` — a copy of the binary the hooks invoke.
- `config.json`, `install-manifest.json` — workspace metadata.

Re-running `ezh install` is safe: it never clobbers an existing contract and
only fills in what is missing.

## Usage

Satisfy the gate in one step (worker check + independent verifier):

```sh
git gate            # defaults to low risk
git gate med        # raise the risk level for this run
```

Or drive the engine directly:

```sh
ezh attest  --contract .harness/project-contract.yaml --risk <low|med|high> [--ledger <path>] [--json]
ezh check   --contract .harness/project-contract.yaml --risk <low|med|high> [--ledger <path>] [--json]
ezh verify  .harness/evidence/ledger.jsonl [--json]
ezh replay  .harness/evidence/ledger.jsonl [--require-verifier] [--expect-tree <tree>] [--json]
ezh version
```

The pre-commit / pre-push hook runs `ezh replay --require-verifier` and blocks
the commit unless the latest **verifier** verdict passes and covers the tree
being committed. The actor who produced the worker proof must not be the
verifier — no self-approval.

## How the gate decides

1. **check** — the worker runs the contract's `build.verify` (and env rules /
   `verify_thick` at higher risk), recording each result as evidence in the
   ledger with a hash of the proof body.
2. **verify** — an independent verifier replays the worker's evidence and
   appends a verifier verdict.
3. **replay** (in the git hook) — re-validates the ledger against the staged
   tree, requiring a fresh passing verifier verdict, and blocks the commit
   otherwise.

A change that touches none of the contract's `risk_paths` is allowed with a
warning; omit `risk_paths` to gate every change strictly.

## License

[MIT](./LICENSE) © 2026 thanhhoanace
