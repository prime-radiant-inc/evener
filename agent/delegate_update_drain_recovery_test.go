package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// drainingDelegateWithUpdate builds a delegate-owned session whose transcript
// and durable queue hold one unresolved update from a child, the state a
// coordinator is in when its drain starts with that update pending.
func drainingDelegateWithUpdate(t *testing.T, configure func(*SessionConfig)) (sess *Session, c *delegateTreeController, updateID string) {
	t.Helper()
	stateDir := t.TempDir()
	cfg := SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}
	if configure != nil {
		configure(&cfg)
	}
	sess = newSession(t, withDir(stateDir), withConfig(cfg))
	c, _ = newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_coordinator", "")
	sess.delegateController = c
	sess.owningDelegateID = "dlg_coordinator"
	updateID = delegateAttentionID(delegatestore.UpdateDeliveryID("dlg_worker", 1))
	if _, err := sess.appendDelegateNotificationDurably(updateID, `<delegate-notification delegate_id="dlg_worker" kind="update">which table holds the drain cursor?</delegate-notification>`); err != nil {
		t.Fatalf("append update attention: %v", err)
	}
	// The delivery's acknowledgment publishes the queue row in production; this
	// setup writes the transcript directly, so seed the row it would have queued.
	if !c.noteDelegateAttentionForTest("dlg_coordinator", updateID) {
		t.Fatal("seed the update in the delegate queue")
	}
	return sess, c, updateID
}

// creditUpdate leaves the coverage a successful leased turn leaves when its
// settled full-history request presented the update.
func creditUpdate(sess *Session, updateID string) {
	sess.attentionMu.Lock()
	defer sess.attentionMu.Unlock()
	sess.attentionCoveredIDs = map[string]struct{}{updateID: {}}
}

func pendingUpdateCount(t *testing.T, c *delegateTreeController, delegateID string) int {
	t.Helper()
	return len(pendingAttentionRowsForTest(t, c, delegateID))
}

// A coordinator's last running turn showed the model an update but could not
// resolve it in the delegate queue. Its drain turn is a responses-continuation
// delta, which credits no attention and starts by clearing the previous turn's
// coverage, so the drain retries the resolution before any turn and runs no
// model turn for an update the model already saw.
func TestDelegateUpdateShownByThePrecedingTurnIsNotRerunByTheDrain(t *testing.T) {
	t.Parallel()
	sess, c, updateID := drainingDelegateWithUpdate(t, nil)
	failing := installAttentionResolutionAppendFailures(t, sess, 1)
	creditUpdate(sess, updateID)
	if err := sess.finishAttentionTurn(nil, nil); err != nil {
		t.Fatalf("finishAttentionTurn on a failed resolution append: %v", err)
	}
	if got := failing.injectedFailures(); got != 1 {
		t.Fatalf("test setup: %d resolution appends failed, want 1", got)
	}
	if got := pendingUpdateCount(t, c, sess.owningDelegateID); got != 1 {
		t.Fatalf("%d updates pending after the failed resolution, want 1", got)
	}

	turns := 0
	deltaTurn := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		turns++
		sess.resetAttentionCoverage()
		if turns > 2 {
			return "", errors.New("the drain keeps running turns for an update the model saw")
		}
		return "", nil
	}
	if _, err := sess.drainJobTreeWith(context.Background(), make(chan time.Time), func(context.Context) error { return nil }, deltaTurn); err != nil {
		t.Fatalf("drainJobTreeWith: %v", err)
	}
	if turns != 0 {
		t.Fatalf("the drain ran %d model turns for an update the preceding turn showed, want 0", turns)
	}
	if got := pendingUpdateCount(t, c, sess.owningDelegateID); got != 0 {
		t.Fatalf("%d updates still pending after storage recovered, want the drain's retry to have resolved it", got)
	}
}

// When the turn's resolution append and the drain's immediate retry both fail,
// the turn's coverage alone says the update was shown: the drain runs no second
// turn for it, and the next retry resolves it without a model request.
func TestDelegateUpdateIsNotRerunWhenTheResolutionRetryAlsoFails(t *testing.T) {
	t.Parallel()
	sess, c, updateID := drainingDelegateWithUpdate(t, nil)
	failing := installAttentionResolutionAppendFailures(t, sess, 2)

	turns := 0
	fullHistoryTurn := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		turns++
		sess.resetAttentionCoverage()
		creditUpdate(sess, updateID)
		// The turn's own resolution append; its failure is the drain's to retry.
		_ = sess.finishAttentionTurn(nil, nil)
		return "", nil
	}
	if _, err := sess.drainJobTreeWith(context.Background(), make(chan time.Time), func(context.Context) error { return nil }, fullHistoryTurn); err != nil {
		t.Fatalf("drainJobTreeWith: %v", err)
	}
	if got := failing.injectedFailures(); got != 2 {
		t.Fatalf("test setup: %d resolution appends failed, want 2", got)
	}
	if turns != 1 {
		t.Fatalf("the drain ran %d model turns, want 1: the update was shown by the first", turns)
	}
	// Both attempts failed, so the row stays durably pending and withheld from
	// any further turn; the fault counts no third failure, so the next retry is
	// the one the journal recovery allows.
	if got := pendingUpdateCount(t, c, sess.owningDelegateID); got != 1 {
		t.Fatalf("%d updates pending after both resolution attempts failed, want 1", got)
	}
	if err := sess.flushAwaitingDelegateAttentionResolutions(); err != nil {
		t.Fatalf("retry the resolution once the journal recovered: %v", err)
	}
	if got := pendingUpdateCount(t, c, sess.owningDelegateID); got != 0 {
		t.Fatalf("%d updates still pending after the retry recovered, want 0", got)
	}
	if turns != 1 {
		t.Fatalf("the retry ran a %dth model turn for an update the model already saw", turns)
	}
}

