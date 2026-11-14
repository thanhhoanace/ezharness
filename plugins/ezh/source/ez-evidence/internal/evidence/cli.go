package evidence

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ez-evidence <check|verify|replay|attest|loopcheck|lesson|plan|capabilities> ...")
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
	case "loopcheck":
		return runLoopCheckCommand(args[1:], stdout, stderr)
	case "lesson":
		return runLessonCommand(args[1:], stdout, stderr)
	case "plan":
		return runPlanCommand(args[1:], stdout, stderr)
	case "capabilities":
		return runCapabilitiesCommand(stdout)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return exitBlock
	}
}

// capabilitiesProtocolVersion identifies the gate's machine-readable contract.
// Bump it when a verb's semantics change incompatibly; add new verbs to
// capabilitiesList without bumping. The installer (and any other tooling)
// checks THIS output instead of grepping human error text to decide whether a
// binary is current enough for the hooks it ships.
const capabilitiesProtocolVersion = 1

var capabilitiesList = []string{
	"check", "verify", "replay", "attest",
	"loopcheck",
	"lesson.lookup", "lesson.lint",
	"plan.validate", "plan.save",
	"capabilities",
}

func runCapabilitiesCommand(stdout io.Writer) int {
	payload := struct {
		ProtocolVersion int      `json:"protocol_version"`
		Capabilities    []string `json:"capabilities"`
	}{capabilitiesProtocolVersion, capabilitiesList}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return exitBlock
	}
	fmt.Fprintln(stdout, string(encoded))
	return exitOK
}

// runLoopCheckCommand enforces the P1 loop brakes (forbidden + budget +
// fail-closed) for a set of changed files. It never runs the agent. Exit 0 =
// allow, 2 = block. A forbidden path always blocks — there is no unlock path
// (D4 final: forbidden = blocked, full stop).
func runLoopCheckCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("loopcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	contractPath := flags.String("contract", "", "path to .harness/project-contract.yaml")
	changedCSV := flags.String("changed", "", "comma or newline separated list of changed repo-relative paths")
	runReportPath := flags.String("run-report", "", "path to run-report.jsonl for the budget count (default: <workspace>/.harness/run-report.jsonl)")
	jsonOut := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return exitBlock
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "loopcheck: unexpected positional arguments %v (paths go in --changed as a comma/newline list)\n", flags.Args())
		return exitBlock
	}
	if *contractPath == "" {
		fmt.Fprintln(stderr, "missing --contract")
		return exitBlock
	}

	contract, err := ReadContract(*contractPath)
	if err != nil {
		// AC-L1: loopcheck's charter is ONLY the loop brakes. A contract that
		// fails to parse for reasons unrelated to loop: must NOT turn a commit
		// with no loop: block into a block — that would diverge from
		// byte-identical v6.2 behaviour. Degrade to allow (fail-open for
		// loopcheck); the real evidence gate (check/replay) still catches a
		// malformed contract when evidence is required.
		fmt.Fprintf(stderr, "loopcheck: contract not parseable, skipping loop brakes: %v\n", err)
		if *jsonOut {
			fmt.Fprintln(stdout, `{"Decision":"allow","Reason":"contract not parseable; loop brakes skipped"}`)
		} else {
			fmt.Fprintln(stdout, "allow")
		}
		return exitOK
	}

	changed := splitChangedPaths(*changedCSV)
	reportPath := strings.TrimSpace(*runReportPath)
	if reportPath == "" {
		reportPath = loopRunReportPath(*contractPath)
	}

	outcome := RunLoopCheck(contract, changed, reportPath)

	if *jsonOut {
		encoded, encErr := json.Marshal(outcome)
		if encErr != nil {
			fmt.Fprintf(stderr, "encode loopcheck: %v\n", encErr)
			return exitBlock
		}
		fmt.Fprintln(stdout, string(encoded))
	} else if outcome.Decision == loopBlock {
		fmt.Fprintln(stderr, outcome.Reason)
	} else {
		fmt.Fprintln(stdout, "allow")
	}

	if outcome.Decision == loopBlock {
		return exitBlock
	}
	return exitOK
}

// runLessonCommand dispatches the P2 stateless lesson verbs (lookup|lint).
func runLessonCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ez-evidence lesson <lookup|lint> ...")
		return lessonExitUsage
	}
	switch args[0] {
	case "lookup":
		return runLessonLookupCommand(args[1:], stdout, stderr)
	case "lint":
		return runLessonLintCommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown lesson verb %q\n", args[0])
		return lessonExitUsage
	}
}

func runLessonLookupCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("lesson lookup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	changedCSV := flags.String("changed", "", "comma or newline separated list of changed repo-relative paths")
	vaultDir := flags.String("dir", "", "lessons vault dir (default: <repo-root>/.harness/lessons)")
	repoRoot := flags.String("repo-root", ".", "repo root for resolving lesson file paths")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return lessonExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "lesson lookup: unexpected positional arguments %v (paths go in --changed as a comma/newline list)\n", flags.Args())
		return lessonExitUsage
	}
	dir := resolveLessonVaultDir(*vaultDir, *repoRoot)
	changed := splitChangedPaths(*changedCSV)

	result, exit := LessonLookup(dir, *repoRoot, changed)
	if *format == "json" {
		encoded, encErr := json.Marshal(result)
		if encErr != nil {
			fmt.Fprintf(stderr, "encode lookup: %v\n", encErr)
			return lessonExitUsage
		}
		fmt.Fprintln(stdout, string(encoded))
	} else {
		for _, m := range result.Matches {
			fmt.Fprintln(stdout, m)
		}
		for _, e := range result.Errors {
			fmt.Fprintln(stderr, "degraded: "+e)
		}
	}
	return exit
}

func runLessonLintCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("lesson lint", flag.ContinueOnError)
	flags.SetOutput(stderr)
	vaultDir := flags.String("dir", "", "lessons vault dir (default: <repo-root>/.harness/lessons)")
	repoRoot := flags.String("repo-root", ".", "repo root for resolving lesson file paths")
	if err := flags.Parse(args); err != nil {
		return lessonExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "lesson lint: unexpected positional arguments %v\n", flags.Args())
		return lessonExitUsage
	}
	dir := resolveLessonVaultDir(*vaultDir, *repoRoot)

	problems, warnings, exit := LessonLint(dir, *repoRoot)
	for _, w := range warnings {
		fmt.Fprintln(stderr, "warn: "+w)
	}
	for _, p := range problems {
		fmt.Fprintln(stderr, "lint: "+p)
	}
	if exit == lessonExitOK {
		fmt.Fprintln(stdout, "lessons index OK")
	}
	return exit
}

func resolveLessonVaultDir(vaultDir string, repoRoot string) string {
	if strings.TrimSpace(vaultDir) != "" {
		return vaultDir
	}
	return filepath.Join(repoRoot, ".harness", "lessons")
}

// runPlanCommand dispatches the B#1 plan-conformance verbs (validate|save).
func runPlanCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ez-evidence plan <validate|save> ...")
		return planExitUsage
	}
	switch args[0] {
	case "validate":
		return runPlanValidateCommand(args[1:], stdout, stderr)
	case "save":
		return runPlanSaveCommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown plan verb %q\n", args[0])
		return planExitUsage
	}
}

func runPlanValidateCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("plan validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "path to the plan markdown file to validate")
	patterns := flags.String("env-patterns", "", "env-patterns file (default: .harness/plankit/env-patterns of the owning workspace)")
	if err := flags.Parse(args); err != nil {
		return planExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "plan validate: unexpected positional arguments %v (pass the plan via --file)\n", flags.Args())
		return planExitUsage
	}
	if strings.TrimSpace(*file) == "" {
		fmt.Fprintln(stderr, "missing --file")
		return planExitUsage
	}

	report, exit := PlanValidate(*file, PlanOptions{EnvPatternsFile: *patterns})
	for _, l := range report {
		fmt.Fprintln(stdout, l)
	}
	return exit
}

func runPlanSaveCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("plan save", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "path to the draft plan markdown file to validate and save")
	repoRoot := flags.String("repo-root", ".", "repo root under which .harness/plans/<basename> is written")
	patterns := flags.String("env-patterns", "", "env-patterns file (default: .harness/plankit/env-patterns of the owning workspace)")
	if err := flags.Parse(args); err != nil {
		return planExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "plan save: unexpected positional arguments %v (pass the plan via --file)\n", flags.Args())
		return planExitUsage
	}
	if strings.TrimSpace(*file) == "" {
		fmt.Fprintln(stderr, "missing --file")
		return planExitUsage
	}

	dest, report, exit := PlanSave(*file, *repoRoot, PlanOptions{EnvPatternsFile: *patterns})
	for _, l := range report {
		fmt.Fprintln(stdout, l)
	}
	if exit == planExitOK {
		fmt.Fprintf(stdout, "plan saved %s\n", dest)
	}
	return exit
}

