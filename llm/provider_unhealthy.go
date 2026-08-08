package llm

import (
	"fmt"
	"time"
)

// ProviderUnhealthyError reports that RetryStream stopped a stream-failure
// group early because the attempt pattern indicts the provider's endpoint or
// transport rather than a single unlucky request: either FailFastAfter
// consecutive consume-phase failures (Shape "stall") or two consecutive
// long-streaming consume-phase failures cut at a hard transport cap (Shape
// "cap"). Identical retries cannot beat either shape, so RetryStream settles
// the round immediately instead of spending the rest of the retry budget.
type ProviderUnhealthyError struct {
	Shape    string // "stall" | "cap"
	Attempts int
	Elapsed  time.Duration
	LastErr  error
}

// Error returns "provider unhealthy after N stream failures (Xs): <last>".
func (e *ProviderUnhealthyError) Error() string {
	return fmt.Sprintf("provider unhealthy after %d stream failures (%s): %v", e.Attempts, e.Elapsed, e.LastErr)
}

// Unwrap returns LastErr so errors.Is/As can see through to the underlying
// attempt failure.
func (e *ProviderUnhealthyError) Unwrap() error { return e.LastErr }
