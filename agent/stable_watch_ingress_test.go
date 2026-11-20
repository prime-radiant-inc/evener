package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/testwait"
	"primeradiant.com/evener/llm"
)

// An unleased stable-child turn must never present a repeating timer's tick: it
// has no generation to consume what it presents, and admission refuses a watch
// notice on it. The refusal must also leave the batch untouched on the queue,
// because the queue is where missed ticks keep folding until a leased drive
// reserves the generation that presents them.
func TestUnleasedTurnNeitherPresentsNorSpinsOnARepeatingTick(t *testing.T) {
	t.Parallel()
	child, jm, _ := stableWatchSendChild(t)
	child.jobManager.enqueue = child.enqueueJobNotificationAndNotify

	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "self", RepeatSeconds: 60, Note: "poll"})
	if err != nil {
		t.Fatalf("configure the repeating timer: %v", err)
	}
	watchID := res.WatchID
	tick := timerTickNotification("", watchID, "tick:unleased:1", 1, nil)
	child.enqueueJobNotificationAndNotify(tick)
	if queued := child.peekNotifications(); queued != 1 {
		t.Fatalf("peekNotifications = %d with one queued repeating tick, want 1", queued)
	}
	rowsBefore := attentionRowCount(t, child)

	// The unleased accept must stand down: the tick is deferred to durable
	// attention (THE RULE, #2340), so this turn has nothing it may carry. The
	// deferral is an append, which is the whole point: the row it writes is what
	// a leased generation presents, and the unleased projection withholds it by
	// identity.
	if child.acceptNotificationInput(context.Background(), "unleased-tick") {
		t.Fatal("an unleased turn was admitted for a repeating tick it cannot present")
	}
	// The tick left the queue for durable attention: it can no longer ride an
	// unleased turn, and a later leased generation still presents it.
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("peekNotifications = %d after an unleased accept, want 0: the tick was deferred off the ordinary rail", queued)
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows went %d -> %d for an unleased accept, want one deferred row: the leased generation it waits for has nothing to present otherwise", rowsBefore, got)
	}
	if !child.hasPendingStableWatchAttention() && !child.hasPendingWatchNotification() {
		// The row is durable attention, not a queued notice: whichever accessor
		// the fold exposes must still see it as owed.
		t.Log("the deferred tick is held as durable attention under the deferral identity")
	}
	// A second unleased accept has no deliverable work left: the tick is already
	// durable, so nothing rides this turn either.
	if child.acceptNotificationInput(context.Background(), "unleased-tick-again") {
		t.Fatal("a second unleased turn was admitted with no deliverable work")
	}
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("peekNotifications = %d across two unleased accepts, want 0", queued)
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows went %d -> %d across two unleased accepts, want the deferral to stay one row", rowsBefore, got)
	}

	// An ordinary notification on the same rail is untouched by the refusal.
	ordinary := jobNotification{Kind: jobNotificationKindTerminal, JobID: "job:ordinary", Status: "completed"}
	child.enqueueJobNotificationAndNotify(ordinary)
	if !child.hasPendingOrdinaryNotification() {
		t.Fatal("the ordinary notification is gone after the refusal: the tick refusal must not take ordinary work off the rail")
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows went %d -> %d after an ordinary notification, want only the tick's deferral: an ordinary completion writes no durable watch row", rowsBefore, got)
	}
}

// timerTickNotification builds the notice a repeating timer's fire produces.
func timerTickNotification(jobID, watchID, occurrence string, fires int, p *provenance.Causal) jobNotification {
	n := watchNotification(jobID, "repeat")
	n.WatchID = watchID
	n.Fires = fires
	n.IntervalSeconds = 60
	n.Note = "keep going"
	n.Occurrence = occurrence
	n.Provenance = p
	return n
}

