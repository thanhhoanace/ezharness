package evidence

// P2 ezlesson — stateless CLI verbs over reviewed repo files (.harness/lessons/).
// ezHarness OWNS `lesson lookup` + `lesson lint` and the shared path matcher; it
// never owns a store, lifecycle, or context injection (STOP-LINES, plan §P2).
//
// Store shape (v6.3 final, skills-standard): one lesson = one markdown file in
// .harness/lessons/ with YAML frontmatter:
//
//   ---
//   name: emfile-frontend-crashloop        # must equal the filename (sans .md)
//   description: Use when ... "verbatim error string" ...   # the rg retrieval key
//   paths:                                 # OPTIONAL — enables path-based lanes
//     - "frontend/**"                      #   (gate hint, lessons_matched)
//   ---
//   Fix:    <imperative>
//   Don't:  <trap>
//   Verify: <runnable command>
//
// Entries under _fixtures/ and any dot/underscore-prefixed names are not lessons
// (they hold retrieval-test inputs and metrics). README.md documents the vault
// and LESSONS.md is the generated index (v6.5 lesson sync) — neither is a lesson.
//
// v6.5: a lesson promoted from Claude Code auto memory keeps Claude's shape:
// `type:` (project|reference, usually nested under `metadata:`), no `paths:`,
// and a fact body ("**Why:** / **How to apply:**") instead of Fix/Don't/Verify.
// Such a lesson is knowledge, not a trap recipe, so the body lint skips it.
//
// Exit-code contract (plan §P2 CLI):
//   lookup: 0 ok (incl. clean no-match and missing vault) · 3 degraded (unreadable
//           file / bad frontmatter / bad glob — still prints what it resolved) ·
//           1 usage. NEVER 2 (reserved for the gate block).
//   lint:   0 clean · 3 problems found · 1 usage.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LessonFile is one parsed lesson: frontmatter plus its repo-relative path.
type LessonFile struct {
	Name        string
	Description string
	Paths       []string // optional; empty = pull-only lesson (rg by description)
	Type        string   // memory type when promoted from Claude memory (project|reference|...)
	RelPath     string   // repo-relative path of the .md file
}

// lookupResult is the JSON shape `lesson lookup --format json` emits.
type lookupResult struct {
	Status  string   `json:"status"` // ok | degraded
	Matches []string `json:"matches"`
	Errors  []string `json:"errors,omitempty"`
}

const (
	lessonStatusOK       = "ok"
	lessonStatusDegraded = "degraded"

	lessonExitOK       = 0
	lessonExitUsage    = 1
	lessonExitDegraded = 3
)

// parseLessonFrontmatter extracts name/description/paths from the leading YAML
// frontmatter block (between two `---` lines). Unknown keys are ignored so the
// format can grow without breaking old binaries.
func parseLessonFrontmatter(data []byte) (LessonFile, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return LessonFile{}, fmt.Errorf("missing frontmatter (file must start with ---)")
	}
	var lf LessonFile
	inPaths := false
	closed := false
	for _, raw := range lines[1:] {
		line := strings.TrimRight(raw, " \t\r")
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
		if inPaths && strings.HasPrefix(trimmed, "- ") {
			lf.Paths = append(lf.Paths, parseYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))))
			continue
		}
		inPaths = false
		if indented {
			// Nested under a parent key (Claude memory's metadata:). Only a nested
			// `type:` is read; any other nested line — including list items — is
			// skipped so it can never break the parse or override a top-level field.
			if key, value, ok := splitYAMLKeyValue(trimmed); ok && key == "type" && lf.Type == "" {
				lf.Type = strings.ToLower(parseYAMLScalar(value))
			}
			continue
		}
		key, value, ok := splitYAMLKeyValue(trimmed)
		if !ok {
			return LessonFile{}, fmt.Errorf("frontmatter line %q: expected key: value", trimmed)
		}
		switch key {
		case "type":
			lf.Type = strings.ToLower(parseYAMLScalar(value))
		case "name":
			lf.Name = parseYAMLScalar(value)
		case "description":
			lf.Description = parseYAMLScalar(value)
		case "paths":
			inPaths = true
			if v := strings.TrimSpace(value); v != "" && v != "[]" {
				lf.Paths = append(lf.Paths, parseYAMLList(value)...)
				inPaths = false
			}
		default:
			// ignore unknown keys (forward compatibility)
		}
	}
	if !closed {
		return LessonFile{}, fmt.Errorf("unterminated frontmatter (missing closing ---)")
	}
	return lf, nil
}

