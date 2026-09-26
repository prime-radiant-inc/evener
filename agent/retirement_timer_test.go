package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/clock"
)

// retirementAckClock is a FakeClock whose timer arming is fully observable and
// whose timer firing is fully test-driven. Virtual time still advances only
// via Advance (eligibleSince/deadline arithmetic depends on Now), but the
// timers it hands out never fire on their own: the test delivers each tick
// explicitly with fire, including stale ticks delivered after a disarm — the
// exact race a daemon-owned timer must survive by re-proving eligibility
// instead of trusting the tick.
type retirementAckClock struct {
	*agenttest.FakeClock
	mu         sync.Mutex
	arms       chan time.Duration
	disarms    chan struct{}
	timers     []*retirementAckTimer
	creates    int
	gateMu     sync.Mutex
	gateNext   bool
	nowEntered chan struct{}
	nowRelease chan struct{}
}

func newRetirementAckClock() *retirementAckClock {
	return &retirementAckClock{
		FakeClock: agenttest.NewFakeClock(),
		arms:      make(chan time.Duration, 32),
		disarms:   make(chan struct{}, 32),
	}
}

func (c *retirementAckClock) NewTimer(d time.Duration) clock.Timer {
	now := c.FakeClock.Now()
	tm := &retirementAckTimer{clk: c, c: make(chan time.Time, 1), deadline: now.Add(d)}
	c.mu.Lock()
	c.creates++
	c.timers = append(c.timers, tm)
	c.mu.Unlock()
	c.arms <- d
	return tm
}

func (c *retirementAckClock) Now() time.Time {
	now := c.FakeClock.Now()
	c.gateMu.Lock()
	gate := c.gateNext
	if gate {
		c.gateNext = false
	}
	entered := c.nowEntered
	release := c.nowRelease
	c.gateMu.Unlock()
	if gate {
		close(entered)
		<-release
	}
	return now
}

func (c *retirementAckClock) gateNextNow() {
	c.gateMu.Lock()
	c.gateNext = true
	c.nowEntered = make(chan struct{})
	c.nowRelease = make(chan struct{})
	c.gateMu.Unlock()
}

func (c *retirementAckClock) awaitNow(t *testing.T) {
	t.Helper()
	c.gateMu.Lock()
	entered := c.nowEntered
	c.gateMu.Unlock()
	select {
	case <-entered:
	case <-time.After(10 * time.Second): // TRIPWIRE: the gate is released by the controller's next evaluation.
		t.Fatal("controller did not reach the gated Now call")
	}
}

func (c *retirementAckClock) releaseNow() {
	c.gateMu.Lock()
	release := c.nowRelease
	c.gateMu.Unlock()
	close(release)
}

func (c *retirementAckClock) Advance(d time.Duration) {
	c.FakeClock.Advance(d)
	now := c.FakeClock.Now()
	c.mu.Lock()
	for _, tm := range c.timers {
		if !now.Before(tm.deadline) {
			tm.fired = true
		}
	}
	c.mu.Unlock()
}

func (c *retirementAckClock) timerFired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers) != 0 && c.timers[len(c.timers)-1].fired
}

func (c *retirementAckClock) timerCreates() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creates
}

// fire delivers one tick to the most recently created timer, whether or not
// the controller still considers it armed: a tick already in flight when a
// disarm lands is indistinguishable from a fresh one on the wire.
func (c *retirementAckClock) fire(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	if len(c.timers) == 0 {
		c.mu.Unlock()
		t.Fatal("fire with no timer created")
	}
	tm := c.timers[len(c.timers)-1]
	c.mu.Unlock()
	select {
	case tm.c <- c.Now():
	case <-time.After(10 * time.Second): // TRIPWIRE: fake-clock tests drive the timer channel synchronously; a hang here means Run stopped selecting.
		t.Fatal("timer channel never drained; Run is not selecting on it")
	}
}

func (c *retirementAckClock) awaitArm(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-c.arms:
		return d
	case <-time.After(10 * time.Second): // TRIPWIRE: arm delivery is synchronous on the ack channel; the expected arm is virtual-time, not wall-time.
		t.Fatal("no timer arm observed")
		return 0
	}
}

