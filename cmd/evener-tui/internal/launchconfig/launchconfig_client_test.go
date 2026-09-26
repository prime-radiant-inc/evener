package launchconfig

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
)

func TestCmdAuthTestUsesSharedMethodAndInstanceName(t *testing.T) {
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client.Start(ctx)

	result := make(chan any, 1)
	go func() { result <- CmdAuthTest(client, "custom / team-east", 7)() }()

	request := <-transport.Sent()
	if request.Request.Method != appwire.MethodEvenerAuthTest {
		t.Fatalf("method=%q, want %q", request.Request.Method, appwire.MethodEvenerAuthTest)
	}
	var params appwire.AuthTestParams
	if err := json.Unmarshal(request.Request.Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.Provider != "custom / team-east" {
		t.Fatalf("provider=%q, want custom / team-east", params.Provider)
	}
	transport.DeliverResponse(request.Request.ID, appwire.AuthTestResponse{
		Provider: params.Provider,
		Status:   appwire.AuthTestStatusSuccess,
		Message:  "Credentials verified.",
	})

	message := <-result
	msg, ok := message.(AuthTestResultMsg)
	if !ok || msg.Err != nil || msg.Generation != 7 || msg.Response.Status != appwire.AuthTestStatusSuccess {
		t.Fatalf("result=%T %+v", message, msg)
	}
}

// The removal RPC carries the endpoint the panel's selected row showed, so the
// hub can refuse a name another client has re-pointed since. Without it the
// hub accepts the empty assertion and a stale screen deletes a replacement
// instance.
func TestCmdInstanceRemoveSendsTheShownEndpointFingerprint(t *testing.T) {
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client.Start(ctx)

	result := make(chan any, 1)
	go func() { result <- CmdInstanceRemove(client, "work", "fp-from-the-screen")() }()

	request := <-transport.Sent()
	if request.Request.Method != appwire.MethodEvenerInstanceRemove {
		t.Fatalf("method=%q, want %q", request.Request.Method, appwire.MethodEvenerInstanceRemove)
	}
	var params appwire.InstanceRemoveParams
	if err := json.Unmarshal(request.Request.Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.Name != "work" || params.ExpectedEndpointFingerprint != "fp-from-the-screen" {
		t.Fatalf("params=%+v, want name work and the shown fingerprint", params)
	}
	transport.DeliverResponse(request.Request.ID, appwire.InstanceListResponse{})

	message := <-result
	msg, ok := message.(InstanceMutateResultMsg)
	if !ok || msg.Err != nil {
		t.Fatalf("result=%T %+v", message, msg)
	}
}

// A stale fingerprint is the hub's refusal, not a silent removal: the client
// carries it back to the caller, which reports it instead of treating the row
// as gone.
func TestCmdInstanceRemoveSurfacesAStaleFingerprintRefusal(t *testing.T) {
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client.Start(ctx)

	result := make(chan any, 1)
	go func() { result <- CmdInstanceRemove(client, "work", "fp-stale")() }()

	request := <-transport.Sent()
	transport.DeliverError(request.Request.ID, appwire.CodeConflict,
		`work no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again`)

	message := <-result
	msg, ok := message.(InstanceMutateResultMsg)
	if !ok {
		t.Fatalf("result=%T, want InstanceMutateResultMsg", message)
	}
	if msg.Err == nil {
		t.Fatal("a stale fingerprint produced no error; the hub's refusal was swallowed")
	}
	if !strings.Contains(msg.Err.Error(), "no longer resolves to the endpoint") {
		t.Fatalf("err=%v, want the hub's stale-endpoint refusal", msg.Err)
	}
	if len(msg.List.Instances) != 0 {
		t.Fatalf("list=%+v, want no refreshed list on a refused removal", msg.List)
	}
}
