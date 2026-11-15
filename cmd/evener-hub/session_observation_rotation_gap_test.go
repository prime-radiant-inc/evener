package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

type observationRotationHarness struct {
	server *appserver.Server
	pool   *aliasRelayPool
	ref    string
	bound  *boundSessionOperations
}

func newObservationRotationHarness(t *testing.T, mutate func(appwire.ThreadReadParams, appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error)) *observationRotationHarness {
	t.Helper()
	pool := &aliasRelayPool{}
	targetSource := &aliasRelaySource{pool: pool}
	sources := appsource.NewRegistry()
	sources.Add(targetSource)
	ref := "local:target"
	cfg := testWebConfig(t, hubcore.WebConfig{})
	local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, SourceID: "local", SessionID: "manager", InstanceID: "manager", WorkspaceRef: "local:manager"}, SessionID: "manager", Manager: true}}
	}, nil)
	server := appserver.NewServer(appserver.ServerConfig{})
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	})
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		read, err := owner.Read(ctx, p)
		if err != nil {
			return read, err
		}
		read.Thread.Evener.InstanceID, read.BootGeneration = "instance", "1"
		return mutate(p, read)
	})
	return &observationRotationHarness{
		server: server,
		pool:   pool,
		ref:    ref,
		bound:  &boundSessionOperations{owner: owner, local: local, binding: appwire.SessionBinding{Ref: "local:manager", InstanceID: "manager", BindingID: "binding"}},
	}
}

func (h *observationRotationHarness) emitResync(t *testing.T) {
	t.Helper()
	h.pool.emit(t, *appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{Ref: h.ref, ThreadID: "target"}).Notification)
}

func (h *observationRotationHarness) awaitSnapshot(ctx context.Context, t *testing.T, updates <-chan appwire.SessionWatchUpdate, incarnation string) {
	t.Helper()
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				t.Fatalf("subscription closed before the %s snapshot", incarnation)
			}
			if u.Snapshot != nil && u.Snapshot.Snapshot != nil && u.Snapshot.Snapshot.Incarnation == incarnation {
				return
			}
		case <-ctx.Done():
			t.Fatalf("%s snapshot never arrived: %v", incarnation, ctx.Err())
		}
	}
}

// A rotation that lands between a watch read and its gap paging answers the
// gap read with a stale cursor. That answer is not fatal: the watch asks for
// the latest cut once and re-anchors instead of ending the subscription
// (issue #5669, path 2).
func TestManagerWatchRecoversFromStaleMidRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var reads atomic.Int32
	h := newObservationRotationHarness(t, func(p appwire.ThreadReadParams, read appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
		if !p.IncludeTurns {
			return read, nil
		}
		switch reads.Add(1) {
		case 1:
			read.Epoch = 1
			read.Snapshot = &appwire.SnapshotIdentity{Incarnation: "history-a", Length: 1}
		case 2:
			return appwire.ThreadReadResponse{}, appwire.TranscriptItemCursorStale()
		default:
			read.Epoch = 2
			read.Snapshot = &appwire.SnapshotIdentity{Incarnation: "history-b", Length: 2}
		}
		return read, nil
	})
	updates, err := h.bound.Watch(ctx, appwire.SessionWatchRequest{Ref: h.ref, Completion: true})
	if err != nil {
		t.Fatal(err)
	}
	h.awaitSnapshot(ctx, t, updates, "history-a")
	h.emitResync(t)
	h.awaitSnapshot(ctx, t, updates, "history-b")
	if got := reads.Load(); got != 3 {
		t.Fatalf("history reads = %d, want the stale read plus one fresh re-read", got)
	}
}

// On a same-target re-anchor, the rebuilt history's latest window is capped at
// one page. The watch must page that history back to the pre-rebuild held
// window, or a terminal older than the latest page strands the watch with no
// completion (issue #5669, path 3).
func TestManagerWatchReanchorPagesTheRebuiltGap(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var reads, pages atomic.Int32
	heldPosition := appwire.ThreadItemPosition{Entry: 10}
	freshPosition := appwire.ThreadItemPosition{Entry: 50}
	accepted := true
	gapItem := appwire.ThreadItem{
		ID: "gap-done", TranscriptKey: "gap-done", Position: &heldPosition,
		Observation: &appwire.SessionObservationMetadata{TurnID: "gap", EntryID: "gap-done", EndTurn: true, EndReason: "done", TerminalAccepted: &accepted, CausalKnown: true},
	}
	h := newObservationRotationHarness(t, func(p appwire.ThreadReadParams, read appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
		if !p.IncludeTurns {
			return read, nil
		}
		switch reads.Add(1) {
		case 1:
			read.Epoch = 1
			read.Snapshot = &appwire.SnapshotIdentity{Incarnation: "history-a", Length: 1}
			read.Thread.Turns = []appwire.Turn{{ID: "held", Items: []appwire.ThreadItem{{ID: "held-item", Position: &heldPosition}}}}
		default:
			read.Epoch = 2
			read.Snapshot = &appwire.SnapshotIdentity{Incarnation: "history-b", Length: 51}
			read.OlderCursor = "older-1"
			read.Thread.Turns = []appwire.Turn{{ID: "fresh", Items: []appwire.ThreadItem{{ID: "fresh-item", Position: &freshPosition}}}}
		}
		return read, nil
	})
	appserver.HandleTyped(h.server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		if params.Ref != h.ref || params.Cursor != "older-1" {
			t.Errorf("gap page request = ref %q cursor %q, want %q and older-1", params.Ref, params.Cursor, h.ref)
		}
		pages.Add(1)
		return appwire.ThreadTurnsListResponse{
			Data:           []appwire.Turn{{ID: "gap", Items: []appwire.ThreadItem{gapItem}}},
			Snapshot:       &appwire.SnapshotIdentity{Incarnation: "history-b", Length: 51},
			BootGeneration: "1",
			Epoch:          2,
		}, nil
	})
	updates, err := h.bound.Watch(ctx, appwire.SessionWatchRequest{Ref: h.ref, Completion: true})
	if err != nil {
		t.Fatal(err)
	}
	h.awaitSnapshot(ctx, t, updates, "history-a")
	h.emitResync(t)
	h.awaitSnapshot(ctx, t, updates, "history-b")
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				t.Fatal("subscription closed before the rebuilt gap evidence replayed")
			}
			if u.Event == "communicate" && u.Metadata.EntryID == "gap-done" {
				if pages.Load() != 1 {
					t.Fatalf("gap pages = %d, want one", pages.Load())
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("rebuilt gap evidence never replayed: %v", ctx.Err())
		}
	}
}
