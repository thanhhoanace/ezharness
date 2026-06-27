// Command ezh is the single-binary entrypoint for ezHarness — the per-repo
// evidence gate. It bundles the evidence engine (check/verify/replay/attest) so
// the whole tool ships as one static binary, installable via `go install`,
// Homebrew, or the curl|sh bootstrap.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/thanhhoanace/ezharness/v6/internal/evidence"
	"github.com/thanhhoanace/ezharness/v6/internal/installer"
)

// version is overridden at release time via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage(os.Stderr)
		os.Exit(2)
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Printf("ezh %s\n", version)
	case "help", "--help", "-h":
		usage(os.Stdout)
	case "install":
		os.Exit(runInstall(args[1:]))
	case "check", "verify", "replay", "attest":
		os.Exit(evidence.Run(args, os.Stdout, os.Stderr))
	default:
		fmt.Fprintf(os.Stderr, "ezh: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(2)
	}
}

// runInstall parses the install subcommand flags and installs the per-repo
// evidence gate using the embedded assets.
func runInstall(args []string) int {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	target := flags.String("target", "", "repository root to install into (default: git toplevel of the current directory)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "ezh install: unexpected positional arguments")
		return 2
	}
	if err := installer.Run(installer.Options{Target: *target}, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "ezh install: %v\n", err)
		return 1
	}
	return 0
}

func usage(w *os.File) {
	fmt.Fprint(w, `ezh — ezHarness per-repo evidence gate ("done means done" at the git boundary)

Usage:
  ezh install [--target <dir>]
  ezh attest  --contract <path> --risk <low|med|high> [--ledger <path>] [--json]
  ezh check   --contract <path> --risk <low|med|high> [--ledger <path>] [--json]
  ezh verify  <ledger> [--json]
  ezh replay  <ledger> [--require-verifier] [--expect-tree <tree>] [--json]
  ezh version

The pre-commit/pre-push hook calls `+"`ezh replay --require-verifier`"+`. Run
`+"`ezh attest`"+` (worker check + independent verifier in one step) before committing,
or `+"`git gate [risk]`"+` if the gate is installed.
`)
}
