package agent

import (
	"context"
	"testing"
)

// TestWithSteeringCarrierTurnAndExtraction covers withSteeringCarrierTurn and
// steeringCarrierTurnIDFromContext (session_client_mutation_queue.go:254-261)
// including the non-empty case not covered by the existing empty-only test.
func TestWithSteeringCarrierTurnAndExtraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// No turn ID in bare context.
	if got := steeringCarrierTurnIDFromContext(ctx); got != "" {
		t.Fatalf("bare context: %q, want empty", got)
	}
	// With a turn ID.
	ctx = withSteeringCarrierTurn(ctx, "turn_42")
	if got := steeringCarrierTurnIDFromContext(ctx); got != "turn_42" {
		t.Fatalf("with turn: %q, want turn_42", got)
	}
	// Overwriting with a different turn ID.
	ctx = withSteeringCarrierTurn(ctx, "turn_99")
	if got := steeringCarrierTurnIDFromContext(ctx); got != "turn_99" {
		t.Fatalf("overwritten: %q, want turn_99", got)
	}
	// Empty string turn ID is a valid value, not a missing one.
	ctx = withSteeringCarrierTurn(ctx, "")
	if got := steeringCarrierTurnIDFromContext(ctx); got != "" {
		t.Fatalf("empty turn: %q, want empty string", got)
	}
}
