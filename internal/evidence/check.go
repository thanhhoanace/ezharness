package evidence

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// RunCheck runs the contract's verify + env_rules and records a worker proof.
//
// repoRoot lets a caller bind evidence to a *different* git checkout than the
// one that owns the contract — needed for git worktrees: `.harness/` lives in
// the main checkout (workspace_root), but the commit happens in a linked
// worktree whose write-tree and verify cwd must be the worktree itself. Pass
// "" to keep the legacy behavior (bind to workspace_root).
func RunCheck(contractPath string, risk string, ledgerPath string, repoRoot string) (ResultSummary, error) {
	contract, err := ReadContract(contractPath)
	if err != nil {
		reason := err.Error()
		return ResultSummary{Command: "check", Verdict: verdictBlock, Reason: reason, ExitCode: exitBlock}, GateError{Check: "contract", Reason: reason, ExitCode: exitBlock}
	}

	workspaceRoot := contractProjectRoot(filepath.Clean(contractPath))
	codeRoot := workspaceRoot
	if r := strings.TrimSpace(repoRoot); r != "" {
		if abs, absErr := filepath.Abs(r); absErr == nil {
			codeRoot = abs
		} else {
			codeRoot = r
		}
	}
	if strings.TrimSpace(ledgerPath) == "" {
		ledgerPath = filepath.Join(workspaceRoot, ".harness", "evidence", "ledger.jsonl")
	}
	writer := NewLedgerWriter(ledgerPath, workspaceRoot)
	writer.Tree = gitTree(codeRoot)

	if err := validateContract(contract, risk); err != nil {
		gateErr := asGateError(err, "contract")
		proofRef, recordErr := writer.Record(gateErr.Check, verdictBlock, proofBody(gateErr.Check, "", gateErr.ExitCode, gateErr.Reason, ""), metadata("", gateErr.ExitCode, gateErr.Reason))
		summary := ResultSummary{
			Command:    "check",
			Verdict:    verdictBlock,
			Reason:     gateErr.Reason,
			Suggestion: gateErr.Suggestion,
			ExitCode:   exitBlock,
			Checks: []CheckResult{{
				Check:      gateErr.Check,
				Verdict:    verdictBlock,
				ProofRef:   proofRef,
				Reason:     gateErr.Reason,
				Suggestion: gateErr.Suggestion,
				ExitCode:   gateErr.ExitCode,
			}},
		}
		if recordErr != nil {
			return summary, GateError{Check: gateErr.Check, Reason: recordErr.Error(), ExitCode: exitBlock}
		}
		return summary, gateErr
	}

	summary := ResultSummary{Command: "check", Verdict: verdictPass, ExitCode: exitOK}
	riskReason := fmt.Sprintf("requested risk %q satisfies contract risk_level %q", risk, contract.RiskLevel)
	proofRef, err := writer.Record("risk", verdictPass, proofBody("risk", "", 0, riskReason, ""), metadata("", 0, riskReason))
	riskResult := CheckResult{Check: "risk", Verdict: verdictPass, ProofRef: proofRef, Reason: riskReason, ExitCode: 0}
	summary.Checks = append(summary.Checks, riskResult)
	if err != nil {
		summary.Verdict = verdictBlock
		summary.Reason = err.Error()
		summary.ExitCode = exitBlock
		return summary, GateError{Check: "risk", Reason: err.Error(), ExitCode: exitBlock}
	}

	checks := []commandCheck{{Name: "build.verify", Command: contract.Build.Verify}}
	if risk == "high" {
		checks = append(checks, commandCheck{Name: "build.verify_thick", Command: contract.Build.VerifyThick})
	}

	if risk == "med" || risk == "high" {
		for _, rule := range contract.EnvRules {
			if strings.TrimSpace(rule.Assert) == "" {
				continue
			}
			result, err := runEnvRuleAssert(rule, codeRoot, writer)
			summary.Checks = append(summary.Checks, result)
			if err != nil {
				gateErr := asGateError(err, result.Check)
				summary.Verdict = verdictBlock
				summary.Reason = gateErr.Reason
				summary.ExitCode = exitBlock
				return summary, gateErr
			}
		}
	}

	for _, check := range checks {
		result, err := runCommandCheck(check, codeRoot, writer)
		summary.Checks = append(summary.Checks, result)
		if err != nil {
			gateErr := asGateError(err, check.Name)
			summary.Verdict = verdictBlock
			summary.Reason = gateErr.Reason
			summary.Suggestion = gateErr.Suggestion
			summary.ExitCode = exitBlock
			return summary, gateErr
		}
	}

	if risk == "med" || risk == "high" {
		reason := fmt.Sprintf("%d env_rules validated", len(contract.EnvRules))
		proofRef, err := writer.Record("env_rules", verdictPass, proofBody("env_rules", "", 0, reason, ""), metadata("", 0, reason))
		result := CheckResult{Check: "env_rules", Verdict: verdictPass, ProofRef: proofRef, Reason: reason, ExitCode: 0}
		summary.Checks = append(summary.Checks, result)
		if err != nil {
			summary.Verdict = verdictBlock
			summary.Reason = err.Error()
			summary.ExitCode = exitBlock
			return summary, GateError{Check: "env_rules", Reason: err.Error(), ExitCode: exitBlock}
		}
	}

	return summary, nil
}

