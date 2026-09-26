package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHostManageRemoveReAddDuringInFlightListSkipsLastGoodStoreWithoutCache
// pins the round-12 M1 finding: the round-11 fence compares the walk's
// read-time generation capture against the remote-thread cache, but a hub
// with no cache — the embedder shape where every tree read walks the live
// sources — has no generation store to compare against, so the store's gate
// admitted every completed list. An in-flight walk then repopulated
// lastGoodThreads after the host's removal finished, and re-adding the name
// displayed the previous host's retained sessions as the re-added host's
// until its own first successful list. The nil-cache walk now carries a
// read-time identity token of its own — the source instance it enumerated —
// and the store drops unless the source registry still maps the name to that
// instance: a removal drops the registration before it forgets the retained
// rows, and a re-add registers a fresh instance, so the late store skips
// either way. Deterministic, no sleeps: the parked list signals entry and
// blocks on release, so the remove/re-add lands inside the round-trip
// exactly.
func TestHostManageRemoveReAddDuringInFlightListSkipsLastGoodStoreWithoutCache(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	// No RemoteThreadCache — the nil-cache shape the finding names — and no
	// attached-only lookup, the walk gate's documented test mode, so the walk
	// reaches the victim's list without a live SSH channel.
	cfg, _, _ := hostManageWiringConfig(t, configPath, nil, detachRefusingRunner{})
	cfg.RemoteHostClientIfAttached = nil
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}

	// Control: a walk over the live registration retains its successful list
	// — the same store the parked walk below hits — proving the nil-cache
	// retention path is live before the remove closes it: the fence must
	// reject stale stores, not every store.
	scripted := appwire.Thread{ID: "t1", Source: "web-side", CWD: "/srv/ws", Name: "side session"}
	web.sources.Remove("web-side")
	web.sources.Add(&scriptedAppSource{id: "web-side", thread: scripted})
	control := web.refreshRemoteThreadSnapshot(context.Background())
	if len(control.threads) != 1 || control.threads[0].ID != "t1" {
		t.Fatalf("control walk threads = %+v, want the scripted row", control.threads)
	}
	if threads := web.lastGoodThreadsForSource("web-side"); len(threads) != 1 || threads[0].ID != "t1" {
		t.Fatalf("control walk retained = %+v, want the scripted row retained", threads)
	}

	// The victim's source becomes one whose list parks for the round-trip:
	// entered fires once the walk is inside ListThreads, and release lets it
	// return. The cleanup releases a failing test's park so the walk
	// goroutine never outlives the test blocked.
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	web.sources.Remove("web-side")
	web.sources.Add(&hookedRemoteSource{
		scriptedAppSource: &scriptedAppSource{id: "web-side", thread: scripted},
		onList: func() {
			close(entered)
			<-release
		},
	})

	// The background refresher's own walk, parked inside ListThreads.
	walkDone := make(chan remoteThreadFetch, 1)
	go func() {
		walkDone <- web.refreshRemoteThreadSnapshot(context.Background())
	}()
	<-entered

	// The churn completes while the list is parked: the remove's finish phase
	// drops the name's source registration and forgets the retained rows, and
	// the re-add registers the name under a fresh source — the same identity
	// rules the cached path enforces through the cache's generations.
	if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{Name: "web-side"}, nil); err != nil {
		t.Fatalf("evener/host/remove: %v", err)
	}
	if threads := web.lastGoodThreadsForSource("web-side"); len(threads) != 0 {
		t.Fatalf("lastGoodThreads after the in-flight remove = %+v, want none", threads)
	}
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add (re-add): %v", err)
	}

	// Release the parked list. The walk's read succeeded — its fetch still
	// carries the row — but the registration that owned the read is gone, so
	// the late store must skip and leave no entry for the re-added name.
	releaseOnce.Do(func() { close(release) })
	inFlight := <-walkDone
	if len(inFlight.threads) != 1 || inFlight.threads[0].ID != "t1" {
		t.Fatalf("in-flight walk threads = %+v, want the scripted row the parked list returned", inFlight.threads)
	}
	if threads := web.lastGoodThreadsForSource("web-side"); len(threads) != 0 {
		t.Fatalf("lastGoodThreads after the parked walk finished = %+v, want none: a remove/re-add that commits inside ListThreads must not be followed by the walk storing the old host's rows", threads)
	}
	web.lastGoodMu.Lock()
	_, retained := web.lastGoodThreads["web-side"]
	web.lastGoodMu.Unlock()
	if retained {
		t.Fatal("the parked walk resurrected the removed host's lastGoodThreads entry under the re-added name")
	}

	// The finding's display harm: the re-added host's own walk cannot list yet
	// (no channel), so it falls back to the retained rows — which must be
	// empty rather than the previous host's sessions rendered as the new
	// host's.
	after := web.refreshRemoteThreadSnapshot(context.Background())
	for _, thread := range after.threads {
		if thread.Source == "web-side" {
			t.Fatalf("the re-added host's walk rendered the previous host's session %q as its own", thread.ID)
		}
	}
}

