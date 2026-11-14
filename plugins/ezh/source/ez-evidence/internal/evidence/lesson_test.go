package evidence

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const goodBody = `Fix:    invalidate the entitlement cache key during revoke
Don't:  rely only on TTL expiry
Verify: revoke integration test passes
<!-- since 2026-07-08 -->
`

func writeLesson(t *testing.T, dir string, filename string, frontmatter string, body string) {
	t.Helper()
	content := "---\n" + frontmatter + "---\n" + body
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildVaultRepo lays out a repo-root with a .harness/lessons vault:
// one path-scoped lesson, one pull-only lesson (no paths).
func buildVaultRepo(t *testing.T) (repoRoot string, vaultDir string) {
	t.Helper()
	repoRoot = t.TempDir()
	vaultDir = filepath.Join(repoRoot, ".harness", "lessons")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLesson(t, vaultDir, "entitlements-revoke-cache.md",
		"name: entitlements-revoke-cache\ndescription: Use when user remains entitled for up to 60s after revocation\npaths:\n  - \"clawfans-api/**/entitlements/**\"\n",
		goodBody)
	writeLesson(t, vaultDir, "emfile-frontend-crashloop.md",
		"name: emfile-frontend-crashloop\ndescription: Use when pod CrashLoopBackOff with \"EMFILE: too many open files\"\n",
		goodBody)
	return repoRoot, vaultDir
}

// AC-S1: lookup resolves path-scoped lessons via the shared matcher; pull-only
// lessons (no paths:) are never returned by the path lane.
func TestLessonLookupResolves(t *testing.T) {
	repoRoot, vaultDir := buildVaultRepo(t)

	result, exit := LessonLookup(vaultDir, repoRoot, []string{"clawfans-api/internal/entitlements/cache.go"})
	if exit != lessonExitOK || result.Status != lessonStatusOK {
		t.Fatalf("lookup exit=%d status=%s errs=%v", exit, result.Status, result.Errors)
	}
	want := ".harness/lessons/entitlements-revoke-cache.md"
	if len(result.Matches) != 1 || result.Matches[0] != want {
		t.Fatalf("matches = %v, want [%s]", result.Matches, want)
	}

	// Clean no-match is still exit 0 (ok).
	none, exit := LessonLookup(vaultDir, repoRoot, []string{"README.md"})
	if exit != lessonExitOK || len(none.Matches) != 0 {
		t.Fatalf("no-match lookup exit=%d matches=%v, want exit 0 empty", exit, none.Matches)
	}
}

// A missing vault dir is a valid empty vault: exit 0, no matches, no errors
// (repos without lessons must stay byte-identical to v6.2 behaviour).
func TestLessonLookupMissingVaultIsClean(t *testing.T) {
	repoRoot := t.TempDir()
	result, exit := LessonLookup(filepath.Join(repoRoot, ".harness", "lessons"), repoRoot, []string{"a.go"})
	if exit != lessonExitOK || len(result.Matches) != 0 || len(result.Errors) != 0 {
		t.Fatalf("missing vault: exit=%d result=%+v, want clean ok", exit, result)
	}
}

// AC-S2: a broken lesson file → degraded exit 3 with the error surfaced, while
// healthy lessons still resolve. NEVER exit 2.
func TestLessonLookupDegradedBrokenFile(t *testing.T) {
	repoRoot, vaultDir := buildVaultRepo(t)
	if err := os.WriteFile(filepath.Join(vaultDir, "broken.md"), []byte("no frontmatter here"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, exit := LessonLookup(vaultDir, repoRoot, []string{"clawfans-api/internal/entitlements/cache.go"})
	if exit != lessonExitDegraded || result.Status != lessonStatusDegraded {
		t.Fatalf("exit=%d status=%s, want degraded 3", exit, result.Status)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("degraded lookup should still resolve healthy lessons, got %v", result.Matches)
	}
	if len(result.Errors) == 0 || !strings.Contains(result.Errors[0], "broken.md") {
		t.Fatalf("errors should name the broken file, got %v", result.Errors)
	}
}

// _fixtures and underscore-prefixed files are not lessons.
func TestLessonVaultSkipsFixtures(t *testing.T) {
	repoRoot, vaultDir := buildVaultRepo(t)
	fixtures := filepath.Join(vaultDir, "_fixtures")
	if err := os.MkdirAll(fixtures, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "_notes.md"), []byte("not a lesson"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "README.md"), []byte("vault docs for humans — no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, exit := LessonLookup(vaultDir, repoRoot, []string{"clawfans-api/internal/entitlements/cache.go"})
	if exit != lessonExitOK || len(result.Errors) != 0 {
		t.Fatalf("underscore entries must be skipped, got exit=%d errs=%v", exit, result.Errors)
	}
}

// AC-S3: lint catches structural problems; a clean vault is exit 0.
func TestLessonLint(t *testing.T) {
	repoRoot, vaultDir := buildVaultRepo(t)

	problems, _, exit := LessonLint(vaultDir, repoRoot)
	if exit != lessonExitOK || len(problems) != 0 {
		t.Fatalf("clean vault: exit=%d problems=%v", exit, problems)
	}

	// name != filename
	writeLesson(t, vaultDir, "wrong-name.md",
		"name: something-else\ndescription: Use when this description is long enough to retrieve\n", goodBody)
	// description too short
	writeLesson(t, vaultDir, "short-desc.md",
		"name: short-desc\ndescription: nope\n", goodBody)
	// bad glob + body missing Verify
	writeLesson(t, vaultDir, "bad-glob.md",
		"name: bad-glob\ndescription: Use when a glob outside the safe subset is present\npaths:\n  - \"src/{a,b}/**\"\n",
		"Fix: x\nDon't: y\n")

	problems, _, exit = LessonLint(vaultDir, repoRoot)
	if exit != lessonExitDegraded {
		t.Fatalf("exit=%d, want 3", exit)
	}
	joined := strings.Join(problems, "\n")
	for _, want := range []string{
		"must equal filename",
		"description missing or too short",
		"out of safe subset",
		"lacks a 'Verify:' line",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("problems missing %q:\n%s", want, joined)
		}
	}
}

// Silent-death guard: a gitignored vault is a lint PROBLEM (exit 3).
func TestLessonLintGitignoredVault(t *testing.T) {
	repoRoot, vaultDir := buildVaultRepo(t)
	runGit := func(args ...string) {
		t.Helper()
		if out, err := gitCommand(repoRoot, args...); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	runGit("init", "-q")
	if err := os.WriteFile(filepath.Join(repoRoot, ".gitignore"), []byte(".harness/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	problems, _, exit := LessonLint(vaultDir, repoRoot)
	if exit != lessonExitDegraded {
		t.Fatalf("gitignored vault must fail lint, exit=%d problems=%v", exit, problems)
	}
	if !strings.Contains(strings.Join(problems, "\n"), "gitignored") {
		t.Fatalf("problems should mention gitignore, got %v", problems)
	}

	// The documented fix pattern clears the problem.
	if err := os.WriteFile(filepath.Join(repoRoot, ".gitignore"), []byte(".harness/*\n!.harness/lessons/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, _, exit = LessonLint(vaultDir, repoRoot)
	if exit != lessonExitOK {
		t.Fatalf("whitelisted vault should pass, exit=%d problems=%v", exit, problems)
	}
}

func gitCommand(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Fixture provenance (structured, round-3 + release hardening): strong claims
// (raw, controlled-reproduction) require the full evidence chain —
// evidence_ref resolving to an existing in-repo file, matching 64-hex
// evidence_sha256, parseable captured_at, explicit redaction — each gap is a
// PROBLEM. `reconstructed` alone is silent. Invalid values, the legacy
// `# provenance:` line, and a missing header WARN. evidence_ref path escapes
// (absolute, `..`, symlink out of root) are PROBLEMS for any status.
// Lesson-body provenance stays never-required (G19/G20) — this only covers
// _fixtures/*.log.
func TestLessonLintFixtureProvenance(t *testing.T) {
	writeFixture := func(t *testing.T, vaultDir, name, content string) {
		t.Helper()
		fxDir := filepath.Join(vaultDir, "_fixtures")
		if err := os.MkdirAll(fxDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fxDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	warningsFor := func(warnings []string, name string) []string {
		var out []string
		for _, w := range warnings {
			if strings.Contains(w, name) {
				out = append(out, w)
			}
		}
		return out
	}

	t.Run("reconstructed status alone is silent", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		writeFixture(t, vaultDir, "ok-reconstructed.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: reconstructed\nsome error\n")
		problems, warnings, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitOK || len(problems) != 0 || len(warnings) != 0 {
			t.Fatalf("reconstructed alone must be silent: exit=%d problems=%v warnings=%v", exit, problems, warnings)
		}
	})

	t.Run("reconstructed with captured_at and redaction has no problems", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		writeFixture(t, vaultDir, "ok-reconstructed-meta.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: reconstructed\n# captured_at: 2026-08-11\n# redaction: none\nsome error\n")
		problems, warnings, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitOK || len(problems) != 0 || len(warnings) != 0 {
			t.Fatalf("reconstructed with valid optional fields must be clean: exit=%d problems=%v warnings=%v", exit, problems, warnings)
		}
	})

	t.Run("raw with full evidence chain is silent", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		evidence := []byte("real captured output\n")
		if err := os.WriteFile(filepath.Join(repoRoot, "proof.log"), evidence, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(evidence))
		writeFixture(t, vaultDir, "ok-raw-chain.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: raw\n# evidence_ref: proof.log\n# evidence_sha256: "+sum+"\n# captured_at: 2026-08-11T09:30:00Z\n# redaction: none\nsome error\n")
		problems, warnings, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitOK || len(problems) != 0 || len(warnings) != 0 {
			t.Fatalf("full raw chain must be silent: exit=%d problems=%v warnings=%v", exit, problems, warnings)
		}
	})

	// Regression (round-5): the CLI default is `--repo-root .`. Containment
	// used to compare symlink-resolved absolute ref paths against an
	// UNRESOLVED relative root, so every existing evidence_ref was falsely
	// judged escaping when lint ran from inside the repo.
	t.Run("raw chain accepted with relative repo root", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		evidence := []byte("real captured output\n")
		if err := os.WriteFile(filepath.Join(repoRoot, "proof.log"), evidence, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(evidence))
		writeFixture(t, vaultDir, "ok-raw-relroot.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: raw\n# evidence_ref: proof.log\n# evidence_sha256: "+sum+"\n# captured_at: 2026-08-11\n# redaction: none\nsome error\n")
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(repoRoot); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chdir(cwd); err != nil {
				t.Fatal(err)
			}
		})
		problems, warnings, exit := LessonLint(filepath.Join(".harness", "lessons"), ".")
		if exit != lessonExitOK || len(problems) != 0 || len(warnings) != 0 {
			t.Fatalf("full raw chain with repo-root '.' must be silent: exit=%d problems=%v warnings=%v", exit, problems, warnings)
		}
	})

	t.Run("raw missing evidence_ref is a problem", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		writeFixture(t, vaultDir, "raw-noref.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: raw\n# captured_at: 2026-08-11\n# redaction: none\nsome error\n")
		problems, _, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitDegraded {
			t.Fatalf("raw without evidence_ref must degrade, exit=%d problems=%v", exit, problems)
		}
		joined := strings.Join(problems, "\n")
		if !strings.Contains(joined, "evidence_ref") || !strings.Contains(joined, "raw-noref.log") {
			t.Fatalf("problems should name evidence_ref and the fixture, got %v", problems)
		}
	})

	t.Run("raw with ref but missing sha is a problem", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		if err := os.WriteFile(filepath.Join(repoRoot, "proof.log"), []byte("real captured output\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, vaultDir, "raw-nosha.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: raw\n# evidence_ref: proof.log\n# captured_at: 2026-08-11\n# redaction: none\nsome error\n")
		problems, _, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitDegraded {
			t.Fatalf("raw without evidence_sha256 must degrade, exit=%d problems=%v", exit, problems)
		}
		if !strings.Contains(strings.Join(problems, "\n"), "evidence_sha256") {
			t.Fatalf("problems should name evidence_sha256, got %v", problems)
		}
	})

	t.Run("invalid captured_at is a problem", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		evidence := []byte("real captured output\n")
		if err := os.WriteFile(filepath.Join(repoRoot, "proof.log"), evidence, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(evidence))
		writeFixture(t, vaultDir, "bad-date.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: raw\n# evidence_ref: proof.log\n# evidence_sha256: "+sum+"\n# captured_at: yesterday-ish\n# redaction: none\nsome error\n")
		problems, _, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitDegraded {
			t.Fatalf("invalid captured_at must degrade, exit=%d problems=%v", exit, problems)
		}
		if !strings.Contains(strings.Join(problems, "\n"), "captured_at") {
			t.Fatalf("problems should name captured_at, got %v", problems)
		}
	})

	t.Run("invalid redaction is a problem", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		evidence := []byte("real captured output\n")
		if err := os.WriteFile(filepath.Join(repoRoot, "proof.log"), evidence, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(evidence))
		writeFixture(t, vaultDir, "bad-redaction.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: raw\n# evidence_ref: proof.log\n# evidence_sha256: "+sum+"\n# captured_at: 2026-08-11\n# redaction: heavily\nsome error\n")
		problems, _, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitDegraded {
			t.Fatalf("invalid redaction must degrade, exit=%d problems=%v", exit, problems)
		}
		if !strings.Contains(strings.Join(problems, "\n"), "redaction") {
			t.Fatalf("problems should name redaction, got %v", problems)
		}
	})

	t.Run("evidence_ref traversal escapes are problems", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		writeFixture(t, vaultDir, "escape-dots.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: reconstructed\n# evidence_ref: ../escape\nsome error\n")
		writeFixture(t, vaultDir, "escape-abs.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: reconstructed\n# evidence_ref: /etc/passwd\nsome error\n")
		problems, _, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitDegraded {
			t.Fatalf("path escapes must degrade, exit=%d problems=%v", exit, problems)
		}
		joined := strings.Join(problems, "\n")
		for _, needle := range []string{"escape-dots.log", "escape-abs.log"} {
			if !strings.Contains(joined, needle) {
				t.Fatalf("problems should flag %s escaping the repository root, got %v", needle, problems)
			}
		}
		if !strings.Contains(joined, "escapes the repository root") {
			t.Fatalf("problems should say 'escapes the repository root', got %v", problems)
		}
	})

	t.Run("invalid status warns naming allowed values", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		writeFixture(t, vaultDir, "bad-status.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: vibes\nsome error\n")
		problems, warnings, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitOK || len(problems) != 0 {
			t.Fatalf("invalid status must stay a warning: exit=%d problems=%v", exit, problems)
		}
		ws := warningsFor(warnings, "bad-status.log")
		if len(ws) != 1 || !strings.Contains(ws[0], "raw | controlled-reproduction | reconstructed") {
			t.Fatalf("want one warning naming allowed values, got %v", warnings)
		}
	})

	t.Run("legacy provenance line warns toward structured header", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		writeFixture(t, vaultDir, "legacy.log",
			"expect: emfile-frontend-crashloop\n# provenance: kubectl logs 2026-08-11\nsome error\n")
		problems, warnings, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitOK || len(problems) != 0 {
			t.Fatalf("legacy line must stay a warning: exit=%d problems=%v", exit, problems)
		}
		ws := warningsFor(warnings, "legacy.log")
		if len(ws) != 1 || !strings.Contains(ws[0], "provenance_status") {
			t.Fatalf("want one warning suggesting provenance_status, got %v", warnings)
		}
	})

	t.Run("no header at all warns undeclared source", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		writeFixture(t, vaultDir, "no-prov.log", "expect: emfile-frontend-crashloop\nsome error\n")
		problems, warnings, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitOK || len(problems) != 0 {
			t.Fatalf("missing header must stay a warning: exit=%d problems=%v", exit, problems)
		}
		if ws := warningsFor(warnings, "no-prov.log"); len(ws) != 1 {
			t.Fatalf("want exactly one warning for no-prov.log, got %v", warnings)
		}
	})

	t.Run("evidence_ref missing file warns for reconstructed", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		writeFixture(t, vaultDir, "gone-ref.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: reconstructed\n# evidence_ref: .harness/evidence/proofs/rotated.log\nsome error\n")
		problems, warnings, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitOK || len(problems) != 0 {
			t.Fatalf("missing evidence file must stay a warning: exit=%d problems=%v", exit, problems)
		}
		ws := warningsFor(warnings, "gone-ref.log")
		if len(ws) != 1 || !strings.Contains(ws[0], "evidence_ref") {
			t.Fatalf("want one evidence_ref warning, got %v", warnings)
		}
	})

	t.Run("evidence sha mismatch is a problem", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		if err := os.WriteFile(filepath.Join(repoRoot, "proof.log"), []byte("real captured output\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, vaultDir, "bad-sha.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: raw\n# evidence_ref: proof.log\n# evidence_sha256: "+strings.Repeat("0", 64)+"\n# captured_at: 2026-08-11\n# redaction: none\nsome error\n")
		problems, _, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitDegraded {
			t.Fatalf("sha mismatch must degrade, exit=%d problems=%v", exit, problems)
		}
		if !strings.Contains(strings.Join(problems, "\n"), "integrity claim broken") {
			t.Fatalf("problems should flag the broken integrity claim, got %v", problems)
		}
	})

	t.Run("evidence sha match is silent", func(t *testing.T) {
		repoRoot, vaultDir := buildVaultRepo(t)
		evidence := []byte("real captured output\n")
		if err := os.WriteFile(filepath.Join(repoRoot, "proof.log"), evidence, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(evidence))
		writeFixture(t, vaultDir, "good-sha.log",
			"expect: emfile-frontend-crashloop\n# provenance_status: raw\n# evidence_ref: proof.log\n# evidence_sha256: "+sum+"\n# captured_at: 2026-08-11\n# redaction: none\nsome error\n")
		problems, warnings, exit := LessonLint(vaultDir, repoRoot)
		if exit != lessonExitOK || len(problems) != 0 || len(warnings) != 0 {
			t.Fatalf("matching sha must be fully silent: exit=%d problems=%v warnings=%v", exit, problems, warnings)
		}
	})
}