func (c *retirementAckClock) awaitDisarm(t *testing.T) {
	t.Helper()
	select {
	case <-c.disarms:
	case <-time.After(10 * time.Second): // TRIPWIRE: disarm delivery is synchronous on the ack channel; the expected disarm is virtual-time, not wall-time.
		t.Fatal("no timer disarm observed")
	}
}

// assertNoArmWithin fails on any arm during the grace window; disarms are
// always legal. The grace is wall time, not virtual — the same "a grace, not
// a race" pattern as serve_shutdown_budget_test.go.
func (c *retirementAckClock) assertNoArmWithin(t *testing.T, grace time.Duration) {
	t.Helper()
	deadline := time.After(grace)
	for {
		select {
		case d := <-c.arms:
			t.Fatalf("unexpected timer arm %v", d)
		case <-c.disarms:
		case <-deadline:
			return
		}
	}
}

func (c *retirementAckClock) assertNoArm(t *testing.T) {
	t.Helper()
	select {
	case d := <-c.arms:
		t.Fatalf("unexpected timer arm %v", d)
	default:
	}
}

type retirementAckTimer struct {
	clk      *retirementAckClock
	c        chan time.Time
	deadline time.Time
	fired    bool
}

func (t *retirementAckTimer) C() <-chan time.Time { return t.c }

func (t *retirementAckTimer) Reset(d time.Duration) bool {
	now := t.clk.FakeClock.Now()
	t.clk.mu.Lock()
	t.deadline = now.Add(d)
	t.fired = false
	t.clk.mu.Unlock()
	t.clk.arms <- d
	return true
}

func (t *retirementAckTimer) Stop() bool {
	t.clk.disarms <- struct{}{}
	return true
}

// retirementRunHarness drives one controller Run with a scripted consumer.
// Consumer invocations are announced on calls so the test can sequence
// virtual-time advances against them.
type retirementRunHarness struct {
	ctrl  *RetirementController
	calls chan *RetirementClaim
	done  chan error
}

func startRetirementRun(t *testing.T, ctrl *RetirementController, consume func(context.Context, *RetirementClaim) error) *retirementRunHarness {
	t.Helper()
	h := &retirementRunHarness{
		ctrl:  ctrl,
		calls: make(chan *RetirementClaim, 8),
		done:  make(chan error, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		h.done <- ctrl.Run(ctx, func(runCtx context.Context, claim *RetirementClaim) error {
			h.calls <- claim
			return consume(runCtx, claim)
		})
	}()
	return h
}

func (h *retirementRunHarness) awaitCall(t *testing.T) *RetirementClaim {
	t.Helper()
	select {
	case c := <-h.calls:
		return c
	case <-time.After(10 * time.Second): // TRIPWIRE: the consumer call follows a test-driven tick with no wall-clock dependency.
		t.Fatal("retire consumer not called")
		return nil
	}
}

func (h *retirementRunHarness) assertNoCall(t *testing.T, grace time.Duration) {
	t.Helper()
	select {
	case <-h.calls:
		t.Fatal("unexpected retire consumer call")
	case <-time.After(grace):
	}
}

// awaitPhase tripwire-polls until the controller reaches phase, so a test
// never ends (and its TempDir is never reaped) while the consumer is still
// preparing durably on the Run goroutine.
func (h *retirementRunHarness) awaitPhase(t *testing.T, phase string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for h.ctrl.Snapshot().Phase != phase {
		if time.Now().After(deadline) {
			t.Fatalf("phase = %q, want %q", h.ctrl.Snapshot().Phase, phase)
		}
		time.Sleep(time.Millisecond)
	}
}

// commitClaim is the well-behaved consumer: prepare durably, then close
// admission. The daemon's real consumer adds root settlement and release on
// top; the timer contract only needs claim semantics.
func commitClaim(ctrl *RetirementController) func(context.Context, *RetirementClaim) error {
	return func(ctx context.Context, claim *RetirementClaim) error {
		if _, err := ctrl.Prepare(ctx, claim); err != nil {
			_ = ctrl.Abort(claim, "prepare_failed")
			return err
		}
		return ctrl.Commit(claim)
	}
}

// TestRetirementTimerFirstSettledStateArmsFullInterval pins the primary
// contract: the first moment the process becomes fully settled starts a full
// timeout interval — never a leftover fraction — a tick before the deadline
// retires nothing, and a tick at the deadline hands the consumer a claim on
// the attached root.
func TestRetirementTimerFirstSettledStateArmsFullInterval(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("first settled arm = %v, want the full 1h interval", d)
	}
	// Half-way tick: nothing is eligible yet; the timer recomputes the
	// remaining interval instead of retiring.
	clk.Advance(30 * time.Minute)
	clk.fire(t)
	h.assertNoCall(t, 100*time.Millisecond)
	if d := clk.awaitArm(t); d != 30*time.Minute {
		t.Fatalf("re-arm after early tick = %v, want the 30m remainder", d)
	}
	clk.Advance(30 * time.Minute)
	clk.fire(t)
	claim := h.awaitCall(t)
	if claim.root != root {
		t.Fatalf("claim root = %p, want the attached root %p", claim.root, root)
	}
	h.awaitPhase(t, "retiring")
}

