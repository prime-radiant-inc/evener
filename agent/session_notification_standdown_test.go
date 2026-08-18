package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/appwire"
)

// standDownHarness is a served session on a fake clock with a notify counter,
// which is everything the notification stand-down's two failure paths need: the
// stand-down's whole output is "did it kick the serve loop, and when".
type standDownHarness struct {
	sess  *Session
	clock *agenttest.FakeClock

	// woken receives one value per notify(). The fake clock runs an AfterFunc
	// callback on its own goroutine (matching time.AfterFunc), so a retried wake
	// has to be waited for rather than counted; wakes covers the immediate case,
	// which the stand-down fires inline on the caller's goroutine.
	woken chan struct{}

	// drained receives one value per drain barrier the consumer observes; see
	// warningsMatching.
	drained chan struct{}

	// warned carries every delivered warning message. warningsMatching's
	// barrier only makes counts exact for warnings the TEST goroutine emitted
	// before it; a warning emitted from a clock callback has no such ordering,
	// so a test that waits for one waits here. Sends drop when full, which no
	// test in this file comes close to.
	warned chan string

	mu       sync.Mutex
	wakes    int
	warnings []string
}

func newStandDownHarness(t *testing.T) *standDownHarness {
	t.Helper()
	h := &standDownHarness{
		clock:   agenttest.NewFakeClock(),
		woken:   make(chan struct{}, 64),
		drained: make(chan struct{}, 1),
		warned:  make(chan string, 64),
	}
	h.sess = newTestSessionForEnvctx(t)
	h.sess.clock = h.clock
	// A real drain, not a flag: ConsumeEventsLossless is the only writer of
	// authoritativeConsumer, and an unserved session never stands down at all.
	h.sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		// The drain barrier warningsMatching emits: nothing else in these
		// tests loads a prompt, so every one of these is a barrier.
		if ev.Kind == events.EventPromptLoaded {
			h.drained <- struct{}{}
			return
		}
		if ev.Kind != events.EventWarning {
			return
		}
		data, ok := ev.Data.(events.WarningData)
		if !ok {
			return
		}
		h.mu.Lock()
		h.warnings = append(h.warnings, data.Message)
		h.mu.Unlock()
		// After the append, so a test that returns from awaitWarning can read
		// an exact count immediately.
		select {
		case h.warned <- data.Message:
		default:
		}
	}, func() {})
	h.sess.SetNotifyFunc(func() {
		h.mu.Lock()
		h.wakes++
		h.mu.Unlock()
		select {
		case h.woken <- struct{}{}:
		default:
			t.Error("more wakes than this harness can hold; the stand-down is spinning")
		}
	})
	if err := h.sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	return h
}

func (h *standDownHarness) wakeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wakes
}

// warningsMatching counts delivered warnings containing substr. A warning
// travels emit -> the session's buffered event channel -> the
// ConsumeEventsLossless drain goroutine, so the count trails the emit: a
// buffered send never yields the processor, and at GOMAXPROCS=1 the drain
// goroutine stays runnable-but-unscheduled while the test goroutine reads a
// stale count. The barrier rides the same FIFO stream, so once the consumer
// hands it back every warning emitted before this call has been counted --
// which makes the counts exact, not merely eventual.
func (h *standDownHarness) warningsMatching(substr string) int {
	h.sess.emit(events.EventPromptLoaded, events.PromptLoadedData{Label: "warningsMatching drain barrier"})
	<-h.drained
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, w := range h.warnings {
		if strings.Contains(w, substr) {
			n++
		}
	}
	return n
}

// awaitWarning blocks until a delivered warning contains substr. A blocking
// receive is the assertion: the package test timeout fails the run if the
// warning never comes.
func (h *standDownHarness) awaitWarning(t *testing.T, substr string) {
	t.Helper()
	for message := range h.warned {
		if strings.Contains(message, substr) {
			return
		}
	}
}

// seedOwnedRunningTurn puts the durable slot in the state a live turn/start
// leaves behind: a name, and a pending execution that claims it.
func (h *standDownHarness) seedOwnedRunningTurn(t *testing.T) {
	t.Helper()
	if err := h.sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		turnID := appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		snapshot.ActiveTurnID = turnID
		snapshot.PendingExecutions["cm-owner"] = appwire.PendingMutation{
			ClientMutationID: "cm-owner",
			Method:           clientMutationMethodStart,
			ExecutionState:   "accepted",
			TurnID:           turnID,
			ProjectionState:  appwire.MutationProjectionPending,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed an owned running turn: %v", err)
	}
	if !h.sess.runningTurnNameHasOwner() {
		t.Fatal("seeded slot has no owner; the test needs one")
	}
}

