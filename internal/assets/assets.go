// Package assets embeds the ezHarness hook scripts and project templates into
// the ezh binary so that `ezh install` can materialize a per-repo evidence gate
// without any external files.
//
// The embedded trees under hooks/ and templates/ are the single source of truth
// for the distributable assets. scripts/install.sh (the curl|sh bootstrap that
// fetches a release binary) does not read these files; it only installs the
// binary, which then carries these assets itself.
package assets

import (
	"embed"
	"io/fs"
)

// hookFiles holds the deterministic git-hook implementation copied verbatim from
// the canonical src/hooks tree (the fail-closed v6.1 versions).
//
//go:embed all:hooks
var hookFiles embed.FS

// templateFiles holds the project templates (project-contract.yaml, AGENTS.md,
// engine skills, context scaffolding) copied verbatim from src/templates.
//
//go:embed all:templates
var templateFiles embed.FS

// Hooks returns the embedded hook scripts rooted at the hooks/ directory.
func Hooks() (fs.FS, error) {
	return fs.Sub(hookFiles, "hooks")
}

// Templates returns the embedded templates rooted at the templates/ directory.
func Templates() (fs.FS, error) {
	return fs.Sub(templateFiles, "templates")
}

// ReadTemplate returns the contents of a single template file, addressed
// relative to the templates/ root (for example "project-contract.yaml").
func ReadTemplate(name string) ([]byte, error) {
	return templateFiles.ReadFile("templates/" + name)
}

// ReadHook returns the contents of a single hook file, addressed relative to the
// hooks/ root (for example "pre-commit").
func ReadHook(name string) ([]byte, error) {
	return hookFiles.ReadFile("hooks/" + name)
}
