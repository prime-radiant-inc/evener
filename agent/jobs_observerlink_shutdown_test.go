package agent

import (
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provenance"
)

// observerLinkTimeoutRig builds a manager whose single scheduled observer-link
// task blocks until the returned release func runs, and captures every warning
// the manager emits through jm.emit (the job manager's warning convention).
func observerLinkTimeoutRig(t *testing.T) (
	jm *jobManager,
	clk *agenttest.FakeClock,
	warnings func() []events.WarningData,
	release func(),
) {
	t.Helper()
	jm, clk, _ = newTimerTestJM(t)
	jm.closeGrace = time.Second

	var mu sync.Mutex
	var got []events.WarningData
	jm.emit = func(kind events.EventKind, data events.EventData, _ *provenance.Causal) {
		if kind != events.EventWarning {
			return
		}
		if w, ok := data.(events.WarningData); ok {
			mu.Lock()
			got = append(got, w)
			mu.Unlock()
		}
	}

	started := make(chan struct{})
	releaseCh := make(chan struct{})
	jm.scheduleObserverLink(func() {
		close(started)
		<-releaseCh
	})
	<-started

	var releaseOnce sync.Once
	release = func() { releaseOnce.Do(func() { close(releaseCh) }) }
	// Ensure the blocked task never outlives the test, so a failed assertion
	// cannot hang the manager's cleanup close on the never-released WaitGroup.
	t.Cleanup(release)

	return jm, clk, func() []events.WarningData {
		mu.Lock()
		defer mu.Unlock()
		return append([]events.WarningData(nil), got...)
	}, release
}

func requireObserverLinkTimeoutWarning(t *testing.T, warnings []events.WarningData, phase string) {
	t.Helper()
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want exactly one observer-link timeout diagnostic", warnings)
	}
	msg := warnings[0].Message
	if !strings.Contains(msg, "observer-link") || !strings.Contains(msg, phase) {
		t.Fatalf("warning message = %q, want it to name the %s observer-link timeout", msg, phase)
	}
}

// TestCloseRuntimeState_ObserverLinkTimeoutIsReported pins that a terminal
// close whose bounded wait for observer-link metadata expires emits exactly one
// warning instead of silently reporting success. The close still succeeds: the
// wait stays best-effort, so the returned error is nil.
func TestCloseRuntimeState_ObserverLinkTimeoutIsReported(t *testing.T) {
	t.Parallel()
	jm, clk, warnings, release := observerLinkTimeoutRig(t)

	done := make(chan error, 1)
	go func() { done <- jm.closeRuntimeState() }()

	// Arm both bounded timers the close creates (running-jobs deadline and the
	// independent observer-link deadline) before advancing past them.
	clk.BlockUntil(2)
	clk.Advance(time.Second)

	if err := <-done; err != nil {
		t.Fatalf("closeRuntimeState error = %v, want a best-effort nil", err)
	}

	requireObserverLinkTimeoutWarning(t, warnings(), "close")

	// Releasing the blocked task after the bounded wait must not add another
	// diagnostic or re-run any shutdown step.
	release()
	jm.observerLinkWG.Wait()
	requireObserverLinkTimeoutWarning(t, warnings(), "close")
}

// TestReleaseQuiescentRuntime_ObserverLinkTimeoutIsReported covers the
// non-terminal counterpart: a bounded release that gives up waiting for
// observer-link metadata warns once and still returns the store-close result.
func TestReleaseQuiescentRuntime_ObserverLinkTimeoutIsReported(t *testing.T) {
	t.Parallel()
	jm, clk, warnings, release := observerLinkTimeoutRig(t)

	done := make(chan error, 1)
	go func() { done <- jm.releaseQuiescentRuntime() }()

	clk.BlockUntil(1)
	clk.Advance(time.Second)

	if err := <-done; err != nil {
		t.Fatalf("releaseQuiescentRuntime error = %v, want a best-effort nil", err)
	}

	requireObserverLinkTimeoutWarning(t, warnings(), "quiescent release")

	release()
	jm.observerLinkWG.Wait()
	requireObserverLinkTimeoutWarning(t, warnings(), "quiescent release")
}

// TestCloseRuntimeState_ObserverLinkCleanDrainWarnsNothing is the counterpart
// to the timeout case: when the observer-link task drains before the deadline,
// the close emits no warning, pinning that the diagnostic is timeout-gated.
func TestCloseRuntimeState_ObserverLinkCleanDrainWarnsNothing(t *testing.T) {
	t.Parallel()
	jm, _, warnings, release := observerLinkTimeoutRig(t)
	release()

	done := make(chan error, 1)
	go func() { done <- jm.closeRuntimeState() }()
	if err := <-done; err != nil {
		t.Fatalf("closeRuntimeState error = %v, want a best-effort nil", err)
	}
	if got := warnings(); len(got) != 0 {
		t.Fatalf("warnings = %+v, want none when the observer link drains in time", got)
	}
}

// TestReleaseQuiescentRuntime_ObserverLinkCleanDrainWarnsNothing pins the same
// clean-drain silence for the non-terminal release path.
func TestReleaseQuiescentRuntime_ObserverLinkCleanDrainWarnsNothing(t *testing.T) {
	t.Parallel()
	jm, _, warnings, release := observerLinkTimeoutRig(t)
	release()

	done := make(chan error, 1)
	go func() { done <- jm.releaseQuiescentRuntime() }()
	if err := <-done; err != nil {
		t.Fatalf("releaseQuiescentRuntime error = %v, want a best-effort nil", err)
	}
	if got := warnings(); len(got) != 0 {
		t.Fatalf("warnings = %+v, want none when the observer link drains in time", got)
	}
}
