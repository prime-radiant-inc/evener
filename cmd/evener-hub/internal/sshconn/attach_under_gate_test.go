package sshconn

// Tests for AttachUnderGate, the operation-owned attach/reattach primitive
// (registry spec 08 §14; deploy pipeline 08b §6 seam (d) and §12's
// restart-reattach row). The primitive runs under the caller's already-held
// per-host gate, never re-acquires the non-reentrant lock, and suppresses
// supervisor startup until the caller's post-verification handoff.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// attachUnderGateHost is the configured host the primitive's tests act on.
func attachUnderGateHost() hostreg.Host {
	return hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
}

// attachUnderGateManager builds a manager whose fake runner answers the canned
// attach sequence (preflight plus a matching running hub), so every attach in
// these tests lands without a deploy or restart. The returned host is the
// registry's own stamped entry: AttachUnderGate takes the generation-pinned
// entry a caller resolved, and the registry assigns the generation.
func attachUnderGateManager(t *testing.T, opts Options) (*Manager, *fakeRunner, hostreg.Host) {
	t.Helper()
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	if opts.controllerVersionOverride == "" {
		opts.controllerVersionOverride = "dev"
	}
	m := newTestManager(t, testRegistry(t, attachUnderGateHost()), fr, opts)
	host, ok := m.reg.Get(attachUnderGateHost().Name)
	if !ok {
		t.Fatalf("the test registry holds no %q", attachUnderGateHost().Name)
	}
	return m, fr, host
}

// supervisorLoops reports how many reconnect loops the manager holds for name.
func supervisorLoops(m *Manager, name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.supervisors[name])
}

// TestAttachUnderGatePublishesUnderTheHeldGateAndSuppressesTheSupervisor pins
// the primitive's contract: called while the caller holds the host's gate (a
// re-acquire of the non-reentrant lock would deadlock), it publishes the
// reattached channel and starts no supervisor until the caller's handoff.
func TestAttachUnderGatePublishesUnderTheHeldGateAndSuppressesTheSupervisor(t *testing.T) {
	m, fr, host := attachUnderGateManager(t, Options{})

	release, err := m.TryAcquire(host.Name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: "op-1"})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	ch, handoff, err := m.AttachUnderGate(context.Background(), host)
	if err != nil {
		t.Fatalf("AttachUnderGate under the held gate: %v", err)
	}
	if ch == nil || m.currentChannel(host.Name) != ch {
		t.Fatalf("AttachUnderGate did not publish its channel: ch=%v current=%v", ch, m.currentChannel(host.Name))
	}
	if _, ok := m.ClientIfAttached(host.Name); !ok {
		t.Fatal("the published channel is not readable through ClientIfAttached")
	}
	if n := supervisorLoops(m, host.Name); n != 0 {
		t.Fatalf("supervisor loops before the handoff = %d, want 0", n)
	}
	if handoff == nil {
		t.Fatal("AttachUnderGate returned no handoff")
	}
	if !handoff() {
		t.Fatal("the handoff did not start the supervisor")
	}
	if n := supervisorLoops(m, host.Name); n != 1 {
		t.Fatalf("supervisor loops after the handoff = %d, want 1", n)
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("attach starts = %d, want 1", got)
	}
}

// TestAttachUnderGateRetiresTheLostPredecessorAndPairsItsEvents pins the
// reconnect handoff's channel ownership: the dropped predecessor is replaced,
// reaped, and paired event-wise (its Attached gets a Detached before the
// replacement's Attached), so a consumer can drop the old source.
func TestAttachUnderGateRetiresTheLostPredecessorAndPairsItsEvents(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	m, _, host := attachUnderGateManager(t, Options{OnEvent: func(ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}})

	first, err := m.Ensure(context.Background(), host.Name)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Hold the gate before the drop, so the dropped channel's supervisor parks
	// on it exactly as it does while an operation owns the host.
	release, err := m.TryAcquire(host.Name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: "op-1"})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()
	first.markLost()

	replacement, handoff, err := m.AttachUnderGate(context.Background(), host)
	if err != nil {
		t.Fatalf("AttachUnderGate over the lost predecessor: %v", err)
	}
	if replacement == first {
		t.Fatal("AttachUnderGate handed back the dropped predecessor")
	}
	if m.currentChannel(host.Name) != replacement {
		t.Fatal("the replacement is not the installed channel")
	}
	if !first.isClosed() {
		t.Fatal("the lost predecessor was not reaped")
	}

	mu.Lock()
	sawAttachedFirst, sawDetached, sawAttachedReplacement := -1, -1, -1
	for i, ev := range events {
		switch {
		case ev.Kind == EventAttached && ev.Host == host.Name && sawAttachedFirst == -1:
			sawAttachedFirst = i
		case ev.Kind == EventDetached && ev.Host == host.Name && sawAttachedFirst != -1 && sawDetached == -1:
			sawDetached = i
		case ev.Kind == EventAttached && ev.Host == host.Name && sawDetached != -1 && sawAttachedReplacement == -1:
			sawAttachedReplacement = i
		}
	}
	snapshot := append([]Event(nil), events...)
	mu.Unlock()
	if sawAttachedFirst == -1 || sawDetached == -1 || sawAttachedReplacement == -1 {
		t.Fatalf("event pairing (first@%d detached@%d replacement@%d) is incomplete: %+v",
			sawAttachedFirst, sawDetached, sawAttachedReplacement, snapshot)
	}
	if !handoff() {
		t.Fatal("the handoff did not start the supervisor")
	}
}

