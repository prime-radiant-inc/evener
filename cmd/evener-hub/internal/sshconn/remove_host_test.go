package sshconn

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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

// A remove/re-add that lands between an Ensure's registry capture and its host
// gate swaps the entry under the name while the old attach is parked. The name
// still resolves after the re-add, so a name-only recheck would pass and the
// stale attach would dial the removed entry's address and publish that channel
// under the re-added name (the round-2 HIGH). The identity recheck refuses the
// stale attach, and the next Ensure attaches the re-added entry — dialing the
// new address, never the removed one.
func TestRemoveReaddRacesEnsureAtGate(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	gate := newGateHook()
	gate.arm()
	defer gate.open()
	var mu sync.Mutex
	var dialed []string
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
			mu.Lock()
			dialed = append(dialed, strings.Join(argv, " "))
			mu.Unlock()
			return goodStartFn(t)(ctx, argv, stderr)
		},
	}
	events := make(chan Event, 64)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:        func(ev Event) { events <- ev },
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
	gate.wait(t, "the parked Ensure")
	// Remove and re-add the name while the old attach is parked before the
	// gate: the swap completes fully, so the name resolves again — to a
	// different entry.
	if err := m.RemoveHost("alpha"); err != nil {
		t.Fatalf("RemoveHost: %v", err)
	}
	if err := m.AddHost(hostreg.Host{Name: "alpha", SSH: "beta.example", KeyPath: "/keys/fresh"}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}
	gate.open()
	r := <-done
	if r.ch != nil {
		t.Fatal("the parked Ensure returned a channel for the re-added name")
	}
	if !errors.Is(r.err, ErrHostNotFound) {
		t.Fatalf("parked Ensure = %v, want ErrHostNotFound (the captured identity is gone)", r.err)
	}
	if kinds := detachHostKinds(events); len(kinds) != 0 {
		t.Fatalf("the refused attach emitted %v, want nothing", kinds)
	}
	// The re-added entry is a new identity the next Ensure attaches: the dial
	// carries the re-added address and key, never the removed entry's.
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure after the re-add: %v", err)
	}
	if !m.Attached("alpha") {
		t.Fatal("the re-added host is not attached after a fresh Ensure")
	}
	waitForEvent(t, events, EventAttached)
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 1 {
		t.Fatalf("dialed %d times, want exactly the fresh attach's one dial", len(dialed))
	}
	if !strings.Contains(dialed[0], "beta.example") || !strings.Contains(dialed[0], "/keys/fresh") {
		t.Fatalf("fresh dial = %q, want the re-added entry's address and key", dialed[0])
	}
	if strings.Contains(dialed[0], "alpha.example") {
		t.Fatalf("fresh dial = %q, still carrying the removed entry's address", dialed[0])
	}
}

// A remove/re-add of a byte-identical entry is still a swap the parked Ensure
// must refuse: content equality cannot tell the removed entry from its
// re-added twin — every configured byte matches — so the registry's per-name
// entry generation is what distinguishes them (the round-3 identity race). The
// re-add's own caller attaches the fresh identity; the parked attach never
// dials for it.
func TestRemoveReaddIdenticalRacesEnsureAtGate(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", KeyPath: "/keys/alpha"}
	reg := testRegistry(t, host)
	gate := newGateHook()
	gate.arm()
	defer gate.open()
	var mu sync.Mutex
	var dialed []string
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
			mu.Lock()
			dialed = append(dialed, strings.Join(argv, " "))
			mu.Unlock()
			return goodStartFn(t)(ctx, argv, stderr)
		},
	}
	events := make(chan Event, 64)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:        func(ev Event) { events <- ev },
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
	gate.wait(t, "the parked Ensure")
	// Remove and re-add the byte-identical entry while the old attach is parked
	// before the gate: the swap completes fully and the name resolves again, to
	// an entry whose content matches the captured one in every field.
	if err := m.RemoveHost("alpha"); err != nil {
		t.Fatalf("RemoveHost: %v", err)
	}
	if err := m.AddHost(host); err != nil {
		t.Fatalf("AddHost: %v", err)
	}
	gate.open()
	r := <-done
	if r.ch != nil {
		t.Fatal("the parked Ensure returned a channel for the byte-identical re-added name")
	}
	if !errors.Is(r.err, ErrHostNotFound) {
		t.Fatalf("parked Ensure = %v, want ErrHostNotFound (the captured entry is gone even though its content was re-added)", r.err)
	}
	if kinds := detachHostKinds(events); len(kinds) != 0 {
		t.Fatalf("the refused attach emitted %v, want nothing", kinds)
	}
	// The re-added identity is a new entry the next Ensure attaches: exactly
	// one dial, the fresh attach's own.
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure after the identical re-add: %v", err)
	}
	waitForEvent(t, events, EventAttached)
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 1 {
		t.Fatalf("dialed %d times, want exactly the fresh attach's one dial", len(dialed))
	}
}

