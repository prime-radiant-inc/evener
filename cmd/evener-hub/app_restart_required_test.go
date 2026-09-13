package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

func TestHubProtocolUpgradePreservesTranscriptAndRejectsUndeliverableMessages(t *testing.T) {
	for _, protocol := range []string{"evener-appwire-v3", "evener-appwire-v4"} {
		for _, cleared := range []bool{false, true} {
			for _, cached := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/cleared=%v/cached=%v", protocol, cleared, cached), func(t *testing.T) { testHubProtocolUpgrade(t, protocol, cleared, cached) })
			}
		}
	}
}

func testHubProtocolUpgrade(t *testing.T, protocol string, cleared, cached bool) {
	root := t.TempDir()
	sessionID := buildRPCParentSession(t, filepath.Join(root, "projects", "upgrade-0000000000"))
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	daemonSessionID := sessionID
	if cleared {
		daemonSessionID = "02wMz5Txv1C3Hut0M8GCeC"
	}
	entry := rendezvous.Entry{PID: 1001, Protocol: protocol, ThreadID: daemonSessionID, SessionID: daemonSessionID, WorkspaceRef: "local:" + sessionID, Endpoint: protocolMismatchPeer(t)}
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	if cached {
		writeRendezvous(t, runDir, entry)
		roster.Refresh()
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past, Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	if !cached {
		writeRendezvous(t, runDir, entry)
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
		if !read.Thread.Evener.Capabilities.SharedNotes {
			t.Error("restart-required session must keep saved shared notes readable")
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
	for _, request := range []struct {
		method string
		params any
	}{
		{appwire.MethodThreadReasoningEffortSet, appwire.ThreadReasoningEffortSetParams{Ref: ref, ReasoningEffort: "high"}},
		{appwire.MethodEvenerSandboxEscalationResolve, appwire.SandboxEscalationResolveParams{Ref: ref, EscalationID: "escalation", Approve: true}},
	} {
		t.Run(request.method, func(t *testing.T) {
			var response any
			err := client.Request(context.Background(), request.method, request.params, &response)
			if !isDaemonRestartRequiredError(err) {
				t.Fatalf("error=%v", err)
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
	t.Run("shutdown does not pretend incompatible daemon exited", func(t *testing.T) {
		err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: ref})
		if !isDaemonRestartRequiredError(err) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("shutdown accepts a completed explicit stop", func(t *testing.T) {
		if err := rendezvous.Remove(runDir, 1001); err != nil {
			t.Fatal(err)
		}
		read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, Subscribe: true})
		if err != nil {
			t.Fatal(err)
		}
		if read.Thread.Status.Type == appwire.ThreadStatusRestartRequired || !read.Thread.Evener.Capabilities.Send {
			t.Fatalf("stopped daemon still blocks refreshed session: %+v", read.Thread)
		}
		if err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Fatal(err)
		}
	})

}

func protocolMismatchPeer(t *testing.T) string {
	t.Helper()
	peer := httptest.NewServer(http.HandlerFunc(serveProtocolMismatch))
	t.Cleanup(peer.Close)
	return "ws" + strings.TrimPrefix(peer.URL, "http") + "/rpc"
}

func serveProtocolMismatch(w http.ResponseWriter, r *http.Request) {
	serveInitializeResponse(w, r, map[string]any{"error": map[string]any{"code": appwire.CodeInvalidRequest, "message": "incompatible protocol"}})
}

func serveTypedProtocolMismatch(w http.ResponseWriter, r *http.Request) {
	serveInitializeResponse(w, r, map[string]any{"result": appwire.InitializeResponse{ProtocolVersion: "evener-appwire-v4"}})
}

func serveInitializeResponse(w http.ResponseWriter, r *http.Request, response map[string]any) {
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
	response["id"] = request.ID
	_ = wsjson.Write(r.Context(), conn, response)
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

func TestNavigationDisablesRenameForRestartRequiredDaemon(t *testing.T) {
	tree := hubcore.Tree{Live: []hubcore.TreeNode{{ID: "02wMz5Txv1C3Hut0M8GCeB"}, {ID: "local:02wMz5Txv1C3Hut0M8GCeB"}, {ID: "02wMz5Txv1C3Hut0M8GCeC"}}}
	live := []hubcore.LiveEntry{{SessionID: "02wMz5Txv1C3Hut0M8GCeB", Status: appwire.ThreadStatusRestartRequired}, {SessionID: "02wMz5Txv1C3Hut0M8GCeC", Status: appwire.ThreadStatusIdle}}
	live[0].WorkspaceRef = "local:02wMz5Txv1C3Hut0M8GCeD"
	tree.Live = append(tree.Live, hubcore.TreeNode{ID: "02wMz5Txv1C3Hut0M8GCeD"}, hubcore.TreeNode{ID: "local:02wMz5Txv1C3Hut0M8GCeD"})
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, tree, nil, hubapi.AttentionSummary{}, live, nil, nil, nil, nil)
	if inputs.Renameable["02wMz5Txv1C3Hut0M8GCeB"] || inputs.Renameable["local:02wMz5Txv1C3Hut0M8GCeB"] || inputs.Renameable["02wMz5Txv1C3Hut0M8GCeD"] || inputs.Renameable["local:02wMz5Txv1C3Hut0M8GCeD"] {
		t.Fatal("navigation advertises rename for incompatible owner")
	}
	if !inputs.Renameable["02wMz5Txv1C3Hut0M8GCeC"] {
		t.Fatal("compatible session lost rename")
	}
}

func TestNavigationCrashRecordCannotEnableRenameForIncompatibleReplacement(t *testing.T) {
	const workspaceID = "02wMz5Txv1C3Hut0M8GCeB"
	const currentID = "02wMz5Txv1C3Hut0M8GCeC"
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 2, StartedAt: time.Unix(2, 0), WorkspaceRef: "local:" + workspaceID, SessionID: currentID, Status: appwire.ThreadStatusRestartRequired},
		hubcore.LiveEntry{PID: 1, StartedAt: time.Unix(1, 0), WorkspaceRef: "local:" + workspaceID, SessionID: "02wMz5Txv1C3Hut0M8GCeD", Status: "errored", Crashed: true},
	)
	tree := hubcore.Tree{Live: []hubcore.TreeNode{{ID: workspaceID, State: appwire.ThreadStatusRestartRequired}}}
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, tree, nil, hubapi.AttentionSummary{}, roster.List(), nil, nil, nil, nil)
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatal(err)
	}
	rows := projection.LivePage(0, 50).Sessions
	if len(rows) != 1 || rows[0].Ref != "local:"+workspaceID || rows[0].Rename {
		t.Fatalf("incompatible replacement must remain unrenameable: %+v", rows)
	}
}

