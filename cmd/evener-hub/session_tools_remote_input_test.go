package hub

import (
	"encoding/json"
	"net/http"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestManagerRemoteInputRequiresIdentifiedBridge(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bridge bool
		peer   string
		caller string
		method string
		proof  *appwire.SessionActorProof
		code   int
	}{
		{name: "human", caller: "local:manager", code: appwire.CodeInvalidRequest},
		{name: "unidentified bridge", bridge: true, caller: "local:manager", code: appwire.CodeInvalidRequest},
		{name: "missing caller", bridge: true, peer: "hub-a", code: appwire.CodeInvalidParams},
		{name: "nonlocal caller", bridge: true, peer: "hub-a", caller: "elsewhere:manager", code: appwire.CodeInvalidParams},
		{name: "local peer alias", bridge: true, peer: "local", caller: "local:manager", code: appwire.CodeInvalidRequest},
		{name: "malformed peer", bridge: true, peer: "hub:a", caller: "local:manager", code: appwire.CodeInvalidRequest},
		{name: "forged nested proof", bridge: true, peer: "hub-a", caller: "local:manager", method: appwire.MethodTurnStart, proof: &appwire.SessionActorProof{CallerRef: "local:forged"}, code: appwire.CodeInvalidParams},
		{name: "unsupported method", bridge: true, peer: "hub-a", caller: "local:manager", method: appwire.MethodThreadShutdown, code: appwire.CodeMethodNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newHubRPCTestServer(t, hubcore.WebConfig{StateDir: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-b"})
			defer hub.Close()
			headers := http.Header{}
			if tc.bridge {
				headers.Set(bridgeOriginHeader, "1")
			}
			client := dialHubRPCWithHeaders(t, hub, headers)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			if tc.peer != "" {
				if _, err := client.IdentifySessionToolsPeer(t.Context(), appwire.SessionToolsPeerParams{HubID: tc.peer}); err != nil {
					t.Fatal(err)
				}
			}
			params, err := json.Marshal(appwire.TurnStartParams{Ref: "local:worker", ClientMutationID: "refused", Input: []appwire.InputItem{{Type: "text", Text: "sentinel"}}, ManagerProof: tc.proof})
			if err != nil {
				t.Fatal(err)
			}
			var response appwire.EmptyResponse
			err = client.Request(t.Context(), appwire.MethodHubSessionToolsInput, appwire.SessionToolsInput{CallerRef: tc.caller, Method: tc.method, Params: params}, &response)
			assertWireCode(t, err, tc.code)
		})
	}
}
