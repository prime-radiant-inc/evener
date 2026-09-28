package sshconn

// Tests for AttachUnderGate, the operation-owned attach/reattach primitive
// (registry spec 08 §14; deploy pipeline 08b §6 seam (d) and §12's
// restart-reattach row). The primitive runs under the caller's already-held
// per-host gate, never re-acquires the non-reentrant lock, and suppresses
// supervisor startup until the caller's post-verification handoff.

import (
	"context"
	"errors"
	"strings"
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

// attachUnderGateCaller is the operation holder these tests' holds register and
// present to the primitive.
func attachUnderGateCaller() hostops.Holder {
	return hostops.Holder{Kind: hostops.HolderOperation, OperationID: "op-1"}
}

// TestAttachUnderGatePublishesUnderTheHeldGateAndSuppressesTheSupervisor pins
// the primitive's contract: called while the caller holds the host's gate (a
// re-acquire of the non-reentrant lock would deadlock), it publishes the
// reattached channel and starts no supervisor until the caller's handoff.
func TestAttachUnderGatePublishesUnderTheHeldGateAndSuppressesTheSupervisor(t *testing.T) {
	m, fr, host := attachUnderGateManager(t, Options{})

	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	ch, handoff, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
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
	if !handoff() {
		t.Fatal("a repeat handoff reported failure after succeeding")
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
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()
	first.markLost()

	replacement, handoff, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
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
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	first.markLost()
	replacement, handoff, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
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

	_, _, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
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

	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
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

	_, _, err = m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("AttachUnderGate with a stale registration = %v, want ErrHostNotFound", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("a stale-registration call dialed: %d starts", got)
	}
}

// TestAttachUnderGateRequiresTheCallersOwnHold pins the precondition's
// strength: a gate held by some other holder is not the caller's hold, so a
// call presenting a different holder refuses rather than publishing under
// another holder's exclusion.
func TestAttachUnderGateRequiresTheCallersOwnHold(t *testing.T) {
	m, fr, host := attachUnderGateManager(t, Options{})

	otherRelease, err := m.TryAcquire(host.Name, hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"})
	if err != nil {
		t.Fatalf("TryAcquire (manager hold): %v", err)
	}
	defer otherRelease()

	_, _, err = m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if !errors.Is(err, hostops.ErrGateNotHeld) {
		t.Fatalf("AttachUnderGate under another holder = %v, want hostops.ErrGateNotHeld", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("a foreign-holder call dialed: %d starts", got)
	}
}

// TestAttachUnderGateReapsAndNeverAnnouncesADropAfterPublish pins the
// post-publish death path: a link that dies between the publish and the event
// pair is reaped, the slot is given back, and no Attached is left outstanding —
// the check runs before the pair, exactly as Ensure orders it.
func TestAttachUnderGateReapsAndNeverAnnouncesADropAfterPublish(t *testing.T) {
	host := attachUnderGateHost()
	var mu sync.Mutex
	var events []Event
	var published *Channel
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		afterPublish: func(_ string, ch *Channel) {
			published = ch
			ch.markLost()
		},
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	stamped, ok := m.reg.Get(host.Name)
	if !ok {
		t.Fatalf("the test registry holds no %q", host.Name)
	}
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	ch, handoff, err := m.AttachUnderGate(context.Background(), stamped, attachUnderGateCaller(), true)
	if !errors.Is(err, ErrSSHStart) {
		t.Fatalf("AttachUnderGate with a drop after publish = %v, want the dropped-channel refusal", err)
	}
	if ch != nil || handoff != nil {
		t.Fatalf("a dropped replacement was handed back: ch!=nil=%t handoff!=nil=%t", ch != nil, handoff != nil)
	}
	if published == nil || !published.isClosed() {
		t.Fatal("the dropped replacement was not reaped")
	}
	if cur := m.currentChannel(host.Name); cur != nil {
		t.Fatalf("the dropped replacement stayed mapped: %v", cur)
	}
	mu.Lock()
	snapshot := append([]Event(nil), events...)
	mu.Unlock()
	for _, ev := range snapshot {
		if ev.Kind == EventAttached && ev.Host == host.Name {
			t.Fatalf("an Attached was announced for the dropped replacement: %+v", ev)
		}
	}
	m.mu.Lock()
	announced := m.announced[host.Name]
	m.mu.Unlock()
	if announced != nil {
		t.Fatalf("announced = %v, want none", announced)
	}
}

// TestAttachUnderGateLiveChannelHandoffEnsuresSupervision pins the already-live
// branch: a channel this primitive published earlier has no supervisor until a
// handoff starts one, so a second call that finds it live must still return a
// handoff that ensures supervision — never a no-op that strands the channel.
func TestAttachUnderGateLiveChannelHandoffEnsuresSupervision(t *testing.T) {
	m, _, host := attachUnderGateManager(t, Options{})
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	// The first attach publishes the channel and deliberately starts no
	// supervisor; its handoff is not taken.
	first, _, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if err != nil {
		t.Fatalf("first AttachUnderGate: %v", err)
	}
	if n := supervisorLoops(m, host.Name); n != 0 {
		t.Fatalf("supervisor loops after the suppressed attach = %d, want 0", n)
	}

	// The restart's channel drop has not been observed yet: the second call
	// finds the channel live. Its handoff must still give the channel a
	// supervisor.
	second, handoff, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if err != nil {
		t.Fatalf("second AttachUnderGate: %v", err)
	}
	if second != first {
		t.Fatalf("the live call replaced the channel: first=%v second=%v", first, second)
	}
	if !handoff() {
		t.Fatal("the live-channel handoff did not start the supervisor")
	}
	if !handoff() {
		t.Fatal("a repeat live-channel handoff reported failure after succeeding")
	}
	if n := supervisorLoops(m, host.Name); n != 1 {
		t.Fatalf("supervisor loops after the live-channel handoff = %d, want 1", n)
	}
}

// TestAttachUnderGateHandoffRefusesAfterTheHolderChanged pins the handoff's
// ownership recheck: if the caller's hold is gone — the gate now held by a
// different holder — the handoff refuses instead of starting a supervisor
// under another holder's exclusion.
func TestAttachUnderGateHandoffRefusesAfterTheHolderChanged(t *testing.T) {
	m, _, host := attachUnderGateManager(t, Options{})
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	_, handoff, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if err != nil {
		t.Fatalf("AttachUnderGate: %v", err)
	}
	// The operation's hold ends and another holder takes the gate (a contract
	// break the handoff must refuse, not publish under).
	release()
	otherRelease, err := m.TryAcquire(host.Name, hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"})
	if err != nil {
		t.Fatalf("TryAcquire (other holder): %v", err)
	}
	defer otherRelease()

	if handoff() {
		t.Fatal("the handoff started a supervisor after the caller's hold ended")
	}
	if n := supervisorLoops(m, host.Name); n != 0 {
		t.Fatalf("supervisor loops after the refused handoff = %d, want 0", n)
	}
}

// TestAttachUnderGateLiveChannelWithASupervisorDoesNotDoubleSupervise pins the
// dedup side of the live-channel handoff: a channel an existing loop already
// owns is left to it, and the handoff neither starts a second loop nor
// disturbs the first.
func TestAttachUnderGateLiveChannelWithASupervisorDoesNotDoubleSupervise(t *testing.T) {
	m, _, host := attachUnderGateManager(t, Options{})
	if _, err := m.Ensure(context.Background(), host.Name); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if n := supervisorLoops(m, host.Name); n != 1 {
		t.Fatalf("supervisor loops after Ensure = %d, want 1", n)
	}
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	ch, handoff, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if err != nil {
		t.Fatalf("AttachUnderGate: %v", err)
	}
	if ch != m.currentChannel(host.Name) {
		t.Fatal("the live call did not hand back the installed channel")
	}
	if !handoff() {
		t.Fatal("the live-channel handoff reported failure")
	}
	if n := supervisorLoops(m, host.Name); n != 1 {
		t.Fatalf("supervisor loops after the live-channel handoff = %d, want the existing loop only", n)
	}
}

// TestAttachUnderGateRefusesAClosedManager pins the close discipline every
// attach path carries: a manager already closed refuses before dialing.
func TestAttachUnderGateRefusesAClosedManager(t *testing.T) {
	m, fr, host := attachUnderGateManager(t, Options{})

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, _, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("AttachUnderGate on a closed manager = %v, want ErrManagerClosed", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("a closed-manager call dialed: %d starts", got)
	}
}

// TestAttachUnderGateEmitsDisconnectedOnAnAttachFailure pins the state stream:
// a failed attach ends at the disconnected phase instead of leaving consumers
// parked on the last intermediate state, exactly as Ensure's failure path
// emits it.
func TestAttachUnderGateEmitsDisconnectedOnAnAttachFailure(t *testing.T) {
	host := attachUnderGateHost()
	var mu sync.Mutex
	var states []State
	// No startFn: the bridge start fails, so ensureOnce returns a retryable
	// attach failure after the preflight.
	fr := &fakeRunner{runFn: cannedRun(nil)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		OnEvent: func(ev Event) {
			if ev.Kind != EventState || ev.Host != host.Name {
				return
			}
			mu.Lock()
			states = append(states, ev.State)
			mu.Unlock()
		},
	})
	stamped, ok := m.reg.Get(host.Name)
	if !ok {
		t.Fatalf("the test registry holds no %q", host.Name)
	}
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	if _, _, err := m.AttachUnderGate(context.Background(), stamped, attachUnderGateCaller(), true); err == nil {
		t.Fatal("AttachUnderGate succeeded without a runnable bridge")
	}
	mu.Lock()
	last := State("")
	if len(states) > 0 {
		last = states[len(states)-1]
	}
	snapshot := append([]State(nil), states...)
	mu.Unlock()
	if last != StateDisconnected {
		t.Fatalf("last state after a failed attach = %q, want %q (states: %v)", last, StateDisconnected, snapshot)
	}
}

// TestAttachUnderGateExplicitControlsTheBootstrap pins the two arms' Ensure
// semantics: explicit=true keeps the first-attach bootstrap (nothing answered
// health, so the ladder would start a hub), while the reconnect-shaped
// explicit=false never issues a start command — a reattach after a restart
// must not start a hub of its own.
func TestAttachUnderGateExplicitControlsTheBootstrap(t *testing.T) {
	host := attachUnderGateHost()
	fr := &fakeRunner{
		// Nothing answers the health probe: an explicit attach would take the
		// first-attach bootstrap path.
		runFn:   cannedRun(map[string][]byte{"api/health": nil}),
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "dev"})
	stamped, ok := m.reg.Get(host.Name)
	if !ok {
		t.Fatalf("the test registry holds no %q", host.Name)
	}
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	if _, _, err := m.AttachUnderGate(context.Background(), stamped, attachUnderGateCaller(), false); err != nil {
		t.Fatalf("AttachUnderGate(explicit=false): %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		for _, bootstrap := range []string{"systemctl", "launchctl", "nohup", "lsof", "ss -"} {
			if strings.Contains(joined, bootstrap) {
				t.Fatalf("explicit=false issued a bootstrap command (%q): %v", bootstrap, argv)
			}
		}
	}
}

// TestAttachUnderGateEmitsFailedOnATerminalAttachFailure pins the terminal
// event: with no predecessor to own the outcome, a terminal attach failure is
// announced as EventFailed (the host row's last-attach-error source) before the
// disconnected state, exactly as Ensure announces it.
func TestAttachUnderGateEmitsFailedOnATerminalAttachFailure(t *testing.T) {
	host := attachUnderGateHost()
	var mu sync.Mutex
	var failedErr error
	fr := &fakeRunner{
		// The host speaks another protocol: the attach ladder's terminal
		// ErrProtocolIncompatible.
		runFn:   cannedRun(map[string][]byte{"launch-check": []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":["api-log"]}`)}),
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		OnEvent: func(ev Event) {
			if ev.Kind != EventFailed || ev.Host != host.Name {
				return
			}
			mu.Lock()
			failedErr = ev.Err
			mu.Unlock()
		},
	})
	stamped, ok := m.reg.Get(host.Name)
	if !ok {
		t.Fatalf("the test registry holds no %q", host.Name)
	}
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	_, _, err = m.AttachUnderGate(context.Background(), stamped, attachUnderGateCaller(), true)
	if !errors.Is(err, ErrProtocolIncompatible) {
		t.Fatalf("AttachUnderGate = %v, want ErrProtocolIncompatible", err)
	}
	mu.Lock()
	got := failedErr
	mu.Unlock()
	if got == nil {
		t.Fatal("a terminal attach failure emitted no EventFailed")
	}
}

// TestAttachUnderGateHandoffNeverSkipsADeregisteredLoopsMark pins the handoff's
// supervision question against a teardown already in progress: a loop that has
// deregistered is gone even though its channel's mark may not be cleared yet,
// and a handoff that read the stale mark as live supervision would skip the
// only supervisor the channel will have and leave it unsupervised. The
// teardown is parked at its own decision point, so the interleaving is exact
// rather than a race.
func TestAttachUnderGateHandoffNeverSkipsADeregisteredLoopsMark(t *testing.T) {
	parked := make(chan struct{}, 1)
	resume := make(chan struct{})
	exited := make(chan struct{}, 1)
	var parkOnce sync.Once
	m, _, host := attachUnderGateManager(t, Options{
		beforeSuperviseClear: func(string, *Channel) {
			parkOnce.Do(func() {
				parked <- struct{}{}
				<-resume
			})
		},
		superviseExited: func(string) {
			select {
			case exited <- struct{}{}:
			default:
			}
		},
	})

	gate, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer gate()

	ch, handoff, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if err != nil {
		t.Fatalf("AttachUnderGate: %v", err)
	}
	if !handoff() {
		t.Fatal("the handoff did not start the supervisor")
	}
	if n := supervisorLoops(m, host.Name); n != 1 {
		t.Fatalf("supervisor loops after the handoff = %d, want 1", n)
	}

	// End that loop and park its teardown after the deregistration, so the
	// channel's mark is all that is left of it when the late handoff asks.
	m.stopSupervisor(host.Name)
	select {
	case <-parked:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop's teardown never reached its parked decision point")
	}

	late := m.attachHandoff(host, ch, attachUnderGateCaller())
	done := make(chan bool, 1)
	go func() { done <- late() }()
	var started, decided bool
	// An arm that decides without consulting the teardown answers inside the
	// parked window; the arm that consults it blocks until the teardown
	// resumes. Give the deciding arm its window, then resume regardless.
	select {
	case started = <-done:
		decided = true
	case <-time.After(250 * time.Millisecond):
	}
	close(resume)
	if !decided {
		select {
		case started = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the handoff did not return after the teardown resumed")
		}
	}
	if !started {
		t.Fatal("the handoff reported failure after the teardown")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the torn-down loop never announced its exit")
	}
	if n := supervisorLoops(m, host.Name); n != 1 {
		t.Fatalf("supervisor loops after the handoff interleaved with the teardown = %d, want 1: the handoff read a deregistered loop's mark as live supervision and skipped the only supervisor the channel has", n)
	}
	if !ch.supervised.Load() {
		t.Fatal("the channel's supervision mark is clear after the handoff")
	}
}

// TestAttachUnderGateConcurrentHandoffsStartExactlyOneSupervisor pins the
// exactly-once registration two handoffs for the same channel must honor: the
// supervision decision and the loop registration have to be one critical
// section, or two handoffs using the same held operation can both observe
// "unsupervised" and register duplicate reconnect loops for one channel. Both
// handoffs are parked before the decision, so the interleaving is exact rather
// than a race.
func TestAttachUnderGateConcurrentHandoffsStartExactlyOneSupervisor(t *testing.T) {
	arrived := make(chan struct{}, 2)
	resume := make(chan struct{})
	m, _, host := attachUnderGateManager(t, Options{
		beforeSuperviseStart: func(string, *Channel) {
			arrived <- struct{}{}
			<-resume
		},
	})
	gate, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer gate()

	_, first, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if err != nil {
		t.Fatalf("AttachUnderGate: %v", err)
	}
	// A second attach under the same hold finds the channel live and hands back
	// its own handoff for that same channel — the shape of an operation that
	// both attach-firsts and reattaches, or one whose attach is entered twice.
	ch, second, err := m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if err != nil {
		t.Fatalf("second AttachUnderGate: %v", err)
	}
	if ch == nil || m.currentChannel(host.Name) != ch {
		t.Fatalf("the second attach did not hand back the live channel: ch=%v current=%v", ch, m.currentChannel(host.Name))
	}

	done1 := make(chan bool, 1)
	done2 := make(chan bool, 1)
	go func() { done1 <- first() }()
	go func() { done2 <- second() }()
	// Both handoffs must be parked before their supervision decision, so the
	// duplicate-registration race is exercised deterministically.
	for i := range 2 {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of the two handoffs reached the supervision decision", i)
		}
	}
	close(resume)
	for i, done := range []chan bool{done1, done2} {
		select {
		case ok := <-done:
			if !ok {
				t.Fatalf("handoff %d reported that supervision failed", i+1)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("handoff %d never returned", i+1)
		}
	}
	if n := supervisorLoops(m, host.Name); n != 1 {
		t.Fatalf("supervisor loops after two concurrent handoffs = %d, want exactly 1: the supervision decision and the loop registration must be one atomic step", n)
	}
}

// TestSupervisorReconnectMarksTheReplacementChannel pins the round-7 M2 trace's
// conclusion: when a loop reconnects and a replacement channel is published, the
// replacement is marked supervised by the loop that adopts it, so a later
// live-channel handoff for it finds it supervised and starts no second
// supervisor. The replacement is not left unmarked.
func TestSupervisorReconnectMarksTheReplacementChannel(t *testing.T) {
	exits := make(chan struct{}, 4)
	m, _, host := attachUnderGateManager(t, Options{
		BackoffBase: 2 * time.Millisecond,
		BackoffMax:  10 * time.Millisecond,
		superviseExited: func(string) {
			select {
			case exits <- struct{}{}:
			default:
			}
		},
	})

	first, err := m.Ensure(context.Background(), host.Name)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !first.supervised.Load() {
		t.Fatal("the attached channel is not marked supervised")
	}

	// Drop the link and let the supervisor reconnect: the replacement it
	// publishes is adopted by a fresh loop (reconnectOnce starts it) and must
	// carry its own supervision mark.
	first.markLost()
	var replacement *Channel
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ch := m.currentChannel(host.Name); ch != nil && ch != first {
			replacement = ch
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the supervisor never reconnected the dropped channel")
		}
		time.Sleep(2 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for !replacement.supervised.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the reconnected replacement is unmarked: a later live-channel handoff could start a second supervisor for it")
		}
		time.Sleep(2 * time.Millisecond)
	}
	// The reconnecting loop stands down once its replacement is adopted; wait
	// for that exit so the loop counting below is exact.
	select {
	case <-exits:
	case <-time.After(5 * time.Second):
		t.Fatal("the reconnecting loop never stood down after adopting the replacement")
	}

	gate, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer gate()
	handoff := m.attachHandoff(host, replacement, attachUnderGateCaller())
	if !handoff() {
		t.Fatal("the handoff reported failure for a supervised replacement")
	}
	if n := supervisorLoops(m, host.Name); n != 1 {
		t.Fatalf("supervisor loops after the replacement's handoff = %d, want 1: the replacement is supervised and must not gain a second loop", n)
	}
	if !replacement.supervised.Load() {
		t.Fatal("the replacement's supervision mark was cleared by the handoff")
	}
}