func TestHubUpgradeClassifiesUncachedDaemonOwnership(t *testing.T) {
	for _, method := range []string{appwire.MethodThreadRead, appwire.MethodTurnQueue, appwire.MethodEvenerThreadNameSet, appwire.MethodThreadReasoningEffortSet, appwire.MethodEvenerSandboxEscalationResolve} {
		t.Run(method, func(t *testing.T) {
			root := t.TempDir()
			sessionID := buildRPCParentSession(t, filepath.Join(root, "projects", "upgrade-0000000000"))
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}
			runDir := t.TempDir()
			roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
			roster.Refresh()
			writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: sessionID, SessionID: sessionID, WorkspaceRef: "local:" + sessionID, Endpoint: protocolMismatchPeer(t)})
			hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past, Roster: roster, RunDir: runDir})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			if method == appwire.MethodThreadRead {
				read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, Subscribe: true})
				if err != nil {
					t.Fatal(err)
				}
				if read.Thread.Status.Type != appwire.ThreadStatusRestartRequired || read.Thread.Evener.Capabilities.Send || read.Thread.Evener.Capabilities.Queue || read.Thread.Evener.Capabilities.Rename {
					t.Fatalf("undiscovered incompatible owner was not reflected: %+v", read.Thread)
				}
				return
			}
			var response any
			err := client.Request(context.Background(), method, map[string]any{"ref": "local:" + sessionID, "clientMutationId": "uncertain", "expectedInstanceId": sessionID, "input": []appwire.InputItem{{Type: "text", Text: "sentinel"}}, "name": "renamed", "reasoningEffort": "high", "escalationId": "escalation", "approve": true}, &response)
			if !isDaemonRestartRequiredError(err) {
				t.Fatalf("error=%v", err)
			}
			if method == appwire.MethodTurnQueue {
				var wire appwire.WireError
				if !errors.As(err, &wire) {
					t.Fatal(err)
				}
				data, ok := wire.Data.(map[string]any)
				if !ok || data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) || data["retryDisposition"] != string(appwire.RetryDispositionBlocked) {
					t.Fatalf("outcome=%+v", wire)
				}
			}
		})
	}
}

