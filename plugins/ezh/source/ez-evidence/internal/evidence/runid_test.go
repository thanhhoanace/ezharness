package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunIDStampedAcrossArtifacts is the B#3 regression: one `check` invocation
// emits a ledger row + a run-report row + a proof that all carry the SAME
// non-empty run_id. With the OTel env unset, trace_id/span_id are absent from the
// ledger (byte-identical to pre-trace output), and the ledger still parses.
func TestRunIDStampedAcrossArtifacts(t *testing.T) {
	// Isolate the OTel surface so trace/span stay absent.
	t.Setenv("EZ_TRACE_ID", "")
	t.Setenv("EZ_SPAN_ID", "")
	t.Setenv("TRACEPARENT", "")

	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: demo-service
owners: [alice]
build:
  verify: true
risk_level: low
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(harness, "evidence", "ledger.jsonl")

	var stdout, stderr strings.Builder
	code := Run([]string{"check", "--contract", contractPath, "--risk", "low", "--ledger", ledgerPath}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("check exit = %d (want 0); stderr=%q", code, stderr.String())
	}

	// Ledger rows all carry one non-empty run_id.
	entries := readLedgerEntries(t, ledgerPath)
	if len(entries) == 0 {
		t.Fatal("no ledger entries")
	}
	runID := entries[0].RunID
	if strings.TrimSpace(runID) == "" {
		t.Fatal("ledger run_id is empty")
	}
	for i, e := range entries {
		if e.RunID != runID {
			t.Fatalf("ledger[%d] run_id = %q, want %q (all rows of one invocation share it)", i, e.RunID, runID)
		}
		// Proof filename groups by run_id.
		if !strings.Contains(e.ProofRef, runID) {
			t.Fatalf("proof_ref %q does not carry run_id %q", e.ProofRef, runID)
		}
		proofPath := filepath.Join(project, filepath.FromSlash(e.ProofRef))
		if _, err := os.Stat(proofPath); err != nil {
			t.Fatalf("proof missing: %v", err)
		}
	}

	// Run-report row carries the same run_id.
	records, err := readRunReportRecords(filepath.Join(harness, "run-report.jsonl"))
	if err != nil {
		t.Fatalf("read run-report: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("run-report rows = %d, want 1", len(records))
	}
	if records[0].RunID != runID {
		t.Fatalf("run-report run_id = %q, want %q", records[0].RunID, runID)
	}

	// OTel env unset → trace_id/span_id absent from the raw ledger JSON.
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "trace_id") || strings.Contains(string(raw), "span_id") {
		t.Fatalf("ledger carries trace/span fields with OTel env unset:\n%s", raw)
	}
}

// TestNewRunIDIsTimeSortable documents the ordering contract: a later start time
// yields a lexically greater run_id, so plain string sort == chronological order.
func TestNewRunIDIsTimeSortable(t *testing.T) {
	earlier := newRunID(time.Unix(0, 1_000_000_000))
	later := newRunID(time.Unix(0, 2_000_000_000))
	if !(earlier < later) {
		t.Fatalf("run_id not time-sortable: %q !< %q", earlier, later)
	}
}

// TestResolveTraceSpanFromTraceparent documents the W3C traceparent fallback used
// when the explicit EZ_TRACE_ID/EZ_SPAN_ID vars are unset.
func TestResolveTraceSpanFromTraceparent(t *testing.T) {
	t.Setenv("EZ_TRACE_ID", "")
	t.Setenv("EZ_SPAN_ID", "")
	t.Setenv("TRACEPARENT", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	trace, span := resolveTraceSpan()
	if trace != "4bf92f3577b34da6a3ce929d0e0e4736" || span != "00f067aa0ba902b7" {
		t.Fatalf("traceparent parse = (%q,%q)", trace, span)
	}
}

// TestLegacyLedgerWithoutRunIDReplaysGreen locks backward compatibility: a
// hand-written pre-B#3 ledger row with NO run_id field must still replay AND
// verify GREEN. run_id is a property, not a requirement — this guards against a
// future change that would make it required and reject historical ledgers.
func TestLegacyLedgerWithoutRunIDReplaysGreen(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, ".harness", "evidence", "ledger.jsonl")
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// URI proof_ref keeps replay from requiring a local proof body, isolating the
	// legacy-compat concern (absence of run_id) from proof-hash mechanics.
	legacy := `{"task_id":"legacy-1","check":"build.verify","verdict":"pass","proof_ref":"https://evidence.example/legacy-proof","executed_at":"2026-06-10T08:15:30Z","actor_role":"worker"}` + "\n"
	if err := os.WriteFile(ledgerPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	replay, err := RunReplay(ledgerPath)
	if err != nil {
		t.Fatalf("RunReplay() on legacy no-run_id ledger error = %v (must stay green)", err)
	}
	if replay.Verdict != verdictPass {
		t.Fatalf("replay verdict = %q, want pass", replay.Verdict)
	}

	verify, err := RunVerify(ledgerPath, runIdentity{})
	if err != nil {
		t.Fatalf("RunVerify() on legacy no-run_id ledger error = %v (must stay green)", err)
	}
	if verify.Verdict != verdictPass {
		t.Fatalf("verify verdict = %q, want pass", verify.Verdict)
	}
}

// TestTraceIDStampedFromEnv is the end-to-end trace regression: with EZ_TRACE_ID /
// EZ_SPAN_ID set, a check's ledger rows carry those ids; with them unset, the
// fields are absent from the serialized row (omitempty → byte-identical output).
func TestTraceIDStampedFromEnv(t *testing.T) {
	writeCheck := func(t *testing.T, traceEnv, spanEnv string) ([]LedgerEntry, string) {
		t.Helper()
		t.Setenv("EZ_TRACE_ID", traceEnv)
		t.Setenv("EZ_SPAN_ID", spanEnv)
		t.Setenv("TRACEPARENT", "")
		dir := t.TempDir()
		harness := filepath.Join(dir, ".harness")
		if err := os.MkdirAll(harness, 0o755); err != nil {
			t.Fatal(err)
		}
		contractPath := filepath.Join(harness, "project-contract.yaml")
		body := "service: demo-service\nowners: [alice]\nbuild:\n  verify: true\nrisk_level: low\n"
		if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		ledgerPath := filepath.Join(harness, "evidence", "ledger.jsonl")
		var out, errB strings.Builder
		if code := Run([]string{"check", "--contract", contractPath, "--risk", "low", "--ledger", ledgerPath}, &out, &errB); code != exitOK {
			t.Fatalf("check exit = %d; stderr=%q", code, errB.String())
		}
		raw, err := os.ReadFile(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		return readLedgerEntries(t, ledgerPath), string(raw)
	}

	// Env SET → trace_id/span_id on every row.
	set, _ := writeCheck(t, "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
	for _, e := range set {
		if e.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || e.SpanID != "00f067aa0ba902b7" {
			t.Fatalf("row trace/span = (%q,%q), want the env values", e.TraceID, e.SpanID)
		}
	}

	// Env UNSET → fields absent from the serialized ledger.
	unset, raw := writeCheck(t, "", "")
	for _, e := range unset {
		if e.TraceID != "" || e.SpanID != "" {
			t.Fatalf("row carries trace/span with env unset: (%q,%q)", e.TraceID, e.SpanID)
		}
	}
	if strings.Contains(raw, "trace_id") || strings.Contains(raw, "span_id") {
		t.Fatalf("serialized ledger carries trace/span keys with env unset:\n%s", raw)
	}
}
