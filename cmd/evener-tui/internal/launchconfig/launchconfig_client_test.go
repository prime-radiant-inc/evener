package launchconfig

import (
	"context"
	"encoding/json"
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

// TestCmdInstanceRemoveCarriesTheEndpointFingerprint: the removal request carries
// the endpoint the listed row showed, so the hub can refuse removing a name
// another client re-pointed since that listing - the same assertion the web
// client's removal sends. Empty asserts nothing.
func TestCmdInstanceRemoveCarriesTheEndpointFingerprint(t *testing.T) {
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client.Start(ctx)

	result := make(chan any, 1)
	go func() { result <- CmdInstanceRemove(client, "openai", "fp-row")() }()

	request := <-transport.Sent()
	if request.Request.Method != appwire.MethodEvenerInstanceRemove {
		t.Fatalf("method=%q, want %q", request.Request.Method, appwire.MethodEvenerInstanceRemove)
	}
	var params appwire.InstanceRemoveParams
	if err := json.Unmarshal(request.Request.Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.Name != "openai" {
		t.Fatalf("name=%q, want openai", params.Name)
	}
	if params.ExpectedEndpointFingerprint != "fp-row" {
		t.Fatalf("expectedEndpointFingerprint=%q, want fp-row", params.ExpectedEndpointFingerprint)
	}
	transport.DeliverResponse(request.Request.ID, appwire.InstanceListResponse{})

	message := <-result
	if msg, ok := message.(InstanceMutateResultMsg); !ok || msg.Err != nil {
		t.Fatalf("result=%T %+v", message, message)
	}
}