// isLessonEntry filters vault directory entries: markdown files only;
// underscore/dot prefixes are reserved for non-lesson content (_fixtures,
// metrics, editor droppings) and README.md documents the vault for humans.
func isLessonEntry(e os.DirEntry) bool {
	name := e.Name()
	return !e.IsDir() && strings.HasSuffix(name, ".md") &&
		!strings.HasPrefix(name, "_") && !strings.HasPrefix(name, ".") &&
		!strings.EqualFold(name, "README.md") && !strings.EqualFold(name, "LESSONS.md")
}

// isMemoryLesson reports a lesson promoted from Claude Code auto memory.
func isMemoryLesson(lf LessonFile) bool {
	switch lf.Type {
	case "project", "reference", "feedback", "user":
		return true
	}
	return false
}

// loadVault parses every lesson in dir. Missing dir is a valid empty vault.
// Per-file errors degrade the vault but never hide the healthy files.
func loadVault(dir string, repoRoot string) (lessons []LessonFile, errs []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no vault = clean v6.2-identical behaviour
		}
		return nil, []string{fmt.Sprintf("lessons dir unreadable: %v", err)}
	}
	for _, e := range entries {
		if !isLessonEntry(e) {
			continue
		}
		abs := filepath.Join(dir, e.Name())
		data, readErr := os.ReadFile(abs)
		if readErr != nil {
			errs = append(errs, fmt.Sprintf("lesson %q unreadable: %v", e.Name(), readErr))
			continue
		}
		lf, parseErr := parseLessonFrontmatter(data)
		if parseErr != nil {
			errs = append(errs, fmt.Sprintf("lesson %q: %v", e.Name(), parseErr))
			continue
		}
		rel, relErr := filepath.Rel(repoRoot, abs)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			rel = abs
		}
		lf.RelPath = filepath.ToSlash(rel)
		lessons = append(lessons, lf)
	}
	return lessons, errs
}

// LessonLookup resolves lessons whose `paths:` globs match any changed path.
// Pull-only lessons (no paths:) are never returned here — they are found by rg
// on the description, which is the host's pull lane, not the gate's.
func LessonLookup(vaultDir string, repoRoot string, changed []string) (lookupResult, int) {
	lessons, errs := loadVault(vaultDir, repoRoot)
	seen := map[string]bool{}
	var matches []string
	for _, lf := range lessons {
		for _, pat := range lf.Paths {
			if err := validateGlobPattern(pat); err != nil {
				errs = append(errs, fmt.Sprintf("lesson %q bad glob %q: %v", lf.Name, pat, err))
				continue
			}
			for _, f := range changed {
				if matchGlob(pat, f) {
					if !seen[lf.RelPath] {
						seen[lf.RelPath] = true
						matches = append(matches, lf.RelPath)
					}
					break
				}
			}
		}
	}
	sort.Strings(matches)
	result := lookupResult{Status: lessonStatusOK, Matches: matches, Errors: errs}
	if len(errs) > 0 {
		result.Status = lessonStatusDegraded
		return result, lessonExitDegraded
	}
	return result, lessonExitOK
}