func TestHubUpgradeRestrictsPersistedDelegate(t *testing.T) {
	for _, scenario := range []struct{ delegated, unreadableSibling bool }{{false, false}, {true, false}, {true, true}} {
		delegated := scenario.delegated
		t.Run(fmt.Sprint(scenario), func(t *testing.T) {
			root := t.TempDir()
			stateDir := filepath.Join(root, "projects", "upgrade-0000000000")
			parentID := buildRPCParentSession(t, stateDir)
			childID := "02wMz5Txv1C3Hut0M8GCeC"
			writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", childID+".transcript.jsonl"), transcript.Header{SessionID: childID, ParentSessionID: parentID, ProfileID: "openai", Model: "gpt-5"})
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: childID, ParentSessionID: parentID, IsSubagent: delegated, JobTreeRootSessionID: parentID, ProfileID: "openai", Model: "gpt-5"}); err != nil {
				t.Fatal(err)
			}
			if delegated {
				path := filepath.Join(stateDir, "sessions", parentID, "delegates.jsonl")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				descriptor := map[string]any{"child_session_id": childID, "transcript_ref": "local:" + childID, "owner_session_id": parentID, "task": "sentinel", "agent_type": "explorer", "tool_name_ceiling": []string{"communicate"}, "resumable": true, "config": map[string]any{}}
				events := []map[string]any{{"kind": "delegate_created", "seq": 1, "delegate_id": "dlg_upgrade", "created": map[string]any{"descriptor": descriptor}}}
				if scenario.unreadableSibling {
					sibling := maps.Clone(descriptor)
					siblingID := "02wMz5Txv1C3Hut0M8GCeD"
					sibling["child_session_id"] = siblingID
					sibling["transcript_ref"] = "local:" + siblingID
					events = append(events, map[string]any{"kind": "delegate_created", "seq": 2, "delegate_id": "dlg_sibling", "created": map[string]any{"descriptor": sibling}})
					if err := os.Mkdir(filepath.Join(stateDir, "sessions", siblingID+".transcript.jsonl"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				batch, err := json.Marshal(map[string]any{"events": events})
				if err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(path, append(append([]byte("{\"version\":1}\n"), batch...), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}
			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: parentID, SessionID: parentID, Endpoint: protocolMismatchPeer(t)})
			roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
			roster.Refresh()
			hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past, Roster: roster})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}

			web := &WebServer{cfg: hubcore.WebConfig{Past: past, Roster: roster}}
			snapshot := web.navigationSnapshotInputs(t.Context())
			tree := hubBuildNavigationTree(snapshot.metas, snapshot.live, nil, snapshot.projects)
			inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, tree, nil, hubapi.AttentionSummary{}, snapshot.live, nil, nil, nil, nil)
			if delegated && inputs.Renameable[childID] {
				t.Error("navigation advertises delegate rename")
			}
			childRestart := false
			for _, live := range snapshot.live {
				if live.SessionID == childID && live.Status == appwire.ThreadStatusRestartRequired {
					childRestart = true
				}
			}
			if childRestart != delegated {
				t.Errorf("navigation child restart=%v, delegated=%v", childRestart, delegated)
			}
			if delegated && !scenario.unreadableSibling {
				t.Run("canceled ownership check", func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					_, err := withDeletionTargetOwnership(ctx, web.cfg, localAppRef(childID), "", "canceled-send", func() (struct{}, error) {
						t.Error("canceled ownership check ran the mutation")
						return struct{}{}, nil
					})
					wire, ok := errors.AsType[appwire.WireError](err)
					if !ok || !strings.Contains(wire.Message, context.Canceled.Error()) {
						t.Errorf("ownership check ignored cancellation: %v", err)
					}
					if snapshot := web.navigationSnapshotInputs(ctx); !errors.Is(snapshot.ownershipErr, context.Canceled) {
						t.Errorf("navigation ownership error=%v, want cancellation", snapshot.ownershipErr)
					}
				})
			}
			ref := "local:" + childID
			read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if got := read.Thread.Status.Type == "restartRequired"; got != delegated {
				t.Errorf("restartRequired=%v, delegated=%v", got, delegated)
			}
			if !delegated {
				return
			}
			if read.Thread.Evener.Capabilities.Rename || read.Thread.Evener.Capabilities.Queue {
				t.Error("delegate advertises mutations")
			}
			for _, method := range []string{appwire.MethodEvenerThreadNameSet, appwire.MethodTurnQueue} {
				var response any
				err := client.Request(t.Context(), method, map[string]any{"ref": ref, "name": "changed", "clientMutationId": "child-retry", "expectedInstanceId": childID, "input": []appwire.InputItem{{Type: "text", Text: "sentinel"}}}, &response)
				if !isDaemonRestartRequiredError(err) {
					t.Errorf("%s error=%v", method, err)
				}
				if method == appwire.MethodTurnQueue {
					if wire, ok := errors.AsType[appwire.WireError](err); ok {
						data, _ := wire.Data.(map[string]any)
						if data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) {
							t.Errorf("receipt=%+v", data)
						}
					}
				}
			}
			if !scenario.unreadableSibling {
				for _, rootHint := range []bool{true, false} {
					t.Run(fmt.Sprint("unreadable intermediate metadata/root hint=", rootHint), func(t *testing.T) {
						journalPath := filepath.Join(stateDir, "sessions", parentID, "delegates.jsonl")
						journalBefore, err := os.ReadFile(journalPath)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if err := os.WriteFile(journalPath, journalBefore, 0600); err != nil {
								t.Error(err)
							}
						})

						grandchildID := "02wMz5Txv1C3Hut0M8GCeD"
						t.Cleanup(func() {
							for _, suffix := range []string{".meta.json", ".transcript.jsonl"} {
								if err := os.Remove(filepath.Join(stateDir, "sessions", grandchildID+suffix)); err != nil {
									t.Error(err)
								}
							}
							if _, err := past.Rebuild(); err != nil {
								t.Error(err)
							}
						})

						if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: grandchildID, ParentSessionID: childID, IsSubagent: true, JobTreeRootSessionID: parentID, ProfileID: "openai", Model: "gpt-5"}); err != nil {
							t.Fatal(err)
						}
						writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", grandchildID+".transcript.jsonl"), transcript.Header{SessionID: grandchildID, ParentSessionID: childID, ProfileID: "openai", Model: "gpt-5"})
						if err != nil {
							t.Fatal(err)
						}
						if err := writer.Close(); err != nil {
							t.Fatal(err)
						}
						descriptor := map[string]any{"child_session_id": grandchildID, "transcript_ref": localAppRef(grandchildID), "owner_session_id": parentID, "parent_delegate_id": "dlg_upgrade", "task": "nested sentinel", "agent_type": "explorer", "tool_name_ceiling": []string{"communicate"}, "resumable": true, "config": map[string]any{}}
						batch, err := json.Marshal(map[string]any{"events": []map[string]any{{"kind": "delegate_created", "seq": 2, "delegate_id": "dlg_nested", "created": map[string]any{"descriptor": descriptor}}}})
						if err != nil {
							t.Fatal(err)
						}
						journal, err := os.OpenFile(filepath.Join(stateDir, "sessions", parentID, "delegates.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
						if err != nil {
							t.Fatal(err)
						}
						_, writeErr := journal.Write(append(batch, '\n'))
						if err := journal.Close(); err != nil {
							t.Fatal(err)
						}
						if writeErr != nil {
							t.Fatal(writeErr)
						}
						if _, err := past.Rebuild(); err != nil {
							t.Fatal(err)
						}
						grandRef := localAppRef(grandchildID)
						if err := daemonRestartRequiredError(t.Context(), web.cfg, grandRef, "", ""); !isDaemonRestartRequiredError(err) {
							t.Fatalf("nested fixture has no incompatible owner: %v", err)
						}
						if !rootHint {
							meta, err := schema.LoadSessionMeta(stateDir, grandchildID)
							if err != nil {
								t.Fatal(err)
							}
							meta.JobTreeRootSessionID = ""
							if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
								t.Fatal(err)
							}
						}
						childPath := filepath.Join(stateDir, "sessions", childID+".meta.json")
						original, err := os.ReadFile(childPath)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if err := os.WriteFile(childPath, original, 0600); err != nil {
								t.Error(err)
							}
							if _, err := past.Rebuild(); err != nil {
								t.Error(err)
							}
						})
						if err := os.WriteFile(childPath, []byte("{"), 0600); err != nil {
							t.Fatal(err)
						}
						if _, err := past.Rebuild(); err != nil {
							t.Fatal(err)
						}
						if _, ok := past.Find(childID); ok {
							t.Fatal("unreadable intermediate remained indexed")
						}
						for _, method := range []string{appwire.MethodEvenerThreadNameSet, appwire.MethodTurnQueue} {
							var response any
							err := client.Request(t.Context(), method, map[string]any{"ref": grandRef, "name": "unsafe nested", "clientMutationId": "nested-owner", "expectedInstanceId": grandchildID, "input": []appwire.InputItem{{Type: "text", Text: "sentinel"}}}, &response)
							wire, ok := errors.AsType[appwire.WireError](err)
							if !ok {
								t.Errorf("%s bypassed unresolved nested ownership: %v", method, err)
								continue
							}
							if method == appwire.MethodTurnQueue {
								data, _ := wire.Data.(map[string]any)
								if data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) || data["retryDisposition"] != string(appwire.RetryDispositionBlocked) {
									t.Errorf("receipt=%+v", data)
								}
							}
						}
						meta, err := schema.LoadSessionMeta(stateDir, grandchildID)
						if err != nil {
							t.Fatal(err)
						}
						if meta.Name != "" {
							t.Errorf("nested delegate renamed through unreadable ownership: %q", meta.Name)
						}
						assertNavigationOwnershipReadOnly(t, web)
					})
				}
			}
			t.Run("unreadable parent metadata blocks delegate mutations", func(t *testing.T) {
				parentPath := filepath.Join(stateDir, "sessions", parentID+".meta.json")
				original, err := os.ReadFile(parentPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(parentPath, original, 0600); err != nil {
						t.Error(err)
					}
					if _, err := past.Rebuild(); err != nil {
						t.Error(err)
					}
				})
				if err := os.WriteFile(parentPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := past.Rebuild(); err != nil {
					t.Fatal(err)
				}
				if _, ok := past.Find(parentID); ok {
					t.Fatal("unreadable parent remained in past index")
				}
				before, err := schema.LoadSessionMeta(stateDir, childID)
				if err != nil {
					t.Fatal(err)
				}
				for _, method := range []string{appwire.MethodEvenerThreadNameSet, appwire.MethodTurnQueue} {
					var response any
					err := client.Request(t.Context(), method, map[string]any{"ref": ref, "name": "unsafe", "clientMutationId": "unreadable-parent", "expectedInstanceId": childID, "input": []appwire.InputItem{{Type: "text", Text: "sentinel"}}}, &response)
					wire, ok := errors.AsType[appwire.WireError](err)
					if !ok {
						t.Errorf("%s bypassed unreadable ownership: %v", method, err)
						continue
					}
					if method == appwire.MethodTurnQueue {
						data, _ := wire.Data.(map[string]any)
						if data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) || data["retryDisposition"] != string(appwire.RetryDispositionBlocked) {
							t.Errorf("receipt=%+v", data)
						}
					}
				}
				after, err := schema.LoadSessionMeta(stateDir, childID)
				if err != nil {
					t.Fatal(err)
				}
				if after.Name != before.Name {
					t.Errorf("delegate renamed despite unreadable ownership: %q", after.Name)
				}
				assertNavigationOwnershipReadOnly(t, web)
			})
			for _, fault := range []string{"missing", "unreadable", "torn"} {
				t.Run("ownership journal "+fault, func(t *testing.T) {
					journal := filepath.Join(stateDir, "sessions", parentID, "delegates.jsonl")
					original, err := os.ReadFile(journal)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := os.RemoveAll(journal); err != nil {
							t.Error(err)
						}
						if err := os.WriteFile(journal, original, 0600); err != nil {
							t.Error(err)
						}
					})
					if err := os.Remove(journal); err != nil {
						t.Fatal(err)
					}
					if fault == "unreadable" {
						if err := os.Mkdir(journal, 0700); err != nil {
							t.Fatal(err)
						}
					}
					if fault == "torn" {
						if err := os.WriteFile(journal, append(original, '{'), 0600); err != nil {
							t.Fatal(err)
						}
					}
					before, err := schema.LoadSessionMeta(stateDir, childID)
					if err != nil {
						t.Fatal(err)
					}
					beforeMetas, err := schema.ListSessionMetas(stateDir)
					if err != nil {
						t.Fatal(err)
					}
					for _, method := range []string{appwire.MethodEvenerThreadNameSet, appwire.MethodTurnQueue, appwire.MethodThreadFork} {
						params := map[string]any{"ref": ref, "name": "unsafe", "clientMutationId": "unreadable-owner", "expectedInstanceId": childID, "input": []appwire.InputItem{{Type: "text", Text: "sentinel"}}}
						if method == appwire.MethodThreadFork {
							params = map[string]any{"ref": ref, "aside": true}
						}
						var response any
						err := client.Request(t.Context(), method, params, &response)
						wire, ok := errors.AsType[appwire.WireError](err)
						if !ok {
							t.Errorf("%s error=%v", method, err)
							continue
						}
						if method == appwire.MethodTurnQueue {
							data, _ := wire.Data.(map[string]any)
							if data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) || data["retryDisposition"] != string(appwire.RetryDispositionBlocked) {
								t.Errorf("receipt=%+v", data)
							}
						}
					}
					after, err := schema.LoadSessionMeta(stateDir, childID)
					if err != nil {
						t.Fatal(err)
					}
					if after.Name != before.Name {
						t.Errorf("renamed delegate without ownership journal: %q", after.Name)
					}
					afterMetas, err := schema.ListSessionMetas(stateDir)
					if err != nil {
						t.Fatal(err)
					}
					if len(afterMetas) != len(beforeMetas) {
						t.Error("fork created metadata without ownership journal")
					}
					_, navigationErr := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now())
					if fault == "torn" {
						// A complete matching descriptor still establishes a restriction:
						// the caller blocks mutations rather than permitting them.
						if navigationErr != nil {
							t.Errorf("known owner lost restart projection: %v", navigationErr)
						}
					} else {
						assertNavigationOwnershipReadOnly(t, web)
					}
				})
			}

		})
	}
}

