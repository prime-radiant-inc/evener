package agent

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// modelControlRefusingClient reports a model-control failure carrying the
// snapshot a diagnostic read observed, which is what the hub returns when a
// model mutation's outcome is uncertain.
type modelControlRefusingClient struct{ managerStateForUnit }

func (*modelControlRefusingClient) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	observed := appwire.ModelControlSnapshot{
		Authority: appwire.ModelControlAuthority{InstanceID: "instance-1", BootGeneration: "boot-1"},
		Revision:  4,
		Active:    appwire.ModelControlActive{ModelProvider: "anthropic", Model: "claude-opus-4-6", ReasoningEffortLevels: []string{}},
	}
	wire := appwire.ModelControlAuthorityStale("cm-1", "model control authority is stale").
		WithModelControlObserved(observed)
	return appwire.SessionOperationResponse{}, wire
}

// A failed tool call reaches the model as its output text, so a failure whose
// body carries the state the hub already observed has to put that state in the
// output. Otherwise the model issues a second read for a fact the hub handed
// back with the failure.
func TestManagerSessionControlModelFailureShowsObservedModel(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	root.SetHubClient(&modelControlRefusingClient{})

	result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{
		ID:   "control",
		Name: "session_control",
		Arguments: json.RawMessage(`{"action":"model","arguments":{"ref":"local:worker",` +
			`"modelProvider":"openai","model":"gpt-5.6-sol"},"intent":"Switching the worker model"}`),
	})
	if !result.IsError {
		t.Fatalf("session_control result = %+v, want the refusal", result)
	}

	// Read the output the way the model does: as the result's text.
	var shown struct {
		ObservedModelControl *appwire.ModelControlSnapshot `json:"observed_model_control"`
	}
	if err := json.Unmarshal([]byte(result.Output), &shown); err != nil {
		t.Fatalf("failure output is not the structured failure the model reads: %q", result.Output)
	}
	if shown.ObservedModelControl == nil {
		t.Fatalf("failure output carries no observed model-control state: %s", result.Output)
	}
	if shown.ObservedModelControl.Active.Model != "claude-opus-4-6" {
		t.Fatalf("observed model = %q, want what the hub reported", shown.ObservedModelControl.Active.Model)
	}
	// The observation is additive: the typed failure still decides the outcome.
	observed, ok := appwire.ModelControlObservedOf(result.Err)
	if !ok || observed.Active.Model != shown.ObservedModelControl.Active.Model {
		t.Fatalf("typed failure lost its observation: %v", result.Err)
	}
}
