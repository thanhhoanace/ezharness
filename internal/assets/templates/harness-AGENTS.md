# ezHarness Workflow

This file contains only ezHarness workflow rules. Project conventions remain in root or nested
`AGENTS.md` files; read both.

## What ezHarness owns

ezHarness is the **evidence/policy gate**, not a planner. Planning, task tracking, and
orchestration belong to your host agent (Claude Code / OMC, Codex, etc.). ezHarness only makes
"done" provable: a commit or push is blocked until an independent verifier has replayed evidence
that the repo's contract checks pass.

## Entry point

Use the host-native `ezh` skill for the evidence flow. The scripts under `.harness/hooks/` are
deterministic implementation details and remain directly callable for debugging.

## Evidence flow (before commit / close)

| Observable condition | Required action |
|---|---|
| Implementation is ready to validate | Worker runs `.harness/bin/ezh-evidence check --contract .harness/project-contract.yaml --risk <risk> --json` |
| Worker proof exists; closing is requested | An independent verifier (different session/actor) runs `.harness/bin/ezh-evidence verify .harness/evidence/ledger.jsonl --json` |
| About to commit/push | The Git hook runs `.harness/bin/ezh-evidence replay .harness/evidence/ledger.jsonl --require-verifier --json` and blocks unless the latest verifier verdict passes and covers the current digest |

Do not infer success and do not self-approve: the actor who produced the worker proof must not be
the verifier.

## Environment rules

There is no reliable host hook that injects context before every prompt. ezHarness therefore uses
three layers:

1. This file surfaces reminders at session/workflow boundaries.
2. Host-native project policies gate matching agent shell commands.
3. Executable `env_rules.assert` commands run during evidence checks; blocking failures stop
   closeout and Git gates.

Host policy is defense in depth. Evidence assertions and Git hooks are load-bearing.

## State and evidence

- `.harness/project-contract.yaml` defines service, risk, verify commands, and environment rules.
- `.harness/evidence/ledger.jsonl` holds worker proofs and verifier verdicts (append-only).
- Do not commit `.harness/` or store secrets there.
- Never bypass a blocking sensor; follow its `suggested_action`.
