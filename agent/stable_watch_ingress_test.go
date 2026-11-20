package agent

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// An unleased stable-child turn must never present a repeating timer's tick: it
// has no generation to consume what it presents, and admission refuses a watch
// notice on it. Neither may it stand down and leave the tick queued for the next
// wake to refuse again -- that is a spin, not a deferral. The accept hands the
// batch to the leased rail instead, and an ordinary notification on the same
// queue still moves.
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

	// The unleased accept must stand down: there is nothing this turn can deliver.
	if child.acceptNotificationInput(context.Background(), "unleased-tick") {
		t.Fatal("an unleased turn was admitted for a repeating tick it cannot present")
	}
	// It must have handed the batch to the leased rail rather than left it queued
	// for the next wake to refuse again.
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("peekNotifications = %d after an unleased accept, want 0: the tick stayed queued, so every later wake refuses it again", queued)
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows went %d -> %d for one handed-off tick, want one row", rowsBefore, got)
	}
	if !child.hasPendingStableWatchAttention() {
		t.Fatal("the handed-off tick does not read as pending watch attention, so no leased generation will present it")
	}
	// A second unleased accept must find nothing to refuse: a wake that meets
	// the same tick again and again is a spin, not a deferral.
	if child.acceptNotificationInput(context.Background(), "unleased-tick-again") {
		t.Fatal("a second unleased turn was admitted with no deliverable work")
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows went %d -> %d across two unleased accepts, want one row: the batch must not be written again", rowsBefore+1, got)
	}

	// An ordinary notification on the same rail still moves: the handoff is
	// scoped to repeating ticks.
	ordinary := jobNotification{Kind: jobNotificationKindTerminal, JobID: "job:ordinary", Status: "completed"}
	child.enqueueJobNotificationAndNotify(ordinary)
	if !child.hasPendingOrdinaryNotification() {
		t.Fatal("the ordinary notification is gone after the handoff: the tick handoff must not take ordinary work off the rail")
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows went %d -> %d after an ordinary notification, want no durable row: only a repeating tick is promoted", rowsBefore+1, got)
	}
}

