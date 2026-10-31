package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestManagerWatchSharedRemoteSubscribersAndAliases(t *testing.T) {
	testManagerWatchSharedRemoteSubscribersAndAliases(t, nil)
}

func TestManagerWatchRejectsMalformedSharedEvidence(t *testing.T) {
	for _, event := range []string{"approval", "question", "history"} {
		for _, target := range []string{"wrong-ref", "null-ref", "untargeted"} {
			t.Run(event+"/"+target, func(t *testing.T) {
				params := map[string]any{"invalidEvidence": true}
				if target != "untargeted" {
					params["threadId"] = "child-thread"
					params["ref"] = nil
					if target == "wrong-ref" {
						params["ref"] = 1
					}
				}
				method := appwire.NotifyEvenerSandboxEscalationRequested
				switch event {
				case "approval":
					params["requestId"] = "child-request"
				case "question":
					method = appwire.NotifyThreadStatusChanged
					params["askPending"] = true
				case "history":
					method = appwire.NotifyHistoryUpdated
					accepted := true
					params["items"] = []appwire.ThreadItem{{ID: "invalid-evidence", Observation: &appwire.SessionObservationMetadata{TurnID: "turn", CausalKnown: true, EndTurn: true, EndReason: "done", TerminalAccepted: &accepted}}}
				}
				raw, err := json.Marshal(params)
				if err != nil {
					t.Fatal(err)
				}
				testManagerWatchSharedRemoteSubscribersAndAliases(t, &appwire.Notification{Method: method, Params: raw})
			})
		}
	}
}