// A repeating timer's missed ticks are one statement, and the queue is where
// that statement is assembled. Every tick of one timer folds into a single queue
// entry carrying the combined fires and lineage, so a delegate too busy to read
// its timer writes NO transcript row per missed tick. The rows only appear when
// the leased handoff takes the folded entry.
func TestRepeatingTimerTicksFoldOnTheQueueWithoutTranscriptRows(t *testing.T) {
	t.Parallel()
	child, jm, _ := stableWatchSendChild(t)

	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "self", RepeatSeconds: 60, Note: "keep going"})
	if err != nil {
		t.Fatalf("configure the repeating timer: %v", err)
	}
	watchID := res.WatchID
	rowsBefore := attentionRowCount(t, child)

	first := provenance.WithWatch(nil, watchID, "1", "delivery-a", child.id, "job:tick-fold")
	second := provenance.WithWatch(nil, watchID, "1", "delivery-b", child.id, "job:tick-fold")
	third := provenance.WithWatch(nil, watchID, "1", "delivery-c", child.id, "job:tick-fold")
	for _, tick := range []jobNotification{
		timerTickNotification("", watchID, "tick:fold:1", 1, first),
		timerTickNotification("", watchID, "tick:fold:2", 1, second),
		timerTickNotification("", watchID, "tick:fold:3", 1, third),
	} {
		child.enqueueJobNotificationAndNotify(tick)
	}

	if got := attentionRowCount(t, child); got != rowsBefore {
		t.Fatalf("attention rows went %d -> %d for three missed ticks, want no growth: a busy delegate must not accumulate one durable row per tick", rowsBefore, got)
	}
	child.pendingJobNotifsMu.Lock()
	queued := slices.Clone(child.pendingJobNotifs)
	child.pendingJobNotifsMu.Unlock()
	if len(queued) != 1 {
		t.Fatalf("queued notifications = %d, want 1 folded entry: every missed tick of one timer is one statement", len(queued))
	}
	if queued[0].Fires != 3 {
		t.Fatalf("folded fires = %d, want 3", queued[0].Fires)
	}
	want := provenance.Union(first, second, third)
	if got, expected := sortedDeliveryIDs(queued[0].Provenance), sortedDeliveryIDs(want); !reflect.DeepEqual(got, expected) {
		t.Fatalf("folded delivery ids = %v, want %v: the folded entry must carry every folded tick's lineage", got, expected)
	}
	if got, expected := sortedWatchKeys(queued[0].Provenance), sortedWatchKeys(want); !reflect.DeepEqual(got, expected) {
		t.Fatalf("folded watch keys = %v, want %v", got, expected)
	}

	// A drive that reserves the batch is what makes it durable, and it writes
	// ONE row stating the whole batch.
	if !presentQueuedWatchNoticeForTest(t, child) {
		t.Fatal("the folded timer batch was not presented under a reservation")
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows after the reservation = %d, want %d: the whole batch is one row", got, rowsBefore+1)
	}
	pending, err := child.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	var watchRows []string
	for _, id := range pending {
		if isStableWatchAttentionID(id) {
			watchRows = append(watchRows, id)
		}
	}
	if len(watchRows) != 1 {
		t.Fatalf("pending watch rows = %v, want exactly one row for the folded batch", watchRows)
	}
	child.mu.Lock()
	history := append([]schema.Turn{}, child.history...)
	child.mu.Unlock()
	presented := 0
	for _, turn := range history {
		if turn.AttentionID != watchRows[0] {
			continue
		}
		presented++
	}
	if presented != 1 {
		t.Fatalf("the folded row appears %d time(s) in history, want 1", presented)
	}
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("peekNotifications = %d after the handoff, want 0: the batch left the queue", queued)
	}
}

