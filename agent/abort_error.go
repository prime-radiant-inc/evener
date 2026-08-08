package agent

import (
	"errors"

	"primeradiant.com/serf/llm"
)

// isAbortError reports whether err's chain contains an *llm.AbortError — the
// `var abort *llm.AbortError; errors.As(err, &abort)` fragment that
// roundWasCancelled (failure_steering.go), isTurnCancellation
// (session_stream.go), and queuedInputDrainContext (session_queue.go) each
// reimplemented independently. It answers only that narrow question; each
// call site still does something different with the answer beyond it —
// roundWasCancelled ORs it with a bare context.Canceled check,
// isTurnCancellation adds an llm.Error exclusion after it, and
// queuedInputDrainContext discriminates it against a same-context check (see
// that function's own comment on the honest-Unwrap asymmetry). None of that
// surrounding logic belongs here; collapsing it into one function would lose
// the real behavioral differences between the three.
func isAbortError(err error) bool {
	var abort *llm.AbortError
	return errors.As(err, &abort)
}
