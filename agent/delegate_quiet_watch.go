package agent

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

type delegateQuietAttentionClaim struct {
	token      uint64
	lease      delegateLease
	sequence   uint64
	activityAt time.Time
	// notifiedAt is the tick instant that admitted this claim; a commit
	// re-baselines the repeat cadence on it so the next wake is one further
	// window out.
	notifiedAt  time.Time
	attentionID string
	content     string
	receiver    *Session
	done        chan struct{}
}

// quietRearmPendingLocked reports whether new activity must re-baseline the
// quiet cadence: a wake was committed, or a first wake's claim is still in
// flight for the current sequence. It is ReportActivityPhase's rearm predicate,
// shared so the controller paths that advance activityAt directly (steer
// persistence) rearm exactly the same way.
func (live *delegateLiveState) quietRearmPendingLocked() bool {
	return live.quietNotified || live.quietClaim != nil && live.quietClaim.sequence == live.quietSequence
}

// rearmQuietCadenceLocked clears a pending quiet wake and advances the stretch
// identity so the next watchdog window is measured from fresh activity and gets
// a fresh attention id. Advancing the sequence is what retires an in-flight
// claim: CompleteQuietAttention rejects it as stale, and without the advance the
// next wake would reuse the consumed id with a newer activity timestamp, which
// folds as a permanent "conflicting content" failure.
func (live *delegateLiveState) rearmQuietCadenceLocked() {
	if !live.quietRearmPendingLocked() {
		return
	}
	live.quietNotified = false
	live.quietNotifiedAt = time.Time{}
	live.quietSequence++
}

func (s *Session) runDelegateQuietWatchdogTick(lease delegateLease, now time.Time) error {
	if s == nil || s.delegateController == nil {
		return errDelegateDeliveryReceiverUnavailable
	}
	claim, err := s.delegateController.BeginQuietAttention(s, lease, now)
	if err != nil || claim == nil {
		return err
	}
	deferred, appendErr := s.appendQuietAttentionAtTurnBoundary(claim.attentionID, claim.content)
	if deferred {
		return s.delegateController.CompleteQuietAttention(claim, false)
	}
	return s.completeQuietWatchdogClaim(claim, appendErr)
}

// completeQuietWatchdogClaim commits an admitted quiet claim after its durable
// append attempt and arms the wake. The append is durable even when completion
// then finds the claim stale, so the wake is armed in that case too: a wake that
// is never armed leaves the durable attention pending with nothing to deliver
// it. armDelegateAttention is idempotent and no-ops when the attention is no
// longer pending, so an identity retired by a covering stop is not resurrected.
func (s *Session) completeQuietWatchdogClaim(claim *delegateQuietAttentionClaim, appendErr error) error {
	completionErr := s.delegateController.CompleteQuietAttention(claim, appendErr == nil)
	if appendErr == nil {
		completionErr = errors.Join(completionErr, s.armDelegateAttention(claim.attentionID))
	}
	return errors.Join(appendErr, completionErr)
}

// delegateQuietWatchEntry is one lease registered on the shared quiet-watchdog
// hub: the lease to tick plus a per-lease stop channel. Detach is best-effort:
// closing stop keeps later ticks from being dispatched to the lease, but a tick
// the hub loop already snapshotted may still run once for it.
type delegateQuietWatchEntry struct {
	lease delegateLease
	stop  chan struct{}
	// busy coalesces ticks while one tick for this registration is still
	// running: the hub loop swaps it from 0 to 1 with CompareAndSwap, and a
	// tick that loses the race is dropped rather than queued. *uint32 (not a
	// plain field) so entries stay comparable as map keys while still
	// sharing one atomic word per registration.
	busy *uint32
}

// delegateQuietWatchHub multiplexes one ticker across every live
// delegate-quiet watchdog registered on one Session. The hub keeps a single
// clock.Ticker (one goroutine, one clock waiter per Session) and fans each
// tick out to the currently registered leases. It runs on the session's
// injected clock (s.sclock()), so every lease on the session shares one ticker
// and stays on the fake clock in tests.
//
// The hub is owned by its Session's delegateQuietWatchHost: entries, and the
// host's pointer to the hub, change only under host.mu.
type delegateQuietWatchHub struct {
	ticker interface {
		C() <-chan time.Time
		Stop()
	}
	done    chan struct{}
	entries map[delegateQuietWatchEntry]struct{}
}

// delegateQuietWatchHost holds a session's live quiet-watch hub. mu is a leaf
// lock: nothing else is acquired while it is held, and tick work runs outside
// it. The hub exists exactly while at least one lease is registered, so the
// host never pins an idle session's ticker.
type delegateQuietWatchHost struct {
	mu  sync.Mutex
	hub *delegateQuietWatchHub
}

