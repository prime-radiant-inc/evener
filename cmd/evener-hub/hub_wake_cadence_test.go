package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// stateWatchingSource is a scripted host that reports every Online() read, so a test
// can wait for a notices derive to have happened instead of sleeping. A notices
// derive asks each registered source for this once (hostNotices).
type stateWatchingSource struct {
	*offlineStubSource

	mu     sync.Mutex
	online bool
	reads  chan struct{}
}

func (s *stateWatchingSource) Online() bool {
	s.mu.Lock()
	online := s.online
	s.mu.Unlock()
	select {
	case s.reads <- struct{}{}:
	default:
	}
	return online
}

func (s *stateWatchingSource) setOnline(online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.online = online
}

// waitRead blocks until a notices derive has read this source's online state.
func (s *stateWatchingSource) waitRead(t *testing.T, when string) {
	t.Helper()
	select {
	case <-s.reads:
	case <-time.After(10 * time.Second):
		t.Fatalf("the notices derive never ran (%s)", when)
	}
}

func newStateWatchingSource(id string, online bool) *stateWatchingSource {
	return &stateWatchingSource{
		offlineStubSource: &offlineStubSource{scriptedAppSource: &scriptedAppSource{id: id}},
		online:            online,
		reads:             make(chan struct{}, 64),
	}
}

// remoteWakeThread is a session on another host with a pending approval. The
// roster alone cannot carry it (#2529), so its row in the Manager roster and
// its needs_you attention event are both proof that the wake derived from the
// remote-inclusive snapshot.
func remoteWakeThread() appwire.Thread {
	return appwire.Thread{
		ID:        "wake-remote",
		SessionID: "wake-remote",
		Source:    "wake-live-host",
		Name:      "Remote wake session",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{
			Ref:                "wake-live-host:wake-remote",
			PendingEscalations: []appwire.SandboxEscalationRequested{{}},
		},
	}
}

// TestHubSnapshotWakePublishesAllThreeSnapshots pins #6422's contract: the
// hub's three periodic snapshots — the Manager roster, the attention event, and
// the notices list — all move on ONE production wake.
//
// The watchers start through startHubSnapshotWatchers, their production entry
// point, with hubTicker replaced so the ticker's channel is this test's. One
// send on that channel is one production tick. Against the pre-#6422 wiring —
// attention and notices each starting a watcher of their own, each taking its
// own ticker over the same replaced channel — a single send is received by
// exactly one of the two, so at least one snapshot always stays where it was
// and this test fails. Which assertion fires varies; that it fails does not.
//
// Every wait is on a channel the code under test writes. The fixed timeouts
// only bound a failure.
func TestHubSnapshotWakePublishesAllThreeSnapshots(t *testing.T) {
	root := t.TempDir()
	// One host stays online and owns the remote session whose approval the
	// attention half must see; it is the host whose derive this test waits on.
	liveHost := newStateWatchingSource("wake-live-host", true)
	// Another host is up at the baseline and goes offline below: a host that
	// started offline would already be in the announced notices, so its notice
	// could not prove the tick re-derived anything.
	offlineHost := newStateWatchingSource("wake-offline-host", true)

	cache := &hubcore.RemoteThreadCache{}
	archive := closeOnCleanup(t, hubcore.NewArchiveStore(filepath.Join(root, "archive.db")))
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Past:              closeOnCleanup(t, hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))),
		Roster:            hubcore.NewRosterWithEntries(),
		Archive:           archive,
		RemoteThreadCache: cache,
	})
	defer hub.Close()
	// Both hosts are registered on the one source registry the notices and the
	// navigation tree read, the way newHubSourceRegistry wires production hosts.
	web.sources.Add(liveHost)
	web.sources.Add(offlineHost)
	client := dialHubRPCInitialized(t, hub)

	sub, err := web.navigation.roster.Subscribe(appwire.ManagerView{Ref: "local:manager", Manager: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()

	ticks := make(chan time.Time)
	oldTicker := hubTicker
	hubTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	ctx, cancel := context.WithCancel(context.Background())
	// Unbuffered: a send is accepted only by a watcher already selecting, so a
	// returning send proves the attention watcher's initial seed run has
	// finished. The seed is silent by design (#spec v5), and the changes below
	// must land after it or the tick would find nothing to announce.
	poke := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		startHubSnapshotWatchers(func(fn func()) { go fn() }, ctx, poke, archive, web)
	}()
	t.Cleanup(func() {
		hubTicker = oldTicker
		cancel()
		<-stopped
	})

	// Both baselines, waited on rather than slept on:
	// the notices watcher reads its announced set when it starts,
	liveHost.waitRead(t, "baseline")
	// and the attention watcher seeds before it selects; the poke that returns
	// proves it has.
	select {
	case poke <- struct{}{}:
	case <-time.After(10 * time.Second):
		t.Fatal("the attention watcher never reached its loop")
	}

	// Three changes, none of them a write through this hub:
	// a remote session with a pending approval appears,
	cache.StoreSnapshot([]appwire.Thread{remoteWakeThread()}, true)
	// and the other host is reported offline.
	offlineHost.setOnline(false)

	// One wake.
	select {
	case ticks <- time.Time{}:
	case <-time.After(10 * time.Second):
		t.Fatal("no watcher accepted a tick")
	}

	// One loop reads both streams, so a broadcast that arrives first cannot be
	// consumed by the assertion that is not looking for it.
	var rosterMoved, attentionMoved, noticesMoved bool
	deadline := time.After(10 * time.Second)
	for !(rosterMoved && attentionMoved && noticesMoved) {
		select {
		case update := <-sub.Updates:
			for _, event := range update.Events {
				if event.Ref == "wake-live-host:wake-remote" {
					rosterMoved = true
				}
			}
		case notif := <-client.Notifications():
			switch notif.Method {
			case appwire.NotifyEvenerAttentionChanged:
				var payload appwire.AttentionChangedPayload
				if err := json.Unmarshal(notif.Params, &payload); err != nil {
					t.Fatalf("decode %s: %v", notif.Method, err)
				}
				for _, changed := range payload.Changed {
					// A remote row's session ID is its ref text (source:thread),
					// which is what the derivation keys the entry by.
					if changed.ID == "wake-live-host:wake-remote" && changed.Level == "needs_you" && changed.ApprovalPending {
						attentionMoved = true
					}
				}
			case appwire.NotifyEvenerNoticesChanged:
				var payload appwire.NoticesListResponse
				if err := json.Unmarshal(notif.Params, &payload); err != nil {
					t.Fatalf("decode %s: %v", notif.Method, err)
				}
				for _, notice := range payload.Notices {
					if notice.Kind == appwire.NoticeKindHostOffline && notice.Subject == "wake-offline-host" {
						noticesMoved = true
					}
				}
			}
		case <-deadline:
			t.Fatalf("one wake published roster=%t attention=%t notices=%t, want all three", rosterMoved, attentionMoved, noticesMoved)
		}
	}
}