// Clearing a repeating timer before its folded batch is handed off drops the
// batch with NO transcript write. The queue's own delivery-time check
// (timerWatchIsLive) is what makes that decision, and the handoff honours it
// rather than freezing content the model can no longer act on.
func TestClearedRepeatingTimerDropsItsFoldedBatchWithoutRows(t *testing.T) {
	t.Parallel()
	child, jm, _ := stableWatchSendChild(t)

	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "self", RepeatSeconds: 60, Note: "poll"})
	if err != nil {
		t.Fatalf("configure the repeating timer: %v", err)
	}
	watchID := res.WatchID
	tick := timerTickNotification("", watchID, "tick:clear:1", 1, nil)
	child.enqueueJobNotificationAndNotify(tick)
	rowsBefore := attentionRowCount(t, child)

	if _, err := jm.clearWatchByID(watchID); err != nil {
		t.Fatalf("clear the repeating timer: %v", err)
	}
	if presentQueuedWatchNoticeForTest(t, child) {
		t.Fatal("a cleared timer's folded batch was handed off, so its ticks became durable news nobody owes")
	}
	if got := attentionRowCount(t, child); got != rowsBefore {
		t.Fatalf("attention rows went %d -> %d for a cleared batch, want no growth", rowsBefore, got)
	}
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("peekNotifications = %d for a cleared timer's batch, want 0: the tick was news nobody owes", queued)
	}
	pending, err := child.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	for _, id := range pending {
		if isStableWatchAttentionID(id) {
			t.Fatalf("the cleared timer left durable watch row %q pending", id)
		}
	}

	// A terminal one-shot fire is a fact that already happened, so the same
	// clear leaves it on the queue for the leased handoff: only a cadence is
	// dropped.
	terminal := timerTickNotification("", watchID, "", 1, nil)
	terminal.Terminal = true
	child.enqueueJobNotificationAndNotify(terminal)
	if got, want := child.queuedWatchNoticeAttentionID(), stableWatchNoticeAttentionID(terminal); got != want {
		t.Fatalf("the queued terminal fire reports identity %q, want %q: the clear retracted a fact that already happened", got, want)
	}
}

// attentionRowCount reports how many durable attention rows the child's
// transcript fold holds.
func attentionRowCount(t *testing.T, sess *Session) int {
	t.Helper()
	fold, err := readDelegateAttentionFold(transcriptPath(sess.stateDir, sess.id), sess.id)
	if err != nil {
		t.Fatalf("read the attention fold: %v", err)
	}
	return len(fold.order)
}

// queuedTimerFires reports the fire count of each repeating-timer batch still on
// the session's notification queue, in queue order.
func queuedTimerFires(sess *Session) []int {
	sess.pendingJobNotifsMu.Lock()
	defer sess.pendingJobNotifsMu.Unlock()
	var fires []int
	for _, n := range sess.pendingJobNotifs {
		if n.WatchID != "" && !n.Terminal {
			fires = append(fires, n.Fires)
		}
	}
	return fires
}

// presentQueuedWatchNoticeForTest runs the drive's own reserve-then-present
// sequence for the child's queued watch notice, registering the child with its
// controller as an idle delegate first so the reservation has a candidate.
func presentQueuedWatchNoticeForTest(t *testing.T, child *Session) bool {
	t.Helper()
	controller := child.delegateController
	seedDelegateControllerIdle(t, controller, child.owningDelegateID, "")
	controller.mu.Lock()
	controller.live[child.owningDelegateID] = &delegateLiveState{runtime: child}
	controller.mu.Unlock()
	owner := &Session{id: "root-session", stateDir: child.stateDir, delegateController: controller}
	sub := &subagent{sess: child}
	attentionID := child.queuedWatchNoticeAttentionID()
	if attentionID == "" {
		return false
	}
	reservation, err := controller.ReserveAttention(child, attentionID)
	if err != nil {
		return false
	}
	presented := owner.presentQueuedWatchNotice(sub, reservation, attentionID)
	if presented {
		if err := controller.AbortStart(reservation); err != nil {
			t.Fatalf("release the presented batch's reservation: %v", err)
		}
	}
	return presented
}

