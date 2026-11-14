package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const loopContractBody = `service: clawfans-api
owners: [alice]
build:
  verify: true
risk_level: low
loop:
  budget:
    max_iterations: 3
  require_independent_verifier: true
  forbidden_paths:
    - "clawfans-api/**/payment*/**"
    - "**/migrations/**"
    - "infra/**"
`

func writeLoopContract(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	harness := filepath.Join(dir, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(harness, "project-contract.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseLoopBlock(t *testing.T) {
	c, err := ReadContract(writeLoopContract(t, loopContractBody))
	if err != nil {
		t.Fatalf("ReadContract: %v", err)
	}
	if c.Loop == nil {
		t.Fatal("Loop = nil, want parsed block")
	}
	if c.Loop.Budget.MaxIterations != 3 {
		t.Errorf("MaxIterations = %d, want 3", c.Loop.Budget.MaxIterations)
	}
	if !c.Loop.RequireIndependentVerifier {
		t.Error("RequireIndependentVerifier = false, want true")
	}
	if len(c.Loop.ForbiddenPaths) != 3 {
		t.Errorf("ForbiddenPaths = %v, want 3 entries", c.Loop.ForbiddenPaths)
	}
}

// AC-L1: a contract without a loop: block parses to Loop == nil and loopcheck is
// a pure no-op (v6.2 behaviour preserved).
func TestParseNoLoopIsNil(t *testing.T) {
	body := `service: x
owners: [a]
build:
  verify: true
risk_level: low
`
	c, err := ReadContract(writeLoopContract(t, body))
	if err != nil {
		t.Fatalf("ReadContract: %v", err)
	}
	if c.Loop != nil {
		t.Fatalf("Loop = %#v, want nil", c.Loop)
	}
	if got := RunLoopCheck(c, []string{"infra/main.tf"}, ""); got.Decision != loopAllow {
		t.Fatalf("no-loop RunLoopCheck = %q, want allow", got.Decision)
	}
}

func TestParseLoopMalformedBudget(t *testing.T) {
	body := `service: x
owners: [a]
build:
  verify: true
risk_level: low
loop:
  budget:
    max_iterations: eight
`
	if _, err := ReadContract(writeLoopContract(t, body)); err == nil {
		t.Fatal("expected error for non-integer max_iterations")
	}
}

// Finding 4: inline comments after loop scalars/list items must parse like real
// YAML (the shipped template example carries them).
func TestParseLoopInlineComments(t *testing.T) {
	body := `service: x
owners: [a]
build:
  verify: true
risk_level: low
loop:
  budget:
    max_iterations: 8        # gate rounds per task before escalation
  forbidden_paths:           # hard block even WITH evidence
    - "**/payment*/**"       # money
    - "infra/**"
`
	c, err := ReadContract(writeLoopContract(t, body))
	if err != nil {
		t.Fatalf("ReadContract with inline comments: %v", err)
	}
	if c.Loop == nil || c.Loop.Budget.MaxIterations != 8 {
		t.Fatalf("max_iterations = %#v, want 8", c.Loop)
	}
	if len(c.Loop.ForbiddenPaths) != 2 || c.Loop.ForbiddenPaths[0] != "**/payment*/**" {
		t.Fatalf("forbidden_paths = %v, want 2 clean entries", c.Loop.ForbiddenPaths)
	}
}

// Finding 3: a contract with a block-style command_prefixes list (schema-valid)
// must parse, so loopcheck's whole-contract parse does not reject it.
func TestParseEnvRuleBlockCommandPrefixes(t *testing.T) {
	body := `service: x
owners: [a]
build:
  verify: true
env_rules:
  - id: ecr-prefix
    applies_to: docker_push
    reminder: match prefix
    gate_action: prompt
    command_prefixes:
      - docker push
      - docker buildx build
    severity: blocking
risk_level: med
`
	c, err := ReadContract(writeLoopContract(t, body))
	if err != nil {
		t.Fatalf("ReadContract with block command_prefixes: %v", err)
	}
	if len(c.EnvRules) != 1 || len(c.EnvRules[0].CommandPrefixes) != 2 {
		t.Fatalf("command_prefixes = %#v, want 2", c.EnvRules)
	}
}

// AC-L1: with NO loop: block, a commit whose contract the loopcheck parser
// cannot fully digest must NOT be turned into a block — loopcheck degrades to
// allow so behaviour stays byte-identical to v6.2.
func TestLoopCheckCommandAllowsOnUnparseableNoLoopContract(t *testing.T) {
	body := "service: x\nowners: [a]\nbuild:\n  verify: true\nrisk_level: low\n    bogus: nope\n"
	contractPath := writeLoopContract(t, body)
	if _, err := ReadContract(contractPath); err == nil {
		t.Fatal("precondition: contract should be unparseable")
	}
	t.Setenv("EZ_TASK_ID", "")
	var out, errBuf strings.Builder
	code := Run([]string{"loopcheck", "--contract", contractPath, "--changed", "src/app.go"}, &out, &errBuf)
	if code != exitOK {
		t.Fatalf("loopcheck on unparseable no-loop contract exit=%d (want 0 allow); stderr=%q", code, errBuf.String())
	}
}

// AC-L2 (D4 final): a forbidden path blocks unconditionally — there is no
// unlock path, and EZ_LOOP_APPROVE must have NO effect on the verdict (an env
// the agent can export is not a control).
func TestLoopCheckForbiddenBrake(t *testing.T) {
	c, err := ReadContract(writeLoopContract(t, loopContractBody))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EZ_TASK_ID", "loop-1")
	t.Setenv("EZ_LOOP_APPROVE", "")

	blocked := RunLoopCheck(c, []string{"clawfans-api/internal/payments/charge.go"}, "")
	if blocked.Decision != loopBlock || blocked.ForbiddenHit == "" {
		t.Fatalf("forbidden path outcome = %#v, want block + hit", blocked)
	}

	t.Setenv("EZ_LOOP_APPROVE", "reviewed by owner")
	stillBlocked := RunLoopCheck(c, []string{"clawfans-api/internal/payments/charge.go"}, "")
	if stillBlocked.Decision != loopBlock {
		t.Fatalf("EZ_LOOP_APPROVE changed the verdict to %q — the env must be inert (D4: no unlock)", stillBlocked.Decision)
	}
}

// AC-L4: loop declared but EZ_TASK_ID missing → block (fail-closed).
func TestLoopCheckFailClosedMissingTaskID(t *testing.T) {
	c, err := ReadContract(writeLoopContract(t, loopContractBody))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EZ_TASK_ID", "")
	t.Setenv("EZH_TASK_ID", "")
	t.Setenv("EZ_LOOP_APPROVE", "")
	chdirForTest(t, t.TempDir()) // not a git repo: no branch to fall back on
	got := RunLoopCheck(c, []string{"src/app.go"}, "")
	if got.Decision != loopBlock || !strings.Contains(got.Reason, "EZ_TASK_ID") {
		t.Fatalf("fail-closed outcome = %#v, want block on missing task id", got)
	}
}

// Without EZ_TASK_ID the task id is the branch name; main, master and a
// detached HEAD stay fail-closed.
func TestLoopCheckTaskIDFromBranch(t *testing.T) {
	c, err := ReadContract(writeLoopContract(t, loopContractBody))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EZ_TASK_ID", "")
	t.Setenv("EZH_TASK_ID", "")
	repo := t.TempDir()
	gitInitRepoWithCommit(t, repo)
	chdirForTest(t, repo)

	for _, base := range []string{"main", "master"} {
		runGit(t, repo, "checkout", "-q", "-B", base)
		if got := RunLoopCheck(c, []string{"src/app.go"}, ""); got.Decision != loopBlock {
			t.Fatalf("on %s: outcome = %#v, want fail-closed block", base, got)
		}
	}
	runGit(t, repo, "checkout", "-q", "--detach")
	if got := RunLoopCheck(c, []string{"src/app.go"}, ""); got.Decision != loopBlock {
		t.Fatalf("detached HEAD: outcome = %#v, want fail-closed block", got)
	}

	runGit(t, repo, "checkout", "-q", "-b", "esim-eso-secrets")
	if id, _ := resolveTaskID(); id != "esim-eso-secrets" {
		t.Fatalf("resolveTaskID on a task branch = %q, want the branch name", id)
	}
	if got := RunLoopCheck(c, []string{"src/app.go"}, ""); got.Decision == loopBlock {
		t.Fatalf("task branch: outcome = %#v, want no fail-closed block", got)
	}
	t.Setenv("EZ_TASK_ID", "explicit-id")
	if id, _ := resolveTaskID(); id != "explicit-id" {
		t.Fatalf("EZ_TASK_ID must win over the branch, got %q", id)
	}
}

// AC-L3 + AC-L5: the (max+1)-th round blocks on budget, and omc + omx rows for
// one task_id share the same count.
func TestLoopCheckBudgetBrake(t *testing.T) {
	c, err := ReadContract(writeLoopContract(t, loopContractBody)) // max_iterations: 3
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EZ_TASK_ID", "loop-b")
	t.Setenv("EZ_LOOP_APPROVE", "")

	dir := t.TempDir()
	report := filepath.Join(dir, "run-report.jsonl")
	// 3 rounds for loop-b across BOTH engines (AC-L5): 2 omc + 1 omx.
	lines := []string{
		`{"command":"check","verdict":"block","engine":"omc","task_id":"loop-b"}`,
		`{"command":"check","verdict":"block","engine":"omc","task_id":"loop-b"}`,
		`{"command":"attest","verdict":"block","engine":"omx","task_id":"loop-b"}`,
		`{"command":"verify","verdict":"warn","engine":"omc","task_id":"loop-b"}`, // not counted
		`{"command":"check","verdict":"pass","engine":"omc","task_id":"other"}`,   // different task
	}
	if err := os.WriteFile(report, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := RunLoopCheck(c, []string{"src/app.go"}, report)
	if got.Decision != loopBlock || got.Iterations != 3 {
		t.Fatalf("budget outcome = %#v, want block at 3/3", got)
	}

	// With only 2 counted rounds, the next round is allowed.
	twoRounds := filepath.Join(dir, "two.jsonl")
	if err := os.WriteFile(twoRounds, []byte(strings.Join(lines[:2], "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	allow := RunLoopCheck(c, []string{"src/app.go"}, twoRounds)
	if allow.Decision != loopAllow || allow.Iterations != 2 {
		t.Fatalf("under-budget outcome = %#v, want allow at 2", allow)
	}
}

// AC-L2 CLI: the loopcheck subcommand returns exit 2 on a forbidden block.
func TestLoopCheckCommandBlocks(t *testing.T) {
	contractPath := writeLoopContract(t, loopContractBody)
	t.Setenv("EZ_TASK_ID", "loop-cli")
	t.Setenv("EZ_LOOP_APPROVE", "")
	var out, errBuf strings.Builder
	code := Run([]string{"loopcheck", "--contract", contractPath, "--changed", "infra/main.tf"}, &out, &errBuf)
	if code != exitBlock {
		t.Fatalf("loopcheck exit = %d, want %d; stderr=%q", code, exitBlock, errBuf.String())
	}
}

// Fail-closed budget trust (Codex review 2026-08-11): a run-report that EXISTS
// but cannot be trusted must BLOCK — a deleted-then-recreated or torn file must
// not hand the loop a fresh budget. A MISSING report stays the legitimate
// first-round state (allow at 0 rounds).
func TestLoopCheckBudgetDegradedReport(t *testing.T) {
	c, err := ReadContract(writeLoopContract(t, loopContractBody))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EZ_TASK_ID", "loop-d")
	t.Setenv("EZ_LOOP_APPROVE", "")
	dir := t.TempDir()

	// Missing report → clean first round, allow.
	missing := RunLoopCheck(c, []string{"src/app.go"}, filepath.Join(dir, "absent.jsonl"))
	if missing.Decision != loopAllow || missing.Iterations != 0 {
		t.Fatalf("missing report outcome = %#v, want allow at 0", missing)
	}

	// Malformed line → degraded → block, regardless of the parseable count.
	torn := filepath.Join(dir, "torn.jsonl")
	body := `{"command":"check","verdict":"block","engine":"omc","task_id":"loop-d"}` + "\n" +
		`{"command":"check","ver` + "\n"
	if err := os.WriteFile(torn, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := RunLoopCheck(c, []string{"src/app.go"}, torn)
	if got.Decision != loopBlock || !strings.Contains(got.Reason, "malformed") {
		t.Fatalf("torn report outcome = %#v, want fail-closed block", got)
	}
}

// chdirForTest is testing.T.Chdir for the go1.22 module floor.
func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}