func TestThreadReadRejectsMalformedRefWithoutRoster(t *testing.T) {
	hub := newHubRPCTestServer(t, hubcore.WebConfig{})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	_, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "malformed"})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("error=%v", err)
	}
}

func TestMalformedMutationRefsAreNotReportedAsUncertain(t *testing.T) {
	hub := newHubRPCTestServer(t, hubcore.WebConfig{})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{appwire.MethodTurnStart, appwire.MethodTurnQueue, appwire.MethodTurnSteer} {
		t.Run(method, func(t *testing.T) {
			var response any
			err := client.Request(t.Context(), method, map[string]any{"ref": "malformed", "clientMutationId": "invalid-" + method, "expectedInstanceId": "session", "expectedTurnId": "turn", "input": []appwire.InputItem{{Type: "text", Text: "sentinel"}}}, &response)
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("error=%v", err)
			}
			data, _ := wire.Data.(map[string]any)
			if data["mutationOutcome"] == string(appwire.MutationOutcomeUnknown) {
				t.Fatalf("invalid request reported as uncertain: %+v", data)
			}
		})
	}
}

type canceledOwnershipProber struct {
	started chan struct{}
	release chan struct{}
}

func (p *canceledOwnershipProber) Probe(entry rendezvous.Entry) hubcore.ProbeResult {
	close(p.started)
	<-p.release
	return hubcore.ProbeResult{SessionID: entry.ThreadID, Status: appwire.ThreadStatusIdle, OK: true}
}

func TestHubMutationOwnershipCancellationPreservesUnknownReceipt(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, ThreadID: webTestSessionID})
	synctest.Test(t, func(t *testing.T) {
		prober := &canceledOwnershipProber{started: make(chan struct{}), release: make(chan struct{})}
		cfg := hubcore.WebConfig{Roster: hubcore.NewRoster(runDir, prober)}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := withDeletionTargetOwnership(ctx, cfg, localAppRef(webTestSessionID), "", "pending-send", func() (struct{}, error) {
				return struct{}{}, appwire.SessionUnavailable("daemon unavailable")
			})
			done <- err
		}()
		<-prober.started
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok {
				t.Errorf("expected wire error, got %v", err)
			} else {
				data, ok := wire.Data.(appwire.ErrorData)
				if !ok || data.MutationOutcome != appwire.MutationOutcomeUnknown || data.RetryDisposition != appwire.RetryDispositionBlocked || data.ClientMutationID != "pending-send" {
					t.Errorf("canceled ownership refresh must preserve the blocked unknown receipt: %+v", wire.Data)
				}
			}
		default:
			t.Error("mutation still waits for probe after cancellation")
		}
		close(prober.release)
		synctest.Wait()
	})
}

func TestHubUpgradeBlocksForkWritesUntilParentStops(t *testing.T) {
	for _, scenario := range []struct{ cached, delegate bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		for _, mode := range []string{"aside", "edit", "defer"} {
			t.Run(fmt.Sprintf("cached=%v/delegate=%v/%s", scenario.cached, scenario.delegate, mode), func(t *testing.T) {
				stateDir := t.TempDir()
				rootID := buildRPCParentSession(t, stateDir)
				parentID := rootID
				expectedMetas := 1
				if scenario.delegate {
					parentID = buildUpgradeDelegate(t, stateDir, rootID)
					expectedMetas++
				}
				runDir := t.TempDir()
				writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: rootID, SessionID: rootID, Endpoint: protocolMismatchPeer(t)})
				roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
				if scenario.cached {
					roster.Refresh()
				}
				hub := newHubRPCTestServer(t, hubcore.WebConfig{StateDir: stateDir, Roster: roster})
				defer hub.Close()
				client := dialHubRPC(t, hub)
				defer client.Close()
				if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
					t.Fatal(err)
				}
				params := appwire.ThreadForkParams{Ref: localAppRef(parentID)}
				if mode == "aside" {
					params.Aside = true
				} else {
					params.SourceTurnID = "1"
					params.Label = "parent branch"
					if mode == "defer" {
						params.DeferInput = true
					} else {
						params.EditedInput = "replacement"
					}
				}
				_, err := client.ThreadFork(t.Context(), params)
				if !isDaemonRestartRequiredError(err) {
					t.Errorf("fork bypassed incompatible owner: %v", err)
				}
				metas, err := schema.ListSessionMetas(stateDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(metas) != expectedMetas {
					t.Errorf("fork created session metadata: count=%d, want %d", len(metas), expectedMetas)
				}
				parent, err := schema.LoadSessionMeta(stateDir, parentID)
				if err != nil {
					t.Fatal(err)
				}
				if parent.ForkLabel != "" {
					t.Errorf("fork changed live parent label: %q", parent.ForkLabel)
				}
				if err := rendezvous.Remove(runDir, 1001); err != nil {
					t.Fatal(err)
				}
				forked, err := client.ThreadFork(t.Context(), params)
				if err != nil {
					t.Fatalf("fork after owner stopped: %v", err)
				}
				child, err := schema.LoadSessionMeta(stateDir, forked.Thread.ID)
				if err != nil || child.ParentSessionID != parentID {
					t.Errorf("fork after stop did not create child: %+v, err=%v", child, err)
				}
			})
		}
	}
}

