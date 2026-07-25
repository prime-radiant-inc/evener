package main

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// kata mcgh: a failed turn's diagnostic has to survive the trip from the
// transcript back into a thread item, or the failure reaches the client
// stripped of everything but its text.
func TestReplayTurnCarriesFailureDiagnostic(t *testing.T) {
	persisted := schema.NewTurn(schema.TurnFailure, llm.System("provider error: access denied"))
	persisted.Error = &schema.TurnFailureInfo{
		Message: "provider error: access denied",
		Source:  "provider",
		Title:   "Access denied",
		Hint:    "check the API key",
		Cause:   &schema.TurnFailureCause{Kind: "provider", Provider: "openai", Model: "gpt-5.2", Status: 403},
	}
	got := hubDecodedTurn(t, persisted)

	if got.Kind != schema.TurnFailure {
		t.Fatalf("Kind = %q, want %q", got.Kind, schema.TurnFailure)
	}
	if got.Error == nil {
		t.Fatal("Error is nil: the failure diagnostic did not survive the transcript round trip")
	}
	if got.Error.Message != persisted.Error.Message {
		t.Errorf("Error.Message = %q, want %q", got.Error.Message, persisted.Error.Message)
	}
	if got.Error.Source != "provider" || got.Error.Title != "Access denied" || got.Error.Hint != "check the API key" {
		t.Errorf("Error source/title/hint = %q/%q/%q, want the persisted diagnostic", got.Error.Source, got.Error.Title, got.Error.Hint)
	}
	if got.Error.Cause == nil {
		t.Fatal("Error.Cause is nil: the structured cause did not survive the transcript round trip")
	}
	if got.Error.Cause.Kind != "provider" || got.Error.Cause.Provider != "openai" || got.Error.Cause.Model != "gpt-5.2" || got.Error.Cause.Status != 403 {
		t.Errorf("Error.Cause = %+v", got.Error.Cause)
	}
}

// The whole point of the round trip: after reload, the hub hands the client an
// item that says the turn failed.
func TestReplayedFailureProjectsFailedItem(t *testing.T) {
	persisted := schema.NewTurn(schema.TurnFailure, llm.System("provider error: access denied"))
	persisted.Error = &schema.TurnFailureInfo{Message: "provider error: access denied"}

	items := appItemsFromReplayTurn("s", "turn_1", 1, hubDecodedTurn(t, persisted), map[string]string{})

	if len(items) != 1 {
		t.Fatalf("items = %+v, want exactly one", items)
	}
	if items[0].Status != appwire.TurnStatusFailed {
		t.Errorf("Status = %q, want %q", items[0].Status, appwire.TurnStatusFailed)
	}
	if items[0].EventKind != appwire.ThreadItemEventKindError {
		t.Errorf("EventKind = %q, want %q", items[0].EventKind, appwire.ThreadItemEventKindError)
	}
	if items[0].Error != "provider error: access denied" {
		t.Errorf("Error = %q", items[0].Error)
	}
}