// peekNotifications counts only what a notification turn can actually deliver.
// A retained watch notice has no durable row yet, so acceptNotificationInput
// would drain nothing for it and finish as a no-op -- which is exactly the turn
// the drain must not keep starting. The notice stays owed work, so the paced
// retry and the retirement checks still see it.
func TestPeekNotificationsExcludesRetainedWatchNotice(t *testing.T) {
	t.Parallel()
	child, _ := drainingDelegateWithUpdate(t, nil)
	child.pendingJobNotifsMu.Lock()
	child.stableWatchRetained = nil
	child.pendingJobNotifs = nil
	child.jobNotifsAwaitingRequest = nil
	child.pendingJobNotifsMu.Unlock()

	notice := watchNotification("job:retained-peek", "repeat")
	notice.WatchID = "watch:retained-peek"
	notice.Fires = 1
	notice.IntervalSeconds = 30
	attentionID := stableWatchNoticeAttentionID(notice)
	child.retainStableWatchPending(notice, attentionID, "retained row", nil)

	if peek := child.peekNotifications(); peek != 0 {
		t.Fatalf("peekNotifications = %d, want 0: a retained notice has nothing a notification turn can deliver, so counting it starts empty turns", peek)
	}
	child.pendingJobNotifsMu.Lock()
	owed := child.owedJobNotificationsLocked()
	armed := child.jobNotifyRetry.active
	child.pendingJobNotifsMu.Unlock()
	if owed != 1 {
		t.Fatalf("owedJobNotificationsLocked = %d, want 1: a retained notice is owed work until its retry lands", owed)
	}
	if !armed {
		t.Fatal("the notification retry chain is not armed, so nothing re-attempts the retained notice")
	}

	// A queued notification is deliverable and must still be counted.
	child.pendingJobNotifsMu.Lock()
	child.pendingJobNotifs = append(child.pendingJobNotifs, watchNotification("job:queued-peek", "after"))
	child.pendingJobNotifsMu.Unlock()
	if peek := child.peekNotifications(); peek != 1 {
		t.Fatalf("peekNotifications = %d with one queued notification, want 1", peek)
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
		if jm.persistStableWatchPending(tick) {
			t.Fatalf("tick %q was handled by the durable ingress: a repeating tick must fold on the queue instead of writing an immutable row per tick", tick.Occurrence)
		}
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

	// The handoff is what makes the batch durable, and it writes ONE row stating
	// the whole batch.
	if !child.handOffQueuedWatchTicks() {
		t.Fatal("the folded timer batch was not handed off")
	}
	if got := attentionRowCount(t, child); got != rowsBefore+1 {
		t.Fatalf("attention rows after the handoff = %d, want %d: the whole batch is one row", got, rowsBefore+1)
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
	if jm.persistStableWatchPending(tick) {
		t.Fatal("the repeating tick was handled by the durable ingress")
	}
	child.enqueueJobNotificationAndNotify(tick)
	rowsBefore := attentionRowCount(t, child)

	if _, err := jm.clearWatchByID(watchID); err != nil {
		t.Fatalf("clear the repeating timer: %v", err)
	}
	if child.handOffQueuedWatchTicks() {
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

	// A terminal one-shot fire is a fact that already happened, so it is durable
	// at ingress and the same clear leaves it pending: only a cadence is dropped.
	terminal := timerTickNotification("", watchID, "", 1, nil)
	terminal.Terminal = true
	if !jm.persistStableWatchPending(terminal) {
		t.Fatal("a one-shot's terminal fire was not handled by the durable ingress")
	}
	pending, err = child.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	if !slices.Contains(pending, stableWatchNoticeAttentionID(terminal)) {
		t.Fatal("the terminal one-shot fire is not pending, so the clear retracted a fact that already happened")
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

// A fold is a request input in disguise: what it keeps can reach the model
// through the summary it publishes. On an unleased stable-child turn an
// unresolved watch row must therefore be filtered out before ForceCompact sees
// it, and the row must survive the fold as pending work for the leased turn
// that can present it. The snapshot length stays the resident history's, so the
// publish merges back against the same prefix the fold was snapshotted from
// rather than re-appending the tail the filter only removed from the fold's
// input.
func TestFoldWithForceCompactFiltersUnresolvedWatchRows(t *testing.T) {
	t.Parallel()
	child := newWatchFilterSession(t)
	evs, evMu, doneCh := collectEvents(child)
	child.owningDelegateID = "dlg_fold_filter"
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	child.delegateController = controller

	watchRow := func(occurrence string) schema.Turn {
		n := timerTickNotification("job:fold-filter", "watch:fold-filter", occurrence, 1, nil)
		return delegateAttentionTurn(stableWatchNoticeAttentionID(n), llm.User("watch notice "+occurrence), child.sclock().Now())
	}
	rows := []schema.Turn{watchRow("tick:filter:1"), watchRow("tick:filter:2")}
	ordinaryTexts := []string{"first", "second", "third", "fourth", "fifth", "sixth"}
	// The rows sit INSIDE the folded prefix: everything before the preserved
	// tail is what the fold summarizes away, and both rows fall there.
	history := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(ordinaryTexts[0]))}
	history = append(history, rows...)
	for _, text := range ordinaryTexts[1:] {
		history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User(text)))
	}
	residentLen := len(history)
	child.mu.Lock()
	child.history = history
	child.mu.Unlock()
	for _, row := range rows {
		if _, err := child.appendDelegateNotificationDurably(row.AttentionID, row.Message.Text()); err != nil {
			t.Fatalf("append the unresolved watch row: %v", err)
		}
	}

	child.contextMgr = contextmgr.NewManager(child.profile, nil, nil)
	child.contextMgr.PreserveRecentTurns = 3

	if ok, refusal := child.foldWithForceCompact(context.Background(), "", nil, triggerAuto, false); !ok {
		t.Fatalf("fold lost publication: %v", refusal)
	}
	child.Close()
	<-doneCh

	// The checkpoint layer reports the length of the slice ForceCompact was
	// handed, which is the fold's own input -- the view the summarizer would
	// have had. The two watch rows are the only turns the filter removes.
	var turnsBefore, turnsAfter int
	var sawCheckpointLayer bool
	evMu.Lock()
	for _, ev := range *evs {
		ccd, ok := ev.Data.(events.ContextCompactionData)
		if !ok || ccd.Layer != "checkpoint" {
			continue
		}
		sawCheckpointLayer = true
		turnsBefore = ccd.TurnsBefore
		turnsAfter = ccd.TurnsAfter
	}
	evMu.Unlock()
	if !sawCheckpointLayer {
		t.Fatal("the fold reported no checkpoint layer, so nothing proves what input it folded")
	}
	if want := residentLen - len(rows); turnsBefore != want {
		t.Fatalf("the fold's input held %d turn(s), want %d: an unresolved watch row reached ForceCompact on a turn with no lease to present it", turnsBefore, want)
	}
	if turnsAfter <= 0 || turnsAfter >= turnsBefore {
		t.Fatalf("the checkpoint layer reported %d turn(s) after folding %d, want a shorter non-empty history", turnsAfter, turnsBefore)
	}

	child.mu.Lock()
	published := append([]schema.Turn{}, child.history...)
	child.mu.Unlock()
	if len(published) == 0 || published[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("published history starts with %v, want a checkpoint", published[0].Kind)
	}
	// Dropping it from the fold input DEFERS it: the row stays pending, and the
	// publish puts it back into resident history for the leased turn that can
	// present it.
	pending, err := child.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	for _, row := range rows {
		if !slices.Contains(pending, row.AttentionID) {
			t.Fatalf("watch row %q is no longer pending, so the fold lost it instead of deferring it", row.AttentionID)
		}
	}
	held := 0
	for _, turn := range published {
		if isStableWatchAttentionID(turn.AttentionID) {
			held++
		}
	}
	if held != len(rows) {
		t.Fatalf("published history holds %d watch row(s), want the %d deferred ones", held, len(rows))
	}
	// The snapshot length is the resident history's, not the filtered input's:
	// the turns the filter dropped are still resident and are not appended a
	// second time by the merge. The preserved tail proves it exactly once each.
	for _, text := range ordinaryTexts[len(ordinaryTexts)-3:] {
		if n := countTurnText(published, text); n != 1 {
			t.Fatalf("published history holds %d copies of %q, want 1: the merge boundary was taken from the filtered input", n, text)
		}
	}
}

// makeWatchJournalRefuse makes the manager's watch journal reject every append.
func makeWatchJournalRefuse(jm *jobManager) {
	jm.mu.Lock()
	jm.appendEvent = func(jobstore.Event) error { return errWatchJournalRefused }
	jm.mu.Unlock()
}

var errWatchJournalRefused = errors.New("watch journal refused")

// A caller frame that already reached the receiver's transcript must never be
// rendered again: only its delivered marker is owed. The marker write is retried
// a bounded number of times, and after the limit the frame is settled in memory
// so the pending entry cannot drive empty notification turns forever.
func TestStableDelegateTranscribedWatchSendSettlesWithoutRerendering(t *testing.T) {
	t.Parallel()
	child, jm, _ := stableWatchSendChild(t)

	cfg, key, deliveryID := installCallerSendWatchWithCurrentFrame(t, jm, "already-transcribed frame")
	makeWatchJournalRefuse(jm)
	jm.mu.Lock()
	state := *cfg.pending[key]
	jm.mu.Unlock()
	token := watchSendTokenNotification("", state)

	// The transcript append lands; the journal refuses the delivered marker.
	child.enqueueJobNotificationAndNotify(token)
	if !jm.watchSendTranscribed(deliveryID) {
		t.Fatal("the durable append did not mark the frame transcribed, so a rejected marker would re-render it instead of retrying the settle")
	}
	if jm.watchSendDelivered(deliveryID) {
		t.Fatal("the journal refused the marker but the send reads as delivered")
	}

	child.mu.Lock()
	historyBefore := len(child.history)
	child.mu.Unlock()

	// Repeats of the same token must reuse settleTranscribedWatchSend and never
	// append the frame a second time.
	for attempt := range transcribedWatchSendSettleRetries + 1 {
		child.enqueueJobNotificationAndNotify(watchSendTokenNotification("", state))
		child.mu.Lock()
		historyNow := len(child.history)
		child.mu.Unlock()
		if historyNow != historyBefore {
			t.Fatalf("attempt %d appended the already-transcribed frame again: history %d -> %d", attempt+1, historyBefore, historyNow)
		}
	}

	// After the bounded retries the frame is settled in memory, so the pending
	// entry is gone and no further empty turn is owed for it.
	if pending := jm.hasPendingWatchSends(); pending {
		t.Fatal("the frame is still pending after the bounded settle retries, so every later pass re-enqueues it")
	}
	if peek := child.peekNotifications(); peek != 0 {
		t.Fatalf("peekNotifications = %d after the frame settled, want 0", peek)
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
