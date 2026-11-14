package evidence

// Plan gate (ADR-0011, supersedes the frontmatter schema of ADR-0008).
// `ez plan validate|save` checks the SHAPE of a human-owned plan file with the
// plankit rules, fail-closed. It NEVER runs or drives a plan (ADR-0001): no
// plan FSM, no worktree provisioning, no git writes.
//
// Rules (ported 1:1 from the plankit plan-lint.sh, same semantics):
//   1. required sections: Environment Scope, Multi-Repo Impact, Test Plan,
//      Tasks, Rollback, Plan Quality Gate (`^##+ .*<name>`, case-insensitive)
//   2. a "Target environment" line that says prod or non-prod
//   3. no unfilled template placeholders
//   4. no environment cross-contamination from section 2 on (`env-ok:` escape);
//      the patterns are PROJECT-OWNED (.harness/plankit/env-patterns)
//   5. an "Execution workspace" declaration
//      L1 every `### Repo:` block has a DoD: line
//      L2 repo blocks written to == Execution workspace lines (`no-worktree:` escape)
//      L3 every DoD: line names a command in backticks
//   6. Plan Quality Gate fully checked
//   7. **Status:** approved | in-progress | done (draft/blocked/stale/unknown fail)
// The template file itself (PLAN-TEMPLATE.md) is skipped and passes.
//
// Case sensitivity is deliberately mixed, exactly as in plan-lint.sh: rules
// 1/2/5/7 ignore case; L1 `### Repo:`, L2's `Execution workspace` and rule 6's
// `Plan Quality Gate` header are case-sensitive. There is no code-fence
// awareness — a heading inside a fence counts, as it does for grep/awk.
//
// Exit-code contract (IDENTICAL to `lesson lint`):
//   validate/save: 0 clean · 3 invalid · 1 usage. NEVER 2 (reserved for the gate BLOCK).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	planExitOK      = 0
	planExitUsage   = 1
	planExitInvalid = 3

	planTemplateName = "PLAN-TEMPLATE.md"

	// Built-in generic environment patterns. Project-specific ones (account
	// ids, GitOps branch names) belong in .harness/plankit/env-patterns.
	defaultNonprodPattern = `nonprod|non-prod|overlays/(dev|uat|sit)`
	defaultProdPattern    = `overlays/prod`
)

var (
	planRequiredSections = []string{"Environment Scope", "Multi-Repo Impact", "Test Plan", "Tasks", "Rollback", "Plan Quality Gate"}
	planPlaceholderRe    = regexp.MustCompile(`\[One sentence|\[Explicitly|\[Exactly|exact/path|\[\.\.\.\]|YYYY-MM-DD`)
	planSection2Re       = regexp.MustCompile(`^##+ *2[.)]`)
	planHeadingRe        = regexp.MustCompile(`^##+ `)
	planQualityHeaderRe  = regexp.MustCompile(`^##+ .*Plan Quality Gate`)
	planUncheckedRe      = regexp.MustCompile(`^- \[ \]`)
	planStatusLineRe     = regexp.MustCompile(`(?i)^\*\*Status:\*\*|^> \*\*Status:\*\*`)
	planStatusQuoteRe    = regexp.MustCompile(`^> ?`)
	planStatusPrefixRe   = regexp.MustCompile(`^\*\*Status:\*\*[ \t\n\r\f\v]*`)
)

// EnvPatterns are the regexes rule 4 uses to spot the "other" environment.
type EnvPatterns struct {
	Nonprod string
	Prod    string
	Source  string // file the patterns came from, or "built-in defaults"
}

// PlanOptions configures one validation run.
type PlanOptions struct {
	// EnvPatternsFile overrides env-patterns resolution when non-empty; the
	// file must then exist.
	EnvPatternsFile string
}