// TestRetirementTimerBlockedDurationNeverAccruesEligibility proves an
// admission lease freezes the interval: held time does not count, a stale
// tick delivered while leased retires nothing, and release starts a fresh
// full interval from the release instant.
func TestRetirementTimerBlockedDurationNeverAccruesEligibility(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	release, err := h.ctrl.BeginMutation(root.ID(), "turn")
	if err != nil {
		t.Fatalf("BeginMutation: %v", err)
	}
	clk.awaitDisarm(t)
	// Two full timeouts pass under the lease; nothing may arm or fire.
	clk.Advance(2 * time.Hour)
	clk.assertNoArmWithin(t, 100*time.Millisecond)
	clk.fire(t) // stale in-flight tick from the disarmed timer
	h.assertNoCall(t, 100*time.Millisecond)

	release()
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("re-arm after release = %v, want a fresh full 1h interval", d)
	}
	if got := h.ctrl.Snapshot().EligibleSince; !got.Equal(clk.Now()) {
		t.Fatalf("eligibleSince = %v, want the release instant %v (held time must not accrue)", got, clk.Now())
	}
	clk.Advance(time.Hour)
	clk.fire(t)
	h.awaitCall(t)
	h.awaitPhase(t, "retiring")
}

// TestRetirementTimerStatusTrafficLeavesDeadlineUnchanged proves reads —
// status snapshots and reader borrows — neither reset nor extend the running
// interval. The Hub polls status constantly; if that traffic refreshed
// eligibility the timer would never fire.
func TestRetirementTimerStatusTrafficLeavesDeadlineUnchanged(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	for range 3 {
		_ = h.ctrl.Snapshot()
	}
	unborrow, err := h.ctrl.Borrow()
	if err != nil {
		t.Fatalf("Borrow: %v", err)
	}
	unborrow()
	clk.assertNoArmWithin(t, 100*time.Millisecond)

	clk.Advance(59 * time.Minute)
	clk.fire(t)
	h.assertNoCall(t, 100*time.Millisecond)
	if d := clk.awaitArm(t); d != time.Minute {
		t.Fatalf("re-arm = %v, want the 1m remainder — status traffic must not move the deadline", d)
	}
	clk.Advance(time.Minute)
	clk.fire(t)
	h.awaitCall(t)
	h.awaitPhase(t, "retiring")
}

// TestRetirementTimerZeroTimeoutCreatesNoExpiryTimer pins the disabled-state
// contract: with a zero timeout the controller still tracks eligibility for
// diagnostics, but no expiry timer is ever created and no automatic claim
// happens, however far time advances.
func TestRetirementTimerZeroTimeoutCreatesNoExpiryTimer(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(0, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	// Tripwire: wait until the controller has observed eligibility before
	// asserting no timer exists, so a slow Run cannot mask a late NewTimer.
	deadline := time.Now().Add(10 * time.Second)
	for h.ctrl.Snapshot().EligibleSince.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("eligibleSince never set with zero timeout; eligibility diagnostics lost")
		}
		time.Sleep(time.Millisecond)
	}
	if n := clk.timerCreates(); n != 0 {
		t.Fatalf("zero timeout created %d expiry timers, want 0", n)
	}
	clk.assertNoArmWithin(t, 100*time.Millisecond)
	clk.Advance(100 * time.Hour)
	h.assertNoCall(t, 200*time.Millisecond)
	if snap := h.ctrl.Snapshot(); !snap.Deadline.IsZero() {
		t.Fatalf("zero timeout must not compute a deadline, got %v", snap.Deadline)
	}
}

