// Command ez-evidence is the evidence engine entrypoint (check/verify/replay/
// attest/loopcheck/lesson/capabilities). v6.3 renamed it from `ezh-evidence`;
// `ezh-evidence` remains as a deprecated alias for one release (removed in
// v6.4). The installed and packaged alias is a byte-identical COPY of this
// binary, so the deprecation notice must key off argv[0]: invoked under the
// legacy name, the same binary announces its deprecation.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"ezharness.local/ezh-evidence/internal/evidence"
)

func main() {
	if filepath.Base(os.Args[0]) == "ezh-evidence" {
		fmt.Fprintln(os.Stderr, "ezh-evidence: deprecated — use `ez-evidence` (the `ezh-evidence` alias is removed in v6.4)")
	}
	os.Exit(evidence.Run(os.Args[1:], os.Stdout, os.Stderr))
}
