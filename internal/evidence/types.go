package evidence

import "time"

const (
	exitOK    = 0
	exitBlock = 2

	verdictPass  = "pass"
	verdictWarn  = "warn"
	verdictBlock = "block"

	actorWorker   = "worker"
	actorVerifier = "verifier"

	defaultTaskID = "contract-check"

	checkVerifierReplay = "verifier.replay"

	// verifyUndefined is the sentinel install.sh writes into build.verify when it
	// cannot auto-detect a real verify for the repo's stack. The gate blocks on it
	// (rather than silently passing a placeholder) until a real verify is defined.
	verifyUndefined = "EZH_VERIFY_UNDEFINED"
)

type Contract struct {
	Service   string    `json:"service"`
	Owners    []string  `json:"owners"`
	Build     Build     `json:"build"`
	EnvRules  []EnvRule `json:"env_rules"`
	RiskLevel string    `json:"risk_level"`
	RiskPaths []string  `json:"risk_paths,omitempty"`
}

type Build struct {
	Verify      string `json:"verify"`
	VerifyThick string `json:"verify_thick"`
}

type EnvRule struct {
	ID              string   `json:"id"`
	AppliesTo       string   `json:"applies_to"`
	Reminder        string   `json:"reminder,omitempty"`
	Check           string   `json:"check,omitempty"`
	Assert          string   `json:"assert,omitempty"`
	Severity        string   `json:"severity"`
	GateAction      string   `json:"gate_action,omitempty"`
	CommandPrefixes []string `json:"command_prefixes,omitempty"`
}

type LedgerEntry struct {
	TaskID     string                 `json:"task_id"`
	Check      string                 `json:"check"`
	Verdict    string                 `json:"verdict"`
	ProofRef   string                 `json:"proof_ref"`
	ExecutedAt string                 `json:"executed_at"`
	ActorRole  string                 `json:"actor_role"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

type CheckResult struct {
	Check      string `json:"check"`
	Verdict    string `json:"verdict"`
	ProofRef   string `json:"proof_ref,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
	ExitCode   int    `json:"exit_code"`
}

type ResultSummary struct {
	Command    string        `json:"command"`
	Verdict    string        `json:"verdict"`
	Reason     string        `json:"reason,omitempty"`
	Suggestion string        `json:"suggestion,omitempty"`
	ExitCode   int           `json:"exit_code"`
	Checks     []CheckResult `json:"checks,omitempty"`
}

type GateError struct {
	Check      string
	Reason     string
	Suggestion string
	ExitCode   int
}

func (e GateError) Error() string {
	return e.Reason
}

func utcNowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