// delegateQuietWatchNext arms (or reuses) the session's shared hub and
// registers lease on it. Lookup, creation and registration happen in one
// critical section, so an entry always lands on the hub the host points at and
// can never strand on a stopped one.
// The returned CancelFunc detaches exactly this registration:
// the hub's single ticker keeps serving the remaining leases, and the hub
// goroutine exits (stopping the shared ticker) only when the last
// registration leaves, so no ticker leaks after a lease ends. Cancellation
// rides on the run context via context.AfterFunc rather than a parked
// goroutine per registration, so the hub goroutine stays the only
// steady-state goroutine per session no matter how many leases share it.
func (s *Session) delegateQuietWatchNext(ctx context.Context, lease delegateLease) context.CancelFunc {
	if ctx == nil {
		ctx = context.Background()
	}
	watchCtx, cancel := context.WithCancel(ctx)
	entry := delegateQuietWatchEntry{lease: lease, stop: make(chan struct{}), busy: new(uint32)}
	host := &s.quietWatch
	host.mu.Lock()
	hub := host.hub
	if hub == nil {
		hub = &delegateQuietWatchHub{
			ticker:  s.sclock().NewTicker(delegateQuietCheckInterval),
			done:    make(chan struct{}),
			entries: make(map[delegateQuietWatchEntry]struct{}),
		}
		host.hub = hub
		go s.serveDelegateQuietWatchHub(hub)
	}
	hub.entries[entry] = struct{}{}
	host.mu.Unlock()
	var detachOnce sync.Once
	stopped := make(chan struct{})
	detach := func() {
		detachOnce.Do(func() {
			// The last detach clears host.hub in the same critical section
			// that empties the hub, so exactly one detach retires a hub and a
			// concurrent attach either lands before it or builds a successor.
			host.mu.Lock()
			delete(hub.entries, entry)
			retired := len(hub.entries) == 0
			if retired {
				host.hub = nil
			}
			host.mu.Unlock()
			close(entry.stop)
			close(stopped)
			if retired {
				close(hub.done)
				hub.ticker.Stop()
			}
		})
	}
	stopAfter := context.AfterFunc(watchCtx, detach)
	return func() {
		detach()
		stopAfter()
		cancel()
		<-stopped
	}
}

// serveDelegateQuietWatchHub is the hub's single goroutine: each shared tick
// fans out to every registered lease whose stop channel is still open. Each
// lease's tick runs on its own goroutine so one lease blocked on durable
// transcript I/O (or armDelegateAttention's reservation path) cannot stall
// the remaining leases or delay hub shutdown: the loop never waits on tick
// work and keeps selecting on hub.done.
func (s *Session) serveDelegateQuietWatchHub(hub *delegateQuietWatchHub) {
	host := &s.quietWatch
	for {
		select {
		case now := <-hub.ticker.C():
			host.mu.Lock()
			if host.hub != hub {
				host.mu.Unlock()
				return
			}
			live := make([]delegateQuietWatchEntry, 0, len(hub.entries))
			for entry := range hub.entries {
				live = append(live, entry)
			}
			host.mu.Unlock()
			for _, entry := range live {
				// At most one tick runs per registration: a tick that arrives
				// while the previous one for the same lease is still blocked
				// is coalesced away instead of piling up a goroutine (and a
				// redundant work burst on release) per tick.
				if entry.busy == nil || !atomic.CompareAndSwapUint32(entry.busy, 0, 1) {
					continue
				}
				go func(entry delegateQuietWatchEntry) {
					defer atomic.StoreUint32(entry.busy, 0)
					select {
					case <-entry.stop:
						return
					default:
					}
					_ = s.runDelegateQuietWatchdogTick(entry.lease, now)
					if done := s.cfg.testOnly.delegateQuietWatchTickDone; done != nil {
						done(entry.lease)
					}
				}(entry)
			}
		case <-hub.done:
			return
		}
	}
}

func (s *Session) startDelegateQuietWatchdog(ctx context.Context, lease delegateLease) context.CancelFunc {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.delegateQuietWatchNext(ctx, lease)
}

func delegateQuietAttentionIDForStretch(lease delegateLease, sequence uint64) string {
	return fmt.Sprintf("quiet:%s:%d:%d", lease.delegateID, lease.generation, sequence)
}