// LessonLint runs the structural checks over the vault. Problems exit 3;
// warnings are non-fatal experiments (G14 breadth guard).
func LessonLint(vaultDir string, repoRoot string) (problems []string, warnings []string, exit int) {
	lessons, errs := loadVault(vaultDir, repoRoot)
	problems = append(problems, errs...)

	// Silent-death guard: a gitignored vault cannot be PR-reviewed and silently
	// stops travelling with the repo. Hard problem, not a warning.
	if ignored, why := vaultGitignored(vaultDir, repoRoot); ignored {
		problems = append(problems, fmt.Sprintf(
			"lessons dir is gitignored (%s) — fix .gitignore: use `.harness/*` + `!.harness/lessons/` (a child cannot be re-included when the parent dir itself is excluded)", why))
	}

	seenName := map[string]bool{}
	repoFiles := gitLsFiles(repoRoot)
	for _, lf := range lessons {
		base := strings.TrimSuffix(filepath.Base(lf.RelPath), ".md")
		if lf.Name == "" {
			problems = append(problems, fmt.Sprintf("lesson %q: missing name", lf.RelPath))
		} else if lf.Name != base {
			problems = append(problems, fmt.Sprintf("lesson %q: name %q must equal filename %q", lf.RelPath, lf.Name, base))
		}
		if seenName[lf.Name] {
			problems = append(problems, fmt.Sprintf("duplicate lesson name %q", lf.Name))
		}
		seenName[lf.Name] = true
		if len(strings.TrimSpace(lf.Description)) < 20 {
			problems = append(problems, fmt.Sprintf("lesson %q: description missing or too short to retrieve (<20 chars)", lf.Name))
		}
		for _, pat := range lf.Paths {
			if err := validateGlobPattern(pat); err != nil {
				problems = append(problems, fmt.Sprintf("lesson %q glob %q out of safe subset: %v", lf.Name, pat, err))
				continue
			}
			if len(repoFiles) > 0 {
				hits := 0
				for _, rf := range repoFiles {
					if matchGlob(pat, rf) {
						hits++
					}
				}
				if hits*5 > len(repoFiles) {
					warnings = append(warnings, fmt.Sprintf("lesson %q pattern %q matches %d/%d repo files (>20%%) — likely too broad", lf.Name, pat, hits, len(repoFiles)))
				}
			}
		}
		abs := filepath.Join(repoRoot, filepath.FromSlash(lf.RelPath))
		body, readErr := os.ReadFile(abs)
		if readErr == nil && !isMemoryLesson(lf) {
			problems = append(problems, lintLessonBody(lf.Name, string(body))...)
		}
	}

	// Fixture provenance (Codex review 2026-08-11, structured round-3): a
	// fixture whose raw-log source is not declared cannot be told apart from a
	// transcript reconstruction. Undeclared/legacy headers WARN (legacy
	// fixtures must not brick a vault), but strong claims (raw,
	// controlled-reproduction) must carry a complete, verifiable evidence
	// chain — see lintFixtureProvenance. Distinct from lesson-BODY provenance,
	// which stays never-required (G19/G20).
	fixtures, _ := filepath.Glob(filepath.Join(vaultDir, "_fixtures", "*.log"))
	for _, fx := range fixtures {
		data, readErr := os.ReadFile(fx)
		if readErr != nil {
			continue
		}
		fxProblems, fxWarnings := lintFixtureProvenance(filepath.Base(fx), string(data), repoRoot)
		problems = append(problems, fxProblems...)
		warnings = append(warnings, fxWarnings...)
	}

	exit = lessonExitOK
	if len(problems) > 0 {
		exit = lessonExitDegraded
	}
	return problems, warnings, exit
}

// validProvenanceStatuses are the allowed values of the structured
// `# provenance_status:` fixture header (round-3 measurement-honesty,
// ADR-0007): raw = untouched captured log · controlled-reproduction = re-run
// of the real failure captured fresh · reconstructed = written from
// memory/transcript (weakest claim, but declared).
var validProvenanceStatuses = map[string]bool{
	"raw":                     true,
	"controlled-reproduction": true,
	"reconstructed":           true,
}

