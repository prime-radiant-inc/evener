package schema

import (
	"encoding/json"
	"testing"
)

func TestTurnRoundTripsCanonicalAttemptProvenance(t *testing.T) {
	want := Turn{
		Kind:                   TurnAssistant,
		AttemptGroupID:         "ag_test",
		ResponseEndpointFamily: "responses",
		ResponseEndpoint:       "https://provider.test/v1/responses",
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Turn
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.AttemptGroupID != want.AttemptGroupID || got.ResponseEndpointFamily != want.ResponseEndpointFamily || got.ResponseEndpoint != want.ResponseEndpoint {
		t.Fatalf("provenance = group %q family %q endpoint %q; want group %q family %q endpoint %q", got.AttemptGroupID, got.ResponseEndpointFamily, got.ResponseEndpoint, want.AttemptGroupID, want.ResponseEndpointFamily, want.ResponseEndpoint)
	}
}
