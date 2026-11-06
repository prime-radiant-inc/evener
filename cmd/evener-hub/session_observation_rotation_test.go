package hub

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// A same-target history rotation (an index rebuild behind a resync: new epoch
// and incarnation, same ref, instance and boot generation) is a transient
// rebuild, which the watch contract says must recover without re-registration.
// The subscription must re-anchor on the fresh authoritative cut and keep
// delivering recorded evidence; ending it here silently stopped completion
// delivery on live lanes after every history recovery under load.
func TestManagerWatchSurvivesSameTargetHistoryRotation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool := &aliasRelayPool{}
	targetSource := &aliasRelaySource{pool: pool}
	sources := appsource.NewRegistry()
	sources.Add(targetSource)
	ref := "local:target"
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, SourceID: "local", SessionID: "manager", InstanceID: "manager", WorkspaceRef: "local:manager"}, SessionID: "manager", Manager: true}}
	}, nil)
	server := appserver.NewServer(appserver.ServerConfig{})
	defer server.Shutdown(ctx)
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}
	var reads atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		read, err := owner.Read(ctx, p)
		if err != nil {
			return read, err
		}
		read.Thread.Evener.InstanceID, read.BootGeneration = "instance", "1"
		if p.IncludeTurns {
			switch reads.Add(1) {
			case 1:
				// The watch's initial subscribe: an unrotated history.
				read.Epoch = 1
				read.Snapshot = &appwire.SnapshotIdentity{Incarnation: "history-a", Length: 1}
			default:
				// Every later read serves the rebuilt history: a new epoch and
				// incarnation for the same target.
				read.Epoch = 2
				read.Snapshot = &appwire.SnapshotIdentity{Incarnation: "history-b", Length: 2}
			}
		}
		return read, nil
	})
	bound := &boundSessionOperations{owner: owner, local: local, binding: appwire.SessionBinding{Ref: "local:manager", InstanceID: "manager", BindingID: "binding"}}
	updates, err := bound.Watch(ctx, appwire.SessionWatchRequest{Ref: ref, Completion: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-updates:
		if u.Snapshot == nil || u.Snapshot.Snapshot == nil || u.Snapshot.Snapshot.Incarnation != "history-a" {
			t.Fatalf("missing unrotated first cut: %+v", u)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	emitResync := func() {
		raw, err := json.Marshal(appwire.ThreadResyncParams{Ref: ref, ThreadID: "target"})
		if err != nil {
			t.Fatal(err)
		}
		pool.emit(t, appwire.Notification{Method: appwire.NotifyEvenerThreadResync, Params: raw})
	}
	emitResync()
	// The re-read after the resync serves the rotated history. The runtime must
	// re-anchor the subscription on that cut rather than ending the watch: the
	// updates channel must stay open and deliver the authoritative snapshot.
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				t.Fatal("subscription died on a same-target history rotation; recorded evidence after the rebuild can no longer deliver")
			}
			if u.Snapshot != nil && u.Snapshot.Snapshot != nil && u.Snapshot.Snapshot.Incarnation == "history-b" {
				if u.Snapshot.Epoch != 2 {
					t.Fatalf("re-anchor cut kept the pre-rotation epoch: %+v", u.Snapshot)
				}
				goto reanchored
			}
		case <-ctx.Done():
			t.Fatalf("re-anchor cut never arrived: %v", ctx.Err())
		}
	}
reanchored:
	// Recorded evidence that lands after the rotation must still deliver on
	// the same subscription, without any re-registration.
	accepted := true
	item := appwire.ThreadItem{ID: "rotated-done", TranscriptKey: "rotated-done", Observation: &appwire.SessionObservationMetadata{
		TurnID: "watched-turn", EntryID: "rotated-done", EndTurn: true, EndReason: "done", TerminalAccepted: &accepted, CausalKnown: true,
	}}
	raw, err := json.Marshal(appwire.HistoryUpdatedParams{Ref: ref, ThreadID: "target", BootGeneration: "1", Epoch: 2, Snapshot: appwire.SnapshotIdentity{Incarnation: "history-b", Length: 3}, Items: []appwire.ThreadItem{item}})
	if err != nil {
		t.Fatal(err)
	}
	pool.emit(t, appwire.Notification{Method: appwire.NotifyHistoryUpdated, Params: raw})
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				t.Fatal("subscription died before rotated terminal evidence delivered")
			}
			if u.Event == "communicate" && u.Metadata.EntryID == "rotated-done" {
				if u.Metadata.TerminalAccepted == nil || !*u.Metadata.TerminalAccepted {
					t.Fatalf("rotated terminal evidence lost its acceptance verdict: %+v", u.Metadata)
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("recorded evidence after the rotation never delivered: %v", ctx.Err())
		}
	}
}
