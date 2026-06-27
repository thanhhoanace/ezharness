package evidence

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadContractYAMLExampleShape(t *testing.T) {
	dir := t.TempDir()
	contractPath := filepath.Join(dir, "project-contract.yaml")
	body := `service: tapp-merchant
owners:
  - hoannt1
build:
  verify: true
  verify_thick: true
env_rules:
  - id: ecr-prefix-match
    applies_to: docker_push
    reminder: image tag matches REGISTRY_ECR_PREFIXES
    assert: test "$IMAGE_PREFIX" = "$REGISTRY_ECR_PREFIX"
    gate_action: prompt
    command_prefixes: ["docker push", "docker buildx build"]
    severity: blocking
  - id: runner-no-net
    applies_to: runner_cwd
    check: no curl/wget/pip/npm in runner
    severity: blocking
risk_level: high
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	contract, err := ReadContract(contractPath)
	if err != nil {
		t.Fatalf("ReadContract() error = %v", err)
	}
	if contract.Service != "tapp-merchant" {
		t.Fatalf("service = %q", contract.Service)
	}
	if len(contract.Owners) != 1 || contract.Owners[0] != "hoannt1" {
		t.Fatalf("owners = %#v", contract.Owners)
	}
	if contract.Build.Verify != "true" || contract.Build.VerifyThick != "true" {
		t.Fatalf("build = %#v", contract.Build)
	}
	if len(contract.EnvRules) != 2 {
		t.Fatalf("env_rules len = %d", len(contract.EnvRules))
	}
	if contract.EnvRules[0].Reminder != "image tag matches REGISTRY_ECR_PREFIXES" ||
		contract.EnvRules[0].Assert == "" ||
		contract.EnvRules[0].GateAction != "prompt" ||
		len(contract.EnvRules[0].CommandPrefixes) != 2 {
		t.Fatalf("env_rules[0] = %#v", contract.EnvRules[0])
	}
	if contract.RiskLevel != "high" {
		t.Fatalf("risk_level = %q", contract.RiskLevel)
	}
}

func TestRunCheckHighWritesLedgerAndReplayPasses(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
  verify_thick: true
env_rules:
  - id: runner-no-net
    applies_to: runner_cwd
    check: no network in runner
    severity: blocking
risk_level: high
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(harness, "evidence", "ledger.jsonl")

	summary, err := RunCheck(contractPath, "high", ledgerPath, "")
	if err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}
	if summary.Verdict != verdictPass {
		t.Fatalf("verdict = %q", summary.Verdict)
	}
	if len(summary.Checks) != 4 {
		t.Fatalf("checks len = %d", len(summary.Checks))
	}

	replaySummary, err := RunReplay(ledgerPath)
	if err != nil {
		t.Fatalf("RunReplay() error = %v", err)
	}
	if replaySummary.Verdict != verdictPass {
		t.Fatalf("replay verdict = %q", replaySummary.Verdict)
	}

	entries := readLedgerEntries(t, ledgerPath)
	if len(entries) != 4 {
		t.Fatalf("ledger entries len = %d", len(entries))
	}
	for _, entry := range entries {
		if entry.ActorRole != actorWorker {
			t.Fatalf("check wrote actor_role = %q", entry.ActorRole)
		}
	}
}

func TestRunCheckHighMissingVerifyThickBlocks(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
env_rules:
  - id: runner-no-net
    applies_to: runner_cwd
    check: no network in runner
    severity: blocking
risk_level: high
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := RunCheck(contractPath, "high", "", "")
	if err == nil {
		t.Fatal("RunCheck() expected error")
	}
	if summary.ExitCode != exitBlock || summary.Verdict != verdictBlock {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Reason != "missing required command build.verify_thick for high risk" {
		t.Fatalf("reason = %q", summary.Reason)
	}
}

func TestRunCheckMedMissingEnvRulesEmitsTemplate(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
risk_level: med
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var jsonStdout bytes.Buffer
	var jsonStderr bytes.Buffer
	exitCode := Run([]string{"check", "--contract", contractPath, "--risk", "med", "--json"}, &jsonStdout, &jsonStderr)
	if exitCode != exitBlock {
		t.Fatalf("json exit code = %d, stdout = %q, stderr = %q", exitCode, jsonStdout.String(), jsonStderr.String())
	}
	var summary ResultSummary
	if err := json.Unmarshal(jsonStdout.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal json summary: %v", err)
	}
	if summary.Verdict != verdictBlock || summary.ExitCode != exitBlock {
		t.Fatalf("summary = %#v", summary)
	}
	assertEnvRuleTemplate(t, summary.Suggestion)
	if len(summary.Checks) != 1 {
		t.Fatalf("checks len = %d", len(summary.Checks))
	}
	assertEnvRuleTemplate(t, summary.Checks[0].Suggestion)

	var textStdout bytes.Buffer
	var textStderr bytes.Buffer
	exitCode = Run([]string{"check", "--contract", contractPath, "--risk", "med"}, &textStdout, &textStderr)
	if exitCode != exitBlock {
		t.Fatalf("text exit code = %d, stdout = %q, stderr = %q", exitCode, textStdout.String(), textStderr.String())
	}
	if textStdout.Len() != 0 {
		t.Fatalf("text stdout = %q", textStdout.String())
	}
	assertEnvRuleTemplate(t, textStderr.String())
}

func TestRunCheckMedEmptyEnvRulesEmitsTemplate(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
env_rules: []
risk_level: med
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := RunCheck(contractPath, "med", "", "")
	if err == nil {
		t.Fatal("RunCheck() expected missing env_rules to block")
	}
	if summary.Checks[0].Check != "env_rules" {
		t.Fatalf("check = %q", summary.Checks[0].Check)
	}
	assertEnvRuleTemplate(t, summary.Suggestion)
}

func TestRunCheckHighMissingEnvRulesEmitsTemplate(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
  verify_thick: true
risk_level: high
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := RunCheck(contractPath, "high", "", "")
	if err == nil {
		t.Fatal("RunCheck() expected missing env_rules to block")
	}
	if summary.Checks[0].Check != "env_rules" {
		t.Fatalf("check = %q", summary.Checks[0].Check)
	}
	assertEnvRuleTemplate(t, summary.Suggestion)
}

func TestRunCheckCannotLowerContractRisk(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
  verify_thick: true
env_rules:
  - id: runner-no-net
    applies_to: runner_cwd
    check: no network in runner
    severity: blocking
risk_level: high
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := RunCheck(contractPath, "low", "", "")
	if err == nil {
		t.Fatal("RunCheck() expected error")
	}
	if summary.ExitCode != exitBlock || summary.Verdict != verdictBlock {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Reason != `requested risk "low" is lower than contract risk_level "high"` {
		t.Fatalf("reason = %q", summary.Reason)
	}
}

func TestRunVerifyPassesAfterCorrectedRiskEvidence(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
env_rules:
  - id: runner-no-net
    applies_to: runner_cwd
    check: no network in runner
    severity: blocking
risk_level: med
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(harness, "evidence", "ledger.jsonl")

	if _, err := RunCheck(contractPath, "low", ledgerPath, ""); err == nil {
		t.Fatal("RunCheck() expected lowered risk to block")
	}
	summary, err := RunCheck(contractPath, "med", ledgerPath, "")
	if err != nil {
		t.Fatalf("RunCheck() corrected risk error = %v", err)
	}
	if summary.Verdict != verdictPass {
		t.Fatalf("corrected risk verdict = %q", summary.Verdict)
	}

	verify, err := RunVerify(ledgerPath)
	if err != nil {
		t.Fatalf("RunVerify() after corrected risk error = %v, summary = %#v", err, verify)
	}
	if verify.Verdict != verdictPass {
		t.Fatalf("verify verdict = %q", verify.Verdict)
	}
}

func TestRunCheckLowDoesNotRequireEnvRules(t *testing.T) {
	contractPath, ledgerPath := writeLowRiskProject(t)

	summary, err := RunCheck(contractPath, "low", ledgerPath, "")
	if err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}
	if summary.Verdict != verdictPass {
		t.Fatalf("verdict = %q", summary.Verdict)
	}
	for _, check := range summary.Checks {
		if check.Check == "env_rules" {
			t.Fatalf("low risk should not run env_rules check: %#v", summary.Checks)
		}
	}
}

func TestRunCheckBlockingEnvAssertStopsBeforeBuildVerify(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: touch build-ran
env_rules:
  - id: aws-profile
    applies_to: verify
    reminder: AWS profile must match the target environment
    assert: "exit 9"
    gate_action: prompt
    command_prefixes: ["terraform apply"]
    severity: blocking
risk_level: med
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := RunCheck(contractPath, "med", "", "")
	if err == nil {
		t.Fatal("RunCheck() expected blocking env assert failure")
	}
	if summary.Verdict != verdictBlock || summary.Checks[len(summary.Checks)-1].Check != "env_rule.aws-profile" {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := os.Stat(filepath.Join(project, "build-ran")); !os.IsNotExist(err) {
		t.Fatalf("build verify ran after blocking env assert: %v", err)
	}
}

func TestRunCheckWarningEnvAssertDoesNotBlock(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
env_rules:
  - id: optional-profile
    applies_to: verify
    reminder: Optional profile check
    assert: "exit 3"
    severity: warning
risk_level: med
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := RunCheck(contractPath, "med", "", "")
	if err != nil {
		t.Fatalf("RunCheck() warning error = %v", err)
	}
	if summary.Verdict != verdictPass {
		t.Fatalf("summary = %#v", summary)
	}
	foundWarning := false
	for _, result := range summary.Checks {
		if result.Check == "env_rule.optional-profile" && result.Verdict == verdictWarn {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("warning result missing: %#v", summary.Checks)
	}
}

func TestRunCheckWritesDefaultLedger(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
risk_level: low
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := RunCheck(contractPath, "low", "", "")
	if err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}
	if summary.Verdict != verdictPass {
		t.Fatalf("verdict = %q", summary.Verdict)
	}
	ledgerPath := filepath.Join(harness, "evidence", "ledger.jsonl")
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("default ledger missing: %v", err)
	}
}

func TestRunCheckRelativeLedgerUsesProjectRoot(t *testing.T) {
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
risk_level: low
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if chdirErr := os.Chdir(cwd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}

	summary, err := RunCheck(".harness/project-contract.yaml", "low", ".harness/evidence/ledger.jsonl", "")
	if err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}
	if summary.Verdict != verdictPass {
		t.Fatalf("verdict = %q", summary.Verdict)
	}

	entries := readLedgerEntries(t, filepath.Join(project, ".harness", "evidence", "ledger.jsonl"))
	if len(entries) != 2 {
		t.Fatalf("ledger entries len = %d", len(entries))
	}
	proofPath := filepath.Join(project, filepath.FromSlash(entries[1].ProofRef))
	if _, err := os.Stat(proofPath); err != nil {
		t.Fatalf("proof missing under project root: %v", err)
	}
}

func TestRunVerifyAppendsVerifierVerdict(t *testing.T) {
	contractPath, ledgerPath := writeLowRiskProject(t)

	if _, err := RunCheck(contractPath, "low", ledgerPath, ""); err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}
	workerOnly, err := RunReplayRequireVerifier(ledgerPath, "")
	if err == nil {
		t.Fatal("RunReplayRequireVerifier() expected worker-only ledger to block")
	}
	if !strings.Contains(workerOnly.Reason, "latest ledger entry is not a verifier verdict") {
		t.Fatalf("worker-only reason = %q", workerOnly.Reason)
	}

	summary, err := RunVerify(ledgerPath)
	if err != nil {
		t.Fatalf("RunVerify() error = %v", err)
	}
	if summary.Verdict != verdictPass {
		t.Fatalf("verify verdict = %q", summary.Verdict)
	}

	entries := readLedgerEntries(t, ledgerPath)
	last := entries[len(entries)-1]
	if last.ActorRole != actorVerifier {
		t.Fatalf("last actor_role = %q", last.ActorRole)
	}
	if last.Check != checkVerifierReplay {
		t.Fatalf("last check = %q", last.Check)
	}

	replay, err := RunReplayRequireVerifier(ledgerPath, "")
	if err != nil {
		t.Fatalf("RunReplayRequireVerifier() error = %v", err)
	}
	if replay.Verdict != verdictPass {
		t.Fatalf("replay verdict = %q", replay.Verdict)
	}
}

func TestRunReplayRequireVerifierBlocksStaleVerdict(t *testing.T) {
	contractPath, ledgerPath := writeLowRiskProject(t)
	if _, err := RunCheck(contractPath, "low", ledgerPath, ""); err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}
	if _, err := RunVerify(ledgerPath); err != nil {
		t.Fatalf("RunVerify() error = %v", err)
	}
	if _, err := RunCheck(contractPath, "low", ledgerPath, ""); err != nil {
		t.Fatalf("second RunCheck() error = %v", err)
	}

	summary, err := RunReplayRequireVerifier(ledgerPath, "")
	if err == nil {
		t.Fatal("RunReplayRequireVerifier() expected stale verifier to block")
	}
	if !strings.Contains(summary.Reason, "latest ledger entry is not a verifier verdict") {
		t.Fatalf("reason = %q", summary.Reason)
	}
}

func TestRunReplayBlocksMismatchedProofBody(t *testing.T) {
	contractPath, ledgerPath := writeLowRiskProject(t)
	if _, err := RunCheck(contractPath, "low", ledgerPath, ""); err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}

	entry := findLedgerEntry(t, readLedgerEntries(t, ledgerPath), "build.verify")
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(ledgerPath)))
	proofPath := filepath.Join(projectRoot, filepath.FromSlash(entry.ProofRef))
	if err := os.WriteFile(proofPath, []byte("check: build.verify\nexit_code: 0\nreason: tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := RunReplay(ledgerPath)
	if err == nil {
		t.Fatal("RunReplay() expected proof mismatch to block")
	}
	if !strings.Contains(summary.Reason, "proof body mismatch for build.verify: reason") {
		t.Fatalf("reason = %q", summary.Reason)
	}
}

func TestRunReplayBlocksProofHashMismatch(t *testing.T) {
	contractPath, ledgerPath := writeLowRiskProject(t)
	if _, err := RunCheck(contractPath, "low", ledgerPath, ""); err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}

	entry := findLedgerEntry(t, readLedgerEntries(t, ledgerPath), "build.verify")
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(ledgerPath)))
	proofPath := filepath.Join(projectRoot, filepath.FromSlash(entry.ProofRef))
	proof, err := os.ReadFile(proofPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proofPath, append(proof, []byte("extra: tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := RunReplay(ledgerPath)
	if err == nil {
		t.Fatal("RunReplay() expected proof hash mismatch to block")
	}
	if !strings.Contains(summary.Reason, "proof body mismatch for build.verify: proof_sha256") {
		t.Fatalf("reason = %q", summary.Reason)
	}
}

func writeLowRiskProject(t *testing.T) (string, string) {
	t.Helper()
	project := t.TempDir()
	harness := filepath.Join(project, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(harness, "project-contract.yaml")
	body := `service: tapp-merchant
owners: [hoannt1]
build:
  verify: true
risk_level: low
`
	if err := os.WriteFile(contractPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return contractPath, filepath.Join(harness, "evidence", "ledger.jsonl")
}

func readLedgerEntries(t *testing.T, ledgerPath string) []LedgerEntry {
	t.Helper()
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	entries := make([]LedgerEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry LedgerEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("unmarshal ledger line %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func findLedgerEntry(t *testing.T, entries []LedgerEntry, check string) LedgerEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.Check == check {
			return entry
		}
	}
	t.Fatalf("ledger entry for check %q not found", check)
	return LedgerEntry{}
}

func assertEnvRuleTemplate(t *testing.T, output string) {
	t.Helper()
	required := []string{
		"env_rules:",
		"- id: tapp-merchant-runner-no-net",
		"applies_to: tapp-merchant_evidence_runner",
		"reminder: \"No curl/wget/pip/npm or other network dependency fetches during tapp-merchant evidence runs; dependencies must be vendored or preinstalled.\"",
		"assert: \"test -z \\\"${EZH_NETWORK_FETCH_DETECTED:-}\\\"\"",
		"severity: blocking",
	}
	for _, want := range required {
		if !strings.Contains(output, want) {
			t.Fatalf("template missing %q in:\n%s", want, output)
		}
	}
}
