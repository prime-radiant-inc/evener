package schema

import (
	"encoding/json"
	"strings"
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

// TestTurn_UsageAlwaysShipsOnWire locks in that Usage has no "omitempty" tag:
// encoding/json can never omit a struct value regardless of the tag, so a
// non-assistant turn (which never sets Usage) still ships a zero-valued
// "usage" key. Nothing decodes this field by checking key presence — every
// reader (agent/internal/atif, appwire.SerfUsageFromLLM) checks the token
// count fields themselves — so the tag was already a no-op lie.
func TestTurn_UsageAlwaysShipsOnWire(t *testing.T) {
	data, err := json.Marshal(Turn{Kind: TurnUserInput})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"usage":{`) {
		t.Fatalf("expected usage key present even on a non-assistant turn, got %s", data)
	}
}
