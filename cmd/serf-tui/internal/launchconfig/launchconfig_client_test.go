package launchconfig

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/appwire/appwiretest"
)

func TestCmdAuthTestUsesSharedMethodAndInstanceName(t *testing.T) {
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client.Start(ctx)

	result := make(chan any, 1)
	go func() { result <- CmdAuthTest(client, "custom / team-east")() }()

	request := <-transport.Sent()
	if request.Request.Method != appwire.MethodSerfAuthTest {
		t.Fatalf("method=%q, want %q", request.Request.Method, appwire.MethodSerfAuthTest)
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
	if !ok || msg.Err != nil || msg.Response.Status != appwire.AuthTestStatusSuccess {
		t.Fatalf("result=%T %+v", message, msg)
	}
}
