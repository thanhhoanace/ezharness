package evidence

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validPlan is the smallest plan that passes every plankit rule: a non-prod
// plan for one written repo plus one read-only repo.
const validPlan = "# Sample feature plan\n" +
	"\n" +
	"**Status:** approved\n" +
	"**Updated:** 2026-10-01 09:00 +07\n" +
	"**Goal:** Ship the sample feature.\n" +
	"**Execution workspace:**\n" +
	"- `app`: branch `feat/sample`, worktree `app/.worktrees/sample`\n" +
	"\n" +
	"## 1. Environment Scope\n" +
	"\n" +
	"**Target environment for THIS plan:** `non-prod`\n" +
	"\n" +
	"| Item | non-prod | prod |\n" +
	"|---|---|---|\n" +
	"| Overlay | overlays/dev | overlays/prod |\n" +
	"\n" +
	"## 2. Multi-Repo Impact\n" +
	"\n" +
	"### Repo: app\n" +
	"- **Files:** `cmd/app/main.go`\n" +
	"- **DoD:** `make verify` run in `app/.worktrees/sample` -> PASS\n" +
	"\n" +
	"### Repo: infra\n" +
	"- read only: no-worktree: prerequisite, nothing written\n" +
	"- **DoD:** `terraform plan` shows no diff\n" +
	"\n" +
	"## 4. Test Plan\n" +
	"\n" +
	"- Unit: `go test ./...` against overlays/dev\n" +
	"\n" +
	"## 5. Tasks\n" +
	"\n" +
	"### Task 1: implement\n" +
	"- Step 1: write the failing test\n" +
	"\n" +
	"## 7. Rollback\n" +
	"\n" +
	"- revert the merge request\n" +
	"\n" +
	"## Plan Quality Gate\n" +
	"\n" +
	"- [x] env matrix filled\n" +
	"- [x] reviewed by a second pass\n"

func writePlanFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func reportText(r []string) string { return strings.Join(r, "\n") }

