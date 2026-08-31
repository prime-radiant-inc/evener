package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestShapeRequestForcesStoreForPlannedContinuation(t *testing.T) {
	res := registry.Resolved{Protocol: registry.ProtocolOpenAIResponses, Caps: registry.Caps{Fields: map[string]bool{"store": true, "previous_response_id": true}}}
	req := Request{HistoryMode: HistoryModeResponsesDelta, PreviousResponseID: "resp_1"}
	if got := ShapeRequest(req, res); got.Store == nil || !*got.Store {
		t.Fatal("a planned continuation needs store = true (spec §7.6)")
	}
	explicit := Request{HistoryMode: HistoryModeResponsesDelta, PreviousResponseID: "resp_1", Store: new(false)}
	if got := ShapeRequest(explicit, res); *got.Store {
		t.Fatal("an explicit store decision is never overridden")
	}
	full := Request{HistoryMode: HistoryModeFullHistory}
	if got := ShapeRequest(full, res); got.Store != nil {
		t.Fatal("no continuation, no override")
	}
}

// TestShapeRequestStoreOverrideFollowsTheRowsContinuationSupport pins §7.6's
// definition of a planned continuation: the Responses protocol on a row that
// sends both store and previous_response_id. Nothing else is forced to store.
func TestShapeRequestStoreOverrideFollowsTheRowsContinuationSupport(t *testing.T) {
	req := Request{HistoryMode: HistoryModeResponsesDelta, PreviousResponseID: "resp_1"}
	for _, tc := range []struct {
		name string
		res  registry.Resolved
	}{
		{"chat protocol", registry.Resolved{Protocol: registry.ProtocolOpenAIChat, Caps: registry.Caps{Fields: map[string]bool{"store": true, "previous_response_id": true}}}},
		{"store off", registry.Resolved{Protocol: registry.ProtocolOpenAIResponses, Caps: registry.Caps{Fields: map[string]bool{"store": false, "previous_response_id": true}}}},
		{"previous_response_id off", registry.Resolved{Protocol: registry.ProtocolOpenAIResponses, Caps: registry.Caps{Fields: map[string]bool{"store": true, "previous_response_id": false}}}},
		{"no field table", registry.Resolved{Protocol: registry.ProtocolOpenAIResponses}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShapeRequest(req, tc.res); got.Store != nil {
				t.Fatalf("store = %v, want untouched: the row plans no continuation", *got.Store)
			}
		})
	}
}
