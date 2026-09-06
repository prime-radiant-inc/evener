package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestHubProtocolUpgradePreservesTranscriptAndRejectsUndeliverableMessages(t *testing.T) {
	root := t.TempDir()
	sessionID := buildRPCParentSession(t, filepath.Join(root, "projects", "upgrade-0000000000"))
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: sessionID, SessionID: sessionID, Endpoint: protocolMismatchPeer(t)})
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past, Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	ref := "local:" + sessionID
	t.Run("saved transcript remains readable with explicit restart state", func(t *testing.T) {
		read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, Subscribe: true})
		if err != nil {
			t.Fatal(err)
		}
		if read.Thread.Status.Type != "restartRequired" {
			t.Errorf("status=%q", read.Thread.Status.Type)
		}
		if read.Thread.Evener.Capabilities.Send || read.Thread.Evener.Capabilities.Queue || read.Thread.Evener.Capabilities.Rename {
			t.Error("incompatible session advertises unsupported mutations")
		}
		if len(read.Thread.Turns) != 2 {
			t.Errorf("saved turns=%d", len(read.Thread.Turns))
		}
	})
	for _, method := range []string{appwire.MethodTurnStart, appwire.MethodTurnQueue, appwire.MethodTurnSteer} {
		t.Run(method, func(t *testing.T) {
			var response any
			err := client.Request(context.Background(), method, map[string]any{"ref": ref, "clientMutationId": "upgrade-message", "expectedInstanceId": sessionID, "expectedTurnId": "turn-active", "input": []appwire.InputItem{{Type: "text", Text: "sentinel"}}}, &response)
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("error=%v", err)
			}
			data, ok := wire.Data.(map[string]any)
			if !ok || data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) || data["clientMutationId"] != "upgrade-message" || data["cause"] != "daemonRestartRequired" {
				t.Fatalf("rejection=%+v", wire)
			}
		})
	}
	t.Run("rename refuses while incompatible daemon owns metadata", func(t *testing.T) {
		var response any
		err := client.Request(context.Background(), appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: ref, Name: "sentinel"}, &response)
		if !isDaemonRestartRequiredError(err) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("resume refuses before replacement spawn", func(t *testing.T) {
		_, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Ref: ref})
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("error=%v", err)
		}
		data, ok := wire.Data.(map[string]any)
		if !ok || data["cause"] != "daemonRestartRequired" {
			t.Fatalf("rejection=%+v", wire)
		}
	})
}

func protocolMismatchPeer(t *testing.T) string {
	t.Helper()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var request struct {
			ID any `json:"id"`
		}
		if err := wsjson.Read(r.Context(), conn, &request); err != nil {
			return
		}
		_ = wsjson.Write(r.Context(), conn, map[string]any{"id": request.ID, "error": map[string]any{"code": appwire.CodeInvalidRequest, "message": "incompatible protocol"}})
	}))
	t.Cleanup(peer.Close)
	return "ws" + strings.TrimPrefix(peer.URL, "http") + "/rpc"
}

func TestHubResumeRefreshesProtocolStateBeforeDeciding(t *testing.T) {
	endpoint := protocolMismatchPeer(t)
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint("stopped=", stopped), func(t *testing.T) {
			dir := t.TempDir()
			entry := rendezvous.Entry{PID: 1001, SessionID: "upgrade", ThreadID: "upgrade", Protocol: "evener-appwire-v3", Endpoint: endpoint}
			roster := hubcore.NewRoster(dir, &hubcore.StatusProber{})
			writeRendezvous(t, dir, entry)
			if stopped {
				roster.Refresh()
				if err := rendezvous.Remove(dir, entry.PID); err != nil {
					t.Fatal(err)
				}
			}
			spawned := false
			cfg := hubcore.WebConfig{Roster: roster, ResumeLocks: hubcore.NewResumeLocks(), Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				spawned = true
				return rendezvous.Entry{}, errors.New("spawn sentinel")
			}}}
			_, err := hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{Session: "upgrade"})
			if spawned != stopped {
				t.Fatalf("spawned=%v stopped=%v error=%v", spawned, stopped, err)
			}
			if !stopped {
				var wire appwire.WireError
				if !errors.As(err, &wire) || wire.Data.(appwire.ErrorData).Cause != "daemonRestartRequired" {
					t.Fatalf("error=%v", err)
				}
			}
		})
	}
}