// TestDelegateAttention_FinishAttentionTurnResolvesConsumedQueueRows pins the
// delegate mid-turn path: a successful turn that consumed a child update must
// remove exactly that row from delegatestore while the generation still runs,
// publish the NeedsAttention drop, keep later rows queued, and leave a failed
// queue append for reconciliation without failing the turn.
func TestDelegateAttention_FinishAttentionTurnResolvesConsumedQueueRows(t *testing.T) {
	const (
		sessionID  = "child-dlg_target"
		delegateID = "dlg_target"
	)
	coverAttention := func(sess *Session, ids ...string) {
		sess.attentionMu.Lock()
		sess.attentionCoveredIDs = make(map[string]struct{}, len(ids))
		for _, id := range ids {
			sess.attentionCoveredIDs[id] = struct{}{}
		}
		sess.attentionMu.Unlock()
	}
	queuedRows := func(c *delegateTreeController) []string {
		c.mu.Lock()
		defer c.mu.Unlock()
		aggregate := c.durable[delegateID]
		if aggregate == nil {
			return nil
		}
		ids := make([]string, 0, len(aggregate.PendingAttention))
		for _, row := range aggregate.PendingAttention {
			ids = append(ids, row.AttentionID)
		}
		return ids
	}
	needsAttention := func(c *delegateTreeController) bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.durable[delegateID].NeedsAttention
	}

	t.Run("draining the last row drops NeedsAttention and publishes", func(t *testing.T) {
		const attentionID = "attention-consumed"
		c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
		runtime.owningDelegateID = delegateID
		var published []delegateUpdatePlan
		c.mu.Lock()
		c.emitUpdate = func(plan delegateUpdatePlan) { published = append(published, plan) }
		c.mu.Unlock()
		coverAttention(runtime, attentionID)
		if err := runtime.finishAttentionTurn(nil, nil); err != nil {
			t.Fatalf("finishAttentionTurn: %v", err)
		}
		if rows := queuedRows(c); len(rows) != 0 {
			t.Fatalf("queued rows after the consumed turn = %v, want none", rows)
		}
		if needsAttention(c) {
			t.Fatal("NeedsAttention stayed true after the last row was consumed")
		}
		if len(published) != 1 || len(published[0].rows) != 1 || published[0].rows[0].id != delegateID || published[0].rows[0].needsAttention {
			t.Fatalf("published delegate updates = %#v, want one row with NeedsAttention false", published)
		}
	})

	t.Run("a later row stays queued", func(t *testing.T) {
		c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, "attention-consumed", "attention-later")
		runtime.owningDelegateID = delegateID
		var published []delegateUpdatePlan
		c.mu.Lock()
		c.emitUpdate = func(plan delegateUpdatePlan) { published = append(published, plan) }
		c.mu.Unlock()
		coverAttention(runtime, "attention-consumed")
		if err := runtime.finishAttentionTurn(nil, nil); err != nil {
			t.Fatalf("finishAttentionTurn: %v", err)
		}
		if rows := queuedRows(c); len(rows) != 1 || rows[0] != "attention-later" {
			t.Fatalf("queued rows after the consumed turn = %v, want only attention-later", rows)
		}
		if !needsAttention(c) {
			t.Fatal("NeedsAttention dropped while a later row was still queued")
		}
		if len(published) != 0 {
			t.Fatalf("published delegate updates while attention is still owed = %#v, want none", published)
		}
	})

	t.Run("a failed queue append leaves the row for reconciliation", func(t *testing.T) {
		const attentionID = "attention-append-failure"
		c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
		runtime.owningDelegateID = delegateID
		var faulted atomic.Bool
		var published []delegateUpdatePlan
		c.mu.Lock()
		c.emitUpdate = func(plan delegateUpdatePlan) { published = append(published, plan) }
		c.appendFault = func(events []delegatestore.Event) error {
			for _, event := range events {
				if event.Kind == delegatestore.EventDelegateAttentionResolved && event.DelegateID == delegateID {
					faulted.Store(true)
					return errors.New("test: attention resolution append failure")
				}
			}
			return nil
		}
		c.mu.Unlock()
		coverAttention(runtime, attentionID)
		if err := runtime.finishAttentionTurn(nil, nil); err != nil {
			t.Fatalf("finishAttentionTurn failed on a queue append error: %v", err)
		}
		if !faulted.Load() {
			t.Fatal("finishAttentionTurn did not attempt to remove the consumed row")
		}
		if rows := queuedRows(c); len(rows) != 1 || rows[0] != attentionID {
			t.Fatalf("queued rows after the failed append = %v, want the consumed row retained", rows)
		}
		if !needsAttention(c) {
			t.Fatal("NeedsAttention dropped although the queue append failed")
		}

		c.mu.Lock()
		c.appendFault = nil
		c.mu.Unlock()
		if err := c.forgetDelegateAttention(delegateID, attentionID); err != nil {
			t.Fatalf("retry the resolved row: %v", err)
		}
		if rows := queuedRows(c); len(rows) != 0 {
			t.Fatalf("queued rows after reconciliation = %v, want none", rows)
		}
		if needsAttention(c) {
			t.Fatal("NeedsAttention stayed true after reconciliation")
		}
		if len(published) != 1 || published[0].rows[0].needsAttention {
			t.Fatalf("published delegate updates after reconciliation = %#v, want one clear update", published)
		}
	})
}