// A remove/re-add that bypasses the host gate entirely (a caller swapping the
// registry entry directly, the way the hub's host manager does when no sshconn
// manager is wired) must not gain the in-flight attach's channel either: the
// pre-publish recheck compares the full entry, so a same-name swap is refused
// exactly like the deregistration its removal half was, and the re-added
// identity is left intact for its own attach.
func TestEnsureRefusesPublishAfterReplace(t *testing.T) {
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
	// Park the attach mid-flight: Start blocks until the swap below has
	// landed, so the pre-publish identity recheck is the only thing standing
	// between the attach and a channel published for the removed entry.
	<-started
	if err := reg.Remove("alpha"); err != nil {
		t.Fatalf("reg.Remove: %v", err)
	}
	if err := reg.Add(hostreg.Host{Name: "alpha", SSH: "beta.example"}); err != nil {
		t.Fatalf("reg.Add: %v", err)
	}
	close(release)
	r := <-done
	if r.ch != nil {
		t.Fatal("Ensure published a channel built from the replaced entry")
	}
	if !errors.Is(r.err, ErrHostNotFound) {
		t.Fatalf("Ensure = %v, want ErrHostNotFound (the captured identity was replaced)", r.err)
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after the replaced attach = true, want false")
	}
	// The refusal reaped the stale channel without tearing the re-added
	// identity down: the registry keeps the replacement, unattached.
	cur, ok := reg.Get("alpha")
	if !ok || cur.SSH != "beta.example" {
		t.Fatalf("registry entry after the refusal = %+v, want the re-added entry intact", cur)
	}
}

// The pre-publish recheck must refuse a mid-flight attach whose captured entry
// was swapped for a byte-identical re-add: content equality passes — the
// round-3 identity race — so the captured registry-entry generation is what
// marks the channel as built from the removed entry, and it is reaped instead
// of published under the re-added name.
func TestEnsureRefusesPublishAfterIdenticalReplace(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", KeyPath: "/keys/alpha"}
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
	// Park the attach mid-flight: Start blocks until the swap below has
	// landed, so the pre-publish recheck is the only thing standing between the
	// attach and a channel published for the removed entry.
	<-started
	// Swap the entry for a byte-identical re-add, bypassing the host gate the
	// way a gate-less caller (the hub's host manager with no sshconn manager
	// wired) does.
	if err := reg.Remove("alpha"); err != nil {
		t.Fatalf("reg.Remove: %v", err)
	}
	if err := reg.Add(host); err != nil {
		t.Fatalf("reg.Add: %v", err)
	}
	close(release)
	r := <-done
	if r.ch != nil {
		t.Fatal("Ensure published a channel built from the removed entry behind byte-identical content")
	}
	if !errors.Is(r.err, ErrHostNotFound) {
		t.Fatalf("Ensure = %v, want ErrHostNotFound (the captured entry's generation was replaced)", r.err)
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after the identical-replaced attach = true, want false")
	}
	// The refusal reaped the stale channel without tearing the re-added identity
	// down: the registry keeps the replacement, unattached.
	cur, ok := reg.Get("alpha")
	if !ok || cur.SSH != "alpha.example" || cur.KeyPath != "/keys/alpha" {
		t.Fatalf("registry entry after the refusal = %+v, want the re-added entry intact", cur)
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

// A reconnect attempt whose captured entry is swapped for a byte-identical
// re-add while it is in flight must not publish either (the round-3 identity
// race at the reconnect recheck): the supervisor reaps the replacement, reports
// the host honestly disconnected, and stands down; the re-added identity is
// left intact for its own attach.
func TestReconnectRefusesPublishAfterIdenticalReplace(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", KeyPath: "/keys/alpha"}
	reg := testRegistry(t, host)
	var mu sync.Mutex
	starts := 0
	parked := make(chan struct{})
	release := make(chan struct{})
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
			mu.Lock()
			starts++
			attempt := starts
			mu.Unlock()
			// Park the supervisor's reconnect attempt — the second bridge —
			// so the swap lands while the attempt is in flight, past the
			// loop's own cancellation check.
			if attempt == 2 {
				close(parked)
				<-release
			}
			return goodStartFn(t)(ctx, argv, stderr)
		},
	}
	events := make(chan Event, 64)
	supervisorExited := make(chan struct{}, 8)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:         func(ev Event) { events <- ev },
		superviseExited: func(string) { supervisorExited <- struct{}{} },
		BackoffBase:     time.Millisecond,
		BackoffMax:      time.Millisecond,
		sleep:           func(context.Context, time.Duration) error { return nil },
		jitter:          func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	waitForEvent(t, events, EventAttached)
	// Drop the link: the supervisor reconnects immediately (the faked backoff
	// sleeps nothing) and parks inside the second bridge's Start.
	ch.markLost()
	waitForEvent(t, events, EventDetached)
	<-parked
	// Swap the entry for a byte-identical re-add while the reconnect is in
	// flight. The supervisor holds the host gate for its attempt, so the swap
	// bypasses it the way a gate-less caller does.
	if err := reg.Remove("alpha"); err != nil {
		t.Fatalf("reg.Remove: %v", err)
	}
	if err := reg.Add(host); err != nil {
		t.Fatalf("reg.Add: %v", err)
	}
	close(release)
	// The supervisor stands down after the one refused attempt.
	select {
	case <-supervisorExited:
	case <-time.After(5 * time.Second):
		t.Fatal("the reconnect supervisor never stood down after the refusal")
	}
	// Nothing was published under the re-added name: no second Attached, and no
	// live channel.
	if kinds := detachHostKinds(events); detachHostCount(kinds, EventAttached) != 0 {
		t.Fatalf("the refused reconnect announced %v, want no Attached for the re-added entry", kinds)
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after the identical-replaced reconnect = true, want false")
	}
	// The re-added identity is intact and attachable by its own caller.
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure after the identical re-add: %v", err)
	}
}

