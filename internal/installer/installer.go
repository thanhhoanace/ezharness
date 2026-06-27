// Package installer implements `ezh install`: a self-contained, per-repo
// installation of the ezHarness v6 evidence gate using assets embedded in the
// running binary. It ports the load-bearing gate-core steps of the canonical
// shell installer (src/install.sh) — directory layout, hook materialization,
// contract/AGENTS scaffolding, binary self-copy, config + manifest, .gitignore,
// and the worktree-aware git hook/alias wiring — without the full multi-engine
// surface.
package installer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/thanhhoanace/ezharness/internal/assets"
)

// verifyUndefined is the sentinel verify command written when no project verify
// command can be detected. It matches src/install.sh detect_verify semantics.
const verifyUndefined = "EZH_VERIFY_UNDEFINED"

// hookExecutables is the set of materialized hook files that must be executable.
var hookExecutables = map[string]struct{}{
	"pre-commit":           {},
	"pre-push":             {},
	"ezh-evidence-gate.sh": {},
	"_lib.sh":              {},
	"locate-workspace.sh":  {},
}

// makeVerifyTarget matches a Makefile target whose name ends in "verify",
// mirroring the has_make_verify check in src/install.sh.
var makeVerifyTarget = regexp.MustCompile(`(?m)^[A-Za-z0-9_.%/-]*verify\s*:`)

// Options configures an installation run.
type Options struct {
	// Target is the repository root to install into. When empty, the installer
	// resolves it from `git rev-parse --show-toplevel` against the current
	// working directory.
	Target string
}

// Run performs the per-repo gate-core install and writes a human-readable
// summary to stdout. It is idempotent: re-running never clobbers an existing
// user contract and only fills in missing artifacts.
func Run(opts Options, stdout io.Writer, stderr io.Writer) error {
	repoRoot, err := resolveRepoRoot(opts.Target)
	if err != nil {
		return err
	}

	if !gitIsWorkTree(repoRoot) {
		return fmt.Errorf("target is not a git work tree: %s", repoRoot)
	}

	harness := filepath.Join(repoRoot, ".harness")
	for _, dir := range []string{
		filepath.Join(harness, "hooks"),
		filepath.Join(harness, "bin"),
		filepath.Join(harness, "evidence"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	if err := materializeHooks(filepath.Join(harness, "hooks")); err != nil {
		return err
	}

	verifyCommand, riskLevel := detectVerify(repoRoot)

	contractPath := filepath.Join(harness, "project-contract.yaml")
	contractCreated, err := writeContractIfMissing(contractPath, filepath.Base(repoRoot), verifyCommand, riskLevel)
	if err != nil {
		return err
	}

	agentsPath := filepath.Join(harness, "AGENTS.md")
	agentsCreated, err := writeTemplateIfMissing(agentsPath, "harness-AGENTS.md")
	if err != nil {
		return err
	}

	binPath := filepath.Join(harness, "bin", "ezh-evidence")
	if err := copySelf(binPath); err != nil {
		return err
	}

	if err := writeConfig(filepath.Join(harness, "config.json"), repoRoot, verifyCommand, riskLevel); err != nil {
		return err
	}

	gitignoreAdded, err := ensureGitignoreEntry(filepath.Join(repoRoot, ".gitignore"))
	if err != nil {
		return err
	}

	if err := configureGitHooks(repoRoot); err != nil {
		return err
	}

	if err := writeManifest(filepath.Join(harness, "install-manifest.json"), repoRoot, contractCreated, agentsCreated, gitignoreAdded); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "ezh install complete\n")
	fmt.Fprintf(stdout, "repo_root: %s\n", repoRoot)
	fmt.Fprintf(stdout, "verify:    %s\n", verifyCommand)
	fmt.Fprintf(stdout, "risk:      %s\n", riskLevel)
	fmt.Fprintf(stdout, "hooks:     %s (core.hooksPath -> .harness/hooks)\n", filepath.Join(harness, "hooks"))
	fmt.Fprintf(stdout, "alias:     git gate [risk] runs the worker check + verifier in one step\n")
	if contractCreated {
		fmt.Fprintf(stdout, "contract:  created .harness/project-contract.yaml (edit owners/risk_paths before committing)\n")
	} else {
		fmt.Fprintf(stdout, "contract:  kept existing .harness/project-contract.yaml\n")
	}
	if verifyCommand == verifyUndefined {
		fmt.Fprintf(stderr, "ezh install: no verify command detected; set build.verify in .harness/project-contract.yaml before the gate will pass\n")
	}
	return nil
}

// resolveRepoRoot returns the absolute repository root to install into.
func resolveRepoRoot(target string) (string, error) {
	if target != "" {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", fmt.Errorf("resolve target %q: %w", target, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("target directory does not exist: %s", abs)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("target is not a directory: %s", abs)
		}
		return abs, nil
	}
	out, err := runGit("", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repository (pass --target <dir>): %w", err)
	}
	return strings.TrimSpace(out), nil
}

