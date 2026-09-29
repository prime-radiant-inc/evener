package sshconn

// Tests for the gate-inheriting teardown entries (registry spec 08 §4's "gate
// released last"; deploy pipeline 08b §5's mutation rebind ordering): under the
// caller's already-held per-host gate they run RemoveHost/UpdateHost's teardown
// and rebind, never acquire the non-reentrant lock, refuse with
// hostops.ErrGateNotHeld when the presented holder is not the gate's current
// one, and leave the caller's hold held — the caller releases last.

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// underGateHolder is the manager-class holder a mutation's reservation
// registers, matching the hub's own acquisition.
func underGateHolder(activity string) hostops.Holder {
	return hostops.Holder{Kind: hostops.HolderManager, Activity: activity}
}

// underGateAssertRefuses pins that a plan-class acquisition cannot enter name's
// gate, and that the refusal names the presented holder.
func underGateAssertRefuses(t *testing.T, m *Manager, name string, holder hostops.Holder) {
	t.Helper()
	release, err := m.TryAcquire(name, hostops.Holder{Kind: hostops.HolderPlan})
	if err == nil {
		release()
		t.Fatalf("a competing acquisition entered %q's gate while %+v held it", name, holder)
	}
	var busy *hostops.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("competing acquisition refusal = %v, want *hostops.BusyError", err)
	}
	if busy.Holder.Kind != holder.Kind || busy.Holder.Activity != holder.Activity {
		t.Fatalf("busy holder = %+v, want %+v", busy.Holder, holder)
	}
}

// reapCountingStdio wraps the fake child and counts the Kill/Wait pair
// Channel.Close runs, so a test can pin that the reap happened inside the
// under-gate call rather than being left to the caller.
type reapCountingStdio struct {
	Stdio
	mu    sync.Mutex
	kills int
	waits int
}

func (s *reapCountingStdio) Kill() error {
	s.mu.Lock()
	s.kills++
	s.mu.Unlock()
	return s.Stdio.Kill()
}

func (s *reapCountingStdio) Wait() error {
	s.mu.Lock()
	s.waits++
	s.mu.Unlock()
	return s.Stdio.Wait()
}

func (s *reapCountingStdio) reaped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kills == 1 && s.waits >= 1
}

// reapRecordingRunner answers the standard attach sequence and records the
// started child, so the test can assert the child was reaped.
type reapRecordingRunner struct {
	mu    sync.Mutex
	stdio *reapCountingStdio
}

func (r *reapRecordingRunner) Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	return cannedRun(nil)(ctx, argv, stdin)
}

func (r *reapRecordingRunner) Start(_ context.Context, _ []string, _ io.Writer) (Stdio, error) {
	rec := &reapCountingStdio{Stdio: newFakeBridge(appwire.ProtocolVersion).stdio}
	r.mu.Lock()
	r.stdio = rec
	r.mu.Unlock()
	return rec, nil
}

func (r *reapRecordingRunner) started() *reapCountingStdio {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stdio
}

// underGateFixture attaches alpha through the recording runner and returns the
// manager plus the recording, ready for an under-gate call.
func underGateFixture(t *testing.T) (*Manager, *reapRecordingRunner) {
	t.Helper()
	runner := &reapRecordingRunner{}
	m := newTestManager(t, testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"}), &fakeRunner{
		runFn:   runner.Run,
		startFn: runner.Start,
	}, Options{})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return m, runner
}

