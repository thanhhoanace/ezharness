package evidence

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"
)

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ezh-evidence <check|verify|replay|attest> ...")
		return exitBlock
	}

	switch args[0] {
	case "check":
		return runCheckCommand(args[1:], stdout, stderr)
	case "replay":
		return runReplayCommand(args[1:], stdout, stderr)
	case "verify":
		return runVerifyCommand(args[1:], stdout, stderr)
	case "attest":
		return runAttestCommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return exitBlock
	}
}

func runCheckCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	contractPath := flags.String("contract", "", "path to .harness/project-contract.yaml")
	risk := flags.String("risk", "", "risk level: high, med, or low")
	ledgerPath := flags.String("ledger", "", "optional evidence ledger JSONL path")
	repoRoot := flags.String("repo-root", "", "git repo/worktree root to bind evidence to (default: contract workspace_root). Required for worktree commits so write-tree and verify cwd match the worktree, not the main checkout.")
	jsonOut := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return exitBlock
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "check does not accept positional arguments")
		return exitBlock
	}
	if *contractPath == "" {
		fmt.Fprintln(stderr, "missing --contract")
		return exitBlock
	}
	if *risk == "" {
		fmt.Fprintln(stderr, "missing --risk")
		return exitBlock
	}

	start := time.Now()
	summary, err := RunCheck(*contractPath, *risk, *ledgerPath, *repoRoot)
	recordRunReport(runReportContext{command: "check", risk: *risk, workspaceHint: *ledgerPath, repoRoot: *repoRoot}, summary, err, time.Since(start), start)
	return emitResult(summary, err, *jsonOut, stdout, stderr)
}

func runReplayCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	jsonOut := false
	requireVerifier := false
	expectTree := ""
	ledgerPath := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--json":
			jsonOut = true
		case "--require-verifier":
			requireVerifier = true
		case "--expect-tree":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "replay: --expect-tree requires a value")
				return exitBlock
			}
			i++
			expectTree = args[i]
		default:
			if ledgerPath != "" {
				fmt.Fprintln(stderr, "replay accepts exactly one ledger path")
				return exitBlock
			}
			ledgerPath = arg
		}
	}
	if ledgerPath == "" {
		fmt.Fprintln(stderr, "usage: ezh-evidence replay <ledger> [--json] [--require-verifier] [--expect-tree <tree>]")
		return exitBlock
	}

	var summary ResultSummary
	var err error
	start := time.Now()
	if requireVerifier {
		summary, err = RunReplayRequireVerifier(ledgerPath, expectTree)
	} else {
		summary, err = RunReplay(ledgerPath)
	}
	recordRunReport(runReportContext{command: "replay", risk: "", workspaceHint: ledgerPath}, summary, err, time.Since(start), start)
	return emitResult(summary, err, jsonOut, stdout, stderr)
}

func runVerifyCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	jsonOut := false
	ledgerPath := ""
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		default:
			if ledgerPath != "" {
				fmt.Fprintln(stderr, "verify accepts exactly one ledger path")
				return exitBlock
			}
			ledgerPath = arg
		}
	}
	if ledgerPath == "" {
		fmt.Fprintln(stderr, "usage: ezh-evidence verify <ledger> [--json]")
		return exitBlock
	}

	start := time.Now()
	summary, err := RunVerify(ledgerPath)
	recordRunReport(runReportContext{command: "verify", risk: "", workspaceHint: ledgerPath}, summary, err, time.Since(start), start)
	return emitResult(summary, err, jsonOut, stdout, stderr)
}

// runAttestCommand is the one-command convenience path: it runs the worker
// `check` (contract verify + env_rules, recording proof bound to the staged
// tree) and, if that passes, the independent `verify` verdict — so a single
// invocation produces exactly the evidence the commit/push gate replays.
// Worker and verifier are the same actor here (collapsed for the solo/fast
// path); for true independence on high-risk work, run check and verify as
// separate actors/sessions instead.
func runAttestCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("attest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	contractPath := flags.String("contract", "", "path to .harness/project-contract.yaml")
	risk := flags.String("risk", "", "risk level: high, med, or low")
	ledgerPath := flags.String("ledger", ".harness/evidence/ledger.jsonl", "evidence ledger JSONL path")
	repoRoot := flags.String("repo-root", "", "git repo/worktree root to bind evidence to (default: contract workspace_root). Required for worktree commits so write-tree and verify cwd match the worktree, not the main checkout.")
	jsonOut := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return exitBlock
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "attest does not accept positional arguments")
		return exitBlock
	}
	if *contractPath == "" {
		fmt.Fprintln(stderr, "missing --contract")
		return exitBlock
	}
	if *risk == "" {
		fmt.Fprintln(stderr, "missing --risk")
		return exitBlock
	}
	if *ledgerPath == "" {
		fmt.Fprintln(stderr, "missing --ledger")
		return exitBlock
	}

	start := time.Now()
	checkSummary, checkErr := RunCheck(*contractPath, *risk, *ledgerPath, *repoRoot)
	if checkErr != nil {
		recordRunReport(runReportContext{command: "attest", risk: *risk, workspaceHint: *ledgerPath, repoRoot: *repoRoot}, checkSummary, checkErr, time.Since(start), start)
		return emitResult(checkSummary, checkErr, *jsonOut, stdout, stderr)
	}
	verifySummary, verifyErr := RunVerify(*ledgerPath)
	recordRunReport(runReportContext{command: "attest", risk: *risk, workspaceHint: *ledgerPath, repoRoot: *repoRoot}, verifySummary, verifyErr, time.Since(start), start)
	return emitResult(verifySummary, verifyErr, *jsonOut, stdout, stderr)
}

func emitResult(summary ResultSummary, err error, jsonOut bool, stdout io.Writer, stderr io.Writer) int {
	if jsonOut {
		target := stdout
		encoded, encodeErr := json.Marshal(summary)
		if encodeErr != nil {
			fmt.Fprintf(stderr, "encode result: %v\n", encodeErr)
			return exitBlock
		}
		fmt.Fprintln(target, string(encoded))
	} else if err != nil {
		fmt.Fprintln(stderr, summary.Reason)
		if summary.Suggestion != "" {
			fmt.Fprintln(stderr)
			fmt.Fprintln(stderr, summary.Suggestion)
		}
	} else {
		fmt.Fprintf(stdout, "%s\n", summary.Verdict)
	}

	if err != nil {
		return exitBlock
	}
	return exitOK
}