// materializeHooks writes every embedded hook into destDir, marking the known
// hook scripts executable.
func materializeHooks(destDir string) error {
	hooks, err := assets.Hooks()
	if err != nil {
		return fmt.Errorf("load embedded hooks: %w", err)
	}
	return fs.WalkDir(hooks, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		dest := filepath.Join(destDir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := fs.ReadFile(hooks, path)
		if err != nil {
			return fmt.Errorf("read embedded hook %s: %w", path, err)
		}
		mode := os.FileMode(0o644)
		if _, ok := hookExecutables[filepath.Base(path)]; ok {
			mode = 0o755
		}
		if err := os.WriteFile(dest, data, mode); err != nil {
			return fmt.Errorf("write hook %s: %w", dest, err)
		}
		return nil
	})
}

// detectVerify infers the verify command and default risk level for the repo,
// mirroring the detect_verify logic in src/install.sh. Only the load-bearing
// cases are ported; unknown stacks fall back to the undefined sentinel.
func detectVerify(repoRoot string) (string, string) {
	makefile := filepath.Join(repoRoot, "Makefile")
	if data, err := os.ReadFile(makefile); err == nil && makeVerifyTarget.Match(data) {
		return "make verify", "med"
	}
	switch {
	case fileExists(filepath.Join(repoRoot, "go.mod")):
		return "go test ./... && go build ./...", "low"
	case fileExists(filepath.Join(repoRoot, "Cargo.toml")):
		return "cargo test", "low"
	default:
		return verifyUndefined, "low"
	}
}

// writeContractIfMissing renders the embedded project-contract.yaml template,
// substituting the detected verify command and risk level. It never overwrites
// an existing contract. Returns whether a new contract was created.
func writeContractIfMissing(dest string, projectName string, verifyCommand string, riskLevel string) (bool, error) {
	if fileExists(dest) {
		return false, nil
	}
	raw, err := assets.ReadTemplate("project-contract.yaml")
	if err != nil {
		return false, fmt.Errorf("load contract template: %w", err)
	}
	nameJSON, err := json.Marshal(projectName)
	if err != nil {
		return false, fmt.Errorf("encode project name: %w", err)
	}
	verify := verifyCommand
	if verify == "" {
		verify = "git status --short --branch"
	}
	verifyJSON, err := json.Marshal(verify)
	if err != nil {
		return false, fmt.Errorf("encode verify command: %w", err)
	}
	text := string(raw)
	text = strings.ReplaceAll(text, "{{PROJECT_NAME_JSON}}", string(nameJSON))
	text = strings.ReplaceAll(text, "{{VERIFY_COMMAND_JSON}}", string(verifyJSON))
	risk := riskLevel
	if risk == "" {
		risk = "med"
	}
	text = strings.ReplaceAll(text, "risk_level: med", "risk_level: "+risk)
	if err := os.WriteFile(dest, []byte(text), 0o644); err != nil {
		return false, fmt.Errorf("write contract %s: %w", dest, err)
	}
	return true, nil
}

// writeTemplateIfMissing copies an embedded template verbatim when dest is
// absent. Returns whether the file was created.
func writeTemplateIfMissing(dest string, templateName string) (bool, error) {
	if fileExists(dest) {
		return false, nil
	}
	raw, err := assets.ReadTemplate(templateName)
	if err != nil {
		return false, fmt.Errorf("load template %s: %w", templateName, err)
	}
	if err := os.WriteFile(dest, raw, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", dest, err)
	}
	return true, nil
}

// copySelf copies the running binary to dest and marks it executable so the
// hooks' resolve_ezh_evidence can find .harness/bin/ezh-evidence.
func copySelf(dest string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve running binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolve running binary symlinks: %w", err)
	}
	src, err := os.Open(self)
	if err != nil {
		return fmt.Errorf("open running binary: %w", err)
	}
	defer src.Close()

	tmp := dest + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install binary %s: %w", dest, err)
	}
	return nil
}

// config is the minimal .harness/config.json schema read by the hooks'
// _lib.sh (existence marks the workspace root; workspace_root + repos are read
// for repo-type resolution).
type config struct {
	SchemaVersion    int          `json:"schema_version"`
	WorkspaceRoot    string       `json:"workspace_root"`
	StalledAfterDays int          `json:"stalled_after_days"`
	Engines          configEngine `json:"engines"`
	Repos            []configRepo `json:"repos"`
}