// lintFixtureProvenance checks one fixture's structured provenance header:
//
//	# provenance_status: raw | controlled-reproduction | reconstructed
//	# evidence_ref: <path relative to repo root>
//	# evidence_sha256: <64 hex chars of the referenced file>
//	# captured_at: <YYYY-MM-DD or RFC3339>
//	# redaction: none | applied
//
// Strong claims (raw, controlled-reproduction) promise a verifiable capture,
// so the FULL evidence chain is required: evidence_ref resolving to an
// existing file, a matching 64-hex evidence_sha256, a parseable captured_at,
// and an explicit redaction value. Each missing/invalid item is a PROBLEM
// (degraded exit).
//
// `reconstructed` is an honesty label — the fixture was written from
// memory/transcript, the weakest claim — and can never close release gate 4;
// that policy lives in ADR-0007 (docs/adr/0007-index-conformance-naming.md). Valid status alone is silent; optional
// evidence fields are validated when present (sha mismatch and path escapes
// stay problems; a missing ref target is only a warning).
//
// Capture origin vs redaction/transformation: the checksum proves integrity
// of the REFERENCED file only. It does not prove semantic equivalence between
// the fixture's excerpt and the raw log — redaction/trimming between the two
// is declared by the `redaction` field, not verified by the hash.
func lintFixtureProvenance(name string, content string, repoRoot string) (problems []string, warnings []string) {
	fields := parseFixtureHeader(content)
	status, hasStatus := fields["provenance_status"]
	_, hasLegacy := fields["provenance"]
	strong := false
	switch {
	case hasStatus:
		if !validProvenanceStatuses[status] {
			warnings = append(warnings, fmt.Sprintf("fixture %q has invalid provenance_status %q — allowed values: raw | controlled-reproduction | reconstructed", name, status))
		} else if status == "raw" || status == "controlled-reproduction" {
			strong = true
		}
	case hasLegacy:
		warnings = append(warnings, fmt.Sprintf("fixture %q uses the legacy '# provenance:' line — switch to the structured '# provenance_status:' header (raw | controlled-reproduction | reconstructed)", name))
	default:
		warnings = append(warnings, fmt.Sprintf("fixture %q has no '# provenance_status:' header — declare the raw-log source (or mark it reconstructed)", name))
	}

	ref := fields["evidence_ref"]
	wantSum := fields["evidence_sha256"]
	capturedAt := fields["captured_at"]
	redaction := fields["redaction"]

	if strong {
		if ref == "" {
			problems = append(problems, fmt.Sprintf("fixture %q: provenance_status %q requires evidence_ref (path to the captured log under the repo root)", name, status))
		}
		if wantSum == "" {
			problems = append(problems, fmt.Sprintf("fixture %q: provenance_status %q requires evidence_sha256 (64 hex chars of the referenced file)", name, status))
		}
		if capturedAt == "" {
			problems = append(problems, fmt.Sprintf("fixture %q: provenance_status %q requires captured_at (YYYY-MM-DD or RFC3339)", name, status))
		}
		if redaction == "" {
			problems = append(problems, fmt.Sprintf("fixture %q: provenance_status %q requires redaction (none | applied)", name, status))
		}
	}
	if wantSum != "" && !isHex64(wantSum) {
		problems = append(problems, fmt.Sprintf("fixture %q: evidence_sha256 %q is not 64 hex chars", name, wantSum))
	}
	if capturedAt != "" && !validCapturedAt(capturedAt) {
		problems = append(problems, fmt.Sprintf("fixture %q: captured_at %q is not YYYY-MM-DD or RFC3339", name, capturedAt))
	}
	if redaction != "" && redaction != "none" && redaction != "applied" {
		problems = append(problems, fmt.Sprintf("fixture %q: redaction %q must be one of none | applied", name, redaction))
	}

	if ref != "" {
		// Path safety for ANY status: evidence_ref is attacker-adjacent input
		// (it arrives via PR); it must never read outside the repo root.
		escapes, evalErr := evidenceRefEscapes(repoRoot, ref)
		if evalErr != nil {
			problems = append(problems, fmt.Sprintf("fixture %q: evidence_ref %q could not be safely resolved: %v", name, ref, evalErr))
			return problems, warnings
		}
		if escapes {
			problems = append(problems, fmt.Sprintf("fixture %q: evidence_ref %q escapes the repository root", name, ref))
			return problems, warnings
		}
		abs := filepath.Join(repoRoot, filepath.FromSlash(ref))
		data, readErr := os.ReadFile(abs)
		if readErr != nil {
			if strong {
				problems = append(problems, fmt.Sprintf("fixture %q: evidence_ref %q does not resolve to an existing file — provenance_status %q requires the captured evidence to be present", name, ref, status))
			} else {
				warnings = append(warnings, fmt.Sprintf("fixture %q evidence_ref %q not found under repo root (evidence may be gitignored or rotated)", name, ref))
			}
		} else if wantSum != "" && isHex64(wantSum) {
			gotSum := fmt.Sprintf("%x", sha256.Sum256(data))
			if !strings.EqualFold(gotSum, wantSum) {
				problems = append(problems, fmt.Sprintf("fixture %q evidence_sha256 mismatch for %q: header claims %s, file hashes to %s — integrity claim broken", name, ref, wantSum, gotSum))
			}
		}
	}
	return problems, warnings
}

