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
	// Loop is the optional v6.3 loop-gate block. Absent (nil) means pure v6.2
	// behaviour, byte-identical (AC-L1). When present the gate enforces the loop
	// brakes via `ez-evidence loopcheck`.
	Loop *Loop `json:"loop,omitempty"`
}

// Loop declares the loop-gate brakes (P1 ezloop). All fields are optional; an
// empty budget simply means no budget brake.
type Loop struct {
	Budget                     LoopBudget `json:"budget"`
	RequireIndependentVerifier bool       `json:"require_independent_verifier,omitempty"`
	// ForbiddenPaths are globs (shared safe-subset matcher) that hard-block a
	// commit even WITH valid evidence. No unlock path exists (D4 final:
	// forbidden = blocked, full stop) — such changes are made by a human
	// outside the loop, or not at all.
	ForbiddenPaths []string `json:"forbidden_paths,omitempty"`
}

// LoopBudget caps how many gate cycles a single EZ_TASK_ID may run before human
// escalation. MaxIterations == 0 means the budget brake is disabled.
type LoopBudget struct {
	MaxIterations int `json:"max_iterations,omitempty"`
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
	TaskID     string `json:"task_id"`
	Check      string `json:"check"`
	Verdict    string `json:"verdict"`
	ProofRef   string `json:"proof_ref"`
	ExecutedAt string `json:"executed_at"`
	ActorRole  string `json:"actor_role"`
	// RunID identifies the single gate invocation that wrote this entry (peer of
	// task_id, which groups many runs). Always present. TraceID/SpanID carry
	// optional OpenTelemetry context resolved from the environment; omitempty so
	// an unset env is byte-identical to pre-trace output.
	RunID    string                 `json:"run_id"`
	TraceID  string                 `json:"trace_id,omitempty"`
	SpanID   string                 `json:"span_id,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
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