// TestRetirementTimerAttachRootRestartsInterval proves publishing a new root
// (thread/clear) restarts the interval from scratch and the eventual claim is
// bound to the NEW root, not the replaced one.
func TestRetirementTimerAttachRootRestartsInterval(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	var h *retirementRunHarness
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h = startRetirementRun(t, ctrl, commitClaim(ctrl))
	root1 := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root1); err != nil {
		t.Fatalf("AttachRoot(root1): %v", err)
	}
	clk.awaitArm(t)

	clk.Advance(30 * time.Minute)
	root2 := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root2); err != nil {
		t.Fatalf("AttachRoot(root2): %v", err)
	}
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("arm after root swap = %v, want a fresh full 1h interval", d)
	}
	// The OLD root's deadline (t0+1h) passes half-way through the new
	// interval: quiet recompute, no claim.
	clk.Advance(30 * time.Minute)
	clk.fire(t)
	h.assertNoCall(t, 100*time.Millisecond)
	if d := clk.awaitArm(t); d != 30*time.Minute {
		t.Fatalf("re-arm = %v, want the 30m remainder of the new interval", d)
	}
	clk.Advance(30 * time.Minute)
	clk.fire(t)
	claim := h.awaitCall(t)
	if claim.root != root2 {
		t.Fatalf("claim root = %p, want the replacement root %p", claim.root, root2)
	}
	h.awaitPhase(t, "retiring")
}

// TestRetirementTimerStaleTickRecomputes isolates the in-flight-tick race: a
// tick delivered after a lease disarmed its timer must be re-proven against
// live controller state, and the re-arm after release reflects a fresh
// interval from the release instant, not the stale deadline.
func TestRetirementTimerStaleTickRecomputes(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	clk.Advance(59 * time.Minute)
	release, err := h.ctrl.BeginMutation(root.ID(), "job")
	if err != nil {
		t.Fatalf("BeginMutation: %v", err)
	}
	clk.awaitDisarm(t)
	clk.Advance(2 * time.Minute) // the original t0+1h deadline passes leased
	clk.fire(t)                  // stale tick, delivered after the disarm
	h.assertNoCall(t, 100*time.Millisecond)

	release()
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("re-arm after stale tick = %v, want a fresh full 1h from release", d)
	}
	clk.Advance(time.Hour)
	clk.fire(t)
	h.awaitCall(t)
	h.awaitPhase(t, "retiring")
}

// TestRetirementTimerAdvanceAfterEvaluationSnapshot preserves the absolute
// eligibility deadline when virtual time advances between clock.Now and the
// timer Reset. The current relative timer seam fails this intentionally gated
// interleaving by anchoring the stale one-hour duration at the later time.
func TestRetirementTimerAdvanceAfterEvaluationSnapshot(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("initial arm = %v, want 1h", d)
	}

	// The second evaluation snapshots t0 and stops before it can reset the
	// timer. The test then advances to t0+30m, exactly between those calls.
	clk.gateNextNow()
	ctrl.Changed()
	clk.awaitNow(t)
	clk.Advance(30 * time.Minute)
	clk.releaseNow()

	// A third gated evaluation is the completion barrier for the raced one:
	// Run cannot reach this Now call until it has finished computing and
	// arming (or deliberately retaining) the previous deadline. A duplicate
	// Reset is therefore observable before the barrier is released, while a
	// correct unchanged-deadline path leaves no arm event behind.
	clk.gateNextNow()
	ctrl.Changed()
	clk.awaitNow(t)
	clk.assertNoArm(t)
	clk.releaseNow()

	// Let the barrier evaluation complete before checking the absolute
	// deadline. This keeps the assertion independent of wall-clock scheduling.
	clk.gateNextNow()
	ctrl.Changed()
	clk.awaitNow(t)
	clk.assertNoArm(t)
	clk.releaseNow()

	// The eligibility deadline is t0+1h. A correct absolute-deadline
	// implementation therefore fires after this second 30-minute advance.
	clk.Advance(30 * time.Minute)
	if !clk.timerFired() {
		t.Fatalf("timer did not fire at the absolute eligibility deadline")
	}
}

