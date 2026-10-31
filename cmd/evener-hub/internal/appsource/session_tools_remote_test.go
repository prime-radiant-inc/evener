package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// An independent assertion of the shared human/Manager input qualification:
// the forwarding set and the host's own claims both gate advertising. Adding
// a capability to the production mask cannot make this test agree by accident.
func TestSessionToolsRemoteQualifiedInputCapabilities(t *testing.T) {
	full := allRemoteThreadCapabilities()
	full.CommandInput = true
	want := appwire.ThreadCapabilities{
		Send: true, Queue: true, Steer: true, SkillInput: true,
		CommandInput: true, Shutdown: true, Interrupt: true, Compact: true,
		Clear: true, ForkFromTurn: true, ChangeModel: true, ChangeVisionModel: true,
		Goal: true, SharedNotes: true, Rename: true, StopSubagent: true,
	}
	if got := maskRemoteThreadCapabilities(full, forwardedRemoteThreadCapabilities(appwire.FeatureSet{TurnStart: true, TurnSteer: true, ThreadClear: true, ForkFromTurn: true, ThreadShutdown: true})); got != want {
		t.Fatalf("qualified input mask = %+v, want %+v", got, want)
	}
	wantWithoutFeatures := appwire.ThreadCapabilities{
		Interrupt: true, Compact: true, ChangeModel: true, ChangeVisionModel: true,
		Queue: true, Goal: true, SharedNotes: true, Rename: true,
		SkillInput: true, CommandInput: true, StopSubagent: true,
	}
	if got := maskRemoteThreadCapabilities(full, forwardedRemoteThreadCapabilities(appwire.FeatureSet{})); got != wantWithoutFeatures {
		t.Fatalf("negotiated feature refusal not preserved: got %+v, want %+v", got, wantWithoutFeatures)
	}
	full.Steer = false
	full.SkillInput = false
	want.Steer = false
	want.SkillInput = false
	if got := maskRemoteThreadCapabilities(full, forwardedRemoteThreadCapabilities(appwire.FeatureSet{TurnStart: true, TurnSteer: true, ThreadClear: true, ForkFromTurn: true, ThreadShutdown: true})); got != want {
		t.Fatalf("host refusal not preserved: got %+v, want %+v", got, want)
	}
}

func TestSessionToolsRemotePrivateInputResponseLoss(t *testing.T) {
	requests := make(chan struct {
		method string
		params json.RawMessage
	}, 1)
	source, _ := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		requests <- struct {
			method string
			params json.RawMessage
		}{method: method, params: params}
		return scriptedReply{closeConn: true}
	})
	const mutationID = "manager-private-response-lost"
	_, err := source.StartTurn(WithManagerInputCaller(context.Background(), "local:manager"), appwire.TurnStartParams{
		Ref: testControllerRef, ExpectedInstanceID: "instance-1", ClientMutationID: mutationID,
		Input: []appwire.InputItem{{Type: "text", Text: "send"}},
	})
	if err == nil {
		t.Fatal("private bridge mutation reported success after its response was lost")
	}
	request := <-requests
	if request.method != appwire.MethodHubSessionToolsInput {
		t.Fatalf("method=%q, want private bridge input", request.method)
	}
	var input appwire.SessionToolsInput
	if err := json.Unmarshal(request.params, &input); err != nil {
		t.Fatal(err)
	}
	var turn appwire.TurnStartParams
	if err := json.Unmarshal(input.Params, &turn); err != nil {
		t.Fatal(err)
	}
	if input.CallerRef != "local:manager" || input.Method != appwire.MethodTurnStart || turn.Ref != testRemoteRef || turn.ClientMutationID != mutationID || turn.ExpectedInstanceID != "instance-1" || len(turn.Input) != 1 || turn.Input[0].Text != "send" || turn.ManagerProof != nil {
		t.Fatalf("private bridge request lost identity or input: caller=%q method=%q ref=%q mutation=%q instance=%q input=%+v proof=%+v", input.CallerRef, input.Method, turn.Ref, turn.ClientMutationID, turn.ExpectedInstanceID, turn.Input, turn.ManagerProof)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("response loss=%T %v, want wire error", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown || data.MutationOutcome != appwire.MutationOutcomeUnknown || data.ClientMutationID != mutationID || data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("response loss discarded mutation identity: %+v", wire)
	}
}

func TestRemoteHubCapabilitiesInputPreservesSourceAndInstance(t *testing.T) {
	source := NewRemoteHubSource("host-b", nil, nil)
	thread, err := source.fromRemoteThread(appwire.Thread{
		ID: "same-bare-id", Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:same-bare-id", InstanceID: "instance-b", Capabilities: allRemoteThreadCapabilities()},
	}, forwardedRemoteThreadCapabilities(appwire.FeatureSet{TurnStart: true, TurnSteer: true, ThreadClear: true, ForkFromTurn: true, ThreadShutdown: true}))
	if err != nil {
		t.Fatal(err)
	}
	if thread.Evener.Ref != "host-b:same-bare-id" || thread.Evener.InstanceID != "instance-b" {
		t.Fatalf("remote identity crossed source or instance: %+v", thread.Evener)
	}
	if !thread.Evener.Capabilities.Send || !thread.Evener.Capabilities.Queue || !thread.Evener.Capabilities.Steer || !thread.Evener.Capabilities.SkillInput {
		t.Fatalf("shared remote input unavailable: %+v", thread.Evener.Capabilities)
	}
}
