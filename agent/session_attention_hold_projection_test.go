package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A request built between a fresh arm's wake and the arm's release callback
// must present the report that same wake freed, and must present it LAST. The
// release's journal record is the only order such a request can read, and the
// release's tail move is the only thing that puts the report behind the rows
// that arrived while the hold stood. A projection that captures the history
// first and settles the holds afterwards filters a pre-move snapshot: the
// report stays where the hold left it, ahead of everything appended since, and
// the request ends on a later row while the freed report waits another
// generation for the reader.
func TestHeldTurnProjectionReleasesOnTheJournaledFloor(t *testing.T) {
	t.Parallel()
	const (
		delegateID = "dlg_hold"
		reopenedID = "delegate:dlg_worker/delivery/1/reopened/1"
		freshID    = "delegate:dlg_worker/delivery/2"
		freshText  = "fresh row admitted after the stop"
	)
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{StateDir: dir, MaxSubagentDepth: 1}))
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, controller, delegateID, "")
	// Generation 1 is the attention run a user stopped, and generation 2 is the
	// successor run still open, so the settled generation a wake names is 1.
	journalDeriveHeldStopWithTrigger(t, controller, delegatestore.TriggerAttention, delegatestore.OutcomeCancelled, "cancelled")
	controller.mu.Lock()
	_, err := controller.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateRunStarted,
		DelegateID: delegateID,
		RunStarted: &delegatestore.RunStarted{
			Generation: 2,
			Trigger:    delegatestore.TriggerAttention,
			StartedAt:  time.Unix(30, 0).UTC(),
		},
	})
	controller.mu.Unlock()
	if err != nil {
		t.Fatalf("open the successor run: %v", err)
	}
	lease := delegateLease{delegateID: delegateID, generation: 2}
	seedDelegateControllerRunningRuntime(t, controller, lease, sess)
	ownController := sess.delegateController
	sess.mu.Lock()
	sess.delegateController = controller
	sess.owningDelegateID = delegateID
	sess.mu.Unlock()
	// The tree names this session as its running delegate only for the request
	// under test; give the session its own tree back before its Close.
	defer func() {
		sess.mu.Lock()
		sess.delegateController = ownController
		sess.owningDelegateID = ""
		sess.mu.Unlock()
	}()

	// Both rows are durable and pending, and the stopped report sits where the
	// hold left it — ahead of the fresh row that arrived after it.
	if _, err := sess.appendDelegateNotificationDurably(reopenedID, acceptedTriggerText); err != nil {
		t.Fatalf("append the stopped report: %v", err)
	}
	if _, err := sess.appendDelegateNotificationDurably(freshID, freshText); err != nil {
		t.Fatalf("append the fresh row: %v", err)
	}
	sess.attentionMu.Lock()
	sess.attentionAutomaticDriveHolds = map[string]uint64{reopenedID: 1}
	sess.attentionMu.Unlock()

	// The arm admitted its wake at the settled generation and journaled the
	// release; its release callback has not run, so the hold map still names
	// the row.
	journalDeriveHeldRelease(t, sess, 1)

	ctx := context.WithValue(context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease), delegateAttentionRunContextKey{}, struct{}{})
	var timings events.RoundTimings
	_, _, history, _, _, _, err := sess.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepare model request: %v", err)
	}
	reportAt, freshAt := -1, -1
	for i, message := range history {
		switch message.Text() {
		case acceptedTriggerText:
			reportAt = i
		case freshText:
			freshAt = i
		}
	}
	if reportAt < 0 {
		t.Fatalf("the request dropped the report the same wake freed: the reader waits another generation for it (%d messages)", len(history))
	}
	if freshAt < 0 {
		t.Fatalf("the request dropped the row that arrived while the hold stood (%d messages)", len(history))
	}
	if reportAt < freshAt {
		t.Fatalf("the request presented the freed report at message %d, ahead of the row that arrived after it at %d: the release's tail move must put the report last", reportAt, freshAt)
	}
	if reportAt != len(history)-1 {
		t.Fatalf("the request ends on message %d of %d, not the freed report: the model reads the report as a mid-conversation aside", reportAt, len(history)-1)
	}
}

// seedDelegateControllerRunningRuntime points lease's live state at runtime as
// its ready binding, the carrier admitLeaseLocked needs to admit a model
// request for that lease.
func seedDelegateControllerRunningRuntime(t *testing.T, c *delegateTreeController, lease delegateLease, runtime *Session) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live[lease.delegateID]
	if live == nil {
		live = &delegateLiveState{}
		c.live[lease.delegateID] = live
	}
	live.binding = &delegateRuntimeBinding{
		lease:  lease,
		cancel: func() {},
		ready:  true,
		// An attention run's binding always carries report-required
		// evidence, as CommitStart attaches it.
		evidence: &delegateGenerationEvidence{requirement: delegateCompletionReportRequired},
		runtime:  runtime,
	}
}
