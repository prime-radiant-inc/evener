package sshconn

import (
	"context"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// detachHostFixture attaches alpha through the fake runner and returns the
// manager with its events channel positioned after the initial Attached.
func detachHostFixture(t *testing.T, hosts ...hostreg.Host) (*Manager, chan Event) {
	t.Helper()
	if len(hosts) == 0 {
		hosts = []hostreg.Host{{Name: "alpha", SSH: "alpha.example"}}
	}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	events := make(chan Event, 64)
	m := newTestManager(t, testRegistry(t, hosts...), fr, Options{
		OnEvent: func(ev Event) { events <- ev },
	})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	waitForEvent(t, events, EventAttached)
	return m, events
}

// detachHostKinds drains whatever events are buffered without blocking.
func detachHostKinds(events chan Event) []EventKind {
	var kinds []EventKind
	for {
		select {
		case ev := <-events:
			kinds = append(kinds, ev.Kind)
		default:
			return kinds
		}
	}
}

func detachHostCount(kinds []EventKind, want EventKind) int {
	n := 0
	for _, k := range kinds {
		if k == want {
			n++
		}
	}
	return n
}

// DetachHost of an unknown name is a no-op: it reports nil, emits nothing,
// and leaves the registry untouched.
func TestDetachHostUnknown(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	events := make(chan Event, 64)
	m := newTestManager(t, reg, fr, Options{
		OnEvent: func(ev Event) { events <- ev },
	})
	if err := m.DetachHost("nope"); err != nil {
		t.Fatalf("DetachHost(unknown) = %v, want nil", err)
	}
	if kinds := detachHostKinds(events); len(kinds) != 0 {
		t.Fatalf("DetachHost(unknown) emitted %v, want no events", kinds)
	}
	if _, ok := reg.Get("alpha"); !ok {
		t.Fatal("DetachHost(unknown) disturbed the registry: alpha no longer registered")
	}
}

// DetachHost with a nil registry is a no-op returning nil rather than a
// nil-pointer dereference.
func TestDetachHostNilRegistry(t *testing.T) {
	m := New(nil, Options{Runner: &fakeRunner{}})
	t.Cleanup(func() { _ = m.Close() })
	if err := m.DetachHost("alpha"); err != nil {
		t.Fatalf("DetachHost(nil reg) = %v, want nil", err)
	}
}

// DetachHost of a known but never-attached host is a no-op: nil, no event.
func TestDetachHostUnattached(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	events := make(chan Event, 64)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent: func(ev Event) { events <- ev },
	})
	if err := m.DetachHost("alpha"); err != nil {
		t.Fatalf("DetachHost(unattached) = %v, want nil", err)
	}
	if kinds := detachHostKinds(events); len(kinds) != 0 {
		t.Fatalf("DetachHost(unattached) emitted %v, want no events", kinds)
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after DetachHost(unattached) = true, want false")
	}
}

// DetachHost of an attached host drops the channel and pairs the announced
// Attached with a Detached.
func TestDetachHostAttached(t *testing.T) {
	m, events := detachHostFixture(t)
	if !m.Attached("alpha") {
		t.Fatal("Attached before DetachHost = false, want true")
	}
	if _, ok := m.ClientIfAttached("alpha"); !ok {
		t.Fatal("ClientIfAttached before DetachHost = false, want true")
	}
	if err := m.DetachHost("alpha"); err != nil {
		t.Fatalf("DetachHost = %v, want nil", err)
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after DetachHost = true, want false")
	}
	if _, ok := m.ClientIfAttached("alpha"); ok {
		t.Fatal("ClientIfAttached after DetachHost = true, want false")
	}
	// DetachHost emits the Detached synchronously, so it is already buffered;
	// waitForEvent only confirms its presence without new ordering.
	ev := waitForEvent(t, events, EventDetached)
	if ev.Host != "alpha" {
		t.Fatalf("Detached for host %q, want alpha", ev.Host)
	}
	if rest := detachHostKinds(events); detachHostCount(rest, EventDetached) != 0 {
		t.Fatalf("extra Detached events after DetachHost: %v", rest)
	}
}

// DetachHost is idempotent: the second call is a no-op returning nil and
// emits no second Detached.
func TestDetachHostTwice(t *testing.T) {
	m, events := detachHostFixture(t)
	if err := m.DetachHost("alpha"); err != nil {
		t.Fatalf("first DetachHost = %v, want nil", err)
	}
	waitForEvent(t, events, EventDetached)
	if err := m.DetachHost("alpha"); err != nil {
		t.Fatalf("second DetachHost = %v, want nil", err)
	}
	if kinds := detachHostKinds(events); detachHostCount(kinds, EventDetached) != 0 {
		t.Fatalf("second DetachHost emitted Detached: %v", kinds)
	}
}

// A detached host re-adds cleanly: Ensure attaches fresh and announces a new
// Attached, with no dial of the old channel.
func TestDetachHostReensure(t *testing.T) {
	m, events := detachHostFixture(t)
	first, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure before detach: %v", err)
	}
	if err := m.DetachHost("alpha"); err != nil {
		t.Fatalf("DetachHost = %v, want nil", err)
	}
	waitForEvent(t, events, EventDetached)
	second, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure after DetachHost: %v", err)
	}
	if second == first {
		t.Fatal("Ensure after DetachHost returned the detached channel, want a fresh attach")
	}
	if !m.Attached("alpha") {
		t.Fatal("Attached after re-Ensure = false, want true")
	}
	waitForEvent(t, events, EventAttached)
}

// The same window on the detach-only path: a detach that resolved the old
// identity before the gate must leave the re-added host's channel alone. The
// teardown is scoped to the identity the call captured, not to whatever holds
// the name once the gate is taken, so the re-added host's own live attach — its
// channel and its supervisor — survives a detach meant for its predecessor.
func TestDetachHostLeavesTheReaddedChannelAttached(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, reg, fr, Options{})

	hold := m.hostLock("alpha")
	hold.Lock()
	done := make(chan error, 1)
	go func() { done <- m.DetachHost("alpha") }()
	waitForGateContenders(t, m, "alpha", 2)

	// Remove and re-add the name while the detach waits: the entry it resolved
	// is gone and a fresh identity, with a different address, holds the name.
	if err := reg.Remove("alpha"); err != nil {
		t.Fatalf("reg.Remove: %v", err)
	}
	if err := reg.Add(hostreg.Host{Name: "alpha", SSH: "beta.example"}); err != nil {
		t.Fatalf("reg.Add: %v", err)
	}
	readded, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("reg.Get after the re-add: not registered")
	}
	m.publishChannel("alpha", &Channel{host: readded, lost: make(chan struct{}), done: make(chan struct{})}, readded)

	hold.Unlock()
	m.releaseHostLock("alpha")
	if err := <-done; err != nil {
		t.Fatalf("DetachHost = %v, want nil", err)
	}

	if m.currentChannel("alpha") == nil {
		t.Fatal("the stale detach took down the re-added host's channel")
	}
	if _, ok := reg.Get("alpha"); !ok {
		t.Fatal("DetachHost dropped the registry entry; only RemoveHost does")
	}
}