func runEnvRuleAssert(rule EnvRule, cwd string, writer LedgerWriter) (CheckResult, error) {
	name := "env_rule." + rule.ID
	exitCode, output := executeShell(rule.Assert, cwd)
	verdict := verdictPass
	reason := "assert command passed"
	if exitCode != 0 {
		if rule.Severity == "warning" {
			verdict = verdictWarn
			reason = fmt.Sprintf("%s warning assert failed with exit code %d", name, exitCode)
		} else {
			verdict = verdictBlock
			reason = fmt.Sprintf("%s blocking assert failed with exit code %d", name, exitCode)
		}
	}

	proofRef, err := writer.Record(name, verdict, proofBody(name, rule.Assert, exitCode, reason, output), metadata(rule.Assert, exitCode, reason))
	result := CheckResult{Check: name, Verdict: verdict, ProofRef: proofRef, Reason: reason, ExitCode: exitCode}
	if err != nil {
		return result, GateError{Check: name, Reason: err.Error(), ExitCode: exitBlock}
	}
	if exitCode != 0 && rule.Severity == "blocking" {
		return result, GateError{Check: name, Reason: reason, ExitCode: exitBlock}
	}
	return result, nil
}

type commandCheck struct {
	Name    string
	Command string
}

func runCommandCheck(check commandCheck, cwd string, writer LedgerWriter) (CheckResult, error) {
	if check.Name == "build.verify" {
		if trimmed := strings.TrimSpace(check.Command); trimmed == "" || trimmed == verifyUndefined {
			reason := "build.verify is undefined for this repo — define a real verify (see .harness AGENTS \"Define verify\"); the gate will not pass an unverified change"
			proofRef, err := writer.Record(check.Name, verdictBlock, proofBody(check.Name, trimmed, exitBlock, reason, ""), metadata(trimmed, exitBlock, reason))
			result := CheckResult{Check: check.Name, Verdict: verdictBlock, ProofRef: proofRef, Reason: reason, ExitCode: exitBlock}
			if err != nil {
				return result, GateError{Check: check.Name, Reason: err.Error(), ExitCode: exitBlock}
			}
			return result, GateError{Check: check.Name, Reason: reason, ExitCode: exitBlock}
		}
	}
	exitCode, output := executeShell(check.Command, cwd)
	verdict := verdictPass
	reason := "command passed"
	if exitCode != 0 {
		verdict = verdictBlock
		reason = fmt.Sprintf("%s failed with exit code %d", check.Name, exitCode)
	}

	proofRef, err := writer.Record(check.Name, verdict, proofBody(check.Name, check.Command, exitCode, reason, output), metadata(check.Command, exitCode, reason))
	result := CheckResult{
		Check:    check.Name,
		Verdict:  verdict,
		ProofRef: proofRef,
		Reason:   reason,
		ExitCode: exitCode,
	}
	if err != nil {
		return result, GateError{Check: check.Name, Reason: err.Error(), ExitCode: exitBlock}
	}
	if exitCode != 0 {
		return result, GateError{Check: check.Name, Reason: reason, ExitCode: exitBlock}
	}
	return result, nil
}

func executeShell(command string, cwd string) (int, string) {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = cwd
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	if err == nil {
		return 0, combined.String()
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), combined.String()
	}
	if strings.TrimSpace(combined.String()) == "" {
		return 127, err.Error()
	}
	return 127, combined.String() + "\n" + err.Error()
}

func proofBody(check string, command string, exitCode int, reason string, output string) string {
	var builder strings.Builder
	builder.WriteString("check: ")
	builder.WriteString(check)
	builder.WriteString("\n")
	if command != "" {
		builder.WriteString("command: ")
		builder.WriteString(command)
		builder.WriteString("\n")
	}
	builder.WriteString("exit_code: ")
	builder.WriteString(fmt.Sprintf("%d", exitCode))
	builder.WriteString("\n")
	builder.WriteString("reason: ")
	builder.WriteString(reason)
	builder.WriteString("\n")
	if output != "" {
		builder.WriteString("\noutput:\n")
		builder.WriteString(output)
		if !strings.HasSuffix(output, "\n") {
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func metadata(command string, exitCode int, reason string) map[string]interface{} {
	return map[string]interface{}{
		"command":   command,
		"exit_code": exitCode,
		"reason":    reason,
	}
}

func asGateError(err error, fallbackCheck string) GateError {
	if gateErr, ok := err.(GateError); ok {
		return gateErr
	}
	return GateError{Check: fallbackCheck, Reason: err.Error(), ExitCode: exitBlock}
}

func gitTree(dir string) string {
	cmd := exec.Command("git", "write-tree")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
