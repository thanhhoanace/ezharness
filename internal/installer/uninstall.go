package installer

// uninstall reverses `ezh install`. It mirrors install's manifest shape and is
// idempotent: removing already-absent files is not an error.
//
// Two modes:
//   - default: reverse the gate WIRING (hooks/alias/scaffold) but PRESERVE
//     user/runtime state — the contract, the evidence ledger + proofs, and the
//     run-report telemetry. This is the audit-safe default.
//   - --purge: remove the entire .harness/ directory (evidence, telemetry, all),
//     closing the "uninstall left junk ledger entries" gap. Opt-in and explicit.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// UninstallOptions configures an uninstall run.
type UninstallOptions struct {
	// Target is the repository root to uninstall from. When empty, the toplevel
	// of the current git working directory is used (same as install).
	Target string
	// Purge removes the entire .harness/ directory, including evidence and
	// telemetry. When false, those are preserved.
	Purge bool
}

// Uninstall performs the reversal and writes a human-readable summary of what
// was removed vs preserved. It is safe to run when already uninstalled.
func Uninstall(opts UninstallOptions, stdout io.Writer, stderr io.Writer) error {
	repoRoot, err := resolveRepoRoot(opts.Target)
	if err != nil {
		return err
	}

	harness := filepath.Join(repoRoot, ".harness")
	m := readManifest(filepath.Join(harness, "install-manifest.json"))

	// 1. Reverse the gate wiring: restore or unset core.hooksPath.
	restoreOrUnsetHooksPath(repoRoot, m)
	// alias.gate: always unset (install always sets it).
	_, _ = runGit(repoRoot, "config", "--local", "--unset", "alias.gate")

	var removed []string
	var preserved []string

	// 2. Remove the materialized hooks + bin dirs (always install-created).
	for _, rel := range []string{
		filepath.Join("hooks"),
		filepath.Join("bin"),
	} {
		path := filepath.Join(harness, rel)
		if removeIfPresent(path) {
			removed = append(removed, filepath.Join(".harness", rel))
		}
	}

	// 3. Remove ezha-managed scaffold files the manifest marks as created.
	if m != nil {
		if m.Ownership.AgentsCreated {
			if removeIfPresent(filepath.Join(harness, "AGENTS.md")) {
				removed = append(removed, ".harness/AGENTS.md")
			}
		}
	}
	// config.json and install-manifest.json are always install-created scaffold.
	for _, name := range []string{"config.json", "install-manifest.json"} {
		if removeIfPresent(filepath.Join(harness, name)) {
			removed = append(removed, ".harness/"+name)
		}
	}

	if opts.Purge {
		// Remove the entire .harness/ directory (evidence + telemetry included).
		if removeIfPresent(harness) {
			removed = append(removed, ".harness/ (entire directory, including evidence + telemetry)")
		}
		// .gitignore: only remove the entry install added.
		if m != nil && m.Ownership.GitignoreEntryAdded {
			if removeGitignoreEntry(filepath.Join(repoRoot, ".gitignore")) {
				removed = append(removed, ".gitignore: .harness/ entry")
			}
		}
	} else {
		// Default: preserve user/runtime state.
		preserved = append(preserved,
			".harness/project-contract.yaml",
			".harness/evidence/ (ledger + proofs)",
			".harness/run-report.jsonl",
			".harness/run-report.md",
		)
	}

	fmt.Fprintf(stdout, "ezh uninstall complete\n")
	fmt.Fprintf(stdout, "repo_root: %s\n", repoRoot)
	fmt.Fprintf(stdout, "wiring:    core.hooksPath + alias.gate reverted\n")
	if len(removed) == 0 {
		fmt.Fprintf(stdout, "removed:   (nothing — already uninstalled)\n")
	} else {
		fmt.Fprintf(stdout, "removed:\n")
		for _, r := range removed {
			fmt.Fprintf(stdout, "  - %s\n", r)
		}
	}
	if len(preserved) > 0 {
		fmt.Fprintf(stdout, "preserved (audit-safe; pass --purge to remove):\n")
		for _, p := range preserved {
			fmt.Fprintf(stdout, "  - %s\n", p)
		}
	}
	return nil
}

// restoreOrUnsetHooksPath restores the previously-recorded local core.hooksPath
// from the manifest, or unsets it when none was recorded.
func restoreOrUnsetHooksPath(repoRoot string, m *manifest) {
	var previous *string
	if m != nil {
		if prev, ok := m.HooksPrevious["."]; ok {
			previous = prev
		}
	}
	// Guard against the gate's own path being recorded as "previous" (install
	// sets core.hooksPath before snapshotting it): restoring that would re-wire
	// the gate we are removing. Treat it as no genuine previous and unset.
	if previous != nil {
		prev := strings.TrimSpace(*previous)
		if prev != "" && prev != ".harness/hooks" {
			_, _ = runGit(repoRoot, "config", "--local", "core.hooksPath", prev)
			return
		}
	}
	_, _ = runGit(repoRoot, "config", "--local", "--unset", "core.hooksPath")
}

// readManifest parses the install manifest, returning nil when absent or
// unreadable (uninstall stays idempotent without it).
func readManifest(path string) *manifest {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return &m
}

// removeIfPresent removes a file or directory tree. Returns true when something
// was removed, false when it was already absent. Other errors are swallowed to
// keep uninstall best-effort and idempotent.
func removeIfPresent(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	if err := os.RemoveAll(path); err != nil {
		return false
	}
	return true
}

// removeGitignoreEntry removes the ".harness/" line from .gitignore. Returns
// true when the entry was present and removed.
func removeGitignoreEntry(path string) bool {
	const entry = ".harness/"
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var kept [][]byte
	found := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if strings.TrimSpace(string(line)) == entry {
			found = true
			continue
		}
		kept = append(kept, append([]byte(nil), line...))
	}
	if !found {
		return false
	}
	var buf bytes.Buffer
	for _, line := range kept {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return false
	}
	return true
}
