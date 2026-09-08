package agent

import (
	"context"
	"sync"
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

// waitForQuietHubCount polls for the hub entry count, since tick dispatch runs
// on the hub goroutine and detached entries land asynchronously to Advance.
func waitForQuietHubCount(t *testing.T, s *Session, want int) *delegateQuietWatchHub {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		delegateQuietWatchHubs.Lock()
		hub := delegateQuietWatchHubs.hubs[s]
		n := -1
		if hub != nil {
			hub.mu.Lock()
			n = len(hub.entries)
			hub.mu.Unlock()
		} else if want == 0 {
			delegateQuietWatchHubs.Unlock()
			return nil
		}
		delegateQuietWatchHubs.Unlock()
		if n == want {
			return hub
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub entry count = %d, want %d", n, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForPendingQuiet polls the receiver transcript fold until want attention
// IDs land (tick work runs on detached goroutines, so it is not synchronous
// with the fake-clock Advance that fires the ticker).
func waitForPendingQuiet(t *testing.T, root *Session, want int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := pendingQuietAttention(t, root); len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending quiet attention = %#v, want at least %d", pendingQuietAttention(t, root), want)
		}
		time.Sleep(time.Millisecond)
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

	clk.Advance(delegateQuietCheckInterval)
	got := waitForPendingQuiet(t, root, 2)
	if len(got) != 2 {
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

	cancelA()
	waitForQuietHubCount(t, root, 1)

	clk.Advance(delegateQuietCheckInterval)
	got := waitForPendingQuiet(t, root, 1)
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

	if hub := waitForQuietHubCount(t, root, 0); hub != nil {
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

	clk.Advance(delegateQuietCheckInterval)
	got := waitForPendingQuiet(t, root, 1)
	if len(got) != 1 || got[0] != delegateQuietAttentionID(leaseFast) {
		t.Fatalf("attention with blocked lease = %#v, want only %q", got, delegateQuietAttentionID(leaseFast))
	}

	// Shutdown must not wait for the wedged tick: detach everything and
	// require both cancels to return promptly.
	var wg sync.WaitGroup
	wg.Add(2)
	done := make(chan struct{})
	go func() { defer wg.Done(); cancelSlow() }()
	go func() { defer wg.Done(); cancelFast() }()
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("hub shutdown blocked on wedged lease tick")
	}
}
