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

// A repeating timer's missed ticks are one statement. Every tick that arrives
// while an earlier one is still unpresented folds into that batch, so the
// transcript grows by one row rather than one row per missed tick. The folded
// row stands for the whole batch, so the row it replaced is retired and can
// never present a second time.
func TestRepeatingTimerTicksFoldIntoOneDurableBatch(t *testing.T) {
	t.Parallel()
	child, jm, _ := stableWatchSendChild(t)

	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "self", RepeatSeconds: 60, Note: "keep going"})
	if err != nil {
		t.Fatalf("configure the repeating timer: %v", err)
	}
	watchID := res.WatchID

	first := provenance.WithWatch(nil, watchID, "1", "delivery-a", child.id, "job:tick-fold")
	firstTick := timerTickNotification("job:tick-fold", watchID, "tick:fold:1", 1, first)
	firstID := stableWatchNoticeAttentionID(firstTick)
	if !jm.persistStableWatchPending(firstTick) {
		t.Fatal("the first repeating tick was not handled by the durable ingress")
	}
	pending, err := child.pendingDelegateAttentionIDs()
	if err != nil || len(pending) != 1 || pending[0] != firstID {
		t.Fatalf("pending after the first tick = %v, %v: want exactly %q", pending, err, firstID)
	}

	second := provenance.WithWatch(nil, watchID, "1", "delivery-b", child.id, "job:tick-fold")
	secondTick := timerTickNotification("job:tick-fold", watchID, "tick:fold:2", 1, second)
	third := provenance.WithWatch(nil, watchID, "1", "delivery-c", child.id, "job:tick-fold")
	thirdTick := timerTickNotification("job:tick-fold", watchID, "tick:fold:3", 1, third)
	for _, tick := range []jobNotification{secondTick, thirdTick} {
		if !jm.persistStableWatchPending(tick) {
			t.Fatalf("tick %q was not handled by the durable ingress", tick.Occurrence)
		}
	}

	// One folded batch, stated once.
	pending, err = child.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	watches := slices.DeleteFunc(slices.Clone(pending), func(id string) bool { return !isStableWatchAttentionID(id) })
	if len(watches) != 1 {
		t.Fatalf("pending watch rows = %v, want exactly one folded batch: each missed tick must not add its own row", watches)
	}
	foldedID := watches[0]
	if foldedID == firstID {
		t.Fatal("the folded batch reused the superseded row's identity, so the superseded row was never retired")
	}
	child.pendingJobNotifsMu.Lock()
	batch, held := child.stableWatchTickBatch[watchID]
	_, staleHeld := child.stableWatchTickWatch[firstID]
	child.pendingJobNotifsMu.Unlock()
	if !held || batch.attentionID != foldedID {
		t.Fatalf("batch entry = %+v (held %v), want the current row %q", batch, held, foldedID)
	}
	if batch.notification.Fires != 3 {
		t.Fatalf("folded fires = %d, want 3: the batch must state how many ticks it stands for", batch.notification.Fires)
	}
	want := provenance.Union(first, second, third)
	if got, expected := sortedDeliveryIDs(batch.notification.Provenance), sortedDeliveryIDs(want); !reflect.DeepEqual(got, expected) {
		t.Fatalf("folded delivery ids = %v, want %v: the folded row must carry every folded tick's lineage", got, expected)
	}
	if got, expected := sortedWatchKeys(batch.notification.Provenance), sortedWatchKeys(want); !reflect.DeepEqual(got, expected) {
		t.Fatalf("folded watch keys = %v, want %v", got, expected)
	}
	if staleHeld {
		t.Fatal("the superseded row kept its timer record, so a later clear would try to suppress a row that no longer exists")
	}

	// A leased request's projection holds the folded statement once, and not
	// the rows it replaced.
	child.mu.Lock()
	history := append([]schema.Turn{}, child.history...)
	child.mu.Unlock()
	projected, _ := child.filterWatchAttentionTurns(history, false)
	var watchRows []string
	for _, turn := range projected {
		if isStableWatchAttentionID(turn.AttentionID) {
			watchRows = append(watchRows, turn.AttentionID)
		}
	}
	if len(watchRows) != 1 || watchRows[0] != foldedID {
		t.Fatalf("watch rows a request presents = %v, want only %q: the model must read one statement per batch", watchRows, foldedID)
	}

	// Once the batch resolves, the next tick starts fresh: a batch is the row's
	// own identity, and a row the model has seen must not absorb later ticks.
	if err := child.resolveAttentionDurably([]string{foldedID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("resolve the folded batch: %v", err)
	}
	fourth := timerTickNotification("job:tick-fold", watchID, "tick:fold:4", 1, nil)
	if !jm.persistStableWatchPending(fourth) {
		t.Fatal("the post-presentation tick was not handled by the durable ingress")
	}
	child.pendingJobNotifsMu.Lock()
	next, heldAgain := child.stableWatchTickBatch[watchID]
	child.pendingJobNotifsMu.Unlock()
	if !heldAgain {
		t.Fatal("the post-presentation tick recorded no batch")
	}
	if next.notification.Fires != 1 {
		t.Fatalf("post-presentation batch fires = %d, want a fresh batch of 1", next.notification.Fires)
	}
	if next.attentionID == foldedID {
		t.Fatal("the post-presentation tick reused the resolved row's identity")
	}
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

// Clearing a repeating timer mid-turn must retract the tick that timer already
// recorded: the turn's own next request would otherwise present news the model
// can no longer act on. A terminal one-shot fire carries no timer identity, so
// the same clear leaves it alone.
func TestClearRepeatingTimerDiscardsItsPendingTick(t *testing.T) {
	t.Parallel()
	child, jm, _ := stableWatchSendChild(t)

	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "self", RepeatSeconds: 60, Note: "poll"})
	if err != nil {
		t.Fatalf("configure the repeating timer: %v", err)
	}
	watchID := res.WatchID
	tick := timerTickNotification("job:clear-tick", watchID, "tick:clear:1", 1, nil)
	tickID := stableWatchNoticeAttentionID(tick)
	if !jm.persistStableWatchPending(tick) {
		t.Fatal("the repeating tick was not handled by the durable ingress")
	}

	// A one-shot's terminal fire is not retracted by a clear.
	oneShot := timerTickNotification("job:clear-tick", watchID, "", 1, nil)
	oneShot.Terminal = true
	oneShotID := stableWatchNoticeAttentionID(oneShot)
	if _, err := child.appendDelegateNotificationDurably(oneShotID, "one-shot fired"); err != nil {
		t.Fatalf("append the one-shot fire: %v", err)
	}

	if _, err := jm.clearWatchByID(watchID); err != nil {
		t.Fatalf("clear the repeating timer: %v", err)
	}

	child.attentionMu.Lock()
	fold, err := child.delegateAttentionFoldLocked()
	child.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("read the attention fold: %v", err)
	}
	if disposition, resolved := fold.resolutions[tickID]; !resolved || disposition != delegateAttentionDiscarded {
		t.Fatalf("the cleared timer's pending tick resolved as %q (resolved %v), want discarded", disposition, resolved)
	}
	if disposition, resolved := fold.resolutions[oneShotID]; resolved && disposition == delegateAttentionDiscarded {
		t.Fatal("a terminal one-shot fire was discarded by the clear, want it preserved")
	}
	pending, err := child.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	if slices.Contains(pending, tickID) {
		t.Fatal("the cleared timer's tick is still pending, so the next request in the same turn presents it")
	}
	if !slices.Contains(pending, oneShotID) {
		t.Fatal("the terminal one-shot fire is no longer pending, so the clear lost it")
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