// ParseEnvPatterns parses an env-patterns file: `prod: <regex>` and
// `nonprod: <regex>` lines, `#` comments and blank lines. A key that is absent
// keeps its built-in default. Anything else is malformed — fail closed.
func ParseEnvPatterns(path string) (EnvPatterns, error) {
	pats := EnvPatterns{Nonprod: defaultNonprodPattern, Prod: defaultProdPattern, Source: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return pats, fmt.Errorf("cannot read env-patterns file %q: %v", path, err)
	}
	seen := map[string]bool{}
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || (key != "prod" && key != "nonprod") {
			return pats, fmt.Errorf("env-patterns %s line %d: expected 'prod: <regex>' or 'nonprod: <regex>', got %q", path, i+1, line)
		}
		if value == "" {
			return pats, fmt.Errorf("env-patterns %s line %d: empty regex for %s", path, i+1, key)
		}
		if seen[key] {
			return pats, fmt.Errorf("env-patterns %s line %d: duplicate key %s", path, i+1, key)
		}
		seen[key] = true
		if _, err := regexp.Compile("(?i)" + value); err != nil {
			return pats, fmt.Errorf("env-patterns %s line %d: invalid regex for %s: %v", path, i+1, key, err)
		}
		if key == "prod" {
			pats.Prod = value
		} else {
			pats.Nonprod = value
		}
	}
	return pats, nil
}

// resolveEnvPatternsFile finds the project's .harness/plankit/env-patterns:
// nearest `.harness` ancestor of the plan file (covers plans/, stale/,
// archive/), then the workspace that owns the git common dir (works from a
// worktree), then the current directory. "" means none exists.
func resolveEnvPatternsFile(planPath string) string {
	var candidates []string
	if abs, err := filepath.Abs(planPath); err == nil {
		for d := filepath.Dir(abs); ; d = filepath.Dir(d) {
			if filepath.Base(d) == ".harness" {
				candidates = append(candidates, d)
				break
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	if out, err := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		common := strings.TrimSpace(string(out))
		if common != "" {
			candidates = append(candidates, filepath.Join(filepath.Dir(common), ".harness"))
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, ".harness"))
	}
	for _, h := range candidates {
		f := filepath.Join(h, "plankit", "env-patterns")
		if st, err := os.Stat(f); err == nil && !st.IsDir() {
			return f
		}
	}
	return ""
}

func loadEnvPatterns(planPath string, opts PlanOptions) (EnvPatterns, error) {
	file := opts.EnvPatternsFile
	if file == "" {
		file = resolveEnvPatternsFile(planPath)
	}
	if file == "" {
		return EnvPatterns{Nonprod: defaultNonprodPattern, Prod: defaultProdPattern, Source: "built-in defaults"}, nil
	}
	return ParseEnvPatterns(file)
}

// splitPlanLines splits like grep/awk do: "\n" separates lines, a trailing
// newline does not start an extra line, and "\r" stays part of the line.
func splitPlanLines(data []byte) []string {
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" && len(data) <= 1 {
		return nil
	}
	return strings.Split(text, "\n")
}