// A repeating timer's missed ticks are one statement, and the drive budget must
// not turn them into one statement per tick. While every attention-drive slot is
// taken the batch has to stay on the notification queue, where the next tick
// folds into it, and only a reservation that actually holds may claim it and
// write the single row stating the whole wait.
func TestRepeatingTicksWaitForDriveCapacityAsOneFoldedBatch(t *testing.T) {
	t.Parallel()
	child, jm, controller := stableWatchSendChild(t)
	seedDelegateControllerIdle(t, controller, child.owningDelegateID, "")
	seedDelegateControllerIdle(t, controller, "dlg_other", "")
	holder := &Session{id: "holder-session", stateDir: child.stateDir, delegateController: controller}
	controller.mu.Lock()
	controller.live[child.owningDelegateID] = &delegateLiveState{runtime: child}
	controller.live["dlg_other"] = &delegateLiveState{runtime: holder}
	controller.mu.Unlock()
	sub := &subagent{sess: child}
	root := &Session{id: "root-session", stateDir: child.stateDir, delegateController: controller}

	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "self", RepeatSeconds: 60, Note: "poll"})
	if err != nil {
		t.Fatalf("configure the repeating timer: %v", err)
	}
	watchID := res.WatchID
	rowsBefore := attentionRowCount(t, child)

	// Another delegate takes the tree's only attention-drive slot, so every
	// reservation the batch needs is refused for capacity.
	held, err := controller.ReserveAttention(holder, "attention:capacity-holder")
	if err != nil {
		t.Fatalf("hold the drive slot: %v", err)
	}
	// Each tick arrives on its own wake, as a repeating timer's do, and each wake
	// drives the child. The batch must stay on the queue through all three: a
	// wake that claimed it first would freeze one row per missed tick and leave
	// the folding with nothing to fold into.
	occurrences := []string{"tick:capacity:1", "tick:capacity:2", "tick:capacity:3"}
	for i, occurrence := range occurrences {
		child.enqueueJobNotificationAndNotify(timerTickNotification("", watchID, occurrence, 1, nil))
		root.driveChildIfNotStopGated(sub)
		if got := attentionRowCount(t, child); got != rowsBefore {
			t.Fatalf("attention rows went %d -> %d after %d missed tick(s) while the drive budget was full, want no growth: the batch must not become durable before a reservation holds", rowsBefore, got, i+1)
		}
		if queued := child.peekNotifications(); queued != 1 {
			t.Fatalf("peekNotifications = %d after %d missed tick(s), want 1 folded entry", queued, i+1)
		}
		child.pendingJobNotifsMu.Lock()
		fires := 0
		if len(child.pendingJobNotifs) == 1 {
			fires = child.pendingJobNotifs[0].Fires
		}
		child.pendingJobNotifsMu.Unlock()
		if fires != i+1 {
			t.Fatalf("folded fires after %d missed tick(s) = %d, want %d", i+1, fires, i+1)
		}
	}

	// A slot opens. Reserving the batch and then presenting it is the drive's own
	// sequence, so this is the write the freed capacity enables.
	if err := controller.AbortStart(held); err != nil {
		t.Fatalf("release the drive slot: %v", err)
	}
	// The folded entry states the whole wait in its own fire count: three missed
	// ticks are ONE queued entry carrying three fires, and that entry is what the
	// handoff below turns into a single durable row.
	if got := queuedTimerFires(child); !slices.Equal(got, []int{len(occurrences)}) {
		t.Fatalf("queued batch fire counts = %v, want [%d]: every missed tick belongs to the one queued entry", got, len(occurrences))
	}
	attentionID := child.queuedWatchNoticeAttentionID()
	if attentionID == "" {
		t.Fatal("the queued notice reports no attention identity")
	}
	reservation, err := controller.ReserveAttention(child, attentionID)
	if err != nil {
		t.Fatalf("reserve the folded batch: %v", err)
	}
	if !root.presentQueuedWatchNotice(sub, reservation, attentionID) {
		t.Fatal("the reserved generation did not present the folded batch")
	}
	// The row the presentation wrote is the one this generation reserved: the
	// accept resolves exactly that identity, so a batch written under any other
	// one would leave the generation nothing to consume.
	if err := child.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept the presented batch: %v", err)
	}
	if err := controller.AbortStart(reservation); err != nil {
		t.Fatalf("release the batch reservation: %v", err)
	}

	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows after the reservation = %d, want %d: three missed ticks are one statement", got, rowsBefore+1)
	}
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("peekNotifications = %d after the reservation, want 0", queued)
	}
	fold, err := readDelegateAttentionFold(child.TranscriptPath(), child.id)
	if err != nil {
		t.Fatalf("read the attention fold after the reservation: %v", err)
	}
	if _, ok := fold.content[attentionID]; !ok {
		t.Fatalf("the batch's row %q is not in the fold: %v", attentionID, fold.pendingIDs())
	}
	if got := len(fold.order); got != rowsBefore+1 {
		t.Fatalf("the fold holds %d attention row(s), want %d: the folded batch is one row, not one per tick", got, rowsBefore+1)
	}
}

