package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// These tests cross the actual daemon websocket, canonical relay and bound
// Manager operation. Channel deliveries and closure order the assertions.
func TestManagerObservationRecordedCompletionCut(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const ref, instance = "local:manager-workspace", "manager-instance"
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	defer daemon.Close()
	identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity.WithBootGeneration("7@manager"), nil)
	upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	defer upstream.Close()
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: instance, SessionID: instance, WorkspaceRef: ref, InstanceID: instance, HubToken: "test-token"}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: instance, Manager: true}}
	}, upstream.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	store := hubcore.NewSessionPreferencesStore(filepath.Join(t.TempDir(), "index.db"))
	hubID, err := store.HubID()
	if err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), SessionPreferences: store}
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelayFunctions(server, cfg, sources), server: server}
	binding := &boundSessionOperations{owner: owner, local: source, binding: appwire.SessionBinding{Ref: ref, InstanceID: instance, HubID: hubID, BindingID: "binding"}}
	for _, invalid := range []string{"unknown", "assistant.tool"} {
		if _, err := binding.Watch(ctx, appwire.SessionWatchRequest{Ref: ref, Events: []string{invalid}}); err == nil {
			t.Fatalf("unsupported selector %q accepted", invalid)
		}
	}
	updates, err := binding.Watch(ctx, appwire.SessionWatchRequest{Ref: ref, Completion: true})
	if err != nil {
		t.Fatalf("completion watch through real connection: %v", err)
	}
	var initial appwire.SessionWatchUpdate
	select {
	case got, ok := <-updates:
		initial = got
		if !ok || initial.Snapshot == nil || initial.Metadata.InstanceID != instance {
			t.Fatalf("initial snapshot=%+v, open=%t", initial, ok)
		}
	case <-ctx.Done():
		t.Fatal("initial observation blocked")
	}
	if n := daemon.AppServer().SubscriberCount(ref); n != 1 {
		t.Fatalf("canonical upstream subscriptions=%d, want 1", n)
	}
	falseValue, trueValue := false, true
	for _, observation := range []struct {
		id       string
		accepted *bool
		reason   string
	}{
		{"rejected-done", &falseValue, "done"},
		{"accepted-wait", &trueValue, "wait"},
		{"accepted-done", &trueValue, "done"},
	} {
		daemon.AppServer().Broadcast(ref, appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
			Ref: ref, ThreadID: instance, Items: []appwire.ThreadItem{{ID: observation.id, Type: "agentMessage", Observation: &appwire.SessionObservationMetadata{EntryID: observation.id, EndTurn: true, EndReason: observation.reason, TerminalAccepted: observation.accepted, CausalKnown: true}}},
		})
		select {
		case got, open := <-updates:
			if !open || got.Event != "communicate" || got.Metadata.EntryID != observation.id || got.Metadata.TerminalAccepted == nil || *got.Metadata.TerminalAccepted != *observation.accepted || got.Metadata.EndReason != observation.reason || got.Metadata.GenerationID != initial.Metadata.GenerationID {
				t.Fatalf("recorded observation lost verdict %+v: %+v, open=%t", observation, got, open)
			}
		case <-ctx.Done():
			t.Fatal("recorded observation not delivered across relay")
		}
	}
}

func TestManagerWatchRebindDisableAndHubRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const ref, instance = "local:manager-workspace", "manager-instance"
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	defer daemon.Close()
	identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity.WithBootGeneration("7@manager"), nil)
	upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	defer upstream.Close()
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: instance, SessionID: instance, WorkspaceRef: ref, InstanceID: instance, HubToken: "test-token"}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: instance, Manager: true}}
	}, upstream.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	store := hubcore.NewSessionPreferencesStore(filepath.Join(t.TempDir(), "index.db"))
	hubID, err := store.HubID()
	if err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), SessionPreferences: store}
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelayFunctions(server, cfg, sources), server: server}
	binding := &boundSessionOperations{owner: owner, local: source, binding: appwire.SessionBinding{Ref: ref, InstanceID: instance, HubID: hubID, BindingID: "binding"}}
	readFirst := func(updates <-chan appwire.SessionWatchUpdate) appwire.SessionWatchUpdate {
		t.Helper()
		select {
		case update, open := <-updates:
			if !open || update.Snapshot == nil || update.Metadata.Ref != ref {
				t.Fatalf("watch initial cut: update=%+v open=%t", update, open)
			}
			return update
		case <-ctx.Done():
			t.Fatal("initial cut never arrived")
			return appwire.SessionWatchUpdate{}
		}
	}
	awaitClosed := func(updates <-chan appwire.SessionWatchUpdate) {
		t.Helper()
		select {
		case update, open := <-updates:
			if open {
				t.Fatalf("revoked observation delivered %+v", update)
			}
		case <-ctx.Done():
			t.Fatal("observation not closed")
		}
	}
	request := appwire.SessionWatchRequest{Ref: ref, Events: []string{"status"}}
	updates, err := binding.Watch(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	first := readFirst(updates)
	// Resync cancels active observation consumers; a Manager's start-time
	// property never changes, so the rebind needs no re-enable.
	owner.resyncThread(first.Snapshot.Thread, ref)
	awaitClosed(updates)
	rebound, err := binding.Watch(ctx, request)
	if err != nil {
		t.Fatalf("rebound observation: %v", err)
	}
	second := readFirst(rebound)
	if first.Metadata.InstanceID != second.Metadata.InstanceID || first.Metadata.GenerationID != second.Metadata.GenerationID {
		t.Fatalf("same-generation rebound switched target: first=%+v second=%+v", first.Metadata, second.Metadata)
	}
	if n := daemon.AppServer().SubscriberCount(ref); n != 1 {
		t.Fatalf("rebound opened %d upstream subscriptions, want 1", n)
	}
	// A new hub owner can use the same durable setting after the old owner's
	// observers close; it never borrows the old owner's private subscription.
	owner.resyncThread(second.Snapshot.Thread, ref)
	awaitClosed(rebound)
	restarted := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelayFunctions(server, cfg, sources), server: server}
	restartedBinding := &boundSessionOperations{owner: restarted, local: source, binding: binding.binding}
	restored, err := restartedBinding.Watch(ctx, request)
	if err != nil {
		t.Fatalf("hub restart observation: %v", err)
	}
	third := readFirst(restored)
	if third.Metadata.InstanceID != first.Metadata.InstanceID {
		t.Fatalf("hub restart switched target: %+v", third.Metadata)
	}
	restarted.resyncThread(third.Snapshot.Thread, ref)
	awaitClosed(restored)
}