func buildUpgradeDelegate(t *testing.T, stateDir, ownerID string) string {
	t.Helper()
	childID, err := agent.ForkSession(stateDir, ownerID, 1, "delegate prompt", "")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatal(err)
	}
	meta.IsSubagent = true
	meta.JobTreeRootSessionID = ownerID
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	descriptor := map[string]any{"child_session_id": childID, "transcript_ref": localAppRef(childID), "owner_session_id": ownerID, "task": "sentinel", "agent_type": "explorer", "tool_name_ceiling": []string{"communicate"}, "resumable": true, "config": map[string]any{}}
	batch, err := json.Marshal(map[string]any{"events": []map[string]any{{"kind": "delegate_created", "seq": 1, "delegate_id": "dlg_upgrade", "created": map[string]any{"descriptor": descriptor}}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "sessions", ownerID, "delegates.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte("{\"version\":1}\n"), batch...), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return childID
}

func TestHubUpgradeDoesNotTreatFailedProbeAsAbsentOwner(t *testing.T) {
	for _, fault := range []string{"probe", "unidentified", "changed-identity", "malformed", "unreadable", "missing-directory"} {
		for _, delegate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delegate=%v", fault, delegate), func(t *testing.T) {
				stateDir := filepath.Join(t.TempDir(), "projects", "upgrade-0000000000")
				rootID := buildRPCParentSession(t, stateDir)
				targetID := rootID
				if delegate {
					targetID = buildUpgradeDelegate(t, stateDir, rootID)
				}
				before, err := schema.LoadSessionMeta(stateDir, targetID)
				if err != nil {
					t.Fatal(err)
				}
				metas, err := schema.ListSessionMetas(stateDir)
				if err != nil {
					t.Fatal(err)
				}
				runDir := t.TempDir()
				hiddenRunDir := filepath.Join(t.TempDir(), "hidden")
				entry := rendezvous.Entry{PID: os.Getpid(), Protocol: "evener-appwire-v3", ThreadID: rootID, SessionID: rootID}
				if fault == "unidentified" {
					entry.ThreadID = ""
					entry.SessionID = ""
				}
				writeRendezvous(t, runDir, entry)
				roster := hubcore.NewRoster(runDir, failedRPCProber{})
				if fault == "changed-identity" {
					entry.Protocol = appwire.ProtocolVersion
					writeRendezvous(t, runDir, entry)
					prober := &changedOwnershipProber{sessionID: rootID}
					roster = hubcore.NewRoster(runDir, prober)
					roster.Refresh()
					entry.InstanceID = "replacement"
					entry.Protocol = "evener-appwire-v3"
					writeRendezvous(t, runDir, entry)
					prober.fail = true
				}
				if fault != "probe" && fault != "unidentified" && fault != "changed-identity" {
					roster = hubcore.NewRoster(runDir, fakeProber{sessionID: rootID, status: appwire.ThreadStatusRestartRequired})
					roster.Refresh()
					path := filepath.Join(runDir, fmt.Sprintf("%d.json", os.Getpid()))
					switch fault {
					case "malformed":
						if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
							t.Fatal(err)
						}
					case "missing-directory":
						if err := os.Rename(runDir, hiddenRunDir); err != nil {
							t.Fatal(err)
						}
					default:
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
					}
				}

				past := hubcore.NewPastIndex(stateDir)
				if _, err := past.Rebuild(); err != nil {
					t.Fatal(err)
				}
				hub := newHubRPCTestServer(t, hubcore.WebConfig{StateDir: stateDir, Past: past, Roster: roster})
				defer hub.Close()
				client := dialHubRPC(t, hub)
				defer client.Close()
				if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
					t.Fatal(err)
				}
				ref := localAppRef(targetID)
				var response any
				if err := client.Request(t.Context(), appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: ref, Name: "must not write"}, &response); err == nil {
					t.Error("rename bypassed unresolved owner")
				}
				if _, err := client.ThreadFork(t.Context(), appwire.ThreadForkParams{Ref: ref, Aside: true}); err == nil {
					t.Error("fork bypassed unresolved owner")
				}
				err = client.Request(t.Context(), appwire.MethodTurnQueue, map[string]any{"ref": ref, "clientMutationId": "uncertain-upgrade", "expectedInstanceId": targetID, "input": []appwire.InputItem{{Type: "text", Text: "sentinel"}}}, &response)
				var wire appwire.WireError
				if !errors.As(err, &wire) {
					t.Fatalf("queue error=%v", err)
				}
				data, ok := wire.Data.(map[string]any)
				if !ok || data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) || data["retryDisposition"] != string(appwire.RetryDispositionBlocked) {
					t.Errorf("receipt=%+v", wire)
				}
				after, err := schema.LoadSessionMeta(stateDir, targetID)
				if err != nil {
					t.Fatal(err)
				}
				afterMetas, err := schema.ListSessionMetas(stateDir)
				if err != nil {
					t.Fatal(err)
				}
				if after.Name != before.Name || len(afterMetas) != len(metas) {
					t.Error("unresolved owner allowed metadata writes")
				}
				if fault == "probe" || fault == "unidentified" || fault == "changed-identity" {
					web := &WebServer{cfg: hubcore.WebConfig{StateDir: stateDir, Past: past, Roster: roster}}
					assertNavigationOwnershipReadOnly(t, web)
				}
				if fault == "missing-directory" {
					if err := os.Rename(hiddenRunDir, runDir); err != nil {
						t.Fatal(err)
					}
				}
				if err := rendezvous.Remove(runDir, os.Getpid()); err != nil {
					t.Fatal(err)
				}
				if err := client.Request(t.Context(), appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: ref, Name: "released"}, &response); err != nil {
					t.Fatalf("rename after removal: %v", err)
				}
			})
		}
	}
}

func TestRestartRequiredOwnershipSkipsUnspecifiedTarget(t *testing.T) {
	cfg := hubcore.WebConfig{StateDir: t.TempDir(), Roster: hubcore.NewRoster(t.TempDir(), nil)}
	_, required, err := restartRequiredDaemon(t.Context(), cfg, "", "")
	if err != nil || required {
		t.Fatalf("unspecified ownership: required=%v error=%v", required, err)
	}
}

type changedOwnershipProber struct {
	sessionID string
	fail      bool
}

func (p *changedOwnershipProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	return hubcore.ProbeResult{SessionID: p.sessionID, Status: appwire.ThreadStatusIdle, OK: !p.fail}
}