// noPatterns pins the built-in defaults so tests never pick up a stray
// env-patterns file from the developer's checkout.
func noPatterns(t *testing.T) PlanOptions {
	t.Helper()
	f := filepath.Join(t.TempDir(), "env-patterns")
	if err := os.WriteFile(f, []byte("# defaults only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return PlanOptions{EnvPatternsFile: f}
}

func TestPlanValidatePass(t *testing.T) {
	path := writePlanFile(t, t.TempDir(), "sample.md", validPlan)
	report, exit := PlanValidate(path, noPatterns(t))
	if exit != planExitOK {
		t.Fatalf("valid plan: exit=%d\n%s", exit, reportText(report))
	}
	last := report[len(report)-1]
	if !strings.HasPrefix(last, "RESULT: PASS") || !strings.Contains(last, "target env: non-prod") {
		t.Fatalf("missing PASS result line: %q", last)
	}
}

// One FAIL fixture per rule; each must exit 3 and name its rule.
func TestPlanValidateEachRuleFails(t *testing.T) {
	cases := []struct {
		rule, want string
		mutate     func(string) string
	}{
		{"required section", "missing required section: 'Rollback'", func(s string) string {
			return strings.Replace(s, "## 7. Rollback", "## 7. Undo", 1)
		}},
		{"target line missing", "no 'Target environment for THIS plan' line", func(s string) string {
			return strings.Replace(s, "**Target environment for THIS plan:** `non-prod`", "**Env:** dev", 1)
		}},
		{"target not prod/non-prod", "target environment is not declared as prod or non-prod", func(s string) string {
			return strings.Replace(s, "`non-prod`", "`staging`", 1)
		}},
		{"placeholder", "unfilled template placeholders remain", func(s string) string {
			return strings.Replace(s, "**Goal:** Ship the sample feature.", "**Goal:** [One sentence: what]", 1)
		}},
		{"non-prod plan names prod", "non-prod plan references prod resources", func(s string) string {
			return strings.Replace(s, "- revert the merge request", "- revert overlays/prod/app.yaml", 1)
		}},
		{"prod plan names non-prod", "prod plan references non-prod resources", func(s string) string {
			s = strings.Replace(s, "`non-prod`", "`prod`", 1)
			return strings.Replace(s, "- revert the merge request", "- copy from the nonprod cluster", 1)
		}},
		{"execution workspace", "missing 'Execution workspace' declaration", func(s string) string {
			return strings.Replace(s, "**Execution workspace:**", "**Branches:**", 1)
		}},
		{"L1 DoD per repo", "repo block(s) without a 'DoD:' line", func(s string) string {
			return strings.Replace(s, "- **DoD:** `make verify` run in `app/.worktrees/sample` -> PASS\n", "", 1)
		}},
		{"L2 repo count", "repo count mismatch: section 2 has 2 repo(s)", func(s string) string {
			return strings.Replace(s, "- read only: no-worktree: prerequisite, nothing written\n", "", 1)
		}},
		{"L3 DoD command", "DoD line(s) with no command in backticks", func(s string) string {
			return strings.Replace(s, "- **DoD:** `terraform plan` shows no diff", "- **DoD:** the plan shows no diff", 1)
		}},
		{"quality gate", "Plan Quality Gate has unchecked items", func(s string) string {
			return strings.Replace(s, "- [x] reviewed by a second pass", "- [ ] reviewed by a second pass", 1)
		}},
		{"status missing", "no '**Status:**' line", func(s string) string {
			return strings.Replace(s, "**Status:** approved\n", "", 1)
		}},
		{"status draft", "plan status is not executable: 'draft'", func(s string) string {
			return strings.Replace(s, "**Status:** approved", "**Status:** draft", 1)
		}},
		{"status blocked", "plan status is not executable", func(s string) string {
			return strings.Replace(s, "**Status:** approved", "**Status:** blocked until the vendor answers", 1)
		}},
		{"status stale", "plan is marked stale", func(s string) string {
			return strings.Replace(s, "**Status:** approved", "**Status:** stale", 1)
		}},
		{"status unknown", "unrecognized plan status: 'paused'", func(s string) string {
			return strings.Replace(s, "**Status:** approved", "**Status:** paused", 1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			content := tc.mutate(validPlan)
			if content == validPlan {
				t.Fatalf("fixture mutation did not apply")
			}
			path := writePlanFile(t, t.TempDir(), "sample.md", content)
			report, exit := PlanValidate(path, noPatterns(t))
			text := reportText(report)
			if exit != planExitInvalid {
				t.Fatalf("exit=%d want %d\n%s", exit, planExitInvalid, text)
			}
			if !strings.Contains(text, "FAIL: "+tc.want) {
				t.Fatalf("report does not name the rule %q:\n%s", tc.want, text)
			}
			if !strings.HasPrefix(report[len(report)-1], "RESULT: FAIL") {
				t.Fatalf("last line must be the RESULT line:\n%s", text)
			}
		})
	}
}

// Escapes: env-ok: on the line, and section 1 (the env matrix) is never scanned.
func TestPlanValidateEnvEscapes(t *testing.T) {
	content := strings.Replace(validPlan, "- revert the merge request",
		"- compare with overlays/prod/app.yaml env-ok: read-only reference", 1)
	path := writePlanFile(t, t.TempDir(), "sample.md", content)
	if report, exit := PlanValidate(path, noPatterns(t)); exit != planExitOK {
		t.Fatalf("env-ok line must pass: exit=%d\n%s", exit, reportText(report))
	}
}

// Every problem is reported, not just the first.
func TestPlanValidateReportsEveryProblem(t *testing.T) {
	content := strings.Replace(validPlan, "**Status:** approved", "**Status:** draft", 1)
	content = strings.Replace(content, "- [x] reviewed by a second pass", "- [ ] reviewed by a second pass", 1)
	path := writePlanFile(t, t.TempDir(), "sample.md", content)
	report, exit := PlanValidate(path, noPatterns(t))
	if exit != planExitInvalid || !strings.Contains(report[len(report)-1], "RESULT: FAIL (2 problem(s))") {
		t.Fatalf("want 2 problems:\n%s", reportText(report))
	}
}

func TestPlanValidateTemplateSkips(t *testing.T) {
	path := writePlanFile(t, t.TempDir(), planTemplateName, "**Status:** draft | approved\n")
	report, exit := PlanValidate(path, noPatterns(t))
	if exit != planExitOK || !strings.HasPrefix(report[0], "SKIP:") {
		t.Fatalf("template must skip and pass: exit=%d %v", exit, report)
	}
}

// Project patterns come from .harness/plankit/env-patterns next to the plan.
func TestPlanValidateProjectEnvPatterns(t *testing.T) {
	ws := t.TempDir()
	plans := filepath.Join(ws, ".harness", "plans", "stale")
	kit := filepath.Join(ws, ".harness", "plankit")
	for _, d := range []string{plans, kit} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	content := strings.Replace(validPlan, "- revert the merge request", "- revert release/live-cluster", 1)
	path := writePlanFile(t, plans, "sample.md", content)

	// No env-patterns file: the built-in defaults do not know release/live.
	if report, exit := PlanValidate(path, PlanOptions{}); exit != planExitOK {
		t.Fatalf("defaults must pass: %d\n%s", exit, reportText(report))
	}
	// The project's file teaches the gate its prod naming.
	writePlanFile(t, kit, "env-patterns", "# project-owned\nprod: release/live|overlays/prod\nnonprod: overlays/dev\n")
	report, exit := PlanValidate(path, PlanOptions{})
	if exit != planExitInvalid || !strings.Contains(reportText(report), "release/live-cluster") {
		t.Fatalf("project prod pattern must fail the plan: %d\n%s", exit, reportText(report))
	}
	// An explicit --env-patterns wins over the project file.
	if _, exit := PlanValidate(path, noPatterns(t)); exit != planExitOK {
		t.Fatalf("explicit patterns file must win")
	}
}

func TestPlanValidateMalformedEnvPatterns(t *testing.T) {
	path := writePlanFile(t, t.TempDir(), "sample.md", validPlan)
	cases := map[string]string{
		"unknown key":  "staging: foo\n",
		"no colon":     "prod overlays/prod\n",
		"empty regex":  "prod:\n",
		"duplicate":    "prod: a\nprod: b\n",
		"bad regex":    "prod: overlays/(prod\n",
		"missing file": "",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "env-patterns")
			if name != "missing file" {
				writePlanFile(t, filepath.Dir(f), "env-patterns", body)
			}
			report, exit := PlanValidate(path, PlanOptions{EnvPatternsFile: f})
			if exit != planExitInvalid || !strings.Contains(reportText(report), "env-patterns") {
				t.Fatalf("malformed patterns must fail closed: exit=%d\n%s", exit, reportText(report))
			}
		})
	}
}

