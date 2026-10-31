package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// The saved transcript, history index, daemon, source and shared observation
// owner are real. Only the external HTTP/WebSocket connection is held closed.
func TestManagerObservationReconnectBeyondLatestPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const id, ref = "paged-worker", "local:paged-worker"
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	appendEntry := func(entry schema.Turn) {
		t.Helper()
		entry.Format = schema.TurnFormatIdentity
		if err := writer.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	communicate := func(turn, call string, done bool) {
		t.Helper()
		reason := ""
		if done {
			reason = "done"
		}
		appendEntry(schema.Turn{TurnID: turn, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnCommunicate, CausalKnown: true, Communicate: &schema.CommunicateInfo{CallID: call, Message: call, EndTurn: done, EndReason: reason}})
		appendEntry(schema.Turn{TurnID: turn, Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: call, Name: "communicate", Content: fmt.Sprintf(`{"accepted":true,"end_turn":%t}`, done)}}}}})
	}
	communicate("A", "old-done", true)
	communicate("B", "watched-progress", false)
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	defer daemon.Close()
	identity, err := daemonserver.PrepareAppIdentity("local", id, path)
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity.WithBootGeneration("7"), nil)
	var mu sync.Mutex
	var connections []net.Conn
	var gate chan struct{}
	entered := make(chan struct{}, 1)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		wait := gate
		mu.Unlock()
		if wait != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-wait:
			case <-r.Context().Done():
				return
			}
		}
		daemon.AppServer().ServeWebSocket(w, r)
	}))
	upstream.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateHijacked {
			mu.Lock()
			connections = append(connections, conn)
			mu.Unlock()
		}
	}
	upstream.Start()
	defer upstream.Close()
	defer func() {
		mu.Lock()
		all, wait := connections, gate
		gate = nil
		mu.Unlock()
		if wait != nil {
			close(wait)
		}
		for _, conn := range all {
			_ = conn.Close()
		}
	}()
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", SessionID: id, ThreadID: id, WorkspaceRef: ref, InstanceID: id}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: id}, {Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, SourceID: "local", SessionID: "manager", InstanceID: "manager", WorkspaceRef: "local:manager"}, SessionID: "manager", Manager: true}}
	}, upstream.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := newHubAppServer(cfg, sources)
	defer server.Shutdown(ctx)
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server}
	transport, err := server.NewInProcessTransport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	human := appwire.NewClient(transport)
	human.Start(ctx)
	defer human.Close()
	if _, err := human.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := human.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	bound := &boundSessionOperations{owner: owner, local: source, binding: appwire.SessionBinding{Ref: "local:manager", InstanceID: "manager", BindingID: "binding"}}
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	updates, err := bound.Watch(watchCtx, appwire.SessionWatchRequest{Ref: ref, Completion: true})
	if err != nil {
		t.Fatal(err)
	}
	initial := <-updates
	if initial.Snapshot == nil || initial.Snapshot.Snapshot == nil {
		t.Fatalf("no initial saved snapshot: %+v", initial)
	}
	// Drain the two initial recorded communicate updates before closing transport.
	for range 2 {
		select {
		case <-updates:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	mu.Lock()
	gate = make(chan struct{})
	old := connections
	connections = nil
	mu.Unlock()
	for _, conn := range old {
		_ = conn.Close()
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("shared owner never attempted reconnect")
	}
	communicate("B", "watched-done", true)
	for i := range 95 {
		appendEntry(schema.Turn{TurnID: "C", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User(fmt.Sprintf("later-item-%d", i))})
	}
	communicate("C", "newer-done", true)
	mu.Lock()
	release := gate
	gate = nil
	mu.Unlock()
	close(release)
	found, snapshots := 0, 0
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case update, open := <-updates:
			if !open {
				t.Fatal("observation closed during recovery")
			}
			if update.Snapshot != nil {
				snapshots++
				for _, turn := range update.Snapshot.Thread.Turns {
					for _, item := range turn.Items {
						if item.Observation != nil && item.Observation.TurnID == "B" && item.Observation.TerminalAccepted != nil && *item.Observation.TerminalAccepted && item.Observation.EndTurn {
							found++
						}
					}
				}
			}
			if update.Event == "communicate" && update.Metadata.TurnID == "B" && update.Metadata.TerminalAccepted != nil && *update.Metadata.TerminalAccepted && update.Metadata.EndTurn {
				found++
			}
			if found > 1 {
				t.Fatalf("duplicate recovered B completion: %d", found)
			}
			if found == 1 {
				goto recovered
			}
		case <-deadline.C:
			t.Fatalf("watched B completion missing beyond latest page: recovered snapshots=%d, completions=%d", snapshots, found)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
recovered:
	// Cancel only the Manager. The human keeps the canonical upstream alive.
	stopWatch()
	for range updates {
	}
	if got := daemon.AppServer().SubscriberCount(ref); got != 1 {
		t.Fatalf("human upstream subscriptions=%d, want 1", got)
	}
	daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: id, Data: events.NotesUpdatedData{HumanNote: "after-pagination-and-cancel"}})
	for {
		select {
		case n := <-human.Notifications():
			if n.Method == appwire.NotifyEvenerNotesUpdated {
				var p appwire.NotesUpdatedParams
				if err := json.Unmarshal(n.Params, &p); err != nil {
					t.Fatal(err)
				}
				if p.HumanNote == "after-pagination-and-cancel" {
					t.Logf("recovered B beyond latest snapshot, human received live note after Manager cancellation")
					return
				}
			}
		case <-ctx.Done():
			t.Fatal("human lost live subscription after Manager cancellation")
		}
	}
}