// failWrites makes every later durable mutation fail, which is the precondition
// both of this kata's paths need and neither can be reached without.
func (h *standDownHarness) failWrites(t *testing.T) error {
	t.Helper()
	injected := errors.New("injected client mutation write failure")
	h.sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error { return injected }
	return injected
}

// standDown drives one notification wake through the serve loop's shape: the
// loop dequeues the wake, hands it to the session, and the session decides
// whether to ask for another.
func (h *standDownHarness) standDown(t *testing.T) {
	t.Helper()
	if _, err := h.sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}
}

// TestNotificationStandDownDoesNotSpinWhileWritesFail is kata ajg5's first
// path. runningTurnNameHasOwner reads a matching pending execution as proof the
// name will be handed back, so the stand-down kicks the serve loop immediately.
// With store writes failing the pending turn can neither be claimed nor
// released, so the answer stays "owned" forever: the loop dequeues the wake,
// stands down, kicks, and dequeues again — the exact hot loop the owner check
// was added to prevent, reached by a different route.
//
// The loop below IS the serve loop: each iteration is one dequeue of a wake the
// previous stand-down asked for. A kick per iteration means it never terminates.
func TestNotificationStandDownDoesNotSpinWhileWritesFail(t *testing.T) {
	h := newStandDownHarness(t)
	h.seedOwnedRunningTurn(t)
	h.failWrites(t)

	const serveLoopIterations = 10
	timersBefore := h.clock.BlockedCount()
	for range serveLoopIterations {
		h.standDown(t)
	}

	if got := h.wakeCount(); got != 0 {
		t.Fatalf("stand-downs kicked the serve loop %d times immediately (want 0): every kick is another dequeue of a wake that will stand down again, which is the spin",
			got)
	}
	if got := h.clock.BlockedCount(); got != timersBefore+1 {
		t.Fatalf("armed retry timers = %d, want baseline %d plus exactly one: repeated stand-downs must coalesce into one paced wake",
			got, timersBefore)
	}

	// Paced, not dropped: the wake still arrives. A blocking receive is the
	// assertion -- the package test timeout fails the run if it never comes.
	h.clock.Advance(jobNotificationRetryInitialDelay)
	<-h.woken
	if got := h.wakeCount(); got != 1 {
		t.Fatalf("wakes after the retry delay = %d, want exactly 1: the coalesced stand-downs owe one wake, not %d", got, got)
	}
}

// TestNotificationStandDownRetriesAfterAStorageFailure is kata ajg5's second
// path. mintRunningTurnID returns "" for a store that would not take the write
// just as it does for a name someone else holds, so the stand-down read a
// disk failure as a stale name: it warned and did NOT re-arm. Nothing else
// would — rootAttentionWake is still set, which is exactly what suppresses
// armRootDelegateAttention's notify and scheduleRootAttentionRetryLocked — so
// one transient write error left delegate attention waiting on a wake that was
// never coming.
func TestNotificationStandDownRetriesAfterAStorageFailure(t *testing.T) {
	h := newStandDownHarness(t)
	h.failWrites(t)

	// No name is held: only the failing write stops this turn being named.
	if got := h.sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q, want empty: this path is about the store, not the name", got)
	}

	timersBefore := h.clock.BlockedCount()
	h.standDown(t)

	if got := h.clock.BlockedCount(); got != timersBefore+1 {
		t.Fatalf("armed retry timers = %d, want baseline %d plus one: a storage failure says nothing about the name and must be retried",
			got, timersBefore)
	}
	if got := h.warningsMatching("a turn name is held"); got != 0 {
		t.Fatalf("storage failure emitted the stale-name warning %d times, want 0: no name is held here", got)
	}

	h.clock.Advance(jobNotificationRetryInitialDelay)
	<-h.woken
	if got := h.wakeCount(); got != 1 {
		t.Fatalf("wakes after the retry delay = %d, want exactly 1: a transient write error must not strand the wake", got)
	}
}

// TestNotificationStandDownStillRefusesToWaitOnAnOwnerlessName keeps the guard
// this kata narrows rather than removes. A name held by no pending execution
// belongs to a turn that will never finish — a crash between reserving and
// releasing, or a release write that failed. Waiting on it IS the hot loop the
// guard exists for, so that case still says so once and stops.
func TestNotificationStandDownStillRefusesToWaitOnAnOwnerlessName(t *testing.T) {
	h := newStandDownHarness(t)
	if err := h.sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("seed an ownerless running turn: %v", err)
	}
	if h.sess.runningTurnNameHasOwner() {
		t.Fatal("seeded slot has an owner; the test needs none")
	}

	timersBefore := h.clock.BlockedCount()
	h.standDown(t)

	if got := h.wakeCount(); got != 0 {
		t.Fatalf("ownerless name kicked the serve loop %d times, want 0", got)
	}
	if got := h.clock.BlockedCount(); got != timersBefore {
		t.Fatalf("armed retry timers = %d, want the baseline %d: nothing will hand this name back, so retrying only paces the same hot loop",
			got, timersBefore)
	}
	if got := h.warningsMatching("a turn name is held"); got != 1 {
		t.Fatalf("stale-name warnings = %d, want exactly 1", got)
	}
}

