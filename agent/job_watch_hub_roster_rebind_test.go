package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
)

// supersededContext reports cancellation without making Done ready, so the
// closed replacement subscription below is the only ready select case. A real
// cancellation readies both, and select may land on either; this pins the
// branch under test.
type supersededContext struct {
	context.Context
	canceled atomic.Bool
}

func (c *supersededContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return c.Context.Err()
}

// rosterSupersededPeer is the transport a re-registration has already
// canceled: its rebind Watch reports the cancellation and returns a
// subscription that closed without an initial snapshot, as watchRoster leaves
// behind when its context ends.
type rosterSupersededPeer struct {
	*managerSetupCancelClient
	life *supersededContext
}

func (p *rosterSupersededPeer) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	p.life.canceled.Store(true)
	closed := make(chan appwire.SessionWatchUpdate)
	close(closed)
	return closed, nil
}

// rosterLateBindPeer is the replacement transport a late-starting consumer
// rebinds against. Its subscription carries the broker's fresh snapshot.
type rosterLateBindPeer struct {
	*managerSetupCancelClient
	updates chan appwire.SessionWatchUpdate
}

func (p *rosterLateBindPeer) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return p.updates, nil
}

// rosterBlockingStatePeer holds the consumer's binding check open so the
// re-registration handoff can cancel the consumer while that read is in
// flight.
type rosterBlockingStatePeer struct {
	*managerSetupCancelClient
	entered chan struct{}
	release chan struct{}
}

func (p *rosterBlockingStatePeer) State(context.Context) (appwire.ManagerView, error) {
	close(p.entered)
	<-p.release
	return p.view, nil
}

// rosterFrameNotification drains exactly one model-visible roster frame.
func rosterFrameNotification(t *testing.T, root *Session) deliverableJobNotification {
	t.Helper()
	var found []deliverableJobNotification
	delivered, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
	for _, n := range delivered {
		if rosterFrameField(n.notification.watchSendFrame, "roster_events: ") != "" {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		t.Fatalf("roster frames=%d, want one", len(found))
	}
	return found[0]
}

// rosterDeliveryFixture registers a top-level roster watch that already
// reported one worker, and leaves exactly one frame pending for the caller to
// settle before the reconnect.
func rosterDeliveryFixture(t *testing.T) (*Session, *jobManager, string, appwire.RosterEvent, appwire.RosterEvent) {
	t.Helper()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	jm.enqueue = root.enqueueJobNotification
	result, err := jm.configureHubRosterWatch(t.Context(), "local:manager", "")
	if err != nil {
		t.Fatal(err)
	}
	a := rosterEntry("local:a", "idle")
	b := rosterEntry("local:b", "idle")
	if err := jm.onHubRoster(result.WatchID, rosterUpdate(1, true, a)); err != nil {
		t.Fatal(err)
	}
	initial := rosterFrameNotification(t, root)
	if err := jm.settleWatchSendDelivered(initial.watchCfg, initial.watchState); err != nil {
		t.Fatal(err)
	}
	return root, jm, result.WatchID, a, b
}

func currentHubSignal(s *Session) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hubWatchSignal
}

// A re-registration cancels the installed roster consumer. If the consumer is
// waiting on its own rebind snapshot, the canceled subscription must not end
// the durable watch: the replacement compares its fresh snapshot with the
// baseline the watch already reported, so only a newly working worker is new
// instead of the whole roster arriving again as an initial.
func TestHubRosterWatchSupersededRebindKeepsWatch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		root, jm, id, a, b := rosterDeliveryFixture(t)
		base, cancel := context.WithCancel(t.Context())
		life := &supersededContext{Context: base}
		peer := &rosterSupersededPeer{
			managerSetupCancelClient: &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}},
			life:                     life,
		}
		root.SetHubClient(peer)
		updates := make(chan appwire.SessionWatchUpdate)
		done := make(chan struct{})
		if !root.installHubWatchSubscription(jm, id, cancel, done) {
			t.Fatal("roster subscription not installed")
		}
		go root.consumeHubWatch(life, id, appwire.SessionWatchRequest{Scope: "top_level"}, "local:manager", "manager-instance", currentHubSignal(root), updates, rosterUpdate(1, true, a), done)
		// The consumer captured its binding and is waiting on the stream.
		synctest.Wait()
		// A replaced transport wakes the suspended consumer, which rebinds on
		// its own rather than reporting the closed stream as an unexpected end.
		root.HubBindingChanged()
		close(updates)
		// The consumer's rebind subscription is canceled and closed.
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("superseded consumer did not exit")
		}
		jm.mu.Lock()
		_, cfg, live := jm.watchConfigByIDLocked(id)
		retained := live && len(cfg.rosterBaseline) == 1 && cfg.rosterBaseline[a.Ref] == a.AttentionEntry
		jm.mu.Unlock()
		if !retained {
			t.Fatal("a superseded consumer ended the durable roster watch")
		}
		jm.resetHubRosterRevision(id)
		if err := jm.onHubRoster(id, rosterUpdate(2, true, a, b)); err != nil {
			t.Fatal(err)
		}
		got := rosterFrameNotification(t, root)
		entries := rosterFrameEntries(t, got.notification.watchSendFrame)
		if len(entries) != 1 || entries[0].Ref != b.Ref || entries[0].Kind != "new" {
			t.Fatalf("reconnect roster entries=%+v, want only local:b as new", entries)
		}
		if rosterFrameField(got.notification.watchSendFrame, "roster_initial: ") != "" {
			t.Fatalf("reconnect frame is a full initial, not a delta: %s", got.notification.watchSendFrame)
		}
	})
}

