package evidence

// P1 ezloop — the loop brakes the gate enforces BEFORE the risk-path bypass, so
// a forbidden path can never slip through as a "docs bypass".
//
// Threat model (round-3, 2026-08-11): the brakes are a COOPERATIVE SAFETY RAIL
// for a cooperating agent, not a security boundary. They run inside a local
// git hook a same-user agent can skip with --no-verify, and the budget counter
// lives in a best-effort telemetry file (run-report.jsonl) the same user can
// delete — deletion reads as the legitimate first-round state. Enforcement for
// REAL protected paths therefore belongs in CI / protected branches; here the
// job is to stop a cooperating loop from wandering, loudly and deterministically.
//
// Two brakes (plan §P1):
//   - Forbidden brake: a changed file matching loop.forbidden_paths blocks even
//     WITH valid evidence. Per D4 (final, 2026-07-14) there is NO unlock path
//     inside the gate: "forbidden = blocked, full stop". An env-based unlock is
//     not a control (an agent can export the env), so none exists — a change to
//     a forbidden path is made by a human outside the loop, or not at all.
//   - Budget brake: with EZ_TASK_ID set and loop.budget.max_iterations declared,
//     count this task's worker rounds in run-report.jsonl; the (max+1)-th round
//     blocks ("budget exhausted; escalate to human").
//
// Fail-closed applies exactly where a deterministic check can hold the line:
// loop declared but EZ_TASK_ID missing → BLOCK (AC-L4; a loop must be
// identifiable to be budgeted) · forbidden path touched → BLOCK · run-report
// present but unreadable/malformed → BLOCK (a corrupted counter must not
// silently hand out a fresh budget). No claim beyond that is made.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loopCheckOutcome is the decision LoopCheck reached. Decision is "allow" or
// "block". ForbiddenHit carries the first changed path that matched
// forbidden_paths when the block came from the forbidden brake.
type loopCheckOutcome struct {
	Decision     string
	Reason       string
	ForbiddenHit string
	Iterations   int
}

const (
	loopAllow = "allow"
	loopBlock = "block"
)

// loopCountableCommands are the worker entry points that count as one loop
// round for the budget brake (resolves G1: an iteration is one worker
// check/attest cycle, not every gate command and not only blocks). verify and
// replay are downstream of a round and are not counted.
var loopCountableCommands = map[string]bool{"check": true, "attest": true}

// RunLoopCheck evaluates the loop brakes for a set of changed files. runReportPath
// is the task's run-report.jsonl (for the budget count); it may be "" when no
// budget is declared. It never runs the agent — it only enforces the spec.
func RunLoopCheck(contract Contract, changed []string, runReportPath string) loopCheckOutcome {
	if contract.Loop == nil {
		// AC-L1: no loop: block → loopcheck is a pure no-op (v6.2 behaviour).
		return loopCheckOutcome{Decision: loopAllow}
	}
	loop := contract.Loop

	// Forbidden brake — runs first, before any budget/task-id logic, so a
	// forbidden path is blocked even in a docs-only or unbudgeted change.
	for _, f := range changed {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if matchAnyGlob(loop.ForbiddenPaths, f) {
			// D4 (final): no unlock path exists. A forbidden change is made by
			// a human outside the loop, or not at all — an env the agent can
			// export is not a control, so the gate does not consult one.
			return loopCheckOutcome{
				Decision:     loopBlock,
				Reason:       fmt.Sprintf("forbidden path %q touched — hard block, no unlock (D4: forbidden = blocked, full stop). Drop the change, or a human makes it outside the loop.", f),
				ForbiddenHit: f,
			}
		}
	}

	// Fail-closed (AC-L4): a declared loop must carry a task id (EZ_TASK_ID or the branch name).
	taskID, _ := resolveTaskID()
	if taskID == "" {
		return loopCheckOutcome{
			Decision: loopBlock,
			Reason:   "loop: declared but EZ_TASK_ID is not set and HEAD is main, master or detached — fail-closed. A loop must be identifiable to be budgeted: work on a task branch or export EZ_TASK_ID.",
		}
	}

	// Budget brake (AC-L3, AC-L5): count this task's worker rounds across all
	// engines. max_iterations == 0 disables the brake.
	if loop.Budget.MaxIterations > 0 {
		n, degraded := countTaskRounds(runReportPath, taskID)
		if degraded != "" {
			// Fail-closed: the budget is a hard brake, so a run-report that
			// EXISTS but cannot be trusted must not silently reset the count
			// to zero (a deleted-and-recreated or torn file would otherwise
			// hand the loop a fresh budget). A missing report is NOT degraded
			// — that is the legitimate first-round state.
			return loopCheckOutcome{
				Decision: loopBlock,
				Reason:   fmt.Sprintf("budget brake degraded — %s. Fail-closed: cannot trust the round count. A human inspects/repairs %s before the loop continues.", degraded, runReportPath),
			}
		}
		if n >= loop.Budget.MaxIterations {
			return loopCheckOutcome{
				Decision:   loopBlock,
				Reason:     fmt.Sprintf("budget exhausted for task_id %q (%d/%d rounds) — escalate to human.", taskID, n, loop.Budget.MaxIterations),
				Iterations: n,
			}
		}
		return loopCheckOutcome{Decision: loopAllow, Iterations: n}
	}

	return loopCheckOutcome{Decision: loopAllow}
}

// countTaskRounds counts worker rounds (check/attest rows) recorded for taskID
// in the run-report JSONL. It is engine-agnostic so omc + omx rows for the same
// task_id share one budget count (AC-L5).
//
// Trust semantics (fail-closed on corruption, Codex review 2026-08-11): a
// MISSING report is zero rounds — the legitimate first-round state (which also
// means a cooperating-but-tampering agent could delete the file; see the
// package comment — this is a cooperative rail, not a tamper-proof boundary).
// A report that exists but is unreadable, or contains malformed lines, returns
// a non-empty degraded reason and the caller must BLOCK: a counter that can be
// reset by corrupting a file in place would brake nothing.
func countTaskRounds(runReportPath string, taskID string) (int, string) {
	if strings.TrimSpace(runReportPath) == "" || strings.TrimSpace(taskID) == "" {
		return 0, ""
	}
	data, err := os.ReadFile(runReportPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ""
		}
		return 0, fmt.Sprintf("run-report exists but is unreadable (%v)", err)
	}
	n := 0
	malformed := 0
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var rec runReportRecord
		if jsonErr := json.Unmarshal([]byte(line), &rec); jsonErr != nil {
			malformed++
			continue
		}
		if rec.TaskID == taskID && loopCountableCommands[rec.Command] {
			n++
		}
	}
	if malformed > 0 {
		return n, fmt.Sprintf("run-report has %d malformed line(s) — the round count cannot be trusted", malformed)
	}
	return n, ""
}

// loopRunReportPath resolves the run-report.jsonl for a contract path when the
// caller did not pass one explicitly.
func loopRunReportPath(contractPath string) string {
	root := contractProjectRoot(filepath.Clean(contractPath))
	// B#2: resolve to the shared .harness owner so the budget-count READ hits the
	// same file recordRunReport WRITES from a linked worktree. Non-git falls back
	// to the contract's workspace root.
	if shared, ok := harnessRoot(root); ok {
		root = shared
	}
	return filepath.Join(root, ".harness", "run-report.jsonl")
}