type configEngine struct {
	OpenCode bool `json:"opencode"`
	Codex    bool `json:"codex"`
}

type configRepo struct {
	Path          string `json:"path"`
	RepoType      string `json:"repo_type"`
	VerifyCommand string `json:"verify_command"`
	RiskLevel     string `json:"risk_level"`
}

// writeConfig writes .harness/config.json when absent. It is not overwritten on
// re-run so user edits survive.
func writeConfig(dest string, repoRoot string, verifyCommand string, riskLevel string) error {
	if fileExists(dest) {
		return nil
	}
	cfg := config{
		SchemaVersion:    1,
		WorkspaceRoot:    repoRoot,
		StalledAfterDays: 7,
		Engines:          configEngine{},
		Repos: []configRepo{
			{
				Path:          ".",
				RepoType:      "code",
				VerifyCommand: verifyCommand,
				RiskLevel:     riskLevel,
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(dest, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", dest, err)
	}
	return nil
}

// ensureGitignoreEntry appends ".harness/" to .gitignore if absent. Returns
// whether the entry was added.
func ensureGitignoreEntry(path string) (bool, error) {
	const entry = ".harness/"
	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == entry {
				return false, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("read .gitignore: %w", err)
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.WriteString(entry + "\n")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return false, fmt.Errorf("write .gitignore: %w", err)
	}
	return true, nil
}

// configureGitHooks wires the repo to the gate: a RELATIVE core.hooksPath (the
// v6.1 fix) and the `git gate` alias that runs the worker check + verifier in
// one step. The alias body is kept byte-for-byte in sync with src/install.sh.
func configureGitHooks(repoRoot string) error {
	if _, err := runGit(repoRoot, "config", "--local", "core.hooksPath", ".harness/hooks"); err != nil {
		return fmt.Errorf("set core.hooksPath: %w", err)
	}
	const gateAlias = `!f() { ws="$(git rev-parse --git-common-dir)/.."; repo="$(git rev-parse --show-toplevel)"; "$ws/.harness/bin/ezh-evidence" attest --contract "$ws/.harness/project-contract.yaml" --risk "${1:-low}" --ledger "$ws/.harness/evidence/ledger.jsonl" --repo-root "$repo"; }; f`
	if _, err := runGit(repoRoot, "config", "--local", "alias.gate", gateAlias); err != nil {
		return fmt.Errorf("set alias.gate: %w", err)
	}
	return nil
}

// manifest records what the install created, for a future uninstall. It mirrors
// the load-bearing shape of src/install.sh's install-manifest.json.
type manifest struct {
	SchemaVersion int                `json:"schema_version"`
	Installed     bool               `json:"installed"`
	WorkspaceRoot string             `json:"workspace_root"`
	InstalledAt   string             `json:"installed_at"`
	Ownership     manifestOwners     `json:"ownership"`
	HooksPrevious map[string]*string `json:"hooks_previous"`
}

type manifestOwners struct {
	ContractCreated     bool `json:"contract_created"`
	AgentsCreated       bool `json:"agents_created"`
	GitignoreEntryAdded bool `json:"gitignore_entry_added"`
}

// writeManifest writes .harness/install-manifest.json when absent.
func writeManifest(dest string, repoRoot string, contractCreated bool, agentsCreated bool, gitignoreAdded bool) error {
	if fileExists(dest) {
		return nil
	}
	previous := previousHooksPath(repoRoot)
	m := manifest{
		SchemaVersion: 1,
		Installed:     true,
		WorkspaceRoot: repoRoot,
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
		Ownership: manifestOwners{
			ContractCreated:     contractCreated,
			AgentsCreated:       agentsCreated,
			GitignoreEntryAdded: gitignoreAdded,
		},
		HooksPrevious: map[string]*string{".": previous},
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := os.WriteFile(dest, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write manifest %s: %w", dest, err)
	}
	return nil
}

// previousHooksPath returns the repo's prior local core.hooksPath, or nil if
// it was unset, so an uninstall can restore it.
func previousHooksPath(repoRoot string) *string {
	out, err := runGit(repoRoot, "config", "--local", "--get", "core.hooksPath")
	if err != nil {
		return nil
	}
	value := strings.TrimSpace(out)
	if value == "" {
		return nil
	}
	return &value
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func gitIsWorkTree(dir string) bool {
	_, err := runGit(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// runGit runs git in dir (or the current directory when dir is empty) and
// returns its stdout. Stderr is surfaced in the error for diagnostics.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}