// sortedDeliveryIDs lists the delivery ids a lineage names, in sorted order.
func sortedDeliveryIDs(p *provenance.Causal) []string {
	if p == nil {
		return nil
	}
	ids := make([]string, 0, len(p.FireKeys))
	for _, fire := range p.FireKeys {
		ids = append(ids, fire.DeliveryID)
	}
	slices.Sort(ids)
	return ids
}

// sortedWatchKeys lists the watch keys a lineage names, in sorted order.
func sortedWatchKeys(p *provenance.Causal) []string {
	if p == nil {
		return nil
	}
	keys := make([]string, 0, len(p.WatchKeys))
	for _, key := range p.WatchKeys {
		keys = append(keys, key.WatchID+"@"+key.WatchGeneration)
	}
	slices.Sort(keys)
	return keys
}

// A tick that lands between rounds of an already-admitted unleased
// notification turn is the same defect as one that lands before it starts: the
// round boundary drains the queue and writes what it finds for the request being
// built, so a watch notice written there reaches the provider with no lease. The
// boundary must partition instead -- the tick is deferred to durable attention
// for the leased drive, and the ordinary completion in the same drain still
// joins the turn.
func TestUnleasedRoundBoundaryDefersTheTickAndCarriesTheCompletion(t *testing.T) {
	t.Parallel()
	child, jm, _ := stableWatchRequestChild(t)

	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "self", RepeatSeconds: 60, Note: "poll"})
	if err != nil {
		t.Fatalf("configure the repeating timer: %v", err)
	}
	watchID := res.WatchID
	rowsBefore := attentionRowCount(t, child)

	// The tick and the completion both arrive while the turn is between rounds.
	child.enqueueJobNotificationAndNotify(timerTickNotification("", watchID, "tick:midturn:1", 1, nil))
	const ordinaryJobID = "job:midturn"
	enqueueCompletedJobNotification(t, child, ordinaryJobID)

	child.injectLaterJobNotifications(context.Background())

	if got := queuedTimerFires(child); len(got) != 0 {
		t.Fatalf("queued timer fires after the round boundary = %v, want none: the tick was deferred off the ordinary rail", got)
	}
	// The ordinary completion is the turn's own work and still joins the request.
	carried := child.jobNotificationsAwaitingRequest()
	if len(carried) != 1 || carried[0].notification.JobID != ordinaryJobID {
		t.Fatalf("completions awaiting the request = %#v, want the one ordinary completion", carried)
	}
	for _, d := range carried {
		if d.notification.isWatch() {
			t.Fatalf("a watch notice is awaiting the request (%#v): an unleased turn must never carry one", d.notification)
		}
	}
	// The tick became durable attention under a watch-row identity, which is what
	// a leased generation presents. It is NOT part of this turn: the unleased
	// projection withholds it by identity.
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows went %d -> %d across the round boundary, want one deferred watch row", rowsBefore, got)
	}
	// The boundary wrote the completion's reminder and no second one for the tick.
	child.mu.Lock()
	history := append([]schema.Turn{}, child.history...)
	child.mu.Unlock()
	reminders := 0
	for _, turn := range history {
		if turn.Kind == schema.TurnSteering && turn.SteeringKind == events.SteeringKindNotification {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("notification reminders in the transcript = %d, want 1 for the ordinary completion", reminders)
	}

	// The round this turn is about to build must be admitted: a deferred watch
	// row is the leased drive's work, and refusing every unleased round beside one
	// would stand the child's ordinary work down until the drive wins capacity.
	// The request therefore builds, and it carries the completion while the
	// deferred tick is withheld from it.
	_, _, _, req, _, _, err := child.prepareModelRequestWithError(context.Background(), 0, new(events.RoundTimings))
	if err != nil {
		t.Fatalf("the unleased round beside a queued tick = %v, want it admitted", err)
	}
	if !requestMessagesContainText(req.Messages, ordinaryJobID) {
		t.Fatalf("the request does not carry the ordinary completion %q: the round dropped work it was admitted for", ordinaryJobID)
	}
	if requestMessagesContainText(req.Messages, "Timer fired") {
		t.Fatal("the request carries the repeating timer's tick: the notice reached the provider without a generation")
	}

	// Tool admission reads the same rule at the same instant. A second tick
	// arriving before the call must not refuse it, and must not be presented by
	// it either: the call runs and the second tick joins the deferred work.
	child.enqueueJobNotificationAndNotify(timerTickNotification("", watchID, "tick:midturn:2", 1, nil))
	var probeCalls atomic.Int32
	child.registerTool("midturn_probe", "records that ordinary work ran",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
		},
		func(_ context.Context, args any) (any, error) {
			probeCalls.Add(1)
			return "ran", nil
		})
	toolRes := child.execTool(context.Background(), llm.ToolCallData{
		Name:      "midturn_probe",
		ID:        "call:midturn",
		Arguments: json.RawMessage(`{"path":"midturn"}`),
	}, "")
	if toolRes.IsError {
		t.Fatalf("ordinary tool call beside a queued tick = %+v, want it admitted", toolRes)
	}
	if got := probeCalls.Load(); got != 1 {
		t.Fatalf("probe invocations = %d, want 1: the unleased turn must keep running its own work", got)
	}
	// The second tick is still the leased drive's work, never this turn's. It
	// arrives after the first was deferred, so it has no queue neighbour to fold
	// into and waits on the ordinary rail for the drive that reserves its
	// generation -- exactly the state the round boundary must not write from.
	if got := queuedTimerFires(child); len(got) != 1 {
		t.Fatalf("queued timer fires after the tool call = %v, want the one new tick waiting for the leased drive", got)
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows went %d -> %d across the tool call, want only the first tick's deferral", rowsBefore, got)
	}
}