func TestHubRPCListShowsIncompatibleDaemonWithoutPastIndex(t *testing.T) {
	const sessionID = "unindexed-owner"
	runDir := t.TempDir()
	entry := rendezvous.Entry{PID: os.Getpid(), Protocol: "evener-appwire-v4", Endpoint: protocolMismatchPeer(t), SourceID: "local", ThreadID: sessionID, SessionID: sessionID, WorkspaceRef: "local:unindexed-saved"}
	writeRendezvous(t, runDir, entry)
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	list, err := client.ThreadList(t.Context(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("threads=%+v", list.Data)
	}
	thread := list.Data[0]
	if thread.ID != sessionID || thread.Status.Type != appwire.ThreadStatusRestartRequired || thread.Evener.Capabilities != readablePastCapabilities() {
		t.Fatalf("thread=%+v", thread)
	}
	for _, params := range []appwire.ThreadReadParams{
		{Ref: "local:" + sessionID, IncludeTurns: true},
		{Ref: entry.WorkspaceRef, IncludeTurns: true, Subscribe: true},
		{ThreadID: sessionID, IncludeTurns: true, Subscribe: true},
	} {
		read, err := client.ThreadRead(t.Context(), params)
		if err != nil {
			t.Fatalf("read visible incompatible owner (%+v): %v", params, err)
		}
		if read.Thread.ID != sessionID || read.Thread.Evener.Ref != entry.WorkspaceRef || read.Thread.Status.Type != appwire.ThreadStatusRestartRequired || read.Thread.Evener.Capabilities != readablePastCapabilities() || len(read.Thread.Turns) != 0 {
			t.Fatalf("read=%+v", read)
		}
	}
	if err := client.ThreadShutdown(t.Context(), appwire.ThreadShutdownParams{Ref: "local:" + sessionID}); !isDaemonRestartRequiredError(err) {
		t.Fatalf("shutdown error=%v", err)
	}
}

func TestHubRPCListDeduplicatesIncompatibleWorkspaceAlias(t *testing.T) {
	root := t.TempDir()
	sessionID := buildRPCParentSession(t, filepath.Join(root, "projects", "upgrade-0000000000"))
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	const instanceID = "02wMz5Txv1C3Hut0M8GCeC"
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: os.Getpid(), Protocol: "evener-appwire-v4", Endpoint: protocolMismatchPeer(t), SourceID: "local", ThreadID: instanceID, SessionID: instanceID, WorkspaceRef: "local:" + sessionID})
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	list, err := client.ThreadList(t.Context(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("threads=%+v", list.Data)
	}
	thread := list.Data[0]
	if thread.ID != instanceID || thread.Evener.Ref != "local:"+sessionID || thread.Status.Type != appwire.ThreadStatusRestartRequired {
		t.Fatalf("thread=%+v", thread)
	}
	search, err := client.ThreadList(t.Context(), appwire.ThreadListParams{SearchTerm: "second task"})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Data) != 1 || search.Data[0].ID != instanceID || search.Data[0].Name != "second task" {
		t.Fatalf("search=%+v", search.Data)
	}
}

func TestDaemonRejectionSurvivesDiscoveryFailure(t *testing.T) {
	for _, mutationID := range []string{"", "rejected-message"} {
		t.Run("mutation="+mutationID, func(t *testing.T) {
			runDir := filepath.Join(t.TempDir(), "run")
			if err := os.Mkdir(runDir, 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := hubcore.WebConfig{Roster: hubcore.NewRoster(runDir, &hubcore.StatusProber{})}
			cfg.Roster.Refresh()
			rejection := appwire.WireError{Code: appwire.CodeInvalidRequest, Message: "mutation ID already used for another payload", Data: appwire.ErrorData{ClientMutationID: mutationID, MutationOutcome: appwire.MutationOutcomeNotAccepted, RetryDisposition: appwire.RetryDispositionNone}}
			action := func() (struct{}, error) {
				if err := os.Remove(runDir); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(runDir, []byte("unreadable roster"), 0o600); err != nil {
					t.Fatal(err)
				}
				return struct{}{}, rejection
			}
			var err error
			if mutationID == "" {
				_, err = withSessionActionOwnership(t.Context(), cfg, localAppRef(webTestSessionID), "", action)
			} else {
				_, err = withDeletionTargetOwnership(t.Context(), cfg, localAppRef(webTestSessionID), "", mutationID, action)
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != rejection.Code || wire.Message != rejection.Message {
				t.Fatalf("rejection changed: %v", err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != mutationID {
				t.Fatalf("rejection data=%+v", wire.Data)
			}
		})
	}
}

func TestHubRejectsMutationForPreviousPIDIdentity(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var mutations atomic.Int32
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "before", SessionID: "before", Evener: appwire.EvenerThread{Ref: "local:before", Capabilities: appwire.ThreadCapabilities{Compact: true}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadCompactStart, func(context.Context, appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		mutations.Add(1)
		return appwire.EmptyResponse{}, nil
	})
	peer := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer peer.Close()
	runDir := t.TempDir()
	entry := rendezvous.Entry{PID: os.Getpid(), SessionID: "before", ThreadID: "before", Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(peer.URL, "http"), SourceID: "local"}
	writeRendezvous(t, runDir, entry)
	prober := &changedOwnershipProber{sessionID: "before"}
	roster := hubcore.NewRoster(runDir, prober)
	roster.Refresh()
	entry.SessionID, entry.ThreadID = "after", "after"
	writeRendezvous(t, runDir, entry)
	prober.fail = true
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster, StateDir: t.TempDir()})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	var response appwire.EmptyResponse
	err := client.Request(t.Context(), appwire.MethodThreadCompactStart, appwire.ThreadCompactStartParams{Ref: "local:before"}, &response)
	if err == nil || mutations.Load() != 0 {
		t.Fatalf("previous identity mutation error=%v, deliveries=%d", err, mutations.Load())
	}
}

func TestHubResumeRetainedChildWaitsForOwnerRelease(t *testing.T) {
	for _, delegate := range []bool{true, false} {
		t.Run(fmt.Sprint("delegate=", delegate), func(t *testing.T) {
			stateDir := t.TempDir()
			rootID := buildRPCParentSession(t, stateDir)
			var childID string
			if delegate {
				childID = buildUpgradeDelegate(t, stateDir, rootID)
			} else {
				var err error
				childID, err = agent.ForkSession(stateDir, rootID, 1, "independent fork", "")
				if err != nil {
					t.Fatal(err)
				}
			}
			runDir := t.TempDir()
			entry := rendezvous.Entry{PID: 1001, Protocol: appwire.ProtocolVersion, ThreadID: rootID, SessionID: rootID, Endpoint: "ws://unused"}
			writeRendezvous(t, runDir, entry)
			roster := hubcore.NewRoster(runDir, &changedOwnershipProber{sessionID: rootID})
			spawned := 0
			cfg := hubcore.WebConfig{StateDir: stateDir, Roster: roster, ResumeLocks: hubcore.NewResumeLocks(), Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				spawned++
				return rendezvous.Entry{}, errors.New("spawn sentinel")
			}}}
			_, err := hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: localAppRef(childID)})
			if delegate && spawned != 0 {
				t.Fatalf("retained child launched replacement: %v", err)
			}
			if !delegate && spawned != 1 {
				t.Fatalf("independent fork did not reach launcher: %v", err)
			}
			if delegate {
				wire, ok := errors.AsType[appwire.WireError](err)
				if !ok || wire.Code != appwire.CodeUnavailable || wire.Data.(appwire.ErrorData).EvenerErrorInfo != appwire.ErrorActionUnavailable {
					t.Fatalf("expected unavailable owner refusal, got %v", err)
				}
			}
			if delegate {
				_, err := hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: localAppRef(rootID), Session: childID})
				if spawned != 0 || err == nil {
					t.Fatalf("explicit child target lost ownership fence: spawned=%d err=%v", spawned, err)
				}
			}

			if err := rendezvous.Remove(runDir, entry.PID); err != nil {
				t.Fatal(err)
			}
			before := spawned
			_, err = hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: localAppRef(childID)})
			if spawned != before+1 {
				t.Fatalf("released child did not reach launcher: %v", err)
			}
		})
	}
}

func TestHubResumeWithoutRosterReturnsUnavailable(t *testing.T) {
	_, err := hubThreadResume(t.Context(), hubcore.WebConfig{}, nil, appwire.ThreadResumeParams{Session: "saved"})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("resume without roster = %v", err)
	}
}