func delegateQuietAttentionContent(lease delegateLease, activityAt time.Time) string {
	return fmt.Sprintf(
		"<delegate-notification delegate_id=%q>%s</delegate-notification>",
		html.EscapeString(lease.delegateID),
		html.EscapeString(quietWatchdogMessage(delegateQuietWindow, activityAt)),
	)
}

func (c *delegateTreeController) BeginQuietAttention(receiver *Session, lease delegateLease, now time.Time) (*delegateQuietAttentionClaim, error) {
	if c == nil || receiver == nil {
		return nil, errDelegateDeliveryReceiverUnavailable
	}
	release, err := c.beginRetirementMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err != nil {
		return nil, err
	}
	if c.hasSettlementClaimLocked(lease) {
		return nil, errDelegateTargetBusy
	}
	expectedReceiver := c.rootRuntime
	if aggregate.Descriptor.ParentDelegateID != "" {
		parent := c.live[aggregate.Descriptor.ParentDelegateID]
		if parent == nil {
			return nil, errDelegateDeliveryReceiverUnavailable
		}
		expectedReceiver = parent.runtime
	}
	if expectedReceiver == nil || expectedReceiver != receiver {
		return nil, errDelegateDeliveryReceiverUnavailable
	}
	activityAt := live.activityAt
	if activityAt.IsZero() {
		activityAt = aggregate.RunStartedAt
	}
	if now.IsZero() {
		now = c.now()
	}
	if activityAt.IsZero() || live.quietClaim != nil {
		return nil, nil
	}
	// The quiet clock restarts at the last wake, not only at the last activity:
	// a delegate that never reports activity again re-fires once per further
	// delegateQuietWindow instead of going dark after one notification. Activity
	// still rearms through ReportActivityPhase, which clears quietNotified so
	// this baseline falls back to activityAt. The result is one bounded wake per
	// window — never a burst — because each admission requires a further full
	// delegateQuietWindow of silence.
	quietSince := activityAt
	if live.quietNotified {
		quietSince = live.quietNotifiedAt
	}
	if now.Before(quietSince.Add(delegateQuietWindow)) {
		return nil, nil
	}
	if live.quietSequence == 0 {
		live.quietSequence = 1
	}
	c.nextToken++
	claim := &delegateQuietAttentionClaim{
		token:       c.nextToken,
		lease:       lease,
		sequence:    live.quietSequence,
		activityAt:  activityAt,
		notifiedAt:  now,
		attentionID: delegateQuietAttentionIDForStretch(lease, live.quietSequence),
		content:     delegateQuietAttentionContent(lease, activityAt),
		receiver:    receiver,
		done:        make(chan struct{}),
	}
	live.quietClaim = claim
	c.quietClaims[claim.token] = claim
	c.evidenceVersion++
	return claim, nil
}

func (c *delegateTreeController) CompleteQuietAttention(claim *delegateQuietAttentionClaim, committed bool) error {
	defer c.retirementChanged()
	if c == nil || claim == nil {
		return errDelegateStaleLease
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quietClaims[claim.token] != claim || claim.receiver == nil {
		return errDelegateStaleLease
	}
	delete(c.quietClaims, claim.token)
	live := c.live[claim.lease.delegateID]
	if live != nil && live.quietClaim == claim {
		live.quietClaim = nil
	}
	untrackStopLocked(c, func(s *delegateStopState) map[uint64]struct{} { return s.quietClaims }, claim.token)
	var result error
	if committed {
		aggregate := c.durable[claim.lease.delegateID]
		if aggregate == nil || aggregate.Generation != claim.lease.generation || !aggregate.CurrentRunOpen || aggregate.Phase != delegatestore.PhaseRunning || live == nil || live.binding == nil || live.binding.lease != claim.lease || live.quietSequence != claim.sequence || !live.activityAt.Equal(claim.activityAt) {
			result = errDelegateStaleLease
		} else {
			live.quietNotified = true
			live.quietNotifiedAt = claim.notifiedAt
			// Advance the stretch identity so the next repeat carries a fresh
			// attention id: a reused id would replay as a no-op and the repeat
			// wake would be silently swallowed.
			live.quietSequence++
		}
	}
	c.evidenceVersion++
	close(claim.done)
	return result
}

func (s *Session) appendQuietAttentionAtTurnBoundary(attentionID, content string) (bool, error) {
	s.delegateDeliveryMu.Lock()
	defer s.delegateDeliveryMu.Unlock()
	s.mu.Lock()
	processing := s.state == SessionProcessing
	closed := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closed {
		return false, errors.New("delegate attention receiver is closed")
	}
	if processing {
		return true, nil
	}
	_, err := s.appendDelegateNotificationDurably(attentionID, content)
	return false, err
}
