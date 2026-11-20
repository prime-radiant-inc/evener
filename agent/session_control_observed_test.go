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

// modelControlSucceedingClient answers a Manager model control with the full
// wire response a hub returns: the complete ThreadModelSetResponse carrying
// the authority identity, revision and desired record.
type modelControlSucceedingClient struct{ managerStateForUnit }

func (*modelControlSucceedingClient) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{Control: &appwire.SessionControlResponse{
		Actor: appwire.SessionActor{Kind: "manager", ManagerRef: "local:manager"},
		Model: &appwire.ThreadModelSetResponse{
			Disposition: appwire.ModelControlDispositionAccepted,
			ModelControl: &appwire.ModelControlSnapshot{
				Authority: appwire.ModelControlAuthority{InstanceID: "instance-1", BootGeneration: "boot-1"},
				Revision:  7,
				Active: appwire.ModelControlActive{
					ModelProvider:         "anthropic",
					Model:                 "claude-opus-4-6",
					ReasoningEffortLevels: []string{"low", "high"},
					SupportsReasoning:     true,
				},
				Desired: &appwire.ModelControlDesired{
					ClientMutationID: "cm-1",
					ModelProvider:    "openai",
					Model:            "gpt-5.6-sol",
					Lifecycle:        appwire.ModelControlLifecyclePending,
				},
			},
		},
	}}, nil
}

// The model-facing result of a Manager session_control carries what the model
// may act on - the disposition, the active model and the desired record with
// its lifecycle - and never the protocol identity the wire path keeps: the
// authority instance id and boot generation, the snapshot revision, and the
// client mutation id (issue #6914's design rule: Manager never exposes
// protocol identity fields to the model, so it cannot copy or synthesize the
// fences AppWire binds a mutation to).
func TestManagerSessionControlModelResultHidesProtocolIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client appwire.SessionHubClient
		isErr  bool
	}{
		{"success", &modelControlSucceedingClient{}, false},
		{"diagnostic_failure", &modelControlRefusingClient{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
			root.SetHubClient(tc.client)

			result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{
				ID:   "control",
				Name: "session_control",
				Arguments: json.RawMessage(`{"action":"model","arguments":{"ref":"local:worker",` +
					`"modelProvider":"openai","model":"gpt-5.6-sol"},"intent":"Switching the worker model"}`),
			})
			if result.IsError != tc.isErr {
				t.Fatalf("session_control result = %+v, want error=%v", result, tc.isErr)
			}

			// Read the output the way the model does: as the result's text.
			var shown map[string]any
			if err := json.Unmarshal([]byte(result.Output), &shown); err != nil {
				t.Fatalf("result output is not the structured JSON the model reads: %q", result.Output)
			}
			assertNoProtocolIdentity(t, "", shown)
			// And what the model may act on is still there.
			present := map[string]bool{}
			collectKeys(present, shown)
			keys := []string{"active"}
			if !tc.isErr {
				// The disposition is the mutation's outcome; a diagnostic
				// failure observed state but dispatched nothing.
				keys = append(keys, "disposition")
			}
			for _, key := range keys {
				if !present[key] {
					t.Fatalf("model-facing result lost its actionable field %q: %s", key, result.Output)
				}
			}
			if tc.isErr {
				return
			}
			control, _ := shown["control"].(map[string]any)
			if control == nil {
				t.Fatalf("success result lost its control arm: %s", result.Output)
			}
			model, _ := control["model"].(map[string]any)
			if model == nil {
				t.Fatalf("success result lost its model arm: %s", result.Output)
			}
			if model["disposition"] != "accepted" {
				t.Fatalf("projected disposition = %v, want the wire's own accepted", model["disposition"])
			}
			active, _ := model["active"].(map[string]any)
			if active["model"] != "claude-opus-4-6" {
				t.Fatalf("projected active model = %v, want what the wire reported", active["model"])
			}
			desired, _ := model["desired"].(map[string]any)
			if desired["model"] != "gpt-5.6-sol" || desired["lifecycle"] != "pending" {
				t.Fatalf("projected desired record = %v, want the pick the model asked for", desired)
			}
			if actor, _ := control["actor"].(map[string]any); actor == nil {
				t.Fatalf("success result lost its Manager attribution: %s", result.Output)
			}
		})
	}
}

// assertNoProtocolIdentity walks a decoded JSON value and fails on any of the
// identity fields the wire path owns: instanceId, bootGeneration, revision
// and clientMutationId, at any depth.
func assertNoProtocolIdentity(t *testing.T, path string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, inner := range typed {
			switch key {
			case "instanceId", "bootGeneration", "revision", "clientMutationId":
				t.Fatalf("model-facing result exposes protocol identity %s at %s", key, path)
			}
			assertNoProtocolIdentity(t, path+"/"+key, inner)
		}
	case []any:
		for _, inner := range typed {
			assertNoProtocolIdentity(t, path, inner)
		}
	}
}

// collectKeys records every object key the decoded result carries, for the
// actionable-field membership check.
func collectKeys(into map[string]bool, value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, inner := range typed {
			into[key] = true
			collectKeys(into, inner)
		}
	case []any:
		for _, inner := range typed {
			collectKeys(into, inner)
		}
	}
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
