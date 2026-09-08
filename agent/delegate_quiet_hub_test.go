package agent

import (
	"context"
	"maps"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
)

// quietHubTestSession builds a Session on the given fake clock that is able to
// receive quiet attention: a real transcript writer plus a controller whose
// root runtime is the session itself.
func quietHubTestSession(t *testing.T, clk *agenttest.FakeClock) (*Session, *delegateTreeController) {
	t.Helper()
	root := newDelegateAttentionTestSession(t)
	root.clock = clk
	controller, _ := newDelegateControllerTestHarness(t, 2, 2)
	controller.rootRuntime = root
	root.delegateController = controller
	return root, controller
}

func seedQuietHubLease(t *testing.T, controller *delegateTreeController, clk *agenttest.FakeClock, id string) delegateLease {
	t.Helper()
	seedDelegateControllerRunning(t, controller, id, "")
	child := &Session{clock: clk, delegateController: controller}
	controller.live[id].runtime = child
	controller.live[id].binding.runtime = child
	controller.live[id].activityAt = clk.Now()
	return delegateLease{delegateID: id, generation: 1}
}

func quietHubFor(t *testing.T, s *Session) *delegateQuietWatchHub {
	t.Helper()
	delegateQuietWatchHubs.Lock()
	defer delegateQuietWatchHubs.Unlock()
	hub := delegateQuietWatchHubs.hubs[s]
	if hub == nil {
		t.Fatal("no quiet hub registered for session")
	}
	return hub
}

// quietHubTripwire bounds waits on real completion signals: the entry channel
// (closed by detach), the tick hook (fired by tick work), and the hub exit
// channel (closed when the serve loop returns). Detach, tick work, and hub
// shutdown are all in-process with no I/O, so this fires only on a genuine
// hang, never on scheduler contention. It is a tripwire only — the
// synchronization mechanism is the channel, not time
// (docs/developing-evener/testing.md Flakes and Timeouts).
const quietHubTripwire = 10 * time.Second

