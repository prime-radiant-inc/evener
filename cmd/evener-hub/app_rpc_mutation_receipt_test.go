package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

type mutationReceiptEchoRoute struct {
	method   string
	register func(*appserver.Server)
	check    func(*testing.T, *appwire.Client)
}

// mutationReceiptEchoRouteFor scripts one concrete daemon response and checks
// its concrete type after a round trip through both daemon and hub transports.
func mutationReceiptEchoRouteFor[P, R any](method string, params P, response, want R) mutationReceiptEchoRoute {
	return mutationReceiptEchoRoute{
		method: method,
		register: func(daemon *appserver.Server) {
			appserver.HandleTyped(daemon.Router(), method, func(context.Context, P) (R, error) {
				return response, nil
			})
		},
		check: func(t *testing.T, client *appwire.Client) {
			t.Helper()
			var got R
			if err := client.Request(context.Background(), method, params, &got); err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("response = %+v, want %+v", got, want)
			}
		},
	}
}

// Removing a handler's receipt adoption must fail its route case, including
// fields carried alongside the receipt rather than only the caller ID.
func TestHubRPCMutationReceiptEcho(t *testing.T) {
	const callerID = " mutation-padded "
	const sessionID = "receipt-session"
	const ref = "local:" + sessionID
	receipt := appwire.MutationReceipt{
		ClientMutationID:          "mutation-padded",
		Disposition:               appwire.MutationDispositionReplayed,
		ThreadID:                  sessionID,
		InstanceID:                "receipt-instance",
		TurnID:                    "turn_1",
		QueueEntryIDs:             []string{"entry_1", "entry_2"},
		ProjectionState:           appwire.MutationProjectionReflected,
		ConsumedClientMutationIDs: []string{"queued-mutation_1", "queued-mutation_2"},
	}
	wantReceipt := receipt
	wantReceipt.ClientMutationID = callerID
	turn := appwire.Turn{ID: "turn_1", Status: "inProgress"}
	thread := appwire.Thread{
		ID: sessionID, SessionID: sessionID, Source: "local",
		Evener: appwire.EvenerThread{
			Ref: ref, InstanceID: sessionID,
			Capabilities: appwire.ThreadCapabilities{Send: true, Clear: true, SharedNotes: true},
		},
	}
	input := []appwire.InputItem{{Type: "text", Text: "receipt-route-input"}}
	cases := []mutationReceiptEchoRoute{
		mutationReceiptEchoRouteFor(appwire.MethodTurnStart,
			appwire.TurnStartParams{Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID, Input: input},
			appwire.TurnStartResponse{Turn: turn, Receipt: receipt},
			appwire.TurnStartResponse{Turn: turn, Receipt: wantReceipt}),
		mutationReceiptEchoRouteFor(appwire.MethodTurnSteer,
			appwire.TurnSteerParams{Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID, Input: input},
			appwire.TurnSteerResponse{Receipt: receipt}, appwire.TurnSteerResponse{Receipt: wantReceipt}),
		mutationReceiptEchoRouteFor(appwire.MethodTurnInterrupt,
			appwire.TurnInterruptParams{Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID},
			appwire.TurnInterruptResponse{Receipt: receipt}, appwire.TurnInterruptResponse{Receipt: wantReceipt}),
		mutationReceiptEchoRouteFor(appwire.MethodTurnQueue,
			appwire.TurnQueueParams{Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID, Input: input},
			appwire.TurnQueueResponse{Receipt: receipt}, appwire.TurnQueueResponse{Receipt: wantReceipt}),
		mutationReceiptEchoRouteFor(appwire.MethodTurnDrainAsSteer,
			appwire.TurnDrainAsSteerParams{Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID, ExpectedQueueRevision: 7, Input: input},
			appwire.TurnDrainAsSteerResponse{Receipt: receipt}, appwire.TurnDrainAsSteerResponse{Receipt: wantReceipt}),
		mutationReceiptEchoRouteFor(appwire.MethodTurnPromoteQueuedAsSteer,
			appwire.TurnPromoteQueuedAsSteerParams{Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID, ExpectedEntryID: "entry_1"},
			appwire.TurnPromoteQueuedAsSteerResponse{Receipt: receipt}, appwire.TurnPromoteQueuedAsSteerResponse{Receipt: wantReceipt}),
		mutationReceiptEchoRouteFor(appwire.MethodTurnCancelQueued,
			appwire.TurnCancelQueuedParams{Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID, ExpectedEntryID: "entry_1"},
			appwire.TurnCancelQueuedResponse{RemovedText: "removed input", RemovedImages: 2, Receipt: receipt},
			appwire.TurnCancelQueuedResponse{RemovedText: "removed input", RemovedImages: 2, Receipt: wantReceipt}),
		mutationReceiptEchoRouteFor(appwire.MethodThreadClear,
			appwire.ThreadClearParams{Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID},
			appwire.ThreadClearResponse{Thread: thread, Ref: ref, Receipt: receipt},
			appwire.ThreadClearResponse{Thread: thread, Ref: ref, Receipt: wantReceipt}),
		mutationReceiptEchoRouteFor(appwire.MethodNotesHumanSet,
			appwire.NotesHumanSetParams{Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID, Note: "whiteboard input"},
			appwire.NotesHumanSetResponse{Note: "canonical whiteboard", Receipt: receipt},
			appwire.NotesHumanSetResponse{Note: "canonical whiteboard", Receipt: wantReceipt}),
		{
			method: appwire.MethodUrlsRemove,
			register: func(daemon *appserver.Server) {
				appserver.HandleTyped(daemon.Router(), appwire.MethodUrlsRemove, func(context.Context, appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
					return appwire.UrlsRemoveResponse{}, nil
				})
			},
			check: func(t *testing.T, client *appwire.Client) {
				t.Helper()
				var got map[string]json.RawMessage
				err := client.Request(context.Background(), appwire.MethodUrlsRemove, appwire.UrlsRemoveParams{
					Ref: ref, ClientMutationID: callerID, ExpectedInstanceID: sessionID, ID: "url_1",
				}, &got)
				if err != nil {
					t.Fatalf("urls/remove: %v", err)
				}
				if got == nil || len(got) != 0 {
					t.Fatalf("acknowledgement = %s, want an empty object", got)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{Thread: thread}, nil
			})
			tc.register(daemon)
			daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
			defer daemonHTTP.Close()
			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{
				PID: 106, Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):],
				SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
			})
			roster := hubcore.NewRoster(runDir, nil)
			roster.Refresh()
			hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			tc.check(t, client)
		})
	}
}