// TestRetirementTimerPreparationFailureRearmsFreshInterval proves a REAL
// preparation failure (a canceled preparation context rejected by
// validateRetirementRestore) is survivable: the consumer aborts the claim,
// Run stays alive, and the re-arm is a fresh full interval that later
// produces a second, successful claim.
func TestRetirementTimerPreparationFailureRearmsFreshInterval(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	calls := 0
	consume := func(runCtx context.Context, claim *RetirementClaim) error {
		calls++
		if calls == 1 {
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := ctrl.Prepare(canceled, claim); err == nil {
				t.Errorf("Prepare with a canceled context must fail")
			}
			if err := ctrl.Abort(claim, "prepare_failed"); err != nil {
				t.Errorf("Abort: %v", err)
			}
			return errors.New("prepare failed")
		}
		if _, err := ctrl.Prepare(runCtx, claim); err != nil {
			return err
		}
		return ctrl.Commit(claim)
	}
	h := startRetirementRun(t, ctrl, consume)
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	clk.Advance(time.Hour)
	clk.fire(t)
	h.awaitCall(t) // first consumer: real prepare failure, abort, error return
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("re-arm after preparation failure = %v, want a fresh full 1h interval", d)
	}
	if got := h.ctrl.Snapshot().Failure; got != "prepare_failed" {
		t.Fatalf("failure = %q, want prepare_failed recorded", got)
	}

	clk.Advance(time.Hour)
	clk.fire(t)
	h.awaitCall(t) // second consumer commits
	h.awaitPhase(t, "retiring")
	if calls != 2 {
		t.Fatalf("consumer calls = %d, want 2", calls)
	}
}

// TestRetirementTimerRunStopsWithContext proves the timer loop is scoped to
// its context: cancellation returns nil, disarms the timer, and no later
// virtual-time advance can retire anything.
func TestRetirementTimerRunStopsWithContext(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ctrl.Run(ctx, commitClaim(ctrl))
	}()
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on context cancellation", err)
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: Run must return promptly once its context is canceled; synchronization is the done channel.
		t.Fatal("Run did not stop with its context")
	}
	clk.awaitDisarm(t)
	clk.Advance(100 * time.Hour)
	if got := ctrl.Snapshot().Phase; got != "resident" {
		t.Fatalf("phase = %q after Run stop, want resident (no claim taken)", got)
	}
}

// TestRetirementTimerRefusalRearmsIdleInterval proves a refused TryClaim
// re-arms the idle interval. The refusal drops eligibleSince and returns to the
// resident phase, exactly like Abort/AttachRoot/BeginMutation; the controller
// must therefore wake Run so it re-evaluates the now-settled process and starts
// a fresh full interval. Without that change notification Run stays parked on
// its existing timer, no arm is observed, and awaitArm tripwires — the deadline
// would otherwise stay un-armed until an unrelated event forced evaluation.
func TestRetirementTimerRefusalRearmsIdleInterval(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("first settled arm = %v, want the full 1h interval", d)
	}

	// A queued input is an evidence obligation the root reports without being
	// an admission lease, so TryClaim takes the full evidence path and refuses.
	root.mu.Lock()
	root.inputQueue = []queuedInput{{ID: "held", Text: "held input"}}
	root.mu.Unlock()
	clk.Advance(time.Minute)

	claim, state, err := h.ctrl.TryClaim(true)
	if err != nil {
		t.Fatalf("TryClaim: %v", err)
	}
	if claim != nil {
		t.Fatalf("queued input did not refuse the claim: %+v", state)
	}
	if !hasRetirementBlocker(state.Blockers, "input") {
		t.Fatalf("refusal lost the queued-input blocker: %+v", state)
	}

	// Run must have been woken to re-evaluate and arm a fresh full interval
	// from the refusal instant.
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("re-arm after refused claim = %v, want a fresh full 1h interval", d)
	}
	if since := h.ctrl.Snapshot().EligibleSince; since.IsZero() {
		t.Fatal("Run did not re-establish eligibility after the refusal")
	}
}