func testManagerWatchSharedRemoteSubscribersAndAliases(t *testing.T, malformed *appwire.Notification) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool := &aliasRelayPool{}
	source := &aliasRelaySource{pool: pool, canonicalRefs: map[string]appwire.Ref{
		"root-thread":  {SourceID: "local", ThreadID: "root-thread"},
		"child-thread": {SourceID: "local", ThreadID: "root-thread"},
	}}
	remoteSources := appsource.NewRegistry()
	remoteSources.Add(source)
	remoteServer := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}, remoteSources)
	defer remoteServer.Shutdown(ctx)
	remoteHTTP := httptest.NewServer(http.HandlerFunc(remoteServer.ServeWebSocket))
	defer remoteHTTP.Close()
	remoteClient := dialHubRPC(t, remoteHTTP)
	defer remoteClient.Close()
	if _, err := remoteClient.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	remote := appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return remoteClient, nil })
	local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		var entries []appsource.LocalDaemonEntry
		for _, id := range []string{"manager-one", "manager-two"} {
			entries = append(entries, appsource.LocalDaemonEntry{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, SourceID: "local", SessionID: id, InstanceID: id, WorkspaceRef: "local:" + id}, SessionID: id, Manager: true})
		}
		return entries
	}, nil)
	sources := appsource.NewRegistry()
	sources.Add(local)
	sources.Add(remote)
	prefix := "remote:"
	if malformed != nil {
		// Exercise raw malformed fanout at the shared relay, before a remote
		// source's wire decoder can drop or rewrite the invalid identity.
		sources.Add(source)
		prefix = "local:"
	}
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := newHubAppServer(cfg, sources)
	defer server.Shutdown(ctx)
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server}
	binding := func(id string) *boundSessionOperations {
		return &boundSessionOperations{owner: owner, local: local, binding: appwire.SessionBinding{Ref: "local:" + id, InstanceID: id, BindingID: id}}
	}
	hub := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer hub.Close()
	human := dialHubRPC(t, hub)
	defer human.Close()
	if _, err := human.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := human.ThreadRead(ctx, appwire.ThreadReadParams{Ref: prefix + "root-thread", Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	watch := func(c context.Context, manager, target string) <-chan appwire.SessionWatchUpdate {
		t.Helper()
		updates, err := binding(manager).Watch(c, appwire.SessionWatchRequest{Ref: prefix + target, Events: []string{"question", "approval", "communicate"}})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case first := <-updates:
			if first.Snapshot == nil {
				t.Fatalf("initial=%+v", first)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return updates
	}
	firstCtx, clearFirst := context.WithCancel(ctx)
	defer clearFirst()
	first := watch(firstCtx, "manager-one", "root-thread")
	second := watch(ctx, "manager-two", "root-thread")
	child := watch(ctx, "manager-two", "child-thread")
	if got := server.SubscriberCount(prefix + "root-thread"); got != 3 {
		t.Fatalf("root subscribers=%d, want human and two Managers", got)
	}
	if got := source.acquireCallCount(); got != 1 {
		t.Fatalf("canonical upstream acquisitions=%d", got)
	}
	entrySuffix := ""
	alias := "ref"
	emit := func(target, event string) {
		var method string
		var params any
		ref := "local:" + target
		switch event {
		case "question":
			pending := true
			method, params = appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{Ref: ref, ThreadID: target, AskPending: &pending}
		case "approval":
			method, params = appwire.NotifyEvenerSandboxEscalationRequested, map[string]any{"ref": ref, "threadId": target, "requestId": target}
		case "communicate":
			accepted := true
			method, params = appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{Ref: ref, ThreadID: target, Items: []appwire.ThreadItem{{ID: target + entrySuffix, Observation: &appwire.SessionObservationMetadata{EntryID: target + entrySuffix, TurnID: "turn", CausalKnown: true, EndTurn: true, EndReason: "done", TerminalAccepted: &accepted}}}}
		}
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		switch alias {
		case "threadId":
			delete(fields, "ref")
		case "ref-precedence":
			fields["threadId"] = "child-thread"
			if target == "child-thread" {
				fields["threadId"] = "root-thread"
			}
		}
		raw, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		pool.emit(t, appwire.Notification{Method: method, Params: raw})
	}
	next := func(ch <-chan appwire.SessionWatchUpdate, event, ref string) {
		t.Helper()
		for {
			select {
			case u, ok := <-ch:
				if !ok || u.Metadata.Ref != prefix+ref {
					t.Fatalf("event=%+v open=%t, want %s for %s", u, ok, event, ref)
				}
				if u.Snapshot != nil || u.Notification != nil && u.Notification.Method == appwire.NotifyEvenerThreadResync {
					continue
				}
				invalid := u.Metadata.EntryID == "invalid-evidence"
				if u.Notification != nil {
					var fields map[string]any
					if err := json.Unmarshal(u.Notification.Params, &fields); err != nil {
						t.Fatal(err)
					}
					invalid = invalid || fields["invalidEvidence"] == true
					if !invalid {
						if target, present := fields["ref"]; present {
							if target != prefix+ref {
								t.Fatalf("notification target=%v, want %s", target, prefix+ref)
							}
						} else if fields["threadId"] != ref {
							t.Fatalf("notification threadId=%v, want %s", fields["threadId"], ref)
						}
					}
				}
				if invalid {
					t.Fatalf("malformed frame acquired watched target identity: %+v", u)
				}
				if u.Event == "communicate" && u.Metadata.EntryID != ref+entrySuffix {
					t.Fatalf("recorded entry=%s, want %s", u.Metadata.EntryID, ref+entrySuffix)
				}
				if u.Event != event {
					t.Fatalf("event=%+v, want %s", u, event)
				}
				return
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	if malformed != nil {
		pool.emit(t, *malformed)
	}
	for _, alias = range []string{"ref", "threadId", "ref-precedence"} {
		entrySuffix = "-" + alias
		for _, event := range []string{"question", "approval", "communicate"} {
			emit("child-thread", event)
			emit("root-thread", event)
			next(child, event, "child-thread")
			next(first, event, "root-thread")
			next(second, event, "root-thread")
		}
	}
	clearFirst()
	select {
	case _, open := <-first:
		if open {
			t.Fatal("cleared watch stayed open")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if got := server.SubscriberCount(prefix + "root-thread"); got != 2 {
		t.Fatalf("after clear subscribers=%d, want human plus second Manager", got)
	}
	entrySuffix = "-after-clear"
	emit("root-thread", "communicate")
	next(second, "communicate", "root-thread")
	// The human's original connection remains subscribed throughout attachment
	// and clear. Require a newly emitted event, not an earlier buffered one.
	humanSawMalformed := false
	for {
		select {
		case n := <-human.Notifications():
			var fields map[string]any
			if err := json.Unmarshal(n.Params, &fields); err != nil {
				t.Fatal(err)
			}
			if fields["invalidEvidence"] == true {
				humanSawMalformed = true
				continue
			}
			if n.Method == appwire.NotifyHistoryUpdated {
				var history appwire.HistoryUpdatedParams
				if err := json.Unmarshal(n.Params, &history); err != nil {
					t.Fatal(err)
				}
				for _, item := range history.Items {
					if item.ID == "root-thread-after-clear" {
						if malformed != nil && !humanSawMalformed {
							t.Fatal("human relay lost unchanged malformed fanout")
						}
						return
					}
				}
			}
		case <-ctx.Done():
			t.Fatal("human lost its shared subscription")
		}
	}
}
