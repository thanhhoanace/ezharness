package evidence

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func RunReplay(ledgerPath string) (ResultSummary, error) {
	return runReplay(ledgerPath, false, "")
}

func RunReplayRequireVerifier(ledgerPath string, expectTree string) (ResultSummary, error) {
	return runReplay(ledgerPath, true, expectTree)
}

func RunVerify(ledgerPath string) (ResultSummary, error) {
	records, projectRoot, err := readLedgerRecords(ledgerPath)
	if err != nil {
		reason := err.Error()
		return ResultSummary{Command: "verify", Verdict: verdictBlock, Reason: reason, ExitCode: exitBlock}, GateError{Check: "ledger", Reason: reason, ExitCode: exitBlock}
	}

	summary, verifyErr := verifyLedgerRecords(records, projectRoot)
	if verifyErr != nil {
		gateErr := asGateError(verifyErr, checkVerifierReplay)
		proofRef, recordErr := recordVerifierVerdict(ledgerPath, projectRoot, verdictBlock, gateErr.Reason, records)
		if recordErr != nil {
			reason := fmt.Sprintf("%s; record verifier verdict: %v", gateErr.Reason, recordErr)
			return ResultSummary{Command: "verify", Verdict: verdictBlock, Reason: reason, ExitCode: exitBlock}, GateError{Check: checkVerifierReplay, Reason: reason, ExitCode: exitBlock}
		}
		return ResultSummary{
			Command:  "verify",
			Verdict:  verdictBlock,
			Reason:   gateErr.Reason,
			ExitCode: exitBlock,
			Checks: []CheckResult{{
				Check:    checkVerifierReplay,
				Verdict:  verdictBlock,
				ProofRef: proofRef,
				Reason:   gateErr.Reason,
				ExitCode: exitBlock,
			}},
		}, gateErr
	}

	reason := fmt.Sprintf("verified %d ledger entries", len(records))
	proofRef, recordErr := recordVerifierVerdict(ledgerPath, projectRoot, verdictPass, reason, records)
	result := CheckResult{Check: checkVerifierReplay, Verdict: verdictPass, ProofRef: proofRef, Reason: reason, ExitCode: exitOK}
	summary.Command = "verify"
	summary.Checks = append(summary.Checks, result)
	if recordErr != nil {
		reason := recordErr.Error()
		return ResultSummary{Command: "verify", Verdict: verdictBlock, Reason: reason, ExitCode: exitBlock}, GateError{Check: checkVerifierReplay, Reason: reason, ExitCode: exitBlock}
	}
	return summary, nil
}

func runReplay(ledgerPath string, requireVerifier bool, expectTree string) (ResultSummary, error) {
	records, projectRoot, err := readLedgerRecords(ledgerPath)
	if err != nil {
		reason := err.Error()
		return ResultSummary{Command: "replay", Verdict: verdictBlock, Reason: reason, ExitCode: exitBlock}, GateError{Check: "ledger", Reason: reason, ExitCode: exitBlock}
	}

	summary, err := replayLedgerRecords(records, projectRoot, requireVerifier, expectTree)
	if err != nil {
		return summary, err
	}
	return summary, nil
}

type ledgerRecord struct {
	Entry      LedgerEntry
	LineNumber int
	RawLine    string
}

func readLedgerRecords(ledgerPath string) ([]ledgerRecord, string, error) {
	file, err := os.Open(ledgerPath)
	if err != nil {
		return nil, "", fmt.Errorf("open ledger: %w", err)
	}
	defer file.Close()

	projectRoot := projectRootFromLedgerPath(ledgerPath)
	if projectRoot == "" {
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			projectRoot = cwd
		}
	}

	var records []ledgerRecord
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry LedgerEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return records, projectRoot, fmt.Errorf("line %d: invalid JSON: %w", lineNumber, err)
		}
		records = append(records, ledgerRecord{Entry: entry, LineNumber: lineNumber, RawLine: line})
	}
	if err := scanner.Err(); err != nil {
		return records, projectRoot, fmt.Errorf("read ledger: %w", err)
	}
	return records, projectRoot, nil
}

func replayLedgerRecords(records []ledgerRecord, projectRoot string, requireVerifier bool, expectTree string) (ResultSummary, error) {
	summary := ResultSummary{Command: "replay", Verdict: verdictPass, ExitCode: exitOK}
	if len(records) == 0 {
		return replayBlock(summary, 0, "ledger", "ledger contains no entries")
	}

	for _, record := range records {
		entry := record.Entry
		if err := validateLedgerEntry(entry, projectRoot); err != nil {
			return replayBlock(summary, record.LineNumber, entry.Check, fmt.Sprintf("line %d: %v", record.LineNumber, err))
		}
		summary.Checks = append(summary.Checks, CheckResult{
			Check:    entry.Check,
			Verdict:  verdictPass,
			ProofRef: entry.ProofRef,
			Reason:   "proof replayable",
			ExitCode: 0,
		})
	}

	if requireVerifier {
		if err := requireLatestVerifierVerdict(records, expectTree); err != nil {
			return replayBlock(summary, 0, checkVerifierReplay, err.Error())
		}
	}
	return summary, nil
}