// TestRetirementRetargetShortensArmedInterval proves a runtime deadline change
// reaches the armed timer: Retarget(1m) on a controller armed for 1h re-arms to
// the shortened interval from the same settled instant, and a tick at the new
// deadline claims.
func TestRetirementRetargetShortensArmedInterval(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("first settled arm = %v, want the full 1h interval", d)
	}

	if err := h.ctrl.Retarget(time.Minute); err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if d := clk.awaitArm(t); d != time.Minute {
		t.Fatalf("arm after shorten = %v, want the 1m interval", d)
	}
	if got := h.ctrl.Snapshot().Timeout; got != time.Minute {
		t.Fatalf("snapshot timeout = %v, want 1m", got)
	}
	clk.Advance(time.Minute)
	clk.fire(t)
	h.awaitCall(t)
	h.awaitPhase(t, "retiring")
}

// TestRetirementRetargetBelowElapsedIdleFiresAtOnce proves shortening below
// already-elapsed idle time does not wait out the new interval: the re-arm
// clamps to now and the very next tick claims.
func TestRetirementRetargetBelowElapsedIdleFiresAtOnce(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)
	clk.Advance(30 * time.Minute)

	if err := h.ctrl.Retarget(time.Minute); err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if d := clk.awaitArm(t); d != 0 {
		t.Fatalf("arm after below-elapsed shorten = %v, want 0 (deadline already passed)", d)
	}
	clk.fire(t)
	h.awaitCall(t)
	h.awaitPhase(t, "retiring")
}

// TestRetirementRetargetLengthenSurvivesStaleTick proves the lengthen path:
// the re-arm reflects the longer remaining interval, and a stale tick from the
// pre-lengthen timer — fired before the re-arm, delivered after — retires
// nothing because the claim re-proves the deadline against the new timeout.
func TestRetirementRetargetLengthenSurvivesStaleTick(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Minute, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)
	clk.Advance(30 * time.Second)

	if err := h.ctrl.Retarget(time.Hour); err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	wantRemaining := 59*time.Minute + 30*time.Second
	if d := clk.awaitArm(t); d != wantRemaining {
		t.Fatalf("arm after lengthen = %v, want %v", d, wantRemaining)
	}
	// Stale tick: the 1m timer's deadline (t0+1m) has not passed yet under the
	// new deadline (t0+1h); the claim must re-prove and decline.
	clk.fire(t)
	h.assertNoCall(t, 100*time.Millisecond)
	if d := clk.awaitArm(t); d != wantRemaining {
		t.Fatalf("re-arm after stale tick = %v, want %v", d, wantRemaining)
	}
	clk.Advance(wantRemaining)
	clk.fire(t)
	h.awaitCall(t)
	h.awaitPhase(t, "retiring")
}

// TestRetirementRetargetZeroDisarms proves retargeting to zero restores the
// disabled state: the timer disarms, no later arm or claim happens, and the
// snapshot reports the zero timeout.
func TestRetirementRetargetZeroDisarms(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	if err := h.ctrl.Retarget(0); err != nil {
		t.Fatalf("Retarget(0): %v", err)
	}
	clk.awaitDisarm(t)
	clk.assertNoArmWithin(t, 100*time.Millisecond)
	if got := h.ctrl.Snapshot().Timeout; got != 0 {
		t.Fatalf("snapshot timeout = %v, want 0", got)
	}
	clk.Advance(100 * time.Hour)
	clk.fire(t) // stale in-flight tick from the disarmed timer
	h.assertNoCall(t, 200*time.Millisecond)
}