// A consumer goroutine can start after its transport was replaced. Its old
// subscription then closes while the current binding is already installed.
// That is a rebind, not a stream that ended on a healthy connection: ending
// the watch here discards the roster baseline and re-reports every worker as
// an initial.
func TestHubRosterWatchLateConsumerKeepsWatch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		root, jm, id, a, b := rosterDeliveryFixture(t)
		base, cancel := context.WithCancel(t.Context())
		rebound := make(chan appwire.SessionWatchUpdate, 1)
		rebound <- rosterUpdate(2, true, a, b)
		peer := &rosterLateBindPeer{
			managerSetupCancelClient: &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}},
			updates:                  rebound,
		}
		// The old subscription belongs to the binding installed now.
		root.HubBindingChanged()
		binding := currentHubSignal(root)
		updates := make(chan appwire.SessionWatchUpdate)
		close(updates)
		done := make(chan struct{})
		if !root.installHubWatchSubscription(jm, id, cancel, done) {
			t.Fatal("roster subscription not installed")
		}
		// The transport is replaced before the consumer goroutine starts.
		root.SetHubClient(peer)
		if binding == currentHubSignal(root) {
			t.Fatal("replacement did not install a new binding signal")
		}
		go root.consumeHubWatch(base, id, appwire.SessionWatchRequest{Scope: "top_level"}, "local:manager", "manager-instance", binding, updates, rosterUpdate(1, true, a), done)
		synctest.Wait()
		jm.mu.Lock()
		_, cfg, live := jm.watchConfigByIDLocked(id)
		retained := live && len(cfg.rosterBaseline) == 2 && cfg.rosterBaseline[b.Ref] == b.AttentionEntry
		jm.mu.Unlock()
		if !retained {
			t.Fatal("a late-starting consumer ended the durable roster watch")
		}
		got := rosterFrameNotification(t, root)
		entries := rosterFrameEntries(t, got.notification.watchSendFrame)
		if len(entries) != 1 || entries[0].Ref != b.Ref || entries[0].Kind != "new" {
			t.Fatalf("late rebind roster entries=%+v, want only local:b as new", entries)
		}
		if rosterFrameField(got.notification.watchSendFrame, "roster_initial: ") != "" {
			t.Fatalf("late rebind frame is a full initial, not a delta: %s", got.notification.watchSendFrame)
		}
	})
}

// A re-registration handoff cancels the installed consumer. If the consumer's
// binding check answers after that cancellation, the consumer must not treat
// the healthy answer as an ended subscription and delete the durable watch
// the replacement is about to reuse.
func TestHubRosterWatchSupersededStateEndKeepsWatch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		root, jm, id, a, _ := rosterDeliveryFixture(t)
		base, cancel := context.WithCancel(t.Context())
		peer := &rosterBlockingStatePeer{
			managerSetupCancelClient: &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}},
			entered:                  make(chan struct{}),
			release:                  make(chan struct{}),
		}
		root.SetHubClient(peer)
		updates := make(chan appwire.SessionWatchUpdate)
		close(updates)
		done := make(chan struct{})
		if !root.installHubWatchSubscription(jm, id, cancel, done) {
			t.Fatal("roster subscription not installed")
		}
		go root.consumeHubWatch(base, id, appwire.SessionWatchRequest{Scope: "top_level"}, "local:manager", "manager-instance", currentHubSignal(root), updates, rosterUpdate(1, true, a), done)
		// The consumer's state read is in flight when the handoff cancels it.
		<-peer.entered
		root.cancelHubWatch(id)
		close(peer.release)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("superseded consumer did not exit")
		}
		jm.mu.Lock()
		_, cfg, live := jm.watchConfigByIDLocked(id)
		retained := live && len(cfg.rosterBaseline) == 1 && cfg.rosterBaseline[a.Ref] == a.AttentionEntry
		jm.mu.Unlock()
		if !retained {
			t.Fatal("a superseded consumer ended the durable roster watch after its state read answered")
		}
	})
}
