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