// isHex64 reports whether s is exactly 64 hexadecimal characters.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// validCapturedAt accepts a plain ISO date (YYYY-MM-DD) or a full RFC3339
// timestamp.
func validCapturedAt(v string) bool {
	if _, err := time.Parse("2006-01-02", v); err == nil {
		return true
	}
	if _, err := time.Parse(time.RFC3339, v); err == nil {
		return true
	}
	return false
}

// evidenceRefEscapes reports whether ref, joined onto repoRoot, resolves
// outside the repository root. Absolute paths and lexical `..` traversal are
// rejected outright; otherwise symlinks are resolved (EvalSymlinks on the
// deepest existing ancestor, or the file itself when it exists) and the
// result must stay under the resolved repoRoot.
func evidenceRefEscapes(repoRoot string, ref string) (bool, error) {
	if filepath.IsAbs(ref) || filepath.IsAbs(filepath.FromSlash(ref)) {
		return true, nil
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(ref)))
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".." {
			return true, nil
		}
	}
	// The root must be ABSOLUTE before symlink resolution: with a relative
	// root (the CLI default `--repo-root .`), EvalSymlinks returns a relative
	// path and the prefix containment check below can never match, so every
	// existing evidence_ref was falsely judged escaping.
	rootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return false, err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return false, err
	}
	abs := filepath.Join(rootReal, filepath.FromSlash(ref))
	// Resolve symlinks on the deepest existing path component, then re-append
	// the not-yet-existing remainder lexically.
	probe := abs
	for {
		resolved, evalErr := filepath.EvalSymlinks(probe)
		if evalErr == nil {
			final := resolved
			if probe != abs {
				rel, relErr := filepath.Rel(probe, abs)
				if relErr != nil {
					return false, relErr
				}
				final = filepath.Join(resolved, rel)
			}
			within := final == rootReal ||
				strings.HasPrefix(final, rootReal+string(filepath.Separator))
			return !within, nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			// Nothing on the path exists at all — cannot escape via symlink,
			// and the lexical checks above already passed.
			return false, nil
		}
		probe = parent
	}
}

// parseFixtureHeader collects `# key: value` comment lines anywhere in a
// fixture into a map (first occurrence wins). Non-comment log lines are
// ignored, so the header can sit above or between raw log content.
func parseFixtureHeader(content string) map[string]string {
	fields := map[string]string{}
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		idx := strings.Index(body, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(body[:idx])
		value := strings.TrimSpace(body[idx+1:])
		if strings.ContainsAny(key, " \t") {
			continue // "# some prose: with a colon" is log noise, not a header
		}
		if _, seen := fields[key]; !seen {
			fields[key] = value
		}
	}
	return fields
}

// lintLessonBody checks the entry body: Fix/Do, Don't, and a runnable Verify.
// Provenance lives in an HTML comment and is NEVER required (G19/G20).
func lintLessonBody(name string, body string) []string {
	low := strings.ToLower(body)
	var errs []string
	if !strings.Contains(low, "fix:") && !strings.Contains(low, "do:") {
		errs = append(errs, fmt.Sprintf("lesson %q lacks a 'Fix:' (or 'Do:') line", name))
	}
	if !strings.Contains(low, "don't:") && !strings.Contains(low, "dont:") {
		errs = append(errs, fmt.Sprintf("lesson %q lacks a \"Don't:\" line", name))
	}
	if !strings.Contains(low, "verify:") && !strings.Contains(low, "verification:") {
		errs = append(errs, fmt.Sprintf("lesson %q lacks a 'Verify:' line", name))
	}
	return errs
}

// vaultGitignored reports whether the vault dir is excluded by gitignore rules.
// Best-effort: no git, no repo, or no vault dir → not ignored.
func vaultGitignored(vaultDir string, repoRoot string) (bool, string) {
	if strings.TrimSpace(repoRoot) == "" {
		return false, ""
	}
	if _, err := os.Stat(vaultDir); err != nil {
		return false, ""
	}
	cmd := exec.Command("git", "check-ignore", "-q", vaultDir)
	cmd.Dir = repoRoot
	if err := cmd.Run(); err == nil {
		return true, vaultDir
	}
	return false, ""
}

// gitLsFiles returns tracked repo-relative paths (best-effort; empty on error).
func gitLsFiles(repoRoot string) []string {
	if strings.TrimSpace(repoRoot) == "" {
		return nil
	}
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			files = append(files, t)
		}
	}
	return files
}