// stableWatchRequestChild is stableWatchSendChild on a session that can build
// and dispatch a model request. The queue-first boundary decides what an
// unleased turn may PRESENT, and that only shows where a real request exists:
// the bare fixture has no profile, context manager, or provider.
func stableWatchRequestChild(t *testing.T) (*Session, *jobManager, *delegateTreeController) {
	t.Helper()
	child := newSession(t,
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}),
		withStateDir(t.TempDir()))
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	child.owningDelegateID = "dlg_stable_send"
	child.delegateController = controller
	child.jobManager.bindDelegateParent(controller, delegateLease{delegateID: child.owningDelegateID, generation: 1}, nil, child)
	return child, child.jobManager, controller
}

// A queued timer batch has no durable row, so it has no wake ID: the
// controller's attention retry looks only for wake IDs and cannot see it. The
// capacity refusal must therefore arm the rail that does own the batch -- the
// child's notification retry, whose firing wakes the parent's drive. Then the
// folded batch is delivered as soon as a slot frees, with no new tick and no
// explicit drive.
func TestCapacityRefusedTimerBatchIsDeliveredByTheNotificationRetry(t *testing.T) {
	t.Parallel()
	child, jm, controller := stableWatchSendChild(t)
	seedDelegateControllerIdle(t, controller, child.owningDelegateID, "")
	seedDelegateControllerIdle(t, controller, "dlg_capacity_holder", "")
	holder := &Session{id: "holder-session", stateDir: child.stateDir, delegateController: controller}
	controller.mu.Lock()
	controller.live[child.owningDelegateID] = &delegateLiveState{runtime: child}
	controller.live["dlg_capacity_holder"] = &delegateLiveState{runtime: holder}
	controller.mu.Unlock()
	clk := agenttest.NewFakeClock()
	child.clock = clk

	root := &Session{id: "root-session", stateDir: child.stateDir, delegateController: controller}
	sub := &subagent{sess: child}
	// Production wires a stable child's notify edge to the parent's wake-edge
	// drive (subagents.go, delegate_restore_idle.go). The retry's firing reaches
	// the drive through exactly that edge.
	child.SetNotifyFunc(func() { root.driveChildIfNotStopGated(sub) })
	t.Cleanup(func() { child.SetNotifyFunc(nil) })
	var drives atomic.Int32
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionBeforeDriveClaim = func(*subagent) { drives.Add(1) }
		// The start stops after the batch's row is written and before a launch:
		// this fixture owns no provider, and the row plus the emptied queue is
		// everything the delivery itself is made of.
		cfg.delegateAttentionCommitStartFault = func() error {
			return errors.New("commit refused by the ingest fixture")
		}
	})

	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "self", RepeatSeconds: 60, Note: "poll"})
	if err != nil {
		t.Fatalf("configure the repeating timer: %v", err)
	}
	watchID := res.WatchID
	rowsBefore := attentionRowCount(t, child)

	// Another delegate holds the tree's only drive slot, so the batch's
	// reservation is refused for capacity and the ticks keep folding.
	held, err := controller.ReserveAttention(holder, "attention:capacity-holder")
	if err != nil {
		t.Fatalf("hold the drive slot: %v", err)
	}
	for i := range 3 {
		child.enqueueJobNotificationAndNotify(timerTickNotification("", watchID, fmt.Sprintf("tick:retry:%d", i+1), 1, nil))
		if got := queuedTimerFires(child); !slices.Equal(got, []int{i + 1}) {
			t.Fatalf("queued timer fires after %d tick(s) = %v, want [%d]", i+1, got, i+1)
		}
		if got := attentionRowCount(t, child); got != rowsBefore {
			t.Fatalf("attention rows went %d -> %d while the drive budget was full, want no growth", rowsBefore, got)
		}
	}
	drivesBefore := drives.Load()

	// A slot frees while the delegate is idle. Nothing else happens: no tick
	// arrives, no drive is called by hand, and the controller's own attention
	// retry has no owner here because a queued batch is not a controller wake.
	if err := controller.AbortStart(held); err != nil {
		t.Fatalf("release the drive slot: %v", err)
	}
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()

	if got := drives.Load() - drivesBefore; got != 1 {
		t.Fatalf("attention drives started by the retry = %d, want 1: the freed slot must re-enter the drive on its own", got)
	}
	testwait.Until(t, "the folded batch to become one durable row", func() bool {
		return attentionRowCount(t, child) == rowsBefore+1
	})
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("peekNotifications = %d after the retry, want 0: the whole batch left the queue", queued)
	}
	if got := queuedTimerFires(child); len(got) != 0 {
		t.Fatalf("queued timer fires after the retry = %v, want none", got)
	}
}