// TestHostManageRowAfterRemoveReAddDoesNotRecordStaleFacts pins the round-12
// M2 finding: hostRow builds its rows outside the mutation mutex (round-7 M4,
// so a parked facts read holds up no commit), which left its trailing
// recordKnown unfenced — a row whose facts read parked on the network could
// finish after its host was removed and re-added, and the record it then
// wrote recreated the removed host's name-keyed attach state with the old
// host's facts, which the re-added host's offline rows rendered as their
// own. The row now carries the registry entry generation it was built from
// and folds and records retained state only while that generation still owns
// the name, so the delayed row leaves the re-added name's record exactly as
// clean as the re-add's own commit left it. Deterministic, no sleeps: the
// facts seam parks until the remove/re-add has fully committed.
func TestHostManageRowAfterRemoveReAddDoesNotRecordStaleFacts(t *testing.T) {
	live := &appwire.Client{}
	// channelLive is the online signal and the attached-only lookup's answer:
	// the first add renders offline (no lookups run), the stale status row
	// finds a channel, and the final status row renders offline again so any
	// retained facts would have to come from the record.
	var channelLive atomic.Bool
	// serveOldHostFacts stands for "the channel still belongs to the host that
	// was first added": the stale row's parked read keeps serving the old
	// host's facts, while every lookup after the remove — the re-add's own
	// response row included — must see none.
	var serveOldHostFacts atomic.Bool
	channelLive.Store(false)
	serveOldHostFacts.Store(true)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	hosts, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostOnline: func(string) bool { return channelLive.Load() },
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) {
			if channelLive.Load() {
				return live, true
			}
			return nil, false
		},
		RemoteHostHandshake: func(_ string, _ *appwire.Client) (appwire.InitializeResponse, bool) {
			if !serveOldHostFacts.Load() {
				return appwire.InitializeResponse{}, false
			}
			return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "old-hub", Version: "0.1"}}, true
		},
		RemoteHostFacts: func(_ context.Context, _ string, _ *appwire.Client) (appsource.HostFacts, error) {
			if !serveOldHostFacts.Load() {
				return appsource.HostFacts{}, errors.New("no facts")
			}
			close(entered)
			<-release
			return appsource.HostFacts{HubVersion: "8.8.8", OS: "linux", Arch: "amd64"}, nil
		},
	}, "", hosts, nil)

	// The victim enters as a sidecar host (so Remove accepts it), rendered
	// offline so no facts read runs yet.
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "old.example"}}); err != nil {
		t.Fatalf("Add = %v", err)
	}

	// The stale row: a status read whose facts read parks mid-row, holding the
	// registration it captured.
	channelLive.Store(true)
	statusDone := make(chan appwire.HostStatusResponse, 1)
	go func() {
		resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
		if err != nil {
			t.Errorf("stale Status = %v", err)
			return
		}
		statusDone <- resp
	}()
	<-entered

	// The churn completes while the row is parked: the remove's finish phase
	// drops the attach record with every other piece of the name's state, and
	// the re-add's commit starts the name clean under a fresh generation.
	serveOldHostFacts.Store(false)
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"}); err != nil {
		t.Fatalf("Remove = %v", err)
	}
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"}}); err != nil {
		t.Fatalf("re-Add = %v", err)
	}

	// Release the parked facts read. The row finishes under a registration the
	// churn already replaced, so it must fold and record nothing.
	releaseOnce.Do(func() { close(release) })
	stale := <-statusDone
	if !stale.Host.Attached || stale.Host.ServerName != "old-hub" || stale.Host.HubVersion != "8.8.8" {
		t.Fatalf("stale row = %+v, want the point-in-time view its live lookups built", stale.Host)
	}

	// The re-added host's offline row must render with no last-known facts: the
	// delayed row's record was the only writer that could have recreated them.
	channelLive.Store(false)
	fresh, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("fresh Status = %v", err)
	}
	if fresh.Host.Attached {
		t.Fatalf("fresh row = %+v, want offline", fresh.Host)
	}
	if fresh.Host.ServerName != "" || fresh.Host.ServerVersion != "" || fresh.Host.HubVersion != "" || fresh.Host.OS != "" || fresh.Host.Arch != "" {
		t.Fatalf("fresh row = %+v, want no last-known facts: the re-added host must not inherit the removed host's recorded facts", fresh.Host)
	}
	// State level: a record may exist for the live re-added host — its own
	// attached response row creates one, empty, exactly as it would without
	// the churn — but it must carry none of the removed host's facts.
	m.cfg.state.mu.Lock()
	rec := m.cfg.state.records["side"]
	m.cfg.state.mu.Unlock()
	if rec != nil && (rec.known.ServerName != "" || rec.known.ServerVersion != "" || rec.known.HubVersion != "" || rec.known.OS != "" || rec.known.Arch != "") {
		t.Fatalf("the re-added name's attach record = %+v, want no removed-host facts", rec.known)
	}
}