// PlanValidate runs every plankit rule and returns the full report (one FAIL
// line per problem plus indented detail lines, then a blank line and the
// RESULT line) and the exit code: 0 clean · 3 invalid.
func PlanValidate(planPath string, opts PlanOptions) (report []string, exit int) {
	if filepath.Base(planPath) == planTemplateName {
		return []string{"SKIP: template file itself is not lintable"}, planExitOK
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		return []string{fmt.Sprintf("FAIL: cannot read plan file %q: %v", planPath, err), "", "RESULT: FAIL (1 problem(s)). Do NOT execute this plan. Fix the plan first."}, planExitInvalid
	}
	pats, perr := loadEnvPatterns(planPath, opts)
	if perr != nil {
		return []string{"FAIL: " + perr.Error(), "      the environment patterns are project-owned; fix .harness/plankit/env-patterns", "", "RESULT: FAIL (1 problem(s)). Do NOT execute this plan. Fix the plan first."}, planExitInvalid
	}
	lines := splitPlanLines(data)

	failures := 0
	fail := func(msg string, details ...string) {
		report = append(report, "FAIL: "+msg)
		report = append(report, details...)
		failures++
	}

	// 1. Required sections.
	for _, section := range planRequiredSections {
		re := regexp.MustCompile(`(?i)^##+ .*` + section)
		found := false
		for _, l := range lines {
			if re.MatchString(l) {
				found = true
				break
			}
		}
		if !found {
			fail(fmt.Sprintf("missing required section: '%s'", section))
		}
	}

	// 2. Target environment declared and singular.
	targetEnv := ""
	targetLine, hasTarget := "", false
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l), "target environment") {
			targetLine, hasTarget = l, true
			break
		}
	}
	switch {
	case !hasTarget:
		fail("no 'Target environment for THIS plan' line (template §1)")
	case strings.Contains(strings.ToLower(targetLine), "non-prod"):
		targetEnv = "non-prod"
	case strings.Contains(strings.ToLower(targetLine), "prod"):
		targetEnv = "prod"
	default:
		fail("target environment is not declared as prod or non-prod: " + targetLine)
	}

	// 3. Unfilled template placeholders.
	var ph []string
	for i, l := range lines {
		if planPlaceholderRe.MatchString(l) {
			ph = append(ph, fmt.Sprintf("      line %d:%s", i+1, l))
		}
	}
	if len(ph) > 0 {
		fail("unfilled template placeholders remain:", ph...)
	}

	// 4. Environment cross-contamination, from section 2 onward (section 1
	// holds the env comparison matrix, so both envs may appear there).
	bodyStart := -1
	for i, l := range lines {
		if planSection2Re.MatchString(l) {
			bodyStart = i
			break
		}
	}
	checkEnv := func(pattern, msg string) {
		if bodyStart < 0 {
			return
		}
		re := regexp.MustCompile("(?i)" + pattern)
		var hits []string
		for i, l := range lines[bodyStart:] {
			if re.MatchString(l) && !strings.Contains(l, "env-ok:") {
				hits = append(hits, fmt.Sprintf("      %d:%s", i+1, l))
			}
		}
		if len(hits) > 0 {
			fail(msg, hits...)
		}
	}
	switch targetEnv {
	case "prod":
		checkEnv(pats.Nonprod, "prod plan references non-prod resources (add 'env-ok: <reason>' on the line if intentional):")
	case "non-prod":
		checkEnv(pats.Prod, "non-prod plan references prod resources (add 'env-ok: <reason>' on the line if intentional):")
	}

	// 5. Execution workspace declared (GATE 0.5).
	hasWorkspace := false
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l), "execution workspace") {
			hasWorkspace = true
			break
		}
	}
	if !hasWorkspace {
		fail("missing 'Execution workspace' declaration (branch + worktree per affected repo — GATE 0.5)")
	}

	// L1 — every '### Repo:' block carries a DoD: line.
	// L2 — repo blocks that are written to (no `no-worktree:`) are counted.
	var noDoD []string
	repoCount := 0
	inBlock, foundDoD, skip := false, false, false
	blockName := ""
	flush := func() {
		if !inBlock {
			return
		}
		if !foundDoD {
			noDoD = append(noDoD, "      "+blockName)
		}
		if !skip {
			repoCount++
		}
	}
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "### Repo:"):
			flush()
			inBlock, foundDoD, skip, blockName = true, false, false, l
			continue
		case strings.HasPrefix(l, "## "), strings.HasPrefix(l, "### "):
			flush()
			inBlock = false
			continue
		}
		if inBlock {
			if strings.Contains(l, "DoD:") {
				foundDoD = true
			}
			if strings.Contains(l, "no-worktree:") {
				skip = true
			}
		}
	}
	flush()
	if len(noDoD) > 0 {
		fail("repo block(s) without a 'DoD:' line — each repo must state what 'done' means for it:", noDoD...)
	}

	// L2 — count the `- ` lines right after the first "Execution workspace"
	// line (blank lines before the first item are skipped; a repeat of the
	// marker line is skipped too, exactly like the awk original).
	wsCount := 0
	counting, seen := false, false