func validateLedgerEntry(entry LedgerEntry, projectRoot string) error {
	if strings.TrimSpace(entry.TaskID) == "" {
		return fmt.Errorf("missing task_id")
	}
	if strings.TrimSpace(entry.Check) == "" {
		return fmt.Errorf("missing check")
	}
	if entry.Verdict != "pass" && entry.Verdict != "warn" && entry.Verdict != "block" {
		return fmt.Errorf("invalid verdict %q", entry.Verdict)
	}
	if !validProofRefShape(entry.ProofRef) {
		return fmt.Errorf("invalid proof_ref %q", entry.ProofRef)
	}
	if _, err := time.Parse(time.RFC3339, entry.ExecutedAt); err != nil {
		return fmt.Errorf("invalid executed_at %q", entry.ExecutedAt)
	}
	if entry.ActorRole != "worker" && entry.ActorRole != "verifier" {
		return fmt.Errorf("invalid actor_role %q", entry.ActorRole)
	}
	if strings.HasPrefix(entry.ProofRef, ".harness/evidence/proofs/") {
		proofPath := filepath.Join(projectRoot, filepath.FromSlash(entry.ProofRef))
		data, err := os.ReadFile(proofPath)
		if err != nil {
			return fmt.Errorf("local proof_ref missing: %s", entry.ProofRef)
		}
		if err := validateProofBody(entry, string(data)); err != nil {
			return err
		}
	}
	return nil
}

func replayBlock(summary ResultSummary, lineNumber int, check string, reason string) (ResultSummary, error) {
	summary.Verdict = verdictBlock
	summary.Reason = reason
	summary.ExitCode = exitBlock
	summary.Checks = append(summary.Checks, CheckResult{
		Check:    check,
		Verdict:  verdictBlock,
		Reason:   reason,
		ExitCode: exitBlock,
	})
	return summary, GateError{Check: check, Reason: reason, ExitCode: exitBlock}
}

func verifyLedgerRecords(records []ledgerRecord, projectRoot string) (ResultSummary, error) {
	summary, err := replayLedgerRecords(records, projectRoot, false, "")
	if err != nil {
		return summary, err
	}

	workerEvidence := map[string]LedgerEntry{}
	for _, record := range records {
		entry := record.Entry
		if entry.ActorRole != actorWorker {
			continue
		}
		workerEvidence[entry.Check] = entry
	}
	if len(workerEvidence) == 0 {
		return summary, GateError{Check: checkVerifierReplay, Reason: "ledger contains no worker evidence to verify", ExitCode: exitBlock}
	}
	for check, entry := range workerEvidence {
		if entry.Verdict != verdictPass {
			return summary, GateError{Check: check, Reason: fmt.Sprintf("latest worker evidence for %s is %q", check, entry.Verdict), ExitCode: exitBlock}
		}
	}
	return summary, nil
}

func requireLatestVerifierVerdict(records []ledgerRecord, expectTree string) error {
	last := records[len(records)-1].Entry
	if last.ActorRole != actorVerifier || last.Check != checkVerifierReplay {
		return fmt.Errorf("latest ledger entry is not a verifier verdict")
	}
	if last.Verdict != verdictPass {
		return fmt.Errorf("latest verifier verdict is %q", last.Verdict)
	}

	coveredEntries, err := metadataInt(last.Metadata, "ledger_entries")
	if err != nil {
		return err
	}
	if coveredEntries != len(records)-1 {
		return fmt.Errorf("verifier verdict covers %d entries but ledger has %d prior entries", coveredEntries, len(records)-1)
	}
	coveredDigest, err := metadataString(last.Metadata, "ledger_sha256")
	if err != nil {
		return err
	}
	actualDigest := recordsDigest(records[:coveredEntries])
	if coveredDigest != actualDigest {
		return fmt.Errorf("verifier verdict digest mismatch")
	}

	if strings.TrimSpace(expectTree) != "" {
		boundTree, _ := last.Metadata["tree"].(string)
		if strings.TrimSpace(boundTree) == "" {
			return fmt.Errorf("verifier verdict is not bound to a code tree; re-run check then verify for this change")
		}
		if boundTree != expectTree {
			return fmt.Errorf("evidence does not match the current change (stale); re-run check then verify for the staged content")
		}
	}
	return nil
}

