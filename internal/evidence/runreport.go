package evidence

// Passive run-report telemetry. Every gate command (check/verify/replay/attest)
// appends one JSONL line to <workspace>/.harness/run-report.jsonl and
// regenerates <workspace>/.harness/run-report.md from the full JSONL.
//
// IMPORTANT: telemetry is observational, not load-bearing. The gate verdict is
// what matters; writing telemetry must NEVER fail the gate. Every write/lookup
// here is best-effort and errors are deliberately swallowed (see recordRunReport).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// runReportRecord is one telemetry line in run-report.jsonl. Fields are compact
// and stable so an agent can parse the file directly.
type runReportRecord struct {
	Timestamp  string `json:"ts"`
	Command    string `json:"command"`
	Verdict    string `json:"verdict"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Repo       string `json:"repo"`
	Branch     string `json:"branch"`
	Actor      string `json:"actor"`
	Engine     string `json:"engine"`
	Risk       string `json:"risk"`
	Tree       string `json:"tree"`
	TaskID     string `json:"task_id"`
	Reason     string `json:"reason,omitempty"`
}

// runReportContext is the call-site context the capture point in cli.go already
// has. workspaceHint is the ledger path (or a bare repo/ledger path for
// verify/replay) from which the workspace root is resolved.
type runReportContext struct {
	command       string
	risk          string
	workspaceHint string
	repoRoot      string
}

// recordRunReport appends one telemetry line and regenerates the markdown
// summary. It is best-effort: any failure (workspace not resolvable, write
// errors, git lookups failing) is silently swallowed so telemetry never affects
// the gate verdict.
func recordRunReport(ctx runReportContext, summary ResultSummary, runErr error, elapsed time.Duration, start time.Time) {
	workspace := resolveRunReportWorkspace(ctx.workspaceHint)
	if workspace == "" {
		return
	}

	// The git repo to introspect: prefer an explicit repo-root, else the workspace.
	repoDir := workspace
	if strings.TrimSpace(ctx.repoRoot) != "" {
		if abs, err := filepath.Abs(ctx.repoRoot); err == nil {
			repoDir = abs
		} else {
			repoDir = ctx.repoRoot
		}
	}

	verdict := summary.Verdict
	if verdict == "" {
		if runErr != nil {
			verdict = verdictBlock
		} else {
			verdict = verdictPass
		}
	}
	exitCode := summary.ExitCode
	if exitCode == 0 && runErr != nil {
		exitCode = exitBlock
	}

	taskID := strings.TrimSpace(os.Getenv("EZH_TASK_ID"))
	if taskID == "" {
		taskID = defaultTaskID
	}
	engine := strings.TrimSpace(os.Getenv("EZH_ENGINE"))
	if engine == "" {
		engine = "unknown"
	}

	rec := runReportRecord{
		Timestamp:  start.Add(elapsed).UTC().Format(time.RFC3339),
		Command:    ctx.command,
		Verdict:    verdict,
		ExitCode:   exitCode,
		DurationMS: elapsed.Milliseconds(),
		Repo:       gitRepoName(repoDir, workspace),
		Branch:     gitBranch(repoDir),
		Actor:      gitActor(repoDir),
		Engine:     engine,
		Risk:       ctx.risk,
		Tree:       gitTree(repoDir),
		TaskID:     taskID,
		Reason:     strings.TrimSpace(summary.Reason),
	}

	jsonlPath := filepath.Join(workspace, ".harness", "run-report.jsonl")
	if err := appendRunReportLine(jsonlPath, rec); err != nil {
		// Best-effort: swallow write errors, telemetry is observational.
		return
	}
	// Regenerate the markdown cache from the full JSONL (last-writer-wins).
	_ = regenerateRunReportMarkdown(jsonlPath, filepath.Join(workspace, ".harness", "run-report.md"))
}

// resolveRunReportWorkspace derives the workspace root from a ledger/repo path,
// reusing projectRootFromLedgerPath for ledger paths and falling back to the
// path itself (or its containing repo toplevel) for bare paths. Returns "" when
// nothing usable can be resolved.
func resolveRunReportWorkspace(hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return ""
	}
	if root := projectRootFromLedgerPath(hint); root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			return abs
		}
		return root
	}
	// Bare path (verify/replay): treat it as a path inside the workspace. If it
	// is a file, use its directory; resolve git toplevel when possible.
	abs, err := filepath.Abs(hint)
	if err != nil {
		return ""
	}
	dir := abs
	if info, statErr := os.Stat(abs); statErr == nil && !info.IsDir() {
		dir = filepath.Dir(abs)
	}
	if top := gitToplevel(dir); top != "" {
		return top
	}
	return dir
}

// appendRunReportLine appends one compact JSON line to path (O_APPEND).
func appendRunReportLine(path string, rec runReportRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}

// regenerateRunReportMarkdown reads the full JSONL and overwrites the markdown
// summary. The JSONL is the source of truth; the .md is a derived cache.
func regenerateRunReportMarkdown(jsonlPath string, mdPath string) error {
	records, err := readRunReportRecords(jsonlPath)
	if err != nil {
		return err
	}
	md := renderRunReportMarkdown(records)
	return os.WriteFile(mdPath, []byte(md), 0o644)
}

// readRunReportRecords parses every line of the JSONL. Malformed lines are
// skipped so a single bad write never breaks aggregation.
func readRunReportRecords(jsonlPath string) ([]runReportRecord, error) {
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		return nil, err
	}
	var records []runReportRecord
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var rec runReportRecord
		if jsonErr := json.Unmarshal([]byte(line), &rec); jsonErr != nil {
			continue // skip malformed lines
		}
		records = append(records, rec)
	}
	return records, nil
}

// --- Aggregation -----------------------------------------------------------

// runReportAggregate is the computed view over all telemetry records, used to
// render the markdown and asserted in tests.
type runReportAggregate struct {
	Total     int
	Pass      int
	Block     int
	Warn      int
	ByCommand map[string]commandTotals
	Branches  []branchOverhead
	// Signals
	FalseDoneCaught   int            // AC#3: count of block verdicts
	FlaggedBranches   []string       // AC#5: branches with passing high-risk attest
	RiskCounts        map[string]int // AC#6: counts per risk level
	WarnBypass        int            // AC#6: warn verdicts (bypass)
	BlockCount        int            // AC#6: block verdicts
	PassCount         int            // AC#6: pass verdicts
	MedianOverheadMS  int64          // AC#7
	P90OverheadMS     int64          // AC#7
	DistinctPassActor []string       // AC#8: distinct actors with >=1 pass
}

type commandTotals struct {
	Runs  int
	Pass  int
	Block int
	Warn  int
}

type branchOverhead struct {
	Branch                 string
	Runs                   int
	RetriesBeforeFirstPass int
	TotalDurationMS        int64
}

func aggregateRunReport(records []runReportRecord) runReportAggregate {
	agg := runReportAggregate{
		ByCommand:  map[string]commandTotals{},
		RiskCounts: map[string]int{},
	}

	branchRuns := map[string]int{}
	branchDuration := map[string]int64{}
	branchOrder := []string{}
	branchSeen := map[string]bool{}
	// retries: count of blocks seen before the first pass, per branch.
	branchFirstPassFound := map[string]bool{}
	branchRetries := map[string]int{}
	flagged := map[string]bool{}
	passActors := map[string]bool{}

	for _, rec := range records {
		agg.Total++
		switch rec.Verdict {
		case verdictPass:
			agg.Pass++
			agg.PassCount++
		case verdictBlock:
			agg.Block++
			agg.BlockCount++
		case verdictWarn:
			agg.Warn++
			agg.WarnBypass++
		}

		ct := agg.ByCommand[rec.Command]
		ct.Runs++
		switch rec.Verdict {
		case verdictPass:
			ct.Pass++
		case verdictBlock:
			ct.Block++
		case verdictWarn:
			ct.Warn++
		}
		agg.ByCommand[rec.Command] = ct

		branch := rec.Branch
		if !branchSeen[branch] {
			branchSeen[branch] = true
			branchOrder = append(branchOrder, branch)
		}
		branchRuns[branch]++
		branchDuration[branch] += rec.DurationMS
		if !branchFirstPassFound[branch] {
			if rec.Verdict == verdictPass {
				branchFirstPassFound[branch] = true
			} else if rec.Verdict == verdictBlock {
				branchRetries[branch]++
			}
		}

		// AC#5: a passing attest at high risk collapses worker/verifier.
		if rec.Command == "attest" && rec.Risk == "high" && rec.Verdict == verdictPass {
			flagged[branch] = true
		}
		// AC#6: counts by risk level (skip empty risk).
		if strings.TrimSpace(rec.Risk) != "" {
			agg.RiskCounts[rec.Risk]++
		}
		// AC#8: distinct actors with at least one pass.
		if rec.Verdict == verdictPass && strings.TrimSpace(rec.Actor) != "" {
			passActors[rec.Actor] = true
		}
	}

	agg.FalseDoneCaught = agg.Block

	var perBranchDurations []int64
	for _, branch := range branchOrder {
		agg.Branches = append(agg.Branches, branchOverhead{
			Branch:                 branch,
			Runs:                   branchRuns[branch],
			RetriesBeforeFirstPass: branchRetries[branch],
			TotalDurationMS:        branchDuration[branch],
		})
		perBranchDurations = append(perBranchDurations, branchDuration[branch])
	}
	sort.Slice(agg.Branches, func(i, j int) bool {
		return agg.Branches[i].Branch < agg.Branches[j].Branch
	})

	agg.MedianOverheadMS = medianInt64(perBranchDurations)
	agg.P90OverheadMS = percentileInt64(perBranchDurations, 90)

	for branch := range flagged {
		agg.FlaggedBranches = append(agg.FlaggedBranches, branch)
	}
	sort.Strings(agg.FlaggedBranches)
	for actor := range passActors {
		agg.DistinctPassActor = append(agg.DistinctPassActor, actor)
	}
	sort.Strings(agg.DistinctPassActor)

	return agg
}

func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func percentileInt64(values []int64, pct int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// Nearest-rank method.
	rank := (pct * len(sorted)) / 100
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// --- Markdown rendering ----------------------------------------------------

func renderRunReportMarkdown(records []runReportRecord) string {
	agg := aggregateRunReport(records)
	var b strings.Builder

	b.WriteString("# ezHarness run report\n")
	fmt.Fprintf(&b, "generated-at: %s\n\n", utcNowRFC3339())

	// Totals
	b.WriteString("## Totals\n\n")
	b.WriteString("| command | runs | pass | block | warn |\n")
	b.WriteString("|---|---|---|---|---|\n")
	commands := make([]string, 0, len(agg.ByCommand))
	for cmd := range agg.ByCommand {
		commands = append(commands, cmd)
	}
	sort.Strings(commands)
	for _, cmd := range commands {
		ct := agg.ByCommand[cmd]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d |\n", cmd, ct.Runs, ct.Pass, ct.Block, ct.Warn)
	}
	fmt.Fprintf(&b, "| **all** | %d | %d | %d | %d |\n\n", agg.Total, agg.Pass, agg.Block, agg.Warn)

	// Per-ticket overhead
	b.WriteString("## Per-ticket overhead\n\n")
	b.WriteString("overhead = sum of gate command durations per branch (AC#7 added time).\n\n")
	b.WriteString("| branch | runs | retries | total_duration_ms |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, bo := range agg.Branches {
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", bo.Branch, bo.Runs, bo.RetriesBeforeFirstPass, bo.TotalDurationMS)
	}
	fmt.Fprintf(&b, "\nmedian_overhead_ms: %d\np90_overhead_ms: %d\n\n", agg.MedianOverheadMS, agg.P90OverheadMS)

	// Signals
	b.WriteString("## Signals\n\n")
	b.WriteString("| signal | metric | value |\n")
	b.WriteString("|---|---|---|\n")
	fmt.Fprintf(&b, "| AC#3 | false-done caught (block count) | %d |\n", agg.FalseDoneCaught)
	fmt.Fprintf(&b, "| AC#5 | worker=verifier flagged branches | %s |\n", joinOrNone(agg.FlaggedBranches))
	fmt.Fprintf(&b, "| AC#6 | warn(bypass) | %d |\n", agg.WarnBypass)
	fmt.Fprintf(&b, "| AC#6 | block | %d |\n", agg.BlockCount)
	fmt.Fprintf(&b, "| AC#6 | pass | %d |\n", agg.PassCount)
	risks := make([]string, 0, len(agg.RiskCounts))
	for risk := range agg.RiskCounts {
		risks = append(risks, risk)
	}
	sort.Strings(risks)
	for _, risk := range risks {
		fmt.Fprintf(&b, "| AC#6 | risk=%s | %d |\n", risk, agg.RiskCounts[risk])
	}
	fmt.Fprintf(&b, "| AC#7 | median overhead ms (target < 600000) | %d |\n", agg.MedianOverheadMS)
	fmt.Fprintf(&b, "| AC#7 | p90 overhead ms | %d |\n", agg.P90OverheadMS)
	fmt.Fprintf(&b, "| AC#8 | distinct passing actors | %s |\n", joinOrNone(agg.DistinctPassActor))

	return b.String()
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

// --- best-effort git helpers ----------------------------------------------
//
// All helpers below are best-effort: any failure returns the documented
// fallback rather than an error, because telemetry must never fail the gate.

func gitToplevel(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitRepoName returns the basename of the git toplevel, falling back to the
// workspace basename.
func gitRepoName(repoDir string, workspace string) string {
	if top := gitToplevel(repoDir); top != "" {
		return filepath.Base(top)
	}
	if workspace != "" {
		return filepath.Base(workspace)
	}
	return "unknown"
}

func gitBranch(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "unknown"
	}
	return branch
}

func gitActor(dir string) string {
	cmd := exec.Command("git", "config", "user.email")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	actor := strings.TrimSpace(string(out))
	if actor == "" {
		return "unknown"
	}
	return actor
}
