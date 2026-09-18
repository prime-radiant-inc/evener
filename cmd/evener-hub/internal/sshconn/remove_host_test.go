package sshconn

import (
	"context"
	"errors"
	"io"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// RemoveHost of an attached host deregisters it and tears the channel down as
// one step: the registry entry, the mapped channel, and the supervisor all go,
// and the announced Attached is paired with a Detached exactly once. A later
// Ensure for the removed name is ErrHostNotFound and publishes nothing.
func TestRemoveHostTearsDownChannel(t *testing.T) {
	m, events := detachHostFixture(t)
	if !m.Attached("alpha") {
		t.Fatal("Attached before RemoveHost = false, want true")
	}
	if err := m.RemoveHost("alpha"); err != nil {
		t.Fatalf("RemoveHost = %v, want nil", err)
	}
	if _, ok := m.reg.Get("alpha"); ok {
		t.Fatal("registry entry survived RemoveHost")
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after RemoveHost = true, want false")
	}
	if _, ok := m.ClientIfAttached("alpha"); ok {
		t.Fatal("ClientIfAttached after RemoveHost = true, want false")
	}
	ev := waitForEvent(t, events, EventDetached)
	if ev.Host != "alpha" {
		t.Fatalf("Detached for host %q, want alpha", ev.Host)
	}
	if rest := detachHostKinds(events); detachHostCount(rest, EventDetached) != 0 {
		t.Fatalf("extra Detached events after RemoveHost: %v", rest)
	}
	// A later Ensure refuses at the registry lookup and publishes nothing.
	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("Ensure after RemoveHost = %v, want ErrHostNotFound", err)
	}
	if kinds := detachHostKinds(events); len(kinds) != 0 {
		t.Fatalf("Ensure after RemoveHost emitted %v, want nothing", kinds)
	}
}

// RemoveHost of an unknown name is a no-op returning nil: the entry is already
// gone, and nothing is torn down because nothing is left.
func TestRemoveHostUnknown(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	events := make(chan Event, 64)
	m := newTestManager(t, reg, fr, Options{
		OnEvent: func(ev Event) { events <- ev },
	})
	if err := m.RemoveHost("nope"); err != nil {
		t.Fatalf("RemoveHost(unknown) = %v, want nil", err)
	}
	if kinds := detachHostKinds(events); len(kinds) != 0 {
		t.Fatalf("RemoveHost(unknown) emitted %v, want no events", kinds)
	}
	if _, ok := reg.Get("alpha"); !ok {
		t.Fatal("RemoveHost(unknown) disturbed the registry: alpha no longer registered")
	}
}

// The per-host caches do not survive a removal: a re-added host starts clean
// rather than inheriting the previous identity's deploy marker, resolved
// executable, or pending restart.
func TestRemoveHostClearsPerHostCaches(t *testing.T) {
	m, events := detachHostFixture(t)
	m.markDevDeployed("alpha")
	m.setResolvedTarget("alpha", "/home/dev/.local/bin/evener")
	m.setPendingRestart("alpha", "evener hub --addr 127.0.0.1:9180", hubIdentity{})
	if err := m.RemoveHost("alpha"); err != nil {
		t.Fatalf("RemoveHost = %v, want nil", err)
	}
	waitForEvent(t, events, EventDetached)
	if m.isDevDeployed("alpha") {
		t.Fatal("devDeployed survived RemoveHost")
	}
	if m.resolvedTarget("alpha") != "" {
		t.Fatal("resolvedTarget survived RemoveHost")
	}
	if m.pendingRestart("alpha") != (pendingRestartState{}) {
		t.Fatal("pendingRestart survived RemoveHost")
	}
}

