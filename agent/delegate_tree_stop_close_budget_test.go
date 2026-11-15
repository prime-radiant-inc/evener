package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// An admitted attention start whose drive never commits or aborts must not
// spend the whole close cascade budget before the stop is even admitted. The
// pre-admission wait takes the stop-join half, leaving the bounded joins and
// teardown that follow the stop a budget of their own (issue #5957).
func TestDelegateCloseStopAdmissionWaitUsesStopJoinBudget(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	const targetID = "dlg_close_admission"
	seedDelegateControllerIdle(t, c, targetID, "")

	blocker := make(chan struct{})
	c.mu.Lock()
	c.reservations[1] = &delegateStartRecord{
		token:             1,
		delegateID:        targetID,
		trigger:           delegatestore.TriggerAttention,
		attentionAdmitted: true,
		done:              blocker,
	}
	c.mu.Unlock()
	defer close(blocker)

	// Mint the cascade context the way Session.Close does, rather than
	// hand-building its private budget value, so the test follows the
	// production mint if that changes.
	shortenCloseCascadeBudget(t, time.Second)
	cascade, cancel := ensureCloseBudget(context.Background())
	defer cancel()
	cascadeDeadline, ok := cascade.Deadline()
	if !ok {
		t.Fatal("ensureCloseBudget minted no deadline: the cascade budget is gone")
	}

	type observation struct {
		stopDeadline    time.Time
		cascadeDeadline time.Time
		budget          time.Duration
	}
	observed := make(chan observation, 1)
	previous := ObserveCloseStopJoinBudget
	ObserveCloseStopJoinBudget = func(stopDeadline, cascadeDeadline time.Time, budget time.Duration) {
		select {
		case observed <- observation{stopDeadline: stopDeadline, cascadeDeadline: cascadeDeadline, budget: budget}:
		default:
		}
	}
	t.Cleanup(func() { ObserveCloseStopJoinBudget = previous })

	_, _, _, err := c.stopSubtreeForClose(cascade, targetID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close stop admission wait = %v, want the stop-join deadline", err)
	}
	select {
	case got := <-observed:
		budget := laneClosePassBudget()
		if got.budget != budget {
			t.Fatalf("observed cascade budget = %v, want %v", got.budget, budget)
		}
		if got.cascadeDeadline != cascadeDeadline {
			t.Fatalf("observed cascade deadline = %v, want the minted %v", got.cascadeDeadline, cascadeDeadline)
		}
		// As in TestCloseStopJoin_HopelessStopLeavesHalfBudget, the two
		// deadlines are minted back to back; 250ms absorbs scheduling skew
		// while still rejecting a full-budget wait (which would reserve 0).
		want := budget / 2
		reserved := got.cascadeDeadline.Sub(got.stopDeadline)
		if delta := reserved - want; delta < -250*time.Millisecond || delta > 250*time.Millisecond {
			t.Fatalf("stop admission reserves %s of a %s cascade budget; want %s within 250ms", reserved, budget, want)
		}
	default:
		t.Fatal("close stop admission waited on the full cascade context")
	}
}