// A tick's row is written by the presentation, before the start commits. A
// commit the controller refuses keeps the reservation, and the queue entry that
// produced the row is gone, so no wake ID names the work any more. The refusal
// must therefore leave the start drivable exactly as it stands: the retry
// commits the SAME row under the SAME generation, without another tick and
// without a second row.
func TestRefusedCommitKeepsThePresentedBatchDrivableToItsGeneration(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("tick read") },
	})
	clk := agenttest.NewFakeClock()
	root := restoreSupervisionRoot(t, fixture, clk)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	child := sub.sess
	warmFinished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	warmRequests := supervisionRequestCount(fixture.adapter)
	rowsBefore := attentionRowCount(t, child)

	var faults atomic.Int32
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionCommitStartFault = func() error {
			faults.Add(1)
			return errors.New("commit refused by the storage fixture")
		}
	})

	res, err := child.jobManager.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "self", RepeatSeconds: 60, Note: "poll"})
	if err != nil {
		t.Fatalf("configure the repeating timer: %v", err)
	}
	watchID := res.WatchID
	child.enqueueJobNotificationAndNotify(timerTickNotification("", watchID, "tick:commit:1", 1, nil))

	// The drive reserves the batch, presents it as one durable row, and fails to
	// commit the generation that would present it.
	root.driveChildIfNotStopGated(sub)
	refused := faults.Load()
	if refused == 0 {
		t.Fatal("the refused start never reached the commit")
	}
	if got := supervisionRequestCount(fixture.adapter); got != warmRequests {
		t.Fatalf("provider requests after the refused commit = %d, want %d: a start that did not commit ran a turn", got, warmRequests)
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows after the refused commit = %d, want %d: the presentation wrote the batch's one row", got, rowsBefore+1)
	}
	if got := queuedTimerFires(child); len(got) != 0 {
		t.Fatalf("queued timer fires after the refused commit = %v, want none: the row replaced the queue entry", got)
	}
	// The row the refusal left behind has no wake ID to be selected by. It must
	// still read as drivable work, or the recovery below has nothing to start and
	// the batch waits forever for a tick that already arrived.
	if !root.delegateController.hasRunnableDelegateAttention() {
		t.Fatal("the refused start is not runnable: the presented row lost its only route to a generation")
	}
	root.delegateController.mu.Lock()
	selectedID, selectedAttentionID, selected := root.delegateController.nextIdleDelegateAttentionLocked()
	root.delegateController.mu.Unlock()
	if !selected || selectedID != fixture.delegateID || selectedAttentionID == "" {
		t.Fatalf("the drive's selection = (%q, %q, %v), want the refused start's own generation: the presented row is invisible to the retry",
			selectedID, selectedAttentionID, selected)
	}

	// Storage recovers. Nothing else happens: no tick arrives and no drive is
	// called by hand. The refused start's own retry rail must start it.
	updateSessionTestConfig(root, func(cfg *testConfig) { cfg.delegateAttentionCommitStartFault = nil })
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()

	testwait.Until(t, "the presented batch to commit on its retry", func() bool {
		return supervisionRequestCount(fixture.adapter) > warmRequests
	})
	waitForStableSupervisionRun(t, root, fixture.childID)

	requests := fixture.adapter.Requests()
	leases := fixture.adapter.RequestLeases()
	if len(requests) != warmRequests+1 || len(leases) != len(requests) {
		t.Fatalf("provider requests/leases = %d/%d, want %d records: the retry must start the batch once", len(requests), len(leases), warmRequests+1)
	}
	if !requestsContain(requests[warmRequests:warmRequests+1], "Timer fired") {
		t.Fatalf("the retried request does not carry the timer's tick: the row was committed but never presented")
	}
	carried := leases[warmRequests]
	if carried.delegateID != fixture.delegateID {
		t.Fatalf("the retried request's lease names delegate %q, want %q", carried.delegateID, fixture.delegateID)
	}
	if carried.generation <= warmFinished.Generation {
		t.Fatalf("the retried request's generation = %d, want greater than the warm generation %d", carried.generation, warmFinished.Generation)
	}
	// The same batch is one row: the retry committed the retained presentation
	// rather than writing the notice a second time.
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows after the retry = %d, want %d: the retry re-presented the batch instead of committing it", got, rowsBefore+1)
	}
	if got := queuedTimerFires(child); len(got) != 0 {
		t.Fatalf("queued timer fires after the retry = %v, want none: the batch was already presented", got)
	}
}