// TestRetirementRetargetRejectsNegative proves a negative deadline is refused
// without disturbing the armed interval: the error returns, the snapshot keeps
// the old timeout, and the original deadline still claims.
func TestRetirementRetargetRejectsNegative(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	h := startRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := h.ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	if err := h.ctrl.Retarget(-time.Minute); err == nil {
		t.Fatal("Retarget accepted a negative timeout")
	}
	if got := h.ctrl.Snapshot().Timeout; got != time.Hour {
		t.Fatalf("snapshot timeout = %v, want the unchanged 1h", got)
	}
	clk.assertNoArmWithin(t, 100*time.Millisecond)
	clk.Advance(time.Hour)
	clk.fire(t)
	h.awaitCall(t)
	h.awaitPhase(t, "retiring")
}

// TestRetirementRetargetRefusesWhilePreparing proves Retarget is fenced by the
// admission phase like every other controller entry point: a preparing or
// retiring controller refuses, and the refusal clears once the claim settles.
func TestRetirementRetargetRefusesWhilePreparing(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	claim, _, err := ctrl.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("TryClaim(true): claim=%v err=%v", claim, err)
	}
	if got := ctrl.Snapshot().Phase; got != "preparing" {
		t.Fatalf("phase = %q, want preparing", got)
	}
	if err := ctrl.Retarget(time.Minute); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("Retarget while preparing = %v, want ErrRetirementUnavailable", err)
	}
	if got := ctrl.Snapshot().Timeout; got != time.Hour {
		t.Fatalf("refused Retarget changed timeout to %v", got)
	}
	if err := ctrl.Abort(claim, "test_done"); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if err := ctrl.Retarget(time.Minute); err != nil {
		t.Fatalf("Retarget after abort: %v", err)
	}
	if got := ctrl.Snapshot().Timeout; got != time.Minute {
		t.Fatalf("snapshot timeout = %v, want 1m after abort", got)
	}
}

// TestRetirementStampedRetargetUndo proves the write-token undo contract:
// RetargetStamped reports the deadline it replaced and a token naming its own
// write; UndoRetarget restores the replaced deadline only while that write is
// still the newest — a later write, even one choosing the same deadline,
// supersedes the token, because two writers can legitimately choose the same
// value and identity, not equality, must decide which write the deadline
// currently belongs to.
func TestRetirementStampedRetargetUndo(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}

	prev, token, err := ctrl.RetargetStamped(time.Minute)
	if err != nil {
		t.Fatalf("RetargetStamped: %v", err)
	}
	if prev != time.Hour {
		t.Fatalf("first stamped write reported prev = %v, want the constructed 1h", prev)
	}
	if token == 0 {
		t.Fatal("first stamped write minted token 0, want a nonzero write token")
	}
	if !ctrl.UndoRetarget(token, prev) {
		t.Fatal("UndoRetarget with the newest token = false, want true")
	}
	if got := ctrl.Snapshot().Timeout; got != time.Hour {
		t.Fatalf("timeout after undo = %v, want the restored 1h", got)
	}

	// An aliased second write chooses the deadline the first one set; the
	// third write supersedes the second's token, and the second's undo must
	// not reach past either.
	prev2, token2, err := ctrl.RetargetStamped(time.Minute)
	if err != nil {
		t.Fatalf("second stamped write: %v", err)
	}
	if prev2 != time.Hour {
		t.Fatalf("second stamped write prev = %v, want 1h", prev2)
	}
	if token2 == token {
		t.Fatalf("aliased second write minted the first write's token %d", token2)
	}
	_, token3, err := ctrl.RetargetStamped(2 * time.Minute)
	if err != nil {
		t.Fatalf("third stamped write: %v", err)
	}
	if ctrl.UndoRetarget(token2, prev2) {
		t.Fatal("UndoRetarget with a superseded token = true, want false")
	}
	if got := ctrl.Snapshot().Timeout; got != 2*time.Minute {
		t.Fatalf("timeout after refused undo = %v, want the later write's 2m", got)
	}
	if !ctrl.UndoRetarget(token3, time.Minute) {
		t.Fatal("UndoRetarget with the newest token = false, want true")
	}
	if got := ctrl.Snapshot().Timeout; got != time.Minute {
		t.Fatalf("timeout after undo = %v, want the restored 1m", got)
	}
}