// TestAttachUnderGateHandoffStartsTheSupervisorThatReconnectsAfterTheGate pins
// §12's reconnect-after-restart row: once the gate releases, the supervisor the
// handoff started owns the channel and reconnects it exactly as an
// Ensure-started supervisor would.
func TestAttachUnderGateHandoffStartsTheSupervisorThatReconnectsAfterTheGate(t *testing.T) {
	m, fr, host := attachUnderGateManager(t, Options{
		BackoffBase: 2 * time.Millisecond,
		BackoffMax:  10 * time.Millisecond,
	})

	first, err := m.Ensure(context.Background(), host.Name)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	release, err := m.TryAcquire(host.Name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: "op-1"})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	first.markLost()
	replacement, handoff, err := m.AttachUnderGate(context.Background(), host)
	if err != nil {
		t.Fatalf("AttachUnderGate: %v", err)
	}
	if !handoff() {
		t.Fatal("the handoff did not start the supervisor")
	}
	release()

	// Drop the replacement: only a supervisor the handoff started can bring the
	// host back now.
	replacement.markLost()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ch := m.currentChannel(host.Name); ch != nil && ch != replacement {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the handed-off supervisor never reconnected after the gate released")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := len(fr.recordedStarts()); got != 3 {
		t.Fatalf("attach starts = %d, want 3 (Ensure, reattach, supervisor reconnect)", got)
	}
}

// TestAttachUnderGateRefusesWithoutTheHeldGate pins the never-re-acquire
// discipline's precondition: the primitive is for a caller that already holds
// the host's gate, and a call without one refuses instead of dialing ungated.
func TestAttachUnderGateRefusesWithoutTheHeldGate(t *testing.T) {
	m, fr, host := attachUnderGateManager(t, Options{})

	_, _, err := m.AttachUnderGate(context.Background(), host)
	if !errors.Is(err, hostops.ErrGateNotHeld) {
		t.Fatalf("AttachUnderGate without the gate = %v, want hostops.ErrGateNotHeld", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("an unheld-gate call dialed: %d starts", got)
	}
}

// TestAttachUnderGateRefusesAStaleRegistration pins the pinned-entry discipline:
// the primitive re-runs the attach for the registration it was handed, and a
// remove/re-add that swapped the name out from under it refuses before dialing.
func TestAttachUnderGateRefusesAStaleRegistration(t *testing.T) {
	m, fr, host := attachUnderGateManager(t, Options{})

	release, err := m.TryAcquire(host.Name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: "op-1"})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	if err := m.reg.Remove(host.Name); err != nil {
		t.Fatalf("registry Remove: %v", err)
	}
	readded := host
	readded.SSH = "other.example"
	if err := m.reg.Add(readded); err != nil {
		t.Fatalf("registry Add: %v", err)
	}

	_, _, err = m.AttachUnderGate(context.Background(), host)
	if !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("AttachUnderGate with a stale registration = %v, want ErrHostNotFound", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("a stale-registration call dialed: %d starts", got)
	}
}

// TestAttachUnderGateRefusesAClosedManager pins the close discipline every
// attach path carries: a manager already closed refuses before dialing.
func TestAttachUnderGateRefusesAClosedManager(t *testing.T) {
	m, fr, host := attachUnderGateManager(t, Options{})

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, _, err := m.AttachUnderGate(context.Background(), host)
	if !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("AttachUnderGate on a closed manager = %v, want ErrManagerClosed", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("a closed-manager call dialed: %d starts", got)
	}
}
