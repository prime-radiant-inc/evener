package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/provenance"
)

func TestTurnObservedNotificationIDsRoundTrip(t *testing.T) {
	t.Parallel()
	var turn Turn
	if err := json.Unmarshal([]byte(`{"kind":"ASSISTANT","observed_notification_ids":["obs-a","obs-b"]}`), &turn); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		IDs []string `json:"observed_notification_ids"`
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw.IDs, []string{"obs-a", "obs-b"}) {
		t.Fatalf("round-trip observation IDs = %v", raw.IDs)
	}
}

func TestTurnNotificationProvenanceRoundTrip(t *testing.T) {
	t.Parallel()
	data := []byte(`{"kind":"STEERING","notification_provenance":{"watch_keys":[{"watch_id":"watch_accept","watch_generation":"wg_accept"}]}}`)
	var turn Turn
	if err := json.Unmarshal(data, &turn); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Provenance *provenance.Causal `json:"notification_provenance"`
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	want := &provenance.Causal{WatchKeys: []provenance.WatchKey{{WatchID: "watch_accept", WatchGeneration: "wg_accept"}}}
	if !reflect.DeepEqual(raw.Provenance, want) {
		t.Fatalf("round-trip provenance = %#v, want %#v", raw.Provenance, want)
	}
}

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
