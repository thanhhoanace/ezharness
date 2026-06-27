package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// synthetic JSONL exercising: two branches, blocks-before-first-pass retries,
// a distinct second actor, a high-risk passing attest (AC#5 flag).
const syntheticRunReport = `{"ts":"2026-01-01T00:00:00Z","command":"check","verdict":"block","exit_code":2,"duration_ms":1000,"repo":"demo","branch":"feature-a","actor":"alice@example.com","engine":"unknown","risk":"low","tree":"","task_id":"t1"}
{"ts":"2026-01-01T00:01:00Z","command":"check","verdict":"block","exit_code":2,"duration_ms":1200,"repo":"demo","branch":"feature-a","actor":"alice@example.com","engine":"unknown","risk":"low","tree":"","task_id":"t1"}
{"ts":"2026-01-01T00:02:00Z","command":"attest","verdict":"pass","exit_code":0,"duration_ms":800,"repo":"demo","branch":"feature-a","actor":"alice@example.com","engine":"unknown","risk":"low","tree":"abc","task_id":"t1"}
{"ts":"2026-01-01T00:03:00Z","command":"attest","verdict":"pass","exit_code":0,"duration_ms":500,"repo":"demo","branch":"feature-b","actor":"bob@example.com","engine":"unknown","risk":"high","tree":"def","task_id":"t2"}
{"ts":"2026-01-01T00:04:00Z","command":"verify","verdict":"warn","exit_code":0,"duration_ms":300,"repo":"demo","branch":"feature-b","actor":"bob@example.com","engine":"unknown","risk":"","tree":"","task_id":"t2"}
`

func TestAggregateRunReport(t *testing.T) {
	records, err := parseRunReportFromString(syntheticRunReport)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	agg := aggregateRunReport(records)

	if agg.Total != 5 {
		t.Errorf("Total = %d, want 5", agg.Total)
	}
	if agg.Pass != 2 {
		t.Errorf("Pass = %d, want 2", agg.Pass)
	}
	if agg.Block != 2 {
		t.Errorf("Block = %d, want 2", agg.Block)
	}
	if agg.Warn != 1 {
		t.Errorf("Warn = %d, want 1", agg.Warn)
	}
	if agg.FalseDoneCaught != 2 {
		t.Errorf("FalseDoneCaught = %d, want 2", agg.FalseDoneCaught)
	}

	// Per-command totals.
	if ct := agg.ByCommand["check"]; ct.Runs != 2 || ct.Block != 2 {
		t.Errorf("check totals = %+v, want Runs=2 Block=2", ct)
	}
	if ct := agg.ByCommand["attest"]; ct.Runs != 2 || ct.Pass != 2 {
		t.Errorf("attest totals = %+v, want Runs=2 Pass=2", ct)
	}

	// Per-branch retries: feature-a had 2 blocks before its first pass.
	branches := map[string]branchOverhead{}
	for _, bo := range agg.Branches {
		branches[bo.Branch] = bo
	}
	if fa := branches["feature-a"]; fa.RetriesBeforeFirstPass != 2 {
		t.Errorf("feature-a retries = %d, want 2", fa.RetriesBeforeFirstPass)
	}
	if fa := branches["feature-a"]; fa.TotalDurationMS != 3000 {
		t.Errorf("feature-a total duration = %d, want 3000", fa.TotalDurationMS)
	}
	if fb := branches["feature-b"]; fb.RetriesBeforeFirstPass != 0 {
		t.Errorf("feature-b retries = %d, want 0", fb.RetriesBeforeFirstPass)
	}

	// AC#5: feature-b has a passing high-risk attest -> flagged.
	if len(agg.FlaggedBranches) != 1 || agg.FlaggedBranches[0] != "feature-b" {
		t.Errorf("FlaggedBranches = %v, want [feature-b]", agg.FlaggedBranches)
	}

	// AC#8: distinct passing actors = alice + bob.
	if len(agg.DistinctPassActor) != 2 {
		t.Errorf("DistinctPassActor = %v, want 2", agg.DistinctPassActor)
	}

	// AC#6 risk counts (empty risk skipped): low x3, high x1.
	if agg.RiskCounts["low"] != 3 {
		t.Errorf("risk low = %d, want 3", agg.RiskCounts["low"])
	}
	if agg.RiskCounts["high"] != 1 {
		t.Errorf("risk high = %d, want 1", agg.RiskCounts["high"])
	}
}

func TestRegenerateRunReportMarkdown(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "run-report.jsonl")
	mdPath := filepath.Join(dir, "run-report.md")
	if err := os.WriteFile(jsonlPath, []byte(syntheticRunReport), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	if err := regenerateRunReportMarkdown(jsonlPath, mdPath); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	data, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read md: %v", err)
	}
	md := string(data)
	for _, want := range []string{"# ezHarness run report", "## Totals", "## Per-ticket overhead", "## Signals", "feature-b"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

// parseRunReportFromString is a test helper that reuses readRunReportRecords via
// a temp file so the production parser is exercised directly.
func parseRunReportFromString(s string) ([]runReportRecord, error) {
	dir, err := os.MkdirTemp("", "runreport")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "rr.jsonl")
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return nil, err
	}
	return readRunReportRecords(path)
}