func TestHubOwnershipUsesProjectStateLayout(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		for _, retained := range []bool{false, true} {
			t.Run(fmt.Sprintf("indexed=%v/retained=%v", indexed, retained), func(t *testing.T) {
				root := t.TempDir()
				project := filepath.Join(root, "projects", "project-owner-0000000000")
				rootID := buildRPCParentSession(t, project)
				childID := buildUpgradeDelegate(t, project, rootID)
				if !retained {
					var err error
					childID, err = agent.ForkSession(project, rootID, 1, "independent fork", "")
					if err != nil {
						t.Fatal(err)
					}
				}
				runDir := t.TempDir()
				writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: rootID, SessionID: rootID, Endpoint: protocolMismatchPeer(t)})
				spawned := 0
				cfg := hubcore.WebConfig{StateDir: root, Roster: hubcore.NewRoster(runDir, &hubcore.StatusProber{}), ResumeLocks: hubcore.NewResumeLocks(), Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
					spawned++
					return rendezvous.Entry{}, errors.New("spawn sentinel")
				}}}
				if indexed {
					cfg.Past = hubcore.NewPastIndex(filepath.Join(root, "missing", "*"))
				}
				_, err := hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: localAppRef(childID)})
				if retained && (err == nil || spawned != 0) {
					t.Fatalf("retained child resume err=%v launches=%d", err, spawned)
				}
				if !retained && spawned != 1 {
					t.Fatalf("independent fork refused: %v", err)
				}
				err = daemonRestartRequiredError(t.Context(), cfg, localAppRef(childID), "", "pending-input")
				if retained {
					wire, ok := errors.AsType[appwire.WireError](err)
					if !ok {
						t.Fatalf("mutation not blocked: %v", err)
					}
					data := wire.Data.(appwire.ErrorData)
					if data.MutationOutcome != appwire.MutationOutcomeUnknown || data.RetryDisposition != appwire.RetryDispositionBlocked {
						t.Fatalf("unsafe mutation receipt: %+v", data)
					}
				} else if err != nil {
					t.Fatalf("independent mutation refused: %v", err)
				}
				live, ownershipErr := projectSessionOwnership(t.Context(), cfg, childID)
				if ownershipErr != nil || live != retained {
					t.Fatalf("deletion ownership live=%v err=%v, retained=%v", live, ownershipErr, retained)
				}
				if err := rendezvous.Remove(runDir, 1001); err != nil {
					t.Fatal(err)
				}
				before := spawned
				_, err = hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: localAppRef(childID)})
				if spawned != before+1 {
					t.Fatalf("released child refused: %v", err)
				}
			})
		}
	}
}

func TestHubOwnershipProjectDiscoveryPreservesUncertainty(t *testing.T) {
	for _, fault := range []string{"missing", "malformed", "duplicate", "unreadable-projects"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "projects", "project-owner-0000000000")
			rootID := buildRPCParentSession(t, project)
			childID := buildUpgradeDelegate(t, project, rootID)
			path := filepath.Join(project, "sessions", childID+".meta.json")
			switch fault {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				meta, err := schema.LoadSessionMeta(project, childID)
				if err != nil {
					t.Fatal(err)
				}
				if err := schema.SaveSessionMeta(filepath.Join(root, "projects", "project-other-0000000000"), meta); err != nil {
					t.Fatal(err)
				}
			case "unreadable-projects":
				if err := os.Rename(filepath.Join(root, "projects"), filepath.Join(root, "held-projects")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "projects"), []byte("obstruction"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: rootID, SessionID: rootID, Endpoint: protocolMismatchPeer(t)})
			roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
			roster.Refresh()
			cfg := hubcore.WebConfig{StateDir: root, Roster: roster}
			_, _, err := restartRequiredDaemon(t.Context(), cfg, localAppRef(childID), "")
			if err == nil {
				t.Fatal("uncertain project ownership reported as absent")
			}
			if fault == "missing" {
				if err := rendezvous.Remove(runDir, 1001); err != nil {
					t.Fatal(err)
				}
				roster.Refresh()
				_, owned, err := restartRequiredDaemon(t.Context(), cfg, localAppRef(childID), "")
				if err != nil || owned {
					t.Fatalf("confirmed owner absence lost: owned=%v err=%v", owned, err)
				}
			}
		})
	}
}

func TestIndependentForkSurvivesDeletedParentWithUnrelatedDaemon(t *testing.T) {
	stateDir := t.TempDir()
	parentID := buildRPCParentSession(t, stateDir)
	childID, err := agent.ForkSession(stateDir, parentID, 1, "independent fork", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(stateDir, "sessions", parentID+".meta.json")); err != nil {
		t.Fatal(err)
	}
	unrelatedID := "02wMz5Txv1C3Hut0M8GCec"
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: unrelatedID, SessionID: unrelatedID, Endpoint: protocolMismatchPeer(t)})
	spawned := 0
	cfg := hubcore.WebConfig{StateDir: stateDir, Roster: hubcore.NewRoster(runDir, &hubcore.StatusProber{}), ResumeLocks: hubcore.NewResumeLocks(), Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		spawned++
		return rendezvous.Entry{}, errors.New("spawn sentinel")
	}}}
	_, err = hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: localAppRef(childID)})
	if spawned != 1 {
		t.Fatalf("fork resume blocked: %v", err)
	}
	if err := daemonRestartRequiredError(t.Context(), cfg, localAppRef(childID), "", "input"); err != nil {
		t.Fatal(err)
	}
	live, err := projectSessionOwnership(t.Context(), cfg, childID)
	if live || err != nil {
		t.Fatalf("fork deletion blocked: live=%v err=%v", live, err)
	}
}

func TestResumeChecksExplicitSessionTargetForIncompatibleOwner(t *testing.T) {
	stateDir := t.TempDir()
	ownerID := buildRPCParentSession(t, stateDir)
	forkID, err := agent.ForkSession(stateDir, ownerID, 1, "independent fork", "")
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: ownerID, SessionID: ownerID, Endpoint: protocolMismatchPeer(t)})
	spawned := 0
	cfg := hubcore.WebConfig{StateDir: stateDir, Roster: hubcore.NewRoster(runDir, &hubcore.StatusProber{}), ResumeLocks: hubcore.NewResumeLocks(), Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		spawned++
		return rendezvous.Entry{}, errors.New("spawn sentinel")
	}}}
	_, err = hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: localAppRef(forkID), Session: ownerID})
	if err == nil || spawned != 0 {
		t.Fatalf("incompatible explicit target reached launcher: spawned=%d err=%v", spawned, err)
	}
}