func recordVerifierVerdict(ledgerPath string, projectRoot string, verdict string, reason string, records []ledgerRecord) (string, error) {
	writer := NewLedgerWriter(ledgerPath, projectRoot)
	metadata := map[string]interface{}{
		"ledger_entries": len(records),
		"ledger_sha256":  recordsDigest(records),
		"reason":         reason,
	}
	if tree := latestWorkerTree(records); tree != "" {
		metadata["tree"] = tree
	}
	proof := proofBody(checkVerifierReplay, "", exitForVerdict(verdict), reason, fmt.Sprintf("ledger_entries: %d\nledger_sha256: %s\n", len(records), recordsDigest(records)))
	return writer.RecordWithActor(actorVerifier, checkVerifierReplay, verdict, proof, metadata)
}

func validateProofBody(entry LedgerEntry, proof string) error {
	if !strings.Contains(proof, "check: "+entry.Check+"\n") {
		return fmt.Errorf("proof body mismatch for %s: missing check", entry.Check)
	}
	if value, ok := entry.Metadata["exit_code"]; ok {
		exitCode, err := interfaceInt(value)
		if err != nil {
			return fmt.Errorf("invalid exit_code metadata for %s: %w", entry.Check, err)
		}
		if !strings.Contains(proof, fmt.Sprintf("exit_code: %d\n", exitCode)) {
			return fmt.Errorf("proof body mismatch for %s: exit_code", entry.Check)
		}
	}
	if value, ok := entry.Metadata["reason"]; ok {
		reason, ok := value.(string)
		if !ok {
			return fmt.Errorf("invalid reason metadata for %s", entry.Check)
		}
		if !strings.Contains(proof, "reason: "+reason+"\n") {
			return fmt.Errorf("proof body mismatch for %s: reason", entry.Check)
		}
	}
	if value, ok := entry.Metadata["ledger_entries"]; ok {
		ledgerEntries, err := interfaceInt(value)
		if err != nil {
			return fmt.Errorf("invalid ledger_entries metadata for %s: %w", entry.Check, err)
		}
		if !strings.Contains(proof, fmt.Sprintf("ledger_entries: %d\n", ledgerEntries)) {
			return fmt.Errorf("proof body mismatch for %s: ledger_entries", entry.Check)
		}
	}
	if value, ok := entry.Metadata["ledger_sha256"]; ok {
		ledgerSHA, ok := value.(string)
		if !ok {
			return fmt.Errorf("invalid ledger_sha256 metadata for %s", entry.Check)
		}
		if !strings.Contains(proof, "ledger_sha256: "+ledgerSHA+"\n") {
			return fmt.Errorf("proof body mismatch for %s: ledger_sha256", entry.Check)
		}
	}
	if value, ok := entry.Metadata["proof_sha256"]; ok {
		expected, ok := value.(string)
		if !ok || strings.TrimSpace(expected) == "" {
			return fmt.Errorf("invalid proof_sha256 metadata for %s", entry.Check)
		}
		actualHash := sha256.Sum256([]byte(proof))
		if hex.EncodeToString(actualHash[:]) != expected {
			return fmt.Errorf("proof body mismatch for %s: proof_sha256", entry.Check)
		}
	}
	return nil
}

func recordsDigest(records []ledgerRecord) string {
	hash := sha256.New()
	for _, record := range records {
		hash.Write([]byte(record.RawLine))
		hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func latestWorkerTree(records []ledgerRecord) string {
	tree := ""
	for _, record := range records {
		if record.Entry.ActorRole != actorWorker {
			continue
		}
		if value, ok := record.Entry.Metadata["tree"].(string); ok && strings.TrimSpace(value) != "" {
			tree = value
		}
	}
	return tree
}

func metadataInt(metadata map[string]interface{}, key string) (int, error) {
	value, ok := metadata[key]
	if !ok {
		return 0, fmt.Errorf("verifier verdict missing metadata.%s", key)
	}
	result, err := interfaceInt(value)
	if err != nil {
		return 0, fmt.Errorf("invalid verifier metadata.%s: %w", key, err)
	}
	return result, nil
}

func metadataString(metadata map[string]interface{}, key string) (string, error) {
	value, ok := metadata[key]
	if !ok {
		return "", fmt.Errorf("verifier verdict missing metadata.%s", key)
	}
	result, ok := value.(string)
	if !ok || strings.TrimSpace(result) == "" {
		return "", fmt.Errorf("invalid verifier metadata.%s", key)
	}
	return result, nil
}

func interfaceInt(value interface{}) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("not an integer")
		}
		return int(typed), nil
	case json.Number:
		parsed, err := strconv.Atoi(typed.String())
		if err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported type %T", value)
	}
}

func exitForVerdict(verdict string) int {
	if verdict == verdictPass {
		return exitOK
	}
	return exitBlock
}