// These checks call the real daemon's indexed history through its WebSocket.
// Mutations between pages exercise the public cursor and snapshot fences, not
// a substituted ThreadTurnsList implementation.
func TestManagerObservationRecoveryPageBoundaries(t *testing.T) {
	for _, change := range []string{"append", "boot", "history", "cancel"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			const id, ref = "boundary-worker", "local:boundary-worker"
			path := filepath.Join(t.TempDir(), "history.jsonl")
			writer, err := transcript.NewWriter(path, transcript.Header{SessionID: id})
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			appendItems := func(start, count int) {
				t.Helper()
				for i := start; i < start+count; i++ {
					if err := writer.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "work", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User(fmt.Sprintf("item-%d", i))}); err != nil {
						t.Fatal(err)
					}
				}
			}
			appendItems(0, 130)
			daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
			defer daemon.Close()
			identity, err := daemonserver.PrepareAppIdentity("local", id, path)
			if err != nil {
				t.Fatal(err)
			}
			daemon.ReplaceAppIdentity(identity.WithBootGeneration("7"), nil)
			upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
			defer upstream.Close()
			client := dialHubRPC(t, upstream)
			defer client.Close()
			if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			params := appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit}
			held, err := client.ThreadRead(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			appendItems(130, 95)
			params.HeldSnapshot = held.Snapshot
			fresh, err := client.ThreadRead(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			pages, items := 0, 0
			seen := map[string]bool{}
			err = recoverObservationHistory(ctx, client, ref, held, fresh, func(batch []appwire.ThreadItem) bool {
				pages++
				if len(batch) > appwire.TranscriptItemPageLimit {
					t.Fatalf("unbounded page: %d", len(batch))
				}
				for _, item := range batch {
					if seen[item.TranscriptKey] || item.Text == "item-225" {
						t.Fatalf("duplicate or post-cut item: %+v", item)
					}
					seen[item.TranscriptKey] = true
					items++
				}
				if pages == 1 {
					switch change {
					case "append":
						appendItems(225, 1)
					case "boot":
						// A same-ID ReplaceAppIdentity reuses its history. Move
						// away first, as Clear does, to detach that old owner.
						away, err := daemonserver.PrepareAppIdentity("local", "other", "")
						if err != nil {
							t.Fatal(err)
						}
						daemon.ReplaceAppIdentity(away, nil)
						daemon.ReplaceAppIdentity(identity.WithBootGeneration("8"), nil)
					case "history":
						replacement := filepath.Join(t.TempDir(), "replacement.jsonl")
						w, err := transcript.NewWriter(replacement, transcript.Header{SessionID: id})
						if err != nil {
							t.Fatal(err)
						}
						if err := w.Close(); err != nil {
							t.Fatal(err)
						}
						next, err := daemonserver.PrepareAppIdentity("local", id, replacement)
						if err != nil {
							t.Fatal(err)
						}
						away, err := daemonserver.PrepareAppIdentity("local", "other", "")
						if err != nil {
							t.Fatal(err)
						}
						daemon.ReplaceAppIdentity(away, nil)
						daemon.ReplaceAppIdentity(next.WithBootGeneration("7"), nil)
					case "cancel":
						cancel()
					}
				}
				return true
			})
			if change == "append" {
				if err != nil || pages != 3 || items != 120 {
					t.Fatalf("bounded gap recovery: pages=%d items=%d err=%v", pages, items, err)
				}
				// The held window begins at item 90. Three older pages reach it
				// without scanning the remaining older transcript to item 0.
				if !seen[held.Thread.Turns[0].Items[0].TranscriptKey] {
					t.Fatal("recovery stopped before the held window")
				}
			} else if err == nil || pages != 1 {
				t.Fatalf("changed history escaped fence: pages=%d err=%v", pages, err)
			}
		})
	}
}
