package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type LedgerWriter struct {
	LedgerPath  string
	ProjectRoot string
	TaskID      string
	Tree        string
	// RunID/TraceID/SpanID identify the single invocation writing this evidence.
	// Set once at the writer's construction site (like Tree), then stamped onto
	// every LedgerEntry and grouped into the proof filename.
	RunID   string
	TraceID string
	SpanID  string
}

func NewLedgerWriter(ledgerPath string, projectRoot string) LedgerWriter {
	taskID, _ := resolveTaskID()
	if taskID == "" {
		taskID = defaultTaskID
	}
	if root := projectRootFromLedgerPath(ledgerPath); root != "" {
		projectRoot = root
	}
	return LedgerWriter{
		LedgerPath:  ledgerPath,
		ProjectRoot: projectRoot,
		TaskID:      taskID,
	}
}

func (w LedgerWriter) Record(check string, verdict string, proofBody string, metadata map[string]interface{}) (string, error) {
	return w.RecordWithActor(actorWorker, check, verdict, proofBody, metadata)
}

func (w LedgerWriter) RecordWithActor(actorRole string, check string, verdict string, proofBody string, metadata map[string]interface{}) (string, error) {
	if strings.TrimSpace(w.LedgerPath) == "" {
		return "", nil
	}
	if actorRole != actorWorker && actorRole != actorVerifier {
		return "", fmt.Errorf("invalid actor_role %q", actorRole)
	}

	if err := os.MkdirAll(filepath.Dir(w.LedgerPath), 0o755); err != nil {
		return "", fmt.Errorf("create ledger dir: %w", err)
	}

	proofDir := filepath.Join(w.ProjectRoot, ".harness", "evidence", "proofs")
	if err := os.MkdirAll(proofDir, 0o755); err != nil {
		return "", fmt.Errorf("create proof dir: %w", err)
	}

	nano := time.Now().UTC().UnixNano()
	proofName := fmt.Sprintf("%s-%d.log", sanitizeProofName(check), nano)
	if strings.TrimSpace(w.RunID) != "" {
		// Group one invocation's proofs together by embedding the run_id.
		proofName = fmt.Sprintf("%s-%s-%d.log", sanitizeProofName(check), sanitizeProofName(w.RunID), nano)
	}
	proofPath := filepath.Join(proofDir, proofName)
	if err := os.WriteFile(proofPath, []byte(proofBody), 0o644); err != nil {
		return "", fmt.Errorf("write proof: %w", err)
	}

	entryMetadata := map[string]interface{}{}
	for key, value := range metadata {
		entryMetadata[key] = value
	}
	proofHash := sha256.Sum256([]byte(proofBody))
	entryMetadata["proof_sha256"] = hex.EncodeToString(proofHash[:])
	if strings.TrimSpace(w.Tree) != "" {
		entryMetadata["tree"] = w.Tree
	}

	proofRef := filepath.ToSlash(filepath.Join(".harness", "evidence", "proofs", proofName))
	entry := LedgerEntry{
		TaskID:     w.TaskID,
		Check:      check,
		Verdict:    verdict,
		ProofRef:   proofRef,
		ExecutedAt: utcNowRFC3339(),
		ActorRole:  actorRole,
		RunID:      w.RunID,
		TraceID:    w.TraceID,
		SpanID:     w.SpanID,
		Metadata:   entryMetadata,
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("encode ledger entry: %w", err)
	}

	file, err := os.OpenFile(w.LedgerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", fmt.Errorf("open ledger: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(append(line, '\n')); err != nil {
		return "", fmt.Errorf("append ledger: %w", err)
	}
	return proofRef, nil
}

// projectRootFromLedgerPath resolves the workspace root that owns the shared
// .harness for a ledger path. It asks git first (harnessRoot) so a linked
// worktree collapses to its MAIN root and all evidence lands in one .harness.
// The string walk is the mandatory fallback for the non-git case: several tests
// build <TempDir>/.harness/... outside any repo and must keep resolving.
func projectRootFromLedgerPath(ledgerPath string) string {
	if root, ok := harnessRoot(filepath.Dir(ledgerPath)); ok {
		return root
	}
	clean := filepath.Clean(ledgerPath)
	parts := strings.Split(filepath.ToSlash(clean), "/")
	for index := 0; index+1 < len(parts); index++ {
		if parts[index] == ".harness" && parts[index+1] == "evidence" {
			root := strings.Join(parts[:index], "/")
			if root == "" {
				if !filepath.IsAbs(clean) {
					return ""
				}
				return string(filepath.Separator)
			}
			return filepath.FromSlash(root)
		}
	}
	return ""
}

// rebasedLedgerForRead re-roots a ledger path onto the shared .harness owner for
// the READ verbs (verify/replay), so they read the SAME file `check` WROTE. A
// linked worktree collapses to its MAIN root (harnessRoot); a normal repo, a repo
// sub-directory, or a non-git path returns the path unchanged — i.e. the ledger's
// own workspace. This keeps the write path (RunCheck) and the read path aligned.
func rebasedLedgerForRead(ledgerPath string) string {
	if strings.TrimSpace(ledgerPath) == "" {
		return ledgerPath
	}
	if owner, ok := harnessRoot(filepath.Dir(ledgerPath)); ok {
		return rebaseLedgerOntoHarnessRoot(ledgerPath, owner)
	}
	return ledgerPath
}

// rebaseLedgerOntoHarnessRoot re-roots a caller-supplied --ledger path onto the
// shared .harness owner: it keeps the path from the `.harness` segment onward and
// joins it under harnessOwner, so a worktree-relative or worktree-absolute
// --ledger from the hook/alias still writes into the single shared .harness. A
// path with no `.harness` segment is returned unchanged.
func rebaseLedgerOntoHarnessRoot(ledgerPath string, harnessOwner string) string {
	clean := filepath.Clean(ledgerPath)
	parts := strings.Split(filepath.ToSlash(clean), "/")
	for index, part := range parts {
		if part == ".harness" {
			suffix := filepath.FromSlash(strings.Join(parts[index:], "/"))
			return filepath.Join(harnessOwner, suffix)
		}
	}
	return ledgerPath
}

func sanitizeProofName(value string) string {
	value = strings.ToLower(value)
	re := regexp.MustCompile(`[^a-z0-9._-]+`)
	value = re.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-._")
	if value == "" {
		return "proof"
	}
	return value
}

func validProofRefShape(proofRef string) bool {
	if strings.HasPrefix(proofRef, ".harness/evidence/proofs/") {
		rest := strings.TrimPrefix(proofRef, ".harness/evidence/proofs/")
		return rest != "" && !strings.ContainsAny(rest, " \t\r\n")
	}
	if strings.HasPrefix(proofRef, "urn:") {
		return len(proofRef) > len("urn:") && !strings.ContainsAny(proofRef, " \t\r\n")
	}
	parsed, err := url.Parse(proofRef)
	return err == nil && parsed.Scheme != "" && parsed.Host != "" && !strings.ContainsAny(proofRef, " \t\r\n")
}
