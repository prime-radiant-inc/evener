package schema

import (
	"encoding/json"
	"testing"
)

func TestTurnAskResolutionLegacyAndCapturedEvidence(t *testing.T) {
	t.Parallel()
	var legacy Turn
	if err := json.Unmarshal([]byte(`{"kind":"USER_INPUT","answer_to_turn_ids":["ask-turn"]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.AskResolution != nil {
		t.Fatal("legacy turn acquired scoped evidence")
	}
	var captured Turn
	if err := json.Unmarshal([]byte(`{"kind":"STEERING","ask_resolution":{"calls":[{"turnId":"ask-turn","callId":"ask-call"}],"legacy_count":2},"answer_to_turn_ids":[]}`), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.AskResolution == nil || len(captured.AskResolution.Calls) != 1 || captured.AskResolution.Calls[0].TurnID != "ask-turn" || captured.AskResolution.Calls[0].CallID != "ask-call" || captured.AskResolution.LegacyCount != 2 || len(captured.AnswerToTurnIDs) != 0 {
		t.Fatalf("captured turn=%+v", captured)
	}
	data, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Calls []struct {
			TurnID string `json:"turnId"`
			CallID string `json:"callId"`
		} `json:"calls"`
		LegacyCount int `json:"legacy_count"`
	}
	if err := json.Unmarshal(raw["ask_resolution"], &evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence.Calls) != 1 || evidence.Calls[0].TurnID != "ask-turn" || evidence.Calls[0].CallID != "ask-call" || evidence.LegacyCount != 2 {
		t.Fatalf("serialized evidence=%s", raw["ask_resolution"])
	}
}
