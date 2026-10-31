package agent

import (
	"errors"
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
		// A retained child's notification turns run without opening a
		// generation, so their reports arrive on the finished generation's
		// lease and stay rejected: no clock moves, nothing publishes. The
		// rejection is not the whole story — the report is the turn's own
		// activity, so the marker parent's evidence refreshes through it
		// anyway, or a foreign prune can judge the tree inactive past the
		// operator's threshold and release the lane under the running
		// notification turn.
		if errors.Is(err, errDelegateStaleLease) {
			c.refreshLaneParentActivityEvidence(lease.delegateID)
		}
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
	// The published report doubles as the marker parent's activity evidence
	// while the turn runs; the refresh rides this publication, so the floor
	// that bounds it bounds the refresh too.
	c.refreshLaneParentActivityEvidence(aggregate.DelegateID)
	c.emitDelegateUpdate(plan)
	return nil
}

// refreshLaneParentActivityEvidence records a running delegate's ongoing
// activity in the last-activity evidence the orphan reclaim and the session
// listings read: the transcript mtime of the session its lane's marker names.
// A turn that outlasts the operator's inactivity threshold leaves its marker
// parent idle — the parent runs no turn of its own while it waits — so the
// once-per-launch refresh (revalidateLaneForLaunch) goes stale mid-task, and a
// foreign prune, whose live-work view cannot see the turn's work under the
// lane, could judge the parent long-inactive and release the lane under the
// running turn. Best-effort, like the launch refresh: a delegate with no
// isolation lane, and a tree with no root runtime or state directory, has no
// evidence to refresh.
func (c *delegateTreeController) refreshLaneParentActivityEvidence(delegateID string) {
	lane, ok := c.isolatedLaneForDelegate(delegateID)
	if !ok {
		return
	}
	c.rootRuntime.refreshActivityEvidence(lane.markerSessionID)
}
