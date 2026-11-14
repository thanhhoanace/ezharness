package evidence

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs a git command in dir with a self-contained identity so the test
// never depends on the host's global git config.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ezh-test", "GIT_AUTHOR_EMAIL=ezh-test@example.com",
		"GIT_COMMITTER_NAME=ezh-test", "GIT_COMMITTER_EMAIL=ezh-test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitInitRepoWithCommit creates a git repo in dir with one seed commit so a
// linked worktree can be added from it.
func gitInitRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "ezh-test@example.com")
	runGit(t, dir, "config", "user.name", "ezh-test")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "seed.txt")
	runGit(t, dir, "commit", "-m", "seed")
}

// TestSharedHarnessRootFromWorktree is the B#2 regression: a gate run driven from
// a linked worktree — with contract AND --ledger pointing at the worktree's own
// .harness — must land the ledger, proofs, and run-report under the MAIN root's
// shared .harness, never the worktree's. This proves harnessRoot() collapses the
// worktree to its common root instead of splitting evidence per worktree.
func TestSharedHarnessRootFromWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	main := t.TempDir()
	gitInitRepoWithCommit(t, main)

	// Linked worktree checked out beside the main root (target must not pre-exist).
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-b", "feature", wt)

	// Contract created in the WORKTREE's own .harness, as a hook resolving paths
	// against the worktree cwd would produce. Without the shared-root fix the gate
	// would write evidence into <wt>/.harness.
	wtHarness := filepath.Join(wt, ".harness")
	if err := os.MkdirAll(wtHarness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(wtHarness, "project-contract.yaml")
	body := `service: demo-service
owners: [alice]
build:
  verify: true
risk_level: low
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	wtLedger := filepath.Join(wtHarness, "evidence", "ledger.jsonl")

	var stdout, stderr strings.Builder
	code := Run([]string{
		"check",
		"--contract", contractPath,
		"--risk", "low",
		"--ledger", wtLedger,
		"--repo-root", wt,
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("check exit = %d (want 0); stderr=%q", code, stderr.String())
	}

	// Ledger + proofs + run-report all land under the MAIN root.
	mainLedger := filepath.Join(main, ".harness", "evidence", "ledger.jsonl")
	if _, err := os.Stat(mainLedger); err != nil {
		t.Fatalf("ledger not under main root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(main, ".harness", "run-report.jsonl")); err != nil {
		t.Fatalf("run-report not under main root: %v", err)
	}
	entries := readLedgerEntries(t, mainLedger)
	if len(entries) == 0 {
		t.Fatal("no ledger entries under main root")
	}
	for _, e := range entries {
		proofPath := filepath.Join(main, filepath.FromSlash(e.ProofRef))
		if _, err := os.Stat(proofPath); err != nil {
			t.Fatalf("proof not under main root: %v (ref %s)", err, e.ProofRef)
		}
	}

	// Nothing leaked into the worktree's .harness.
	if _, err := os.Stat(wtLedger); !os.IsNotExist(err) {
		t.Fatalf("ledger leaked into worktree .harness: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtHarness, "evidence", "proofs")); !os.IsNotExist(err) {
		t.Fatalf("proofs leaked into worktree .harness: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtHarness, "run-report.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("run-report leaked into worktree .harness: %v", err)
	}
}

// TestAttestFromWorktreeLandsUnderMainRoot is the B#2 FIX #1 regression: `attest`
// (check THEN verify) driven from a linked worktree must succeed — the read path
// (verify) has to rebase the worktree --ledger onto the MAIN root exactly like the
// write path (check), or verify opens a ledger that check never wrote and attest
// exits 2. Both the worker check row(s) AND the verifier verdict must land under
// the MAIN root's .harness; the worktree gets none.
func TestAttestFromWorktreeLandsUnderMainRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	main := t.TempDir()
	gitInitRepoWithCommit(t, main)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-b", "feature", wt)

	wtHarness := filepath.Join(wt, ".harness")
	if err := os.MkdirAll(wtHarness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(wtHarness, "project-contract.yaml")
	body := `service: demo-service
owners: [alice]
build:
  verify: true
risk_level: low
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	wtLedger := filepath.Join(wtHarness, "evidence", "ledger.jsonl")

	var stdout, stderr strings.Builder
	code := Run([]string{
		"attest",
		"--contract", contractPath,
		"--risk", "low",
		"--ledger", wtLedger,
		"--repo-root", wt,
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("attest exit = %d (want 0 — read path must find the ledger check wrote); stderr=%q", code, stderr.String())
	}

	mainLedger := filepath.Join(main, ".harness", "evidence", "ledger.jsonl")
	entries := readLedgerEntries(t, mainLedger)
	var worker, verifier int
	for _, e := range entries {
		switch e.ActorRole {
		case actorWorker:
			worker++
		case actorVerifier:
			verifier++
		}
	}
	if worker == 0 {
		t.Fatalf("no worker rows under main root: %#v", entries)
	}
	if verifier == 0 {
		t.Fatalf("verifier verdict did not land under main root: %#v", entries)
	}
	for _, e := range entries {
		if e.ActorRole == actorVerifier {
			p := filepath.Join(main, filepath.FromSlash(e.ProofRef))
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("verifier proof not under main root: %v (ref %s)", err, e.ProofRef)
			}
		}
	}
	if _, err := os.Stat(wtLedger); !os.IsNotExist(err) {
		t.Fatalf("ledger leaked into worktree .harness: %v", err)
	}
}

// TestNestedNonRepoKeepsOwnHarness is the B#2 FIX #2 regression: a project that is
// NOT its own git repo but sits under an OUTER repo must keep its evidence in its
// own .harness. `--git-common-dir` alone would resolve to the outer repo and
// silently hoist evidence there; harnessRoot must collapse ONLY for a true linked
// worktree, so a repo sub-directory keeps its own workspace.
func TestNestedNonRepoKeepsOwnHarness(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	outer := t.TempDir()
	gitInitRepoWithCommit(t, outer)

	proj := filepath.Join(outer, "proj")
	projHarness := filepath.Join(proj, ".harness")
	if err := os.MkdirAll(projHarness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(projHarness, "project-contract.yaml")
	body := `service: demo-service
owners: [alice]
build:
  verify: true
risk_level: low
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	projLedger := filepath.Join(projHarness, "evidence", "ledger.jsonl")

	var stdout, stderr strings.Builder
	code := Run([]string{
		"check",
		"--contract", contractPath,
		"--risk", "low",
		"--ledger", projLedger,
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("check exit = %d (want 0); stderr=%q", code, stderr.String())
	}

	// Evidence stays in the SUBDIR .harness, never hoisted to the outer repo root.
	if _, err := os.Stat(projLedger); err != nil {
		t.Fatalf("ledger not in project .harness (over-collapse hoisted it): %v", err)
	}
	if _, err := os.Stat(filepath.Join(outer, ".harness")); !os.IsNotExist(err) {
		t.Fatalf("evidence hoisted into OUTER repo .harness (over-collapse): outer/.harness exists")
	}
}