// quietHubEntryCount reads the hub's entry count under both locks. Detach is
// synchronous through the returned CancelFunc (it closes entry.stop before
// returning), so callers that just detached assert on this directly instead
// of polling.
func quietHubEntryCount(s *Session) (hub *delegateQuietWatchHub, n int) {
	delegateQuietWatchHubs.Lock()
	defer delegateQuietWatchHubs.Unlock()
	hub = delegateQuietWatchHubs.hubs[s]
	if hub == nil {
		return nil, -1
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return hub, len(hub.entries)
}

// armQuietTicks installs the tick-done hook for exactly the leases in want and
// returns the wait for their completions. Call it BEFORE advancing the fake
// clock: Advance delivers the buffered tick to the hub loop, which can consume
// it and finish the tick work before a hook installed after Advance exists,
// flaking the wait on the 10s tripwire. The hook fires on the tick worker
// after the durable write lands, so a received signal means the attention is
// already readable (each lease fires once per shared tick). The returned wait
// collects the leases in arrival order.
func armQuietTicks(t *testing.T, want map[delegateLease]int) func() []delegateLease {
	t.Helper()
	ticks := make(chan delegateLease, 16)
	restore := setQuietWatchTickDone(func(lease delegateLease) { ticks <- lease })
	t.Cleanup(restore)
	remaining := make(map[delegateLease]int, len(want))
	maps.Copy(remaining, want)
	left := 0
	for _, n := range want {
		left += n
	}
	var got []delegateLease
	return func() []delegateLease {
		t.Helper()
		timer := time.NewTimer(quietHubTripwire)
		defer timer.Stop()
		for left > 0 {
			select {
			case lease := <-ticks:
				if remaining[lease] <= 0 {
					t.Fatalf("unexpected tick for lease %+v (got so far %+v)", lease, got)
				}
				remaining[lease]--
				left--
				got = append(got, lease)
			case <-timer.C:
				t.Fatalf("timed out waiting for %d more tick(s), got %+v", left, got)
			}
		}
		return got
	}
}

// ageHubLeasesPastQuietWindow moves virtual time past the quiet window BEFORE
// any watchdog is armed, so the tests below fire exactly one shared tick into
// an empty ticker buffer. Advancing while the hub ticker is armed would fire
// ~20 ticks into the size-1 buffer: all but the first (a pre-window no-op)
// drop, and waiting for the hub loop to consume the stale tick without
// stealing it from the loop's own channel is inherently racy. Lease
// eligibility timing is orthogonal here (covered by the supervision tests);
// these tests cover hub fan-out, detach, coalescing, and shutdown.
func ageHubLeasesPastQuietWindow(clk *agenttest.FakeClock) {
	clk.Advance(delegateQuietWindow)
}

func TestDelegateQuietHub_SharedTicksReachAllLeases(t *testing.T) {
	clk := agenttest.NewFakeClockAt(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	root, controller := quietHubTestSession(t, clk)
	leaseA := seedQuietHubLease(t, controller, clk, "dlg_hub_a")
	leaseB := seedQuietHubLease(t, controller, clk, "dlg_hub_b")
	ageHubLeasesPastQuietWindow(clk)

	cancelA := root.startDelegateQuietWatchdog(context.Background(), leaseA)
	defer cancelA()
	cancelB := root.startDelegateQuietWatchdog(context.Background(), leaseB)
	defer cancelB()

	if got := clk.BlockedCount(); got != 1 {
		t.Fatalf("quiet hub tickers = %d, want 1 shared ticker", got)
	}

	want := map[delegateLease]int{leaseA: 1, leaseB: 1}
	wait := armQuietTicks(t, want)
	clk.Advance(delegateQuietCheckInterval)
	if got := wait(); len(got) != 2 {
		t.Fatalf("shared-tick completions = %+v, want one per lease", got)
	}
	if got := pendingQuietAttention(t, root); len(got) != 2 {
		t.Fatalf("shared-tick quiet attention = %#v, want one per lease", got)
	}
}

func TestDelegateQuietHub_DetachedLeaseStopsReceiving(t *testing.T) {
	clk := agenttest.NewFakeClockAt(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	root, controller := quietHubTestSession(t, clk)
	leaseA := seedQuietHubLease(t, controller, clk, "dlg_hub_a")
	leaseB := seedQuietHubLease(t, controller, clk, "dlg_hub_b")
	ageHubLeasesPastQuietWindow(clk)

	cancelA := root.startDelegateQuietWatchdog(context.Background(), leaseA)
	cancelB := root.startDelegateQuietWatchdog(context.Background(), leaseB)
	defer cancelB()

	// Detach is synchronous: cancelA closes the entry's stop channel before
	// it returns, so the entry count below is a direct assertion, not a poll.
	cancelA()
	if _, n := quietHubEntryCount(root); n != 1 {
		t.Fatalf("hub entry count = %d, want 1", n)
	}

	wait := armQuietTicks(t, map[delegateLease]int{leaseB: 1})
	clk.Advance(delegateQuietCheckInterval)
	if got := wait(); len(got) != 1 || got[0] != leaseB {
		t.Fatalf("remaining lease ticks = %+v, want [%+v]", got, leaseB)
	}
	got := pendingQuietAttention(t, root)
	for _, id := range got {
		if id == delegateQuietAttentionID(leaseA) {
			t.Fatalf("detached lease still ticked: %#v", got)
		}
	}
	if len(got) != 1 || got[0] != delegateQuietAttentionID(leaseB) {
		t.Fatalf("remaining lease attention = %#v, want [%q]", got, delegateQuietAttentionID(leaseB))
	}
}

func TestDelegateQuietHub_StopsAfterLastDetach(t *testing.T) {
	clk := agenttest.NewFakeClockAt(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	root, controller := quietHubTestSession(t, clk)
	lease := seedQuietHubLease(t, controller, clk, "dlg_hub_last")

	cancel := root.startDelegateQuietWatchdog(context.Background(), lease)
	if got := clk.BlockedCount(); got != 1 {
		t.Fatalf("quiet hub tickers = %d, want 1", got)
	}
	cancel()

	// Detach unregisters synchronously: the registry check below reads state
	// the CancelFunc established before returning.
	delegateQuietWatchHubs.Lock()
	_, registered := delegateQuietWatchHubs.hubs[root]
	delegateQuietWatchHubs.Unlock()
	if registered {
		t.Fatal("hub still registered after last detach")
	}
	if got := clk.BlockedCount(); got != 0 {
		t.Fatalf("quiet hub tickers after last detach = %d, want 0 (ticker stopped)", got)
	}
}

func TestDelegateQuietHub_BlockedLeaseDelaysNeitherOthersNorShutdown(t *testing.T) {
	clk := agenttest.NewFakeClockAt(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	root, controller := quietHubTestSession(t, clk)
	leaseSlow := seedQuietHubLease(t, controller, clk, "dlg_hub_slow")
	leaseFast := seedQuietHubLease(t, controller, clk, "dlg_hub_fast")
	ageHubLeasesPastQuietWindow(clk)

	cancelSlow := root.startDelegateQuietWatchdog(context.Background(), leaseSlow)
	defer cancelSlow()
	cancelFast := root.startDelegateQuietWatchdog(context.Background(), leaseFast)
	defer cancelFast()

	// Wedge the slow lease's in-flight slot so the hub coalesces (drops) its
	// ticks instead of queueing a goroutine per tick. No tick can fire between
	// arming and the CAS below (no Advance in between), so the swap is exact.
	hub := quietHubFor(t, root)
	hub.mu.Lock()
	var slowEntry delegateQuietWatchEntry
	for entry := range hub.entries {
		if entry.lease == leaseSlow {
			slowEntry = entry
		}
	}
	hub.mu.Unlock()
	if slowEntry.busy == nil {
		t.Fatal("slow lease has no in-flight guard")
	}
	if !atomic.CompareAndSwapUint32(slowEntry.busy, 0, 1) {
		t.Fatal("slow lease already has a tick in flight")
	}
	defer atomic.StoreUint32(slowEntry.busy, 0)

	wait := armQuietTicks(t, map[delegateLease]int{leaseFast: 1})
	clk.Advance(delegateQuietCheckInterval)
	if got := wait(); len(got) != 1 || got[0] != leaseFast {
		t.Fatalf("ticks with blocked lease = %+v, want only [%+v]", got, leaseFast)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != delegateQuietAttentionID(leaseFast) {
		t.Fatalf("attention with blocked lease = %#v, want only %q", got, delegateQuietAttentionID(leaseFast))
	}

	// Shutdown must not wait for the wedged tick: detach is synchronous and
	// never joins tick work, so both cancels return directly. The tripwire
	// below guards the join only against a genuine hang.
	shutdown := make(chan struct{})
	go func() {
		defer close(shutdown)
		cancelSlow()
		cancelFast()
	}()
	select {
	case <-shutdown:
	case <-time.After(quietHubTripwire): // TRIPWIRE: cancels join no tick work; sync detach is the mechanism.
		t.Fatal("hub shutdown blocked on wedged lease tick")
	}
}