// waitForGateContenders blocks until want callers hold or wait on name's host
// gate. A lifecycle call takes its gate reference after its registry capture
// and before it acquires the lock, so this reports the exact window between the
// two without a sleep to bias the race: the caller polling already reached its
// capture, and the test can swap the entry under it.
func waitForGateContenders(t *testing.T, m *Manager, name string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.Lock()
		refs := 0
		if entry := m.locks[name]; entry != nil {
			refs = entry.refs
		}
		m.mu.Unlock()
		if refs >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("host gate %q reached %d contenders, want %d", name, refs, want)
		}
		runtime.Gosched()
	}
}

// A name that was unknown when a removal looked, and that a concurrent add
// registers while the removal waits for the gate, holds a host that removal
// never captured: the unknown-name no-op the doc promises must stay a no-op,
// not become a removal of the host whose add is about to answer success, and
// the added host's own attach must survive too.
func TestRemoveHostLeavesAHostAddedInItsWindowAlone(t *testing.T) {
	reg := testRegistry(t)
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, reg, fr, Options{})

	hold := m.hostLock("alpha")
	hold.Lock()
	done := make(chan error, 1)
	go func() { done <- m.RemoveHost("alpha") }()
	waitForGateContenders(t, m, "alpha", 2)

	// The concurrent add: the name resolves by the time the removal takes the
	// gate, with the added host's own attach mapped under it.
	if err := reg.Add(hostreg.Host{Name: "alpha", SSH: "alpha.example"}); err != nil {
		t.Fatalf("reg.Add: %v", err)
	}
	added, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("reg.Get after the add: not registered")
	}
	m.publishChannel("alpha", &Channel{host: added, lost: make(chan struct{}), done: make(chan struct{})}, added)

	hold.Unlock()
	m.releaseHostLock("alpha")
	if err := <-done; err != nil {
		t.Fatalf("RemoveHost = %v, want nil", err)
	}

	if _, ok := reg.Get("alpha"); !ok {
		t.Fatal("the removal dropped the registry entry of a host added in its own window")
	}
	if m.currentChannel("alpha") == nil {
		t.Fatal("the removal swept the channel of a host added in its own window")
	}
}

