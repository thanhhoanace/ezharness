// Command ez is the single-binary entrypoint for ezHarness — the per-repo
// evidence gate. It bundles the evidence engine (check/verify/replay/attest/
// loopcheck/lesson) so the whole tool ships as one static binary, installable
// via `go install`, Homebrew, or the curl|sh bootstrap.
//
// v6.3 renamed the binary from `ezh` to `ez`; the `ezh` alias was removed in v6.4.
package main

import (
	"os"

	"ezharness.local/ezh-evidence/internal/evidence"
)

// version is overridden at release time via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	os.Exit(evidence.RunMain("ez", version, os.Args[1:], os.Stdout, os.Stderr))
}