// splitChangedPaths accepts either comma- or newline-separated path lists.
func splitChangedPaths(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if t := strings.TrimSpace(f); t != "" {
			out = append(out, t)
		}
	}
	return out
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
	id := newRunIdentity(start)
	summary, err := RunCheck(*contractPath, *risk, *ledgerPath, *repoRoot, id)
	recordRunReport(runReportContext{command: "check", risk: *risk, workspaceHint: *ledgerPath, repoRoot: *repoRoot, runID: id.RunID, traceID: id.TraceID, spanID: id.SpanID}, summary, err, time.Since(start), start)
	return emitResult(summary, err, *jsonOut, stdout, stderr)
}

func runReplayCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	jsonOut := false
	requireVerifier := false
	expectTree := ""
	changedCSV := ""
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
		case "--changed":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "replay: --changed requires a value")
				return exitBlock
			}
			i++
			changedCSV = args[i]
		default:
			if ledgerPath != "" {
				fmt.Fprintln(stderr, "replay accepts exactly one ledger path")
				return exitBlock
			}
			ledgerPath = arg
		}
	}
	if ledgerPath == "" {
		fmt.Fprintln(stderr, "usage: ez-evidence replay <ledger> [--json] [--require-verifier] [--expect-tree <tree>] [--changed <p1,p2,...>]")
		return exitBlock
	}

	var summary ResultSummary
	var err error
	start := time.Now()
	id := newRunIdentity(start)
	if requireVerifier {
		summary, err = RunReplayRequireVerifier(ledgerPath, expectTree)
	} else {
		summary, err = RunReplay(ledgerPath)
	}
	ctx := runReportContext{command: "replay", risk: "", workspaceHint: ledgerPath, runID: id.RunID, traceID: id.TraceID, spanID: id.SpanID}
	// AC-S6: on a BLOCK, attach the lessons the resolver matched for the changed
	// paths so v6.4 can compute the recurrence metric from run-report history.
	// Best-effort — a resolver failure never changes the verdict.
	if err != nil {
		ctx.lessonsMatched = lessonsForChangedPaths(ledgerPath, splitChangedPaths(changedCSV))
	}
	recordRunReport(ctx, summary, err, time.Since(start), start)
	return emitResult(summary, err, jsonOut, stdout, stderr)
}

// lessonsForChangedPaths resolves the lesson files matching the changed paths,
// using the workspace's .harness/lessons/ vault. A degraded vault still returns
// whatever partial matches it could resolve (best for telemetry); the exit code
// is ignored here because this feeds AC-S6 telemetry only, never a verdict.
func lessonsForChangedPaths(ledgerPath string, changed []string) []string {
	if len(changed) == 0 {
		return nil
	}
	workspace := resolveRunReportWorkspace(ledgerPath)
	if workspace == "" {
		return nil
	}
	vaultDir := filepath.Join(workspace, ".harness", "lessons")
	result, _ := LessonLookup(vaultDir, workspace, changed)
	return result.Matches
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
	id := newRunIdentity(start)
	summary, err := RunVerify(ledgerPath, id)
	recordRunReport(runReportContext{command: "verify", risk: "", workspaceHint: ledgerPath, runID: id.RunID, traceID: id.TraceID, spanID: id.SpanID}, summary, err, time.Since(start), start)
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
	id := newRunIdentity(start)
	checkSummary, checkErr := RunCheck(*contractPath, *risk, *ledgerPath, *repoRoot, id)
	if checkErr != nil {
		recordRunReport(runReportContext{command: "attest", risk: *risk, workspaceHint: *ledgerPath, repoRoot: *repoRoot, runID: id.RunID, traceID: id.TraceID, spanID: id.SpanID}, checkSummary, checkErr, time.Since(start), start)
		return emitResult(checkSummary, checkErr, *jsonOut, stdout, stderr)
	}
	verifySummary, verifyErr := RunVerify(*ledgerPath, id)
	recordRunReport(runReportContext{command: "attest", risk: *risk, workspaceHint: *ledgerPath, repoRoot: *repoRoot, runID: id.RunID, traceID: id.TraceID, spanID: id.SpanID}, verifySummary, verifyErr, time.Since(start), start)
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
