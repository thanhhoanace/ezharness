package evidence

// RunMain is the bundled single-binary entrypoint for the `ez` binary. The
// binary name stays a parameter so usage text follows whatever name the
// binary is invoked as (the v6.3 `ezh` alias was removed in v6.4).

import (
	"fmt"
	"io"
)

// RunMain dispatches the top-level bundled commands (version/help + the evidence
// verbs). binName is the invoked binary name; version
// is the ldflags-injected build version. It returns the process exit code.
func RunMain(binName string, version string, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		mainUsage(binName, stderr)
		return exitBlock
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "%s %s\n", binName, version)
		return exitOK
	case "help", "--help", "-h":
		mainUsage(binName, stdout)
		return exitOK
	case "check", "verify", "replay", "attest", "loopcheck", "lesson", "plan", "capabilities":
		return Run(args, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n\n", binName, args[0])
		mainUsage(binName, stderr)
		return exitBlock
	}
}

func mainUsage(binName string, w io.Writer) {
	fmt.Fprintf(w, `%s — ezHarness per-repo evidence gate ("done means done" at the git boundary)

Usage:
  %s attest    --contract <path> --risk <low|med|high> [--ledger <path>] [--json]
  %s check     --contract <path> --risk <low|med|high> [--ledger <path>] [--json]
  %s verify    <ledger> [--json]
  %s replay    <ledger> [--require-verifier] [--expect-tree <tree>] [--json]
  %s loopcheck --contract <path> --changed <p1,p2,...> [--run-report <path>] [--json]
  %s lesson    lookup --changed <p1,p2,...> [--format json] | lint
  %s plan      validate --file <path> [--env-patterns <file>] | save --file <path> [--repo-root <path>]
  %s capabilities
  %s version

The pre-commit/pre-push hook calls `+"`%s replay --require-verifier`"+`. Standard flow:
check (worker), then verify from a DIFFERENT session/actor. `+"`%s attest`"+` is a solo
convenience path — the same actor runs check and verify, which does not satisfy
independent verification for high-risk work.
`, binName, binName, binName, binName, binName, binName, binName, binName, binName, binName, binName, binName)
}