// A channel mapped under the name must belong to the registration the attach
// validated, not merely carry the name: a caller that swaps the registry
// directly (no manager teardown, which is how the hub's own registry behaves
// with no manager wired) leaves the removed identity's channel mapped, and
// returning it would hand the re-added host a live channel to the removed
// address. The attach dials the re-added entry instead and retires the channel
// it replaced.
func TestEnsureDoesNotReturnAReplacedRegistrationsChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	var mu sync.Mutex
	var dialed []string
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
			mu.Lock()
			dialed = append(dialed, strings.Join(argv, " "))
			mu.Unlock()
			return goodStartFn(t)(ctx, argv, stderr)
		},
	}
	m := newTestManager(t, reg, fr, Options{})
	first, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if err := reg.Remove("alpha"); err != nil {
		t.Fatalf("reg.Remove: %v", err)
	}
	if err := reg.Add(hostreg.Host{Name: "alpha", SSH: "beta.example"}); err != nil {
		t.Fatalf("reg.Add: %v", err)
	}

	second, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure after the replace: %v", err)
	}
	if second == first {
		t.Fatal("Ensure returned the removed registration's channel")
	}
	if second.host.SSH != "beta.example" {
		t.Fatalf("returned channel's host = %q, want the re-added entry's address", second.host.SSH)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 2 || !strings.Contains(dialed[1], "beta.example") {
		t.Fatalf("dials = %v, want a second dial for the re-added entry", dialed)
	}
}

// The teardown fences the supervisor too: a loop reconnecting the re-added host
// is that host's own live state — the state a dropped link leaves it in, with
// no channel mapped, which is why the channel fence alone cannot see it — and a
// teardown that resolved the previous identity must leave it running and
// registered. The loop is registered the way startSupervise registers one, with
// a cancel that reports being called.
func TestStaleTeardownLeavesTheReaddHostsReconnectLoopAlone(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, reg, fr, Options{})

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
	cancelled := make(chan struct{})
	loop := &supervisorLoop{host: readded, cancel: func() { close(cancelled) }}
	m.mu.Lock()
	m.supervisors["alpha"] = map[*supervisorLoop]struct{}{loop: {}}
	m.mu.Unlock()

	if ch := m.teardownHostChannel("alpha", host, true); ch != nil {
		t.Fatalf("the stale teardown reaped a channel: %+v", ch)
	}
	select {
	case <-cancelled:
		t.Fatal("the stale teardown cancelled the re-added host's reconnect loop")
	default:
	}
	m.mu.Lock()
	_, kept := m.supervisors["alpha"][loop]
	m.mu.Unlock()
	if !kept {
		t.Fatal("the stale teardown deregistered the re-added host's reconnect loop")
	}
}

// A remove/re-add that swaps the name's entry while the removal waits for the
// host gate leaves the replacement alone: the read before the gate is not a
// title to the entry, so a removal that captured the old identity must not drop
// the entry its caller re-added — doing so deregisters the re-added host behind
// its own add's success — and the re-added identity's own channel stays up with
// it. The captured identity is what the removal targets; the registration that
// took the name mid-call is the caller's re-add, not this call's to undo.
func TestRemoveHostLeavesAReplacedIdentityRegistered(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, reg, fr, Options{})

	// Hold the gate, so the removal captures its entry and then waits for it.
	hold := m.hostLock("alpha")
	hold.Lock()
	done := make(chan error, 1)
	go func() { done <- m.RemoveHost("alpha") }()
	waitForGateContenders(t, m, "alpha", 2)

	// The swap a concurrent remove/re-add performs: the captured entry goes, and
	// a fresh identity with a different address takes the name.
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
	// The re-added identity's own attach, mapped under the name it was added to.
	m.publishChannel("alpha", &Channel{host: readded, lost: make(chan struct{}), done: make(chan struct{})}, readded)

	hold.Unlock()
	m.releaseHostLock("alpha")
	if err := <-done; err != nil {
		t.Fatalf("RemoveHost = %v, want nil", err)
	}

	cur, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("the stale removal dropped the registry entry of the re-added host")
	}
	if cur.SSH != "beta.example" {
		t.Fatalf("registry entry after the stale removal = %+v, want the re-added identity intact", cur)
	}
	if m.currentChannel("alpha") == nil {
		t.Fatal("the stale removal took down the re-added host's channel")
	}
}
