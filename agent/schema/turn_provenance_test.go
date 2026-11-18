package schema

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestTurnRoundTripsCanonicalAttemptProvenance(t *testing.T) {
	want := Turn{
		Kind:                   TurnAssistant,
		AttemptGroupID:         "ag_test",
		ResponseEndpointFamily: "responses",
		ResponseProtocol:       "openai-responses",
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
	if got.AttemptGroupID != want.AttemptGroupID || got.ResponseEndpointFamily != want.ResponseEndpointFamily || got.ResponseProtocol != want.ResponseProtocol || got.ResponseEndpoint != want.ResponseEndpoint {
		t.Fatalf("provenance = group %q family %q protocol %q endpoint %q; want group %q family %q protocol %q endpoint %q", got.AttemptGroupID, got.ResponseEndpointFamily, got.ResponseProtocol, got.ResponseEndpoint, want.AttemptGroupID, want.ResponseEndpointFamily, want.ResponseProtocol, want.ResponseEndpoint)
	}
}

func TestTurnSelectsTheRecordedInstanceForNativeHistory(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses} {
		t.Run(protocol, func(t *testing.T) {
			row := registry.Resolved{Instance: "work", Protocol: protocol, WireID: "model", Caps: registry.Caps{ThinkingAsText: new(true)}}
			for _, tc := range []struct {
				instance, route string
				want            bool
			}{
				{"work", "route", true},
				{"other", "route", false},
				{"work", "", true},
				{"other", "", false},
				{"", "route", true},
			} {
				turn := Turn{
					Message: llm.Message{Role: llm.RoleAssistant}, ResponseProtocol: protocol,
					ResponseProvider: tc.instance, ResponseReasoningRoute: tc.route, ResponseWireModel: row.WireID,
				}
				if got := !turn.DropsReasoningForTarget(&row, "route"); got != tc.want {
					t.Errorf("native history for recorded instance %q and route %q: eligible = %t, want %t", tc.instance, tc.route, got, tc.want)
				}
				if got := turn.MatchesRecordedModelRoute(&row, "route"); protocol == registry.ProtocolOpenAIResponses && got != tc.want {
					t.Errorf("Responses continuation for recorded instance %q and route %q: eligible = %t, want %t", tc.instance, tc.route, got, tc.want)
				}
				if protocol == registry.ProtocolOpenAIChat && !turn.ReplaysChatDisplayText(&row) {
					t.Errorf("Chat display was disabled for recorded instance %q", tc.instance)
				}
			}
		})
	}
}

// TestTurn_UsageAlwaysShipsOnWire locks in that Usage has no "omitempty" tag:
// encoding/json can never omit a struct value regardless of the tag, so a
// non-assistant turn (which never sets Usage) still ships a zero-valued
// "usage" key. Nothing decodes this field by checking key presence — every
// reader (agent/internal/atif, appwire.EvenerUsageFromLLM) checks the token
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
