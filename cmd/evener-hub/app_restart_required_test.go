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
	"primeradiant.com/evener/rendezvous"
)

func TestHubProtocolUpgradePreservesTranscriptAndRejectsUndeliverableMessages(t *testing.T) {
	for _, cleared := range []bool{false, true} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleared=%v/cached=%v", cleared, cached), func(t *testing.T) { testHubProtocolUpgrade(t, cleared, cached) })
		}
	}
}

func testHubProtocolUpgrade(t *testing.T, cleared, cached bool) {
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
	entry := rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: daemonSessionID, SessionID: daemonSessionID, WorkspaceRef: "local:" + sessionID, Endpoint: protocolMismatchPeer(t)}
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
						descriptor := map[string]any{"child_session_id": grandchildID, "transcript_ref": localAppRef(grandchildID), "owner_session_id": childID, "task": "nested sentinel", "agent_type": "explorer", "tool_name_ceiling": []string{"communicate"}, "resumable": true, "config": map[string]any{}}
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
						if _, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now()); err == nil {
							t.Error("navigation suppressed unresolved nested ownership")
						}
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
				if _, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now()); err == nil {
					t.Error("navigation suppressed unreadable parent ownership")
				}
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
					} else if navigationErr == nil {
						t.Error("navigation suppressed ownership read failure")
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
	for _, fault := range []string{"probe", "unidentified", "malformed", "unreadable", "missing-directory"} {
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
				if fault != "probe" && fault != "unidentified" {
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
				if fault == "probe" || fault == "unidentified" {
					web := &WebServer{cfg: hubcore.WebConfig{StateDir: stateDir, Past: past, Roster: roster}}
					if _, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now()); err == nil {
						t.Error("navigation published actions despite unresolved daemon ownership")
					}
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