// TestRemoveHostUnderGateRefusesWithoutTheCallersHold pins the precondition: a
// free gate, or one held by a different holder, refuses with ErrGateNotHeld
// before anything live moves — the entry must prove the caller's own hold, not
// take one.
func TestRemoveHostUnderGateRefusesWithoutTheCallersHold(t *testing.T) {
	m, runner := underGateFixture(t)
	holder := underGateHolder("remove")

	// No hold at all.
	if err := m.RemoveHostUnderGate("alpha", holder); !errors.Is(err, hostops.ErrGateNotHeld) {
		t.Fatalf("RemoveHostUnderGate with a free gate = %v, want ErrGateNotHeld", err)
	}
	if _, ok := m.reg.Get("alpha"); !ok || !m.Attached("alpha") {
		t.Fatal("the refusal moved live state: an entry or channel was torn down")
	}
	if runner.started().reaped() {
		t.Fatal("the refusal reaped the channel")
	}

	// Held by a different holder: not this call's hold to run under.
	release, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()
	if err := m.RemoveHostUnderGate("alpha", holder); !errors.Is(err, hostops.ErrGateNotHeld) {
		t.Fatalf("RemoveHostUnderGate under a foreign holder = %v, want ErrGateNotHeld", err)
	}
	if _, ok := m.reg.Get("alpha"); !ok || !m.Attached("alpha") {
		t.Fatal("the foreign-holder refusal moved live state")
	}
}

// TestUpdateHostUnderGateRefusesWithoutTheCallersHold is the update half of the
// precondition.
func TestUpdateHostUnderGateRefusesWithoutTheCallersHold(t *testing.T) {
	m, _ := underGateFixture(t)
	holder := underGateHolder("update")
	entry := hostreg.Host{Name: "alpha", SSH: "alpha2.example"}

	if err := m.UpdateHostUnderGate(entry, holder, nil); !errors.Is(err, hostops.ErrGateNotHeld) {
		t.Fatalf("UpdateHostUnderGate with a free gate = %v, want ErrGateNotHeld", err)
	}
	live, ok := m.reg.Get("alpha")
	if !ok || live.SSH != "alpha.example" || !m.Attached("alpha") {
		t.Fatalf("the refusal moved live state: live = %+v (ok=%v), attached = %v", live, ok, m.Attached("alpha"))
	}

	release, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()
	if err := m.UpdateHostUnderGate(entry, holder, nil); !errors.Is(err, hostops.ErrGateNotHeld) {
		t.Fatalf("UpdateHostUnderGate under a foreign holder = %v, want ErrGateNotHeld", err)
	}
	if live, ok := m.reg.Get("alpha"); !ok || live.SSH != "alpha.example" {
		t.Fatalf("the foreign-holder refusal moved live state: live = %+v (ok=%v)", live, ok)
	}
}

// TestRemoveHostUnderGateTearsDownUnderTheHold pins the shipped rule for a
// removal: running under the caller's reservation it drops the registry entry,
// stops the supervisor, unmaps and reaps the channel, and leaves the caller's
// hold in place — the caller, not the entry, releases last.
func TestRemoveHostUnderGateTearsDownUnderTheHold(t *testing.T) {
	m, runner := underGateFixture(t)
	holder := underGateHolder("remove")
	events := make(chan Event, 64)
	m.opts.OnEvent = func(ev Event) { events <- ev }

	release, err := m.TryAcquire("alpha", holder)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if err := m.RemoveHostUnderGate("alpha", holder); err != nil {
		t.Fatalf("RemoveHostUnderGate under the held gate: %v", err)
	}
	if _, ok := m.reg.Get("alpha"); ok {
		t.Fatal("registry entry survived RemoveHostUnderGate")
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after RemoveHostUnderGate = true, want false")
	}
	if _, ok := m.ClientIfAttached("alpha"); ok {
		t.Fatal("ClientIfAttached after RemoveHostUnderGate = true, want false")
	}
	if !runner.started().reaped() {
		t.Fatal("the teardown returned without reaping the unmapped channel; the reap must run inside the caller's hold")
	}
	waitForEvent(t, events, EventDetached)

	// The caller's hold is untouched: the entry never acquired, released, or
	// re-registered the gate.
	underGateAssertRefuses(t, m, "alpha", holder)
	release()
	free, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire after the caller released: %v", err)
	}
	free()
}

