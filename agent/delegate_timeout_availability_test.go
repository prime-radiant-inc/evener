package agent

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestTimedOutSendReportsItsOwnGeneration is the medium about a timeout reply
// reading the child session instead of the generation it waited on.
//
// The failure it prevents: an inline send waits, times out, and samples the
// child's registry as it is RIGHT THEN. A retained idle child can run a
// notification turn after that generation settled and heal an MCP server during
// it, registering a name the settled generation never had. The reply would then
// attribute the new tool to the settled generation, whose terminal packet and
// status still carry the original list — one generation answering two ways
// depending on whether the wait completed or timed out.
//
// So the answer must come through the controller for the requested generation:
// the matching binding's runtime while it is attached, and the settled record
// once it is not.
func TestTimedOutSendReportsItsOwnGeneration(t *testing.T) {
	t.Parallel()
	const (
		delegateID = "dlg_1"
		settledGen = uint64(2)
	)

	// The settled generation's record: it registered only these.
	settled := []string{"read_file", "svc__echo"}
	controller := &delegateTreeController{
		live: map[string]*delegateLiveState{
			delegateID: {
				// The resident child, which has since healed a server and registered
				// a name this generation never had. It is deliberately NOT the
				// runtime bound to settledGen.
				runtime: nil,
			},
		},
		durable: map[string]*delegatestore.Aggregate{
			delegateID: {
				DelegateID:                  delegateID,
				LatestObservation:           &settled,
				LatestObservationGeneration: settledGen,
			},
		},
	}

	got := controller.observedToolsForGeneration(delegateID, settledGen)
	if !slices.Equal(got, settled) {
		t.Errorf("settled generation %d answered %v, want its recorded observation %v", settledGen, got, settled)
	}

	// A different generation must not inherit that record: a cold restart commits
	// the new generation before its runtime attaches, so the settled record still
	// describes the previous run.
	if got := controller.observedToolsForGeneration(delegateID, settledGen+1); got != nil {
		t.Errorf("generation %d answered %v from another generation's record, want nil (unobserved)", settledGen+1, got)
	}

	// An empty observation is an answer, not an absence: a generation that
	// registered nothing must report nothing rather than fall back to a ceiling.
	empty := []string{}
	controller.durable[delegateID].LatestObservation = &empty
	got = controller.observedToolsForGeneration(delegateID, settledGen)
	if got == nil {
		t.Error("a generation that registered zero tools answered nil, which callers read as unobserved and replace with the ceiling")
	}
	if len(got) != 0 {
		t.Errorf("a generation that registered zero tools answered %v", got)
	}
}