// TestRetirementUndoRetargetRefusesWhilePreparing proves the undo is fenced
// by the admission phase like Retarget: a preparing controller never
// restores, and the token survives the aborted claim so the same undo works
// once the controller is resident again.
func TestRetirementUndoRetargetRefusesWhilePreparing(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	_, token, err := ctrl.RetargetStamped(time.Minute)
	if err != nil {
		t.Fatalf("RetargetStamped: %v", err)
	}
	claim, _, err := ctrl.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("TryClaim(true): claim=%v err=%v", claim, err)
	}
	if ctrl.UndoRetarget(token, time.Hour) {
		t.Fatal("UndoRetarget while preparing = true, want false")
	}
	if got := ctrl.Snapshot().Timeout; got != time.Minute {
		t.Fatalf("timeout after refused undo = %v, want the undo-untouched 1m", got)
	}
	if err := ctrl.Abort(claim, "test_done"); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if !ctrl.UndoRetarget(token, time.Hour) {
		t.Fatal("UndoRetarget after abort = false, want true")
	}
	if got := ctrl.Snapshot().Timeout; got != time.Hour {
		t.Fatalf("timeout after undo = %v, want the restored 1h", got)
	}
}

// TestRetirementAttachRootResetsDeadlineToConfigured proves a fresh root
// starts from the configured deadline, not whatever the predecessor's
// archive decision left armed: attaching the replacement root restores the
// constructed timeout and mints a write of its own, so a predecessor-era
// token can never undo past the reset.
func TestRetirementAttachRootResetsDeadlineToConfigured(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	_, token, err := ctrl.RetargetStamped(time.Minute)
	if err != nil {
		t.Fatalf("RetargetStamped: %v", err)
	}
	replacement := newQueuePersistTestSession(t, t.TempDir())
	if err := ctrl.AttachRoot(replacement); err != nil {
		t.Fatalf("AttachRoot for the replacement: %v", err)
	}
	if got := ctrl.Snapshot().Timeout; got != time.Hour {
		t.Fatalf("replacement root deadline = %v, want the configured 1h", got)
	}
	if ctrl.UndoRetarget(token, time.Minute) {
		t.Fatal("UndoRetarget with a predecessor-era token = true, want false")
	}
	if got := ctrl.Snapshot().Timeout; got != time.Hour {
		t.Fatalf("deadline after refused undo = %v, want the configured 1h", got)
	}
}

// TestRetirementAttachRootResetsDisabledDeadline proves the root-swap reset
// also restores the disabled (zero) configured baseline: an archived
// predecessor's shortened deadline must not outlive the Hub-wide disable the
// replacement was configured with.
func TestRetirementAttachRootResetsDisabledDeadline(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(0, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	root := newQueuePersistTestSession(t, t.TempDir())
	if err := ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	if _, _, err := ctrl.RetargetStamped(time.Minute); err != nil {
		t.Fatalf("RetargetStamped: %v", err)
	}
	replacement := newQueuePersistTestSession(t, t.TempDir())
	if err := ctrl.AttachRoot(replacement); err != nil {
		t.Fatalf("AttachRoot for the replacement: %v", err)
	}
	if got := ctrl.Snapshot().Timeout; got != 0 {
		t.Fatalf("replacement root deadline = %v, want the disabled 0", got)
	}
}

// TestRetirementUndoRetargetRejectsUnguardedWrites proves the undo's own
// write path is fenced like Retarget: token 0 never names a real write, and a
// negative restore target is refused — neither may bypass the stamped-write
// validation.
func TestRetirementUndoRetargetRejectsUnguardedWrites(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	if ctrl.UndoRetarget(0, 5*time.Minute) {
		t.Fatal("UndoRetarget with token 0 = true, want false")
	}
	if got := ctrl.Snapshot().Timeout; got != time.Hour {
		t.Fatalf("timeout after token-0 undo = %v, want the unchanged 1h", got)
	}
	_, token, err := ctrl.RetargetStamped(time.Minute)
	if err != nil {
		t.Fatalf("RetargetStamped: %v", err)
	}
	if ctrl.UndoRetarget(token, -time.Minute) {
		t.Fatal("UndoRetarget with a negative target = true, want false")
	}
	if got := ctrl.Snapshot().Timeout; got != time.Minute {
		t.Fatalf("timeout after refused negative undo = %v, want the unchanged 1m", got)
	}
}