wsLoop:
	for _, l := range lines {
		if strings.Contains(l, "Execution workspace") {
			counting = true
			continue
		}
		if !counting {
			continue
		}
		switch {
		case strings.HasPrefix(l, "- "):
			wsCount++
			seen = true
		case seen:
			break wsLoop
		case strings.TrimSpace(l) == "":
		default:
			break wsLoop
		}
	}
	if repoCount > 0 && repoCount != wsCount {
		fail(fmt.Sprintf("repo count mismatch: section 2 has %d repo(s) that are written to, but Execution workspace lists %d", repoCount, wsCount),
			"      every repo this plan writes to needs its own branch + worktree line;",
			"      for a read-only/prerequisite repo add 'no-worktree: <reason>' in its block")
	}

	// L3 — a DoD line names an actual command in backticks.
	var dodNoCmd []string
	for i, l := range lines {
		if strings.Contains(l, "DoD:") && !strings.Contains(l, "`") {
			dodNoCmd = append(dodNoCmd, fmt.Sprintf("      line %d:%s", i+1, l))
		}
	}
	if len(dodNoCmd) > 0 {
		fail("DoD line(s) with no command in backticks — 'done' must be a runnable check:", dodNoCmd...)
	}

	// 6. Plan Quality Gate fully checked.
	var unchecked []string
	inGate, gateLine := false, 0
	for _, l := range lines {
		if planQualityHeaderRe.MatchString(l) {
			inGate, gateLine = true, 0
			continue
		}
		if planHeadingRe.MatchString(l) {
			inGate = false
			continue
		}
		if inGate {
			gateLine++
			if planUncheckedRe.MatchString(l) {
				unchecked = append(unchecked, fmt.Sprintf("      %d:%s", gateLine, l))
			}
		}
	}
	if len(unchecked) > 0 {
		fail("Plan Quality Gate has unchecked items — plan is not approved for execution:", unchecked...)
	}

	// 7. Status allows execution.
	statusLine, hasStatus := "", false
	for _, l := range lines {
		if planStatusLineRe.MatchString(l) {
			statusLine, hasStatus = l, true
			break
		}
	}
	if !hasStatus {
		fail("no '**Status:**' line (template header) — cannot tell whether this plan is approved")
	} else {
		val := planStatusQuoteRe.ReplaceAllString(statusLine, "")
		val = strings.ToLower(planStatusPrefixRe.ReplaceAllString(val, ""))
		shown := statusLine
		if idx := strings.Index(statusLine, "Status:** "); idx >= 0 {
			shown = statusLine[idx+len("Status:** "):]
		}
		switch {
		case strings.HasPrefix(val, "approved"), strings.HasPrefix(val, "in-progress"), strings.HasPrefix(val, "done"):
		case strings.HasPrefix(val, "stale"):
			fail("plan is marked stale — reality moved on while this document did not",
				"      finish it and set an executable status, or retire it to archive/")
		case strings.Contains(val, "blocked"), strings.HasPrefix(val, "draft"):
			fail(fmt.Sprintf("plan status is not executable: '%s'", shown),
				"      set Status to 'approved' only after the stated precondition is met")
		default:
			fail(fmt.Sprintf("unrecognized plan status: '%s' (expected draft | approved | in-progress | done)", shown))
		}
	}

	report = append(report, "")
	if failures > 0 {
		report = append(report, fmt.Sprintf("RESULT: FAIL (%d problem(s)). Do NOT execute this plan. Fix the plan first.", failures))
		return report, planExitInvalid
	}
	env := targetEnv
	if env == "" {
		env = "unknown"
	}
	report = append(report, fmt.Sprintf("RESULT: PASS — plan '%s' (target env: %s) is executable.", planPath, env))
	return report, planExitOK
}

// PlanSave validates the draft and, when clean, copies it byte-for-byte to
// <repoRoot>/.harness/plans/<basename>. It NEVER touches git, worktrees or
// branches. An invalid draft is refused (exit 3) and no file is written.
func PlanSave(draftPath string, repoRoot string, opts PlanOptions) (dest string, report []string, exit int) {
	if filepath.Base(draftPath) == planTemplateName {
		return "", []string{"FAIL: " + planTemplateName + " is the template, not a plan — copy it to <slug>.md first"}, planExitInvalid
	}
	report, exit = PlanValidate(draftPath, opts)
	if exit != planExitOK {
		return "", report, exit
	}
	data, err := os.ReadFile(draftPath)
	if err != nil {
		return "", []string{fmt.Sprintf("FAIL: cannot read plan file %q: %v", draftPath, err)}, planExitInvalid
	}
	plansDir := filepath.Join(repoRoot, ".harness", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		return "", []string{fmt.Sprintf("FAIL: cannot create plans dir %q: %v", plansDir, err)}, planExitInvalid
	}
	dest = filepath.Join(plansDir, filepath.Base(draftPath))
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return "", []string{fmt.Sprintf("FAIL: cannot write plan file %q: %v", dest, err)}, planExitInvalid
	}
	return dest, report, planExitOK
}