func TestHubClearedOwnerReleasesHistoricalDelegate(t *testing.T) {
	for _, compatible := range []bool{true, false} {
		t.Run(fmt.Sprint("compatible=", compatible), func(t *testing.T) {
			root := t.TempDir()
			stateDir := filepath.Join(root, "projects", "clear-owner-0000000000")
			oldID := buildRPCParentSession(t, stateDir)
			oldChildID := buildUpgradeDelegate(t, stateDir, oldID)
			newID, err := agent.ForkSession(stateDir, oldID, 1, "replacement root", "")
			if err != nil {
				t.Fatal(err)
			}
			newChildID := buildUpgradeDelegate(t, stateDir, newID)
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}
			runDir := t.TempDir()
			entry := rendezvous.Entry{PID: 1001, Protocol: appwire.ProtocolVersion, ThreadID: oldID, SessionID: oldID, WorkspaceRef: localAppRef(oldID), Endpoint: "ws://unused"}
			prober := &changedOwnershipProber{sessionID: oldID}
			roster := hubcore.NewRoster(runDir, prober)
			if !compatible {
				entry.Protocol = "evener-appwire-v4"
				entry.Endpoint = protocolMismatchPeer(t)
				roster = hubcore.NewRoster(runDir, &hubcore.StatusProber{})
			}
			writeRendezvous(t, runDir, entry)
			roster.Refresh()
			spawned := 0
			cfg := hubcore.WebConfig{StateDir: root, Past: past, Roster: roster, RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				spawned++
				return rendezvous.Entry{}, errors.New("spawn sentinel")
			}}}
			if live, err := projectSessionOwnership(t.Context(), cfg, oldChildID); err != nil || !live {
				t.Fatalf("old root must initially retain its child: live=%v err=%v", live, err)
			}
			oldOwner, err := llm.NewSessionAPILogger(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = oldOwner.Close() })
			if err := oldOwner.ReserveSession(oldChildID); err != nil {
				t.Fatal(err)
			}
			// Clear closes the old session tree and republishes the replacement
			// session ID while retaining its workspace route and saved journals.
			entry.SessionID, entry.ThreadID = newID, newID
			prober.sessionID = newID
			writeRendezvous(t, runDir, entry)
			roster.Refresh()
			hub, web := newHubRPCTestServerWithWeb(t, cfg)
			defer hub.Close()
			if owner, _, err := lookupDaemonOwner(t.Context(), cfg, localAppRef(oldID), "", true); err != nil || owner.SessionID != newID {
				t.Fatalf("stable workspace route lost replacement: owner=%+v err=%v", owner, err)
			}
			_, err = hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: localAppRef(newChildID)})
			if err == nil || spawned != 0 {
				t.Fatalf("current child escaped owner: spawned=%d err=%v", spawned, err)
			}
			blocked, err := web.sessionDelete(t.Context(), appwire.SessionDeleteParams{Ref: localAppRef(newChildID)})
			if err != nil || len(blocked.Deleted) != 0 || len(blocked.Skipped) != 1 {
				t.Fatalf("current child deletion = %+v err=%v", blocked, err)
			}
			// Publication precedes oldSess.Close during clear. The old child's
			// reservation must still prevent deletion in that interval.
			closing, err := web.sessionDelete(t.Context(), appwire.SessionDeleteParams{Ref: localAppRef(oldChildID)})
			if err != nil || len(closing.Deleted) != 0 || len(closing.Skipped) != 1 {
				t.Fatalf("still-reserved historical child deletion = %+v err=%v", closing, err)
			}
			if _, err := schema.LoadSessionMeta(stateDir, oldChildID); err != nil {
				t.Fatalf("still-reserved historical child metadata changed: %v", err)
			}
			if err := oldOwner.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: localAppRef(oldChildID)})
			if spawned != 1 {
				t.Errorf("released child did not reach launcher: spawned=%d err=%v", spawned, err)
			}
			deleted, err := web.sessionDelete(t.Context(), appwire.SessionDeleteParams{Ref: localAppRef(oldChildID)})
			if err != nil || len(deleted.Deleted) != 1 || deleted.Deleted[0] != oldChildID || len(deleted.Skipped) != 0 {
				t.Errorf("released child deletion = %+v err=%v", deleted, err)
			}
		})
	}
}

func TestHubColdReadFindsIncompatibleDaemonByStableThreadID(t *testing.T) {
	runDir := t.TempDir()
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	// The owner appears after hub initialization. Its current session differs
	// from the stable workspace, and no past index can supply an alias row.
	entry := rendezvous.Entry{PID: os.Getpid(), Protocol: "evener-appwire-v4", Endpoint: protocolMismatchPeer(t), SourceID: "local", ThreadID: "current", SessionID: "current", WorkspaceRef: "local:stable"}
	writeRendezvous(t, runDir, entry)
	for _, subscribe := range []bool{false, true} {
		read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{ThreadID: "stable", IncludeTurns: true, Subscribe: subscribe})
		if err != nil {
			t.Fatal(err)
		}
		if read.Thread.ID != "current" || read.Thread.Evener.Ref != "local:stable" || read.Thread.Status.Type != appwire.ThreadStatusRestartRequired || read.Thread.Evener.Capabilities != readablePastCapabilities() || len(read.Thread.Turns) != 0 {
			t.Fatalf("stable-ID read lost the incompatible owner: %+v", read.Thread)
		}
	}
}

func TestSavedReadsSurviveUnrelatedDiscoveryFailure(t *testing.T) {
	for _, incompatible := range []bool{false, true} {
		t.Run(fmt.Sprintf("incompatible=%v", incompatible), func(t *testing.T) {
			testSavedReadsSurviveUnrelatedDiscoveryFailure(t, incompatible)
		})
	}
}

func testSavedReadsSurviveUnrelatedDiscoveryFailure(t *testing.T, incompatible bool) {
	stateDir := filepath.Join(t.TempDir(), "saved-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(stateDir)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	if incompatible {
		writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v4", SessionID: sessionID, ThreadID: sessionID, Endpoint: protocolMismatchPeer(t)})
	}
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()
	if incompatible {
		if _, required, err := restartRequiredDaemon(t.Context(), hubcore.WebConfig{Roster: roster}, localAppRef(sessionID), ""); err != nil || !required {
			t.Fatalf("fixture did not establish incompatible ownership: required=%v err=%v", required, err)
		}
	}
	if err := os.WriteFile(filepath.Join(runDir, fmt.Sprintf("%d.json", os.Getpid())), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if roster.OwnershipError() == nil {
		t.Fatal("fixture did not establish incomplete discovery")
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{StateDir: stateDir, Past: past, Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	assertReadOnly := func(t *testing.T, thread appwire.Thread) {
		t.Helper()
		if incompatible && thread.Status.Type != appwire.ThreadStatusRestartRequired {
			t.Fatalf("confirmed incompatible daemon lost restart status: %+v", thread.Status)
		}
		if thread.ID != sessionID || thread.Evener.MutationStateAuthoritative || thread.Evener.Capabilities != readablePastCapabilities() {
			t.Fatalf("saved snapshot grants authority or changes identity: %+v", thread)
		}
	}
	t.Run("read", func(t *testing.T) {
		read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: localAppRef(sessionID), IncludeTurns: true, Subscribe: true})
		if err != nil {
			t.Fatal(err)
		}
		assertReadOnly(t, read.Thread)
		if len(read.Thread.Turns) != 2 {
			t.Fatalf("saved turns=%d", len(read.Thread.Turns))
		}
	})
	t.Run("list", func(t *testing.T) {
		var listed appwire.ThreadListResponse
		if err := client.Request(t.Context(), appwire.MethodThreadList, appwire.ThreadListParams{}, &listed); err != nil {
			t.Fatal(err)
		}
		if len(listed.Data) != 1 {
			t.Fatalf("saved rows=%d", len(listed.Data))
		}
		assertReadOnly(t, listed.Data[0])
	})
	t.Run("mutation remains blocked", func(t *testing.T) {
		var response any
		if err := client.Request(t.Context(), appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: localAppRef(sessionID), Name: "must not write"}, &response); err == nil {
			t.Fatal("rename bypassed discovery uncertainty")
		}
	})
}