func TestHubTurnStartDiscoversRestartRequiredDuringRecovery(t *testing.T) {
	root := t.TempDir()
	sessionID := buildRPCParentSession(t, filepath.Join(root, "projects", "upgrade-0000000000"))
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: sessionID, SessionID: sessionID, Endpoint: protocolMismatchPeer(t)})
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Past: past, Roster: roster, ResumeLocks: hubcore.NewResumeLocks()})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, ClientMutationID: "upgrade-recovery", ExpectedInstanceID: sessionID, Input: []appwire.InputItem{{Type: "text", Text: "sentinel"}}})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error=%v", err)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok || data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) || data["cause"] != "daemonRestartRequired" || data["clientMutationId"] != "upgrade-recovery" {
		t.Fatalf("rejection=%+v data=%+v", wire, wire.Data)
	}
}

func TestHubUpgradeKeepsLostAcceptedReceiptUnknown(t *testing.T) {
	accepted := make(chan string, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var request struct {
			ID     any            `json:"id"`
			Params map[string]any `json:"params"`
		}
		if err := wsjson.Read(r.Context(), conn, &request); err != nil {
			return
		}
		if request.Params["protocolVersion"] != "evener-appwire-v3" {
			_ = wsjson.Write(r.Context(), conn, map[string]any{"id": request.ID, "error": map[string]any{"code": appwire.CodeInvalidRequest, "message": "incompatible protocol"}})
			return
		}
		if err := wsjson.Write(r.Context(), conn, map[string]any{"id": request.ID, "result": map[string]any{}}); err != nil {
			return
		}
		if err := wsjson.Read(r.Context(), conn, &request); err != nil {
			return
		}
		accepted <- request.Params["clientMutationId"].(string)
		// The peer accepts the mutation, then loses the connection before its receipt.
	}))
	defer peer.Close()
	endpoint := "ws" + strings.TrimPrefix(peer.URL, "http") + "/rpc"
	ctx := t.Context()
	transport, err := appwire.DialWebSocketWithHeaders(ctx, endpoint, peer.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	old := appwire.NewClient(transport)
	old.Start(ctx)
	defer old.Close()
	var response any
	if err := old.Request(ctx, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: "evener-appwire-v3"}, &response); err != nil {
		t.Fatal(err)
	}
	mutationID := "accepted-before-upgrade"
	if err := old.Request(ctx, appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: mutationID}, &response); err == nil {
		t.Fatal("expected lost receipt")
	}
	if got := <-accepted; got != mutationID {
		t.Fatalf("accepted=%q", got)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: "upgrade", SessionID: "upgrade", Endpoint: endpoint})
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	_, err = client.TurnStart(ctx, appwire.TurnStartParams{Ref: "local:upgrade", ClientMutationID: mutationID, ExpectedInstanceID: "upgrade", Input: []appwire.InputItem{{Type: "text", Text: "sentinel"}}})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error=%v", err)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok || data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) || data["retryDisposition"] != string(appwire.RetryDispositionBlocked) || data["cause"] != "daemonRestartRequired" {
		t.Fatalf("receipt=%+v", wire.Data)
	}
}

func TestRestartRequiredRecoveryPreservesDecodedWireData(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var request struct {
			ID any `json:"id"`
		}
		if err := wsjson.Read(r.Context(), conn, &request); err != nil {
			return
		}
		_ = wsjson.Write(r.Context(), conn, map[string]any{"id": request.ID, "error": map[string]any{"code": appwire.CodeConflict, "message": "restart required", "data": map[string]any{"cause": "daemonRestartRequired", "evenerErrorInfo": "conflict", "detail": "preserved"}}})
	}))
	defer peer.Close()
	transport, err := appwire.DialWebSocketWithHeaders(t.Context(), "ws"+strings.TrimPrefix(peer.URL, "http"), peer.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(t.Context())
	defer client.Close()
	var response any
	err = client.Request(t.Context(), appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: "local:upgrade"}, &response)
	var wire appwire.WireError
	if !errors.As(blockedUnknownMutationError("retry-id", err), &wire) {
		t.Fatalf("error=%v", err)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok || data["cause"] != "daemonRestartRequired" || data["evenerErrorInfo"] != "conflict" || data["detail"] != "preserved" || data["clientMutationId"] != "retry-id" || data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) {
		t.Fatalf("data=%+v", wire.Data)
	}
}
