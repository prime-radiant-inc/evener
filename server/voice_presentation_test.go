package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestVoicePresentationServerAdmissionAndCapability(t *testing.T) {
	dir := t.TempDir()
	session, err := agent.NewSession(llm.NewClient(), provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", session.ID())
	seedStatus(srv, StatusInfo{SessionID: session.ID(), State: "idle"})
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Start: session.AcceptClientMutationStart, Steer: session.AcceptClientMutationSteer, Queue: session.AcceptClientMutationQueue, Drain: session.AcceptClientMutationDrainAsSteer})
	conn := srv.AppServer().NewConnection("voice-boundary")
	call := func(method string, params any) appwire.Message {
		return conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), method, params))
	}
	if response := call(appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); response.Kind() != appwire.MessageResponse {
		t.Fatalf("initialize: %+v", response)
	}
	read := call(appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:" + session.ID()})
	if read.Kind() != appwire.MessageResponse {
		t.Fatalf("read: %+v", read)
	}
	if !read.Response.Result.(appwire.ThreadReadResponse).Thread.Evener.Capabilities.VoicePresentationContextV1 {
		t.Fatal("daemon capability not visible on thread read")
	}
	voice := &appwire.VoicePresentationContext{Version: 1, DeliveryID: "delivery", BindingID: "binding", Epoch: "epoch", InstanceID: session.ID(), CanonicalSourceItemID: "source", OriginatingRequestID: "request", FrontierID: "frontier", SampleRateHz: 24000}
	input := []appwire.InputItem{{Type: "text", Text: "opaque-server-719", VoicePresentation: voice}}
	for _, tc := range []struct {
		method   string
		params   any
		accepted bool
	}{
		{appwire.MethodTurnQueue, appwire.TurnQueueParams{Ref: "local:" + session.ID(), ExpectedInstanceID: session.ID(), ClientMutationID: "queue", Input: input}, false},
		{appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{Ref: "local:" + session.ID(), ExpectedInstanceID: session.ID(), ClientMutationID: "drain", Input: input}, false},
		{appwire.MethodTurnStart, appwire.TurnStartParams{Ref: "local:" + session.ID(), ExpectedInstanceID: session.ID(), ClientMutationID: "start", Input: input}, true},
		{appwire.MethodTurnSteer, appwire.TurnSteerParams{Ref: "local:" + session.ID(), ExpectedInstanceID: session.ID(), ClientMutationID: "steer", Input: input}, true},
	} {
		t.Run(tc.method, func(t *testing.T) {
			response := call(tc.method, tc.params)
			if (response.Kind() == appwire.MessageResponse) != tc.accepted {
				t.Fatalf("admission: %+v", response)
			}
			if !tc.accepted && response.Error.Error.Code != appwire.CodeInvalidParams {
				t.Fatalf("wrong rejection: %+v", response.Error)
			}
		})
	}
}