// TestCanceledSupervisorStandsDownWithoutTheGate pins the race behind the two
// teardown tests' final acquisition: a supervisor a teardown canceled must stand
// down without contending for the host gate, so the caller that releases last
// observes the gate free rather than briefly held by a loop whose owner is
// already gone. The test parks the supervisor on the link drop via
// beforeSuperviseGate, cancels its loop with an under-gate update while the
// caller still holds the gate, then releases the hook: a canceled loop must exit
// (superviseExited) while the gate is still the caller's, which it cannot do if
// it first waits for the gate.
func TestCanceledSupervisorStandsDownWithoutTheGate(t *testing.T) {
	runner := &reapRecordingRunner{}
	atGate := make(chan struct{}, 1)
	unblock := make(chan struct{})
	exited := make(chan struct{}, 4)
	m := newTestManager(t, testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"}), &fakeRunner{
		runFn:   runner.Run,
		startFn: runner.Start,
	}, Options{
		beforeSuperviseGate: func(string, *Channel) {
			atGate <- struct{}{}
			<-unblock
		},
		superviseExited: func(string) { exited <- struct{}{} },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Wake the supervisor on the link drop and park it inside beforeSuperviseGate,
	// before it contends for the gate. Its context is still live here, so the
	// select takes the drop branch deterministically.
	ch.markLost()
	<-atGate

	holder := underGateHolder("update")
	release, err := m.TryAcquire("alpha", holder)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()
	// The under-gate update cancels the parked supervisor's loop, exactly as a
	// teardown does, while the caller holds the gate (the caller releases last).
	if err := m.UpdateHostUnderGate(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}, holder, nil); err != nil {
		t.Fatalf("UpdateHostUnderGate: %v", err)
	}
	// Let the loop past the hook with its context already canceled. It must exit
	// without acquiring the gate the caller still holds.
	close(unblock)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("a canceled supervisor blocked on the host gate instead of standing down")
	}
}

// TestUpdateHostUnderGateRebindsUnderTheHold pins the shipped rule for an edit:
// running under the caller's reservation it tears the retired channel down
// before the swap, swaps the registry, runs the caller's retirement hook inside
// the same hold (a competing acquisition there is refused), reaps the unmapped
// channel, and leaves the caller's hold in place.
func TestUpdateHostUnderGateRebindsUnderTheHold(t *testing.T) {
	m, runner := underGateFixture(t)
	holder := underGateHolder("update")
	before, ok := m.reg.Get("alpha")
	if !ok {
		t.Fatal("alpha not registered before the update")
	}

	release, err := m.TryAcquire("alpha", holder)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	var retiredUnderHold bool
	err = m.UpdateHostUnderGate(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}, holder, func(retired hostreg.Host) {
		if retired.Generation != before.Generation || retired.SSH != "alpha.example" {
			t.Errorf("onRetire received %+v, want the pre-swap capture with generation %d", retired, before.Generation)
		}
		// The hook runs inside the entry's hold: a competing acquisition here
		// must be refused, deterministically.
		competing, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
		if err == nil {
			competing()
			retiredUnderHold = false
			return
		}
		retiredUnderHold = true
	})
	if err != nil {
		t.Fatalf("UpdateHostUnderGate under the held gate: %v", err)
	}
	if !retiredUnderHold {
		t.Fatal("the retirement hook did not run under the caller's hold")
	}
	after, ok := m.reg.Get("alpha")
	if !ok || after.SSH != "alpha2.example" {
		t.Fatalf("live entry after the rebind = %+v (ok=%v), want the edited address", after, ok)
	}
	if after.Generation <= before.Generation {
		t.Fatalf("generation = %d, want > %d", after.Generation, before.Generation)
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after UpdateHostUnderGate = true, want false")
	}
	if !runner.started().reaped() {
		t.Fatal("the rebind returned without reaping the unmapped channel; the reap must run inside the caller's hold")
	}
	underGateAssertRefuses(t, m, "alpha", holder)
	release()
	free, err := m.TryAcquire("alpha", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire after the caller released: %v", err)
	}
	free()
}
