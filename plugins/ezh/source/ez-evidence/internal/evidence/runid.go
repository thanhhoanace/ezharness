package evidence

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// runIdentity carries the per-invocation identifiers stamped onto every evidence
// artifact of ONE gate invocation — the ledger rows it writes, their proofs, and
// the run-report row — so a single run is joinable across all three. task_id
// groups many runs; run_id identifies exactly one. Generated once per verb in
// cli.go at start := time.Now().
type runIdentity struct {
	RunID   string
	TraceID string
	SpanID  string
}

// newRunIdentity mints a per-invocation identity anchored at the invocation's
// start time. trace_id/span_id are read once from the environment and stay empty
// when nothing is configured.
func newRunIdentity(start time.Time) runIdentity {
	traceID, spanID := resolveTraceSpan()
	return runIdentity{
		RunID:   newRunID(start),
		TraceID: traceID,
		SpanID:  spanID,
	}
}

// newRunID returns a time-sortable, per-invocation identifier: a fixed-width hex
// UnixNano prefix (lexical order == chronological order) joined to a random hex
// suffix for uniqueness within a single nanosecond. Stdlib only — no ULID/uuid
// dependency.
func newRunID(start time.Time) string {
	nano := uint64(start.UTC().UnixNano())
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// crypto/rand failing is near-impossible; keep the id non-empty and still
		// time-sortable rather than aborting a best-effort identifier.
		return fmt.Sprintf("%016x-%016x", nano, nano)
	}
	return fmt.Sprintf("%016x-%s", nano, hex.EncodeToString(suffix[:]))
}

// resolveTraceSpan reads OpenTelemetry context from the environment once. It
// prefers explicit EZ_TRACE_ID/EZ_SPAN_ID, else parses a W3C traceparent
// ("version-traceid-spanid-flags"). Empty strings when nothing is set, so the
// omitempty ledger/run-report fields stay absent and output is byte-identical to
// pre-trace behavior.
func resolveTraceSpan() (traceID string, spanID string) {
	traceID = strings.TrimSpace(os.Getenv("EZ_TRACE_ID"))
	spanID = strings.TrimSpace(os.Getenv("EZ_SPAN_ID"))
	if traceID != "" || spanID != "" {
		return traceID, spanID
	}
	tp := strings.TrimSpace(os.Getenv("TRACEPARENT"))
	if tp == "" {
		return "", ""
	}
	parts := strings.Split(tp, "-")
	if len(parts) >= 3 {
		return strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	}
	return "", ""
}