// v6.5 lesson sync: a lesson promoted from Claude auto memory keeps Claude's
// own shape — `type:` nested under `metadata:`, no `paths:`, a fact body with
// no Fix/Don't/Verify lines. Lint must accept it; the generated LESSONS.md
// index is not a lesson and must be skipped by lint and lookup.
func TestLessonLintAcceptsPromotedMemory(t *testing.T) {
	repoRoot, vaultDir := buildVaultRepo(t)
	writeLesson(t, vaultDir, "kaniko-no-push-env-beats-flag.md",
		"name: kaniko-no-push-env-beats-flag\n"+
			"description: \"kaniko reads KANIKO_NO_PUSH from the environment and it overrides the --no-push flag\"\n"+
			"metadata:\n  node_type: memory\n  type: project\n  modified: 2026-09-16T07:33:44.457Z\n  tags:\n    - kaniko\n    - ci\n",
		"\nThree projects still set the env var.\n\n**Why:** the flag alone does not stop a push.\n")
	if err := os.WriteFile(filepath.Join(vaultDir, "LESSONS.md"),
		[]byte("# Project lessons\n\n- [kaniko-no-push-env-beats-flag](kaniko-no-push-env-beats-flag.md) — kaniko ...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	problems, _, exit := LessonLint(vaultDir, repoRoot)
	if exit != lessonExitOK || len(problems) != 0 {
		t.Fatalf("lint exit=%d problems=%v", exit, problems)
	}
	lessons, errs := loadVault(vaultDir, repoRoot)
	if len(errs) != 0 {
		t.Fatalf("loadVault errs=%v", errs)
	}
	for _, lf := range lessons {
		if strings.HasSuffix(lf.RelPath, "LESSONS.md") {
			t.Fatalf("LESSONS.md was loaded as a lesson: %+v", lf)
		}
		if lf.Name == "kaniko-no-push-env-beats-flag" && lf.Type != "project" {
			t.Fatalf("nested metadata.type not parsed: %+v", lf)
		}
	}
	result, exit := LessonLookup(vaultDir, repoRoot, []string{"LESSONS.md", ".harness/lessons/LESSONS.md"})
	if exit != lessonExitOK || result.Status != lessonStatusOK || len(result.Errors) != 0 {
		t.Fatalf("lookup exit=%d status=%s errs=%v", exit, result.Status, result.Errors)
	}

	// A trap lesson (no memory type) still needs Fix/Don't/Verify.
	writeLesson(t, vaultDir, "bare-trap.md",
		"name: bare-trap\ndescription: Use when the build fails with \"a verbatim error line here\"\n",
		"\nJust prose.\n")
	problems, _, exit = LessonLint(vaultDir, repoRoot)
	if exit != lessonExitDegraded || !strings.Contains(strings.Join(problems, "\n"), "bare-trap") {
		t.Fatalf("trap lesson without Fix/Verify must still fail: exit=%d problems=%v", exit, problems)
	}
}