func TestPlanUsageErrors(t *testing.T) {
	path := writePlanFile(t, t.TempDir(), "sample.md", validPlan)
	cases := map[string][]string{
		"missing --file":        {"plan", "validate"},
		"unexpected positional": {"plan", "validate", "--file", path, "stray"},
		"unknown verb":          {"plan", "bogus"},
		"no verb":               {"plan"},
		"save missing --file":   {"plan", "save"},
	}
	for desc, args := range cases {
		var out, errBuf bytes.Buffer
		if code := Run(args, &out, &errBuf); code != planExitUsage {
			t.Fatalf("%s: exit=%d want %d", desc, code, planExitUsage)
		}
	}
	patterns := noPatterns(t).EnvPatternsFile
	var out, errBuf bytes.Buffer
	if code := Run([]string{"plan", "validate", "--file", path, "--env-patterns", patterns}, &out, &errBuf); code != planExitOK {
		t.Fatalf("clean validate via Run: exit=%d\n%s%s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "RESULT: PASS") {
		t.Fatalf("clean validate should print a RESULT line, got %q", out.String())
	}
}

// save copies the validated plan byte-for-byte and never touches git.
func TestPlanSave(t *testing.T) {
	repoRoot := t.TempDir()
	draft := writePlanFile(t, t.TempDir(), "sample.md", validPlan)
	dest, report, exit := PlanSave(draft, repoRoot, noPatterns(t))
	if exit != planExitOK {
		t.Fatalf("save valid: exit=%d\n%s", exit, reportText(report))
	}
	if want := filepath.Join(repoRoot, ".harness", "plans", "sample.md"); dest != want {
		t.Fatalf("dest=%q want %q", dest, want)
	}
	saved, err := os.ReadFile(dest)
	if err != nil || string(saved) != validPlan {
		t.Fatalf("saved plan must equal the draft (err=%v)", err)
	}
	assertNoWorktreeOrGit(t, repoRoot)
}

func TestPlanSaveRefusesInvalidAndTemplate(t *testing.T) {
	repoRoot := t.TempDir()
	bad := writePlanFile(t, t.TempDir(), "sample.md", strings.Replace(validPlan, "**Status:** approved", "**Status:** draft", 1))
	if dest, _, exit := PlanSave(bad, repoRoot, noPatterns(t)); exit != planExitInvalid || dest != "" {
		t.Fatalf("invalid draft must be refused: exit=%d dest=%q", exit, dest)
	}
	tmpl := writePlanFile(t, t.TempDir(), planTemplateName, validPlan)
	if dest, _, exit := PlanSave(tmpl, repoRoot, noPatterns(t)); exit != planExitInvalid || dest != "" {
		t.Fatalf("template must not be saved as a plan: exit=%d dest=%q", exit, dest)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".harness", "plans")); !os.IsNotExist(err) {
		t.Fatalf("refused save must not create the plans dir (err=%v)", err)
	}
	assertNoWorktreeOrGit(t, repoRoot)
}

// assertNoWorktreeOrGit fails if any git or worktree artifact appears under root.
func assertNoWorktreeOrGit(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || strings.Contains(d.Name(), "worktree")) {
			t.Fatalf("save created a git/worktree artifact: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
