package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// This records the state an old session prevalidation would have observed: the
// call was unknown before the name was registered and removed again. The
// current implementation re-resolves that state at finalization, so two old
// failures can populate the later absent lifetime and park its first call.
func TestPrevalidationFailure_UnknownABAOldFailureCannotParkNewAbsentLifetime(t *testing.T) {
	r := NewRegistry()
	call := breakerCall("unknown-aba", "unknown_aba_tool", `{"value":"ok"}`)
	_, oldSnapshot := r.SnapshotPrevalidation(call.Name)

	registerBreakerFake(t, r, "unknown_aba_tool", func(int) (any, error) { return "registered", nil })
	r.Remove("unknown_aba_tool")
	for range 2 {
		r.FinalizePrevalidationFailure(context.Background(), oldSnapshot, call, nil, "unknown tool", "unknown_tool", errors.New("unknown tool"))
	}

	res := r.ExecuteCall(context.Background(), breakerEnv(t), call)
	if strings.HasPrefix(res.Output, wantFailurePark("unknown_aba_tool")) {
		t.Fatalf("old unknown failures parked the new absent lifetime: %#v", res)
	}
}

func TestPrevalidationFailure_AbsentResetInvalidatesOutstandingSnapshot(t *testing.T) {
	for _, reset := range []struct {
		name string
		fn   func(*Registry, string)
	}{
		{"remove", (*Registry).Remove},
		{"unregister", (*Registry).Unregister},
	} {
		t.Run(reset.name, func(t *testing.T) {
			r := NewRegistry()
			call := breakerCall("absent-reset", "absent_reset_tool", `{}`)
			_, stale := r.SnapshotPrevalidation(call.Name)

			reset.fn(r, call.Name)
			for range 2 {
				res := r.FinalizePrevalidationFailure(context.Background(), stale, call, nil, "unknown tool", "unknown_tool", errors.New("unknown tool"))
				if res.BreakerExactSignature != "" || res.BreakerSemanticSignature != "" || res.BreakerBypassed {
					t.Fatalf("reset absent name accepted stale finalization: %#v", res)
				}
			}

			res := r.ExecuteCall(context.Background(), breakerEnv(t), call)
			if strings.HasPrefix(res.Output, wantFailurePark(call.Name)) {
				t.Fatalf("stale failures repopulated reset absent lifetime: %#v", res)
			}
		})
	}
}
