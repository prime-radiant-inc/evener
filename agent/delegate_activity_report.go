package agent

import (
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// delegateActivityPublishInterval bounds how often activity alone publishes a
// delegate's snapshot. A streaming child reports activity for every delta, and
// each publication is a DELEGATE_UPDATED that the owning session re-samples its
// diagnostics for (every job and delegate it has) and forwards to every
// client, so publishing per delta made a parent's cost scale with its
// children's token rate.
const delegateActivityPublishInterval = time.Second

// delegateIntentPublishFloor bounds how often an intent change alone may
// publish a delegate's snapshot. The floor is the intent channel's own clock:
// an intent change publishes immediately only once at least this long has
// passed since an intent change last published a frame — a quiescent gap,
// which is exactly the multi-minute-tool-call case immediacy exists for, so
// the line lands the moment the call starts even though the batch-start
// activity report published the same instant. A change inside the window is
// journaled and held pending, and rides the next publication of any kind; it
// is never dropped, and only an intent-caused frame moves the clock the floor
// measures, so two of them are always at least the floor apart.
const delegateIntentPublishFloor = 5 * time.Second

func (c *delegateTreeController) ReportActivity(lease delegateLease, at time.Time) error {
	return c.ReportActivityPhase(lease, at, "")
}

// ReportIntent states the open generation's newest tool-call intent. An
// unchanged folded intent is a no-op — no append, no publication — mirroring
// ReportActivityPhase's own short-circuit. A changed one does three things
// and only those, in order, stopping at the first failure: it appends the
// delegate_intent_stated journal event (a failed append touches nothing else),
// sets the live intent, and publishes a snapshot under the five-second floor
// of its own channel. It is display-only state: it advances no activity clock
// — not the quiet baseline, not the drain-grace clock, not the activity
// channel's publication coalescer — and rearms nothing, and it admits only a
// running lease, so a settling-window report is rejected and the row keeps its
// pre-report intent.
func (c *delegateTreeController) ReportIntent(lease delegateLease, at time.Time, intent string) error {
	if c == nil {
		return errDelegateStaleLease
	}
	if at.IsZero() {
		at = c.now()
	}
	bounded := appwire.Excerpt(intent, appwire.MaxIntentRunes)
	c.mu.Lock()
	aggregate, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if bounded == aggregate.LatestIntent {
		c.mu.Unlock()
		return nil
	}
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:         delegatestore.EventDelegateIntentStated,
		TS:           c.now(),
		DelegateID:   lease.delegateID,
		IntentStated: &delegatestore.IntentStated{Generation: lease.generation, Intent: bounded},
	}); err != nil {
		c.mu.Unlock()
		return err
	}
	c.evidenceVersion++
	live.intent = bounded
	// An intent change inside the floor journals and stands pending: it rides
	// the next publication of any kind, which re-captures this snapshot, so it
	// is never dropped. The floor reads the intent channel's own clock, so the
	// batch-start activity report beside the call's stated intent cannot arm
	// the window the line would wait out; during a single long call nothing
	// else publishes, so a shared clock would shelve the line until the next
	// event.
	if at.Sub(live.intentPublishedAt) < delegateIntentPublishFloor {
		c.mu.Unlock()
		return nil
	}
	live.intentPublishedAt = at
	plan := c.capturedPlanLocked(aggregate.DelegateID)
	c.mu.Unlock()
	c.emitDelegateUpdate(plan)
	return nil
}

// ReportActivityPhase records parent-visible activity and separately advances
// the one-shot drain's productive-liveness clock. Provider retry/backoff is
// ordinary activity for supervision and reporting, but it is not productive
// progress that should extend terminal drain grace.
func (c *delegateTreeController) ReportActivityPhase(lease delegateLease, at time.Time, phase string) error {
	if c == nil {
		return errDelegateStaleLease
	}
	if at.IsZero() {
		at = c.now()
	}
	c.mu.Lock()
	aggregate, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	rearm := live.quietRearmPendingLocked()
	activityChanged := at.After(live.activityAt) || at.Equal(live.activityAt) && rearm
	productiveChanged := phase != jobPhaseModelRetrying && at.After(live.productiveActivityAt)
	if !activityChanged && !productiveChanged {
		c.mu.Unlock()
		return nil
	}
	if activityChanged && rearm {
		live.rearmQuietCadenceLocked()
	}
	if activityChanged {
		live.activityAt = at
	}
	if productiveChanged {
		live.productiveActivityAt = at
	}
	c.evidenceVersion++
	// A rearm changes quiet state, so it publishes; plain activity waits out
	// the interval and rides along with the next snapshot published.
	if !rearm && at.Sub(live.activityPublishedAt) < delegateActivityPublishInterval {
		c.mu.Unlock()
		return nil
	}
	live.activityPublishedAt = at
	plan := c.capturedPlanLocked(aggregate.DelegateID)
	c.mu.Unlock()
	c.emitDelegateUpdate(plan)
	return nil
}