// TestNotificationStandDownRetriesUnderAnInterruptFence closes the third door
// into kata ajg5's strand. mintRunningTurnID refuses under an interrupt fence
// and under an occupied name, and the first taxonomy spelled both turnNameHeld
// — but the stand-down decides with runningTurnNameHasOwner, which asks about
// ActiveTurnID alone.
//
// A Stop pressed on a turn the daemon never named writes a fence whose
// ExpectedTurnID is the empty name that turn had (session_client_mutation.go's
// "the empty name when that turn had none"). That is a fence with NO active
// name, so the owner check answers false, and the wake fell into the
// stale-name arm: a warning about a name that is not held, and no re-arm. The
// fence clears two durable writes later — the name is coming back.
func TestNotificationStandDownRetriesUnderAnInterruptFence(t *testing.T) {
	h := newStandDownHarness(t)
	if err := h.sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{
			ClientMutationID: "cm-stop",
			// Empty: the interrupted turn had no durable name.
			ExpectedTurnID: "",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed an interrupt fence over an unnamed turn: %v", err)
	}
	if got := h.sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q, want empty: this path is a fence with no name behind it", got)
	}
	if h.sess.runningTurnNameHasOwner() {
		t.Fatal("fenced-but-unnamed slot reports an owner; the test needs none")
	}

	timersBefore := h.clock.BlockedCount()
	h.standDown(t)

	if got := h.warningsMatching("a turn name is held"); got != 0 {
		t.Fatalf("fenced stand-down emitted the stale-name warning %d times, want 0: no name is held under this fence", got)
	}
	if got := h.clock.BlockedCount(); got != timersBefore+1 {
		t.Fatalf("armed retry timers = %d, want baseline %d plus one: the fence clears in two durable writes, so this wake must wait for it",
			got, timersBefore)
	}

	h.clock.Advance(jobNotificationRetryInitialDelay)
	<-h.woken
	if got := h.wakeCount(); got != 1 {
		t.Fatalf("wakes after the retry delay = %d, want exactly 1", got)
	}
}

// TestNotificationStandDownWarnsOncePerStorageEpisode bounds what the paced
// retry costs when the store is not transiently but PERMANENTLY unwritable —
// a read-only mount, a full disk, a deleted state dir.
//
// Every retry re-runs mintRunningTurnID, and its failure warning goes out
// through s.emit, which fires the user's Notification hook unconditionally
// (unlike emitDiagnosticWarning). Warning per retry forever means a
// user-visible warning and a hook SUBPROCESS every few seconds for the life of
// the daemon. The diagnostic is worth saying; it is worth saying once.
func TestNotificationStandDownWarnsOncePerStorageEpisode(t *testing.T) {
	h := newStandDownHarness(t)
	h.failWrites(t)

	const retries = 6
	for range retries {
		h.standDown(t)
		// Advance past the ceiling: the store-failure retry backs off toward
		// turnNameStoreRetryMaxDelay, so a fixed seconds-scale advance stops
		// firing the timer after a few doublings.
		h.clock.Advance(turnNameStoreRetryMaxDelay)
		<-h.woken
	}
	if got := h.warningsMatching("name running turn failed"); got != 1 {
		t.Fatalf("store-failure warnings across %d retries = %d, want 1: one unhealthy episode is one diagnostic, not one per retry",
			retries, got)
	}

	// A new episode still speaks. The latch tracks the condition, not the
	// process: once the store takes a write again, the next failure is news.
	h.sess.clientMutations.faults.BeforeEffectSnapshotRename = nil
	turnID, refusal := h.sess.mintRunningTurnID()
	if refusal != turnNameMinted || turnID == "" {
		t.Fatalf("recovered mint = (%q, %v), want a name", turnID, refusal)
	}
	h.sess.releaseRunningTurnID(turnID)
	h.failWrites(t)
	h.standDown(t)
	if got := h.warningsMatching("name running turn failed"); got != 2 {
		t.Fatalf("store-failure warnings after recovery and a fresh failure = %d, want 2", got)
	}
}
