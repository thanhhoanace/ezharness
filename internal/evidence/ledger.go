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
}

func NewLedgerWriter(ledgerPath string, projectRoot string) LedgerWriter {
	taskID := strings.TrimSpace(os.Getenv("EZH_TASK_ID"))
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

	proofName := fmt.Sprintf("%s-%d.log", sanitizeProofName(check), time.Now().UTC().UnixNano())
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

func projectRootFromLedgerPath(ledgerPath string) string {
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