// The same caches do not survive a detach: DetachHost is the teardown a
// removed-then-readded host goes through when the caller keeps the registry
// entry, so it clears them too.
func TestDetachHostClearsPerHostCaches(t *testing.T) {
	m, events := detachHostFixture(t)
	m.markDevDeployed("alpha")
	m.setResolvedTarget("alpha", "/home/dev/.local/bin/evener")
	m.setPendingRestart("alpha", "evener hub --addr 127.0.0.1:9180", hubIdentity{})
	if err := m.DetachHost("alpha"); err != nil {
		t.Fatalf("DetachHost = %v, want nil", err)
	}
	waitForEvent(t, events, EventDetached)
	if m.isDevDeployed("alpha") {
		t.Fatal("devDeployed survived DetachHost")
	}
	if m.resolvedTarget("alpha") != "" {
		t.Fatal("resolvedTarget survived DetachHost")
	}
	if m.pendingRestart("alpha") != (pendingRestartState{}) {
		t.Fatal("pendingRestart survived DetachHost")
	}
	// The registry entry itself survives a detach — that is the difference
	// between DetachHost and RemoveHost.
	if _, ok := m.reg.Get("alpha"); !ok {
		t.Fatal("DetachHost dropped the registry entry; only RemoveHost does")
	}
}

// A removal that lands between an Ensure's registry lookup and its host gate
// must stop the attach: the gate-held recheck sees the entry gone and refuses,
// so the removal cannot be overtaken by a channel published for a host that
// is deregistered. The gateHook parks the Ensure deterministically before it
// takes the gate; RemoveHost completes in that window.
func TestRemoveHostRacesEnsureAtGate(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	gate := newGateHook()
	gate.arm()
	defer gate.open()
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, reg, fr, Options{
		beforeHostGate: func(string) { gate.hook() },
	})
	type ensureResult struct {
		ch  *Channel
		err error
	}
	done := make(chan ensureResult, 1)
	go func() {
		ch, err := m.Ensure(context.Background(), "alpha")
		done <- ensureResult{ch, err}
	}()
	gate.wait(t, "the racing Ensure")
	if err := m.RemoveHost("alpha"); err != nil {
		t.Fatalf("RemoveHost = %v, want nil", err)
	}
	gate.open()
	r := <-done
	if r.ch != nil {
		t.Fatal("racing Ensure returned a channel for the removed host")
	}
	if !errors.Is(r.err, ErrHostNotFound) {
		t.Fatalf("racing Ensure = %v, want ErrHostNotFound", r.err)
	}
	if _, ok := reg.Get("alpha"); ok {
		t.Fatal("registry entry survived RemoveHost")
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after the race = true, want false")
	}
	if _, ok := m.ClientIfAttached("alpha"); ok {
		t.Fatal("ClientIfAttached after the race = true, want false")
	}
}

// A deregistration that bypasses the host gate entirely (a caller dropping the
// registry entry directly, the way the hub's host manager does when no sshconn
// manager is wired) must still not gain a channel: Ensure rechecks the registry
// under the lock before publishing and reaps the replacement instead.
func TestEnsureRefusesPublishAfterDeregistration(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	started := make(chan struct{})
	release := make(chan struct{})
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
			close(started)
			<-release
			return goodStartFn(t)(ctx, argv, stderr)
		},
	}
	m := newTestManager(t, reg, fr, Options{})
	type ensureResult struct {
		ch  *Channel
		err error
	}
	done := make(chan ensureResult, 1)
	go func() {
		ch, err := m.Ensure(context.Background(), "alpha")
		done <- ensureResult{ch, err}
	}()
	// Park the attach mid-flight: Start blocks until the deregistration below
	// has landed, so the pre-publish recheck is the only thing standing
	// between the attach and a published channel for a gone host.
	<-started
	if err := reg.Remove("alpha"); err != nil {
		t.Fatalf("reg.Remove: %v", err)
	}
	close(release)
	r := <-done
	if r.ch != nil {
		t.Fatal("Ensure published a channel for the deregistered host")
	}
	if !errors.Is(r.err, ErrHostNotFound) {
		t.Fatalf("Ensure = %v, want ErrHostNotFound", r.err)
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after the deregistered attach = true, want false")
	}
}
