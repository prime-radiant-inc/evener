package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
)

type coldDelegateAttentionRef struct {
	delegateID    string
	transcriptRef string
}

// delegateAttentionState is one delegate's transient attention bookkeeping.
// The durable queue (delegatestore.Aggregate.PendingAttention) owns which rows
// the delegate owes and which of them have published a wake; this state holds
// only process-local scheduling and retry hints around it. New attention
// starts the delegate's delivery attempts over — clearing restoreFailures and
// parked — while its driveTurn stays, and the bootstrap rebuild replaces the
// whole map at once, so no path can leave one field behind for a delegate that
// owes a fresh row.
type delegateAttentionState struct {
	// restoreFailures counts consecutive cold restores of this delegate's
	// runtime that failed for a reason that is not transient
	// (isTransientStartFailure); the drive gives up at
	// maxDelegateAttentionRestoreFailures.
	restoreFailures int
	// parked is set when the attention could be neither delivered nor handed to
	// the root: the drive stops cold-restoring the delegate until new attention
	// arrives or the daemon restarts.
	parked bool
	// laneHeldQuiet is set when a foreign-held lane left owed attention
	// undeliverable, whether a launch refused the lane or a cold scheduled
	// restore refused it before any launch ran. The wake stays owed and
	// retried, but the drain's quiescence scans do not count it, so the one-shot
	// drain can still give up. A successful lane revalidation ends the quiet.
	laneHeldQuiet bool
	// driveTurn records when the attention drive last selected the delegate
	// (attentionDriveSeq order): the drive picks the delegate it has not picked
	// yet, else the one picked longest ago, so eligible delegates take turns and
	// none, failing or not, holds the others back.
	driveTurn uint64
}

func readExistingDelegateAttentionFold(path, expectedSessionID string) (delegateAttentionFold, error) {
	// The pre-stat is this boundary's strict shape: a transcript missing here
	// is an error, where readDelegateAttentionFold keeps missing-as-empty for
	// its historical callers, and foldcache reports a missing path as an
	// ordinary absent result rather than an error.
	if _, err := os.Stat(path); err != nil {
		return delegateAttentionFold{}, fmt.Errorf("stat delegate attention transcript: %w", err)
	}
	// The fold cache serves an unchanged transcript's fold without re-reading
	// it: the status sweeps this function backs run per thread/read, and
	// folding every eligible child's full transcript each time made a click on
	// a delegate-heavy session cost that session's total delegate transcript
	// bytes (session_attention.go's delegateAttentionFoldCache). A cache hit
	// skips the re-read and the post-read stat: a hit proves identical bytes
	// were fully read and folded in this process already, which is the same
	// readiness the retirement-prepare call below relies on (its
	// EstablishDurability flush changes the identity, so flushed-but-unread
	// bytes still recompute).
	result, err := delegateAttentionFoldCache.Get(context.Background(), path, extendDelegateAttentionFold(expectedSessionID))
	if err != nil {
		return delegateAttentionFold{}, err
	}
	// The memo is keyed to the expected session id as well as the path: a hit
	// computed for a different session (a transcript path reused across
	// sessions) or an absent result (the transcript removed between the
	// pre-stat and the cache's stat) must not serve the cached fold. Both
	// bypass the cache: read, fold, and post-read stat exactly like the
	// uncached path, leaving the memo untouched.
	if result.Value.sessionID != expectedSessionID {
		fold, _, err := readExistingDelegateAttentionFoldCompute(path, expectedSessionID)
		if err != nil {
			return delegateAttentionFold{}, err
		}
		// readDelegateAttentionFold retains missing-as-empty semantics for historical
		// callers. This projection boundary is strict, including a removal racing the
		// fold above.
		if _, err := os.Stat(path); err != nil {
			return delegateAttentionFold{}, fmt.Errorf("stat delegate attention transcript after read: %w", err)
		}
		return fold, nil
	}
	return result.Value.fold, nil
}

// reconcileDelegateAttentionFromTranscripts is the final bootstrap boundary.
// It folds every eligible receiver, queues the rows the fold still holds and
// resolves the rows it no longer carries in one journal batch per delegate,
// and lowers a rowless legacy boolean through the legacy event.
func (c *delegateTreeController) reconcileDelegateAttentionFromTranscripts() error {
	if c == nil {
		return errors.New("delegate attention controller is nil")
	}
	release, err := c.beginRetirementMutation()
	if err != nil {
		return err
	}
	defer release()
	c.mu.Lock()
	stateDir := c.stateDir
	refs := make([]coldDelegateAttentionRef, 0, len(c.durable))
	for id, aggregate := range c.durable {
		if aggregate == nil || !delegatestore.AttentionProjectionEligible(c.durable, id) {
			continue
		}
		refs = append(refs, coldDelegateAttentionRef{delegateID: id, transcriptRef: aggregate.Descriptor.TranscriptRef})
	}
	c.mu.Unlock()
	sort.Slice(refs, func(i, j int) bool { return refs[i].delegateID < refs[j].delegateID })

	pending := make(map[string][]string, len(refs))
	for _, ref := range refs {
		path, sessionID, err := delegateTranscriptPathFromRef(stateDir, ref.transcriptRef)
		if err != nil {
			return fmt.Errorf("delegate %s attention transcript: %w", ref.delegateID, err)
		}
		fold, err := readExistingDelegateAttentionFold(path, sessionID)
		if err != nil {
			return fmt.Errorf("delegate %s attention transcript: %w", ref.delegateID, err)
		}
		if ids := fold.pendingIDs(); len(ids) != 0 {
			pending[ref.delegateID] = ids
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(c.durable))
	for id := range c.durable {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	events := make([]delegatestore.Event, 0, len(ids))
	for _, id := range ids {
		aggregate := c.durable[id]
		if aggregate == nil {
			continue
		}
		want := make(map[string]struct{}, len(pending[id]))
		for _, attentionID := range pending[id] {
			want[attentionID] = struct{}{}
		}
		have := make(map[string]struct{}, len(aggregate.PendingAttention))
		for _, row := range aggregate.PendingAttention {
			have[row.AttentionID] = struct{}{}
			if _, keep := want[row.AttentionID]; !keep {
				events = append(events, attentionResolvedEvent(id, row.AttentionID))
			}
		}
		// Backfill the transcript's pending rows a pre-queue journal never
		// carried. The bootstrap publishes the set it reconciles, so every row
		// it adds counts as a committed wake: this is where a restart's owed
		// rows enter the drive. A row the journal already resolved stays out:
		// a child delegate's transcript keeps the rendered message after its
		// presentation because the resolution lives in the journal, so only a
		// genuinely unjournaled row is a pre-upgrade entry.
		added := make([]string, 0, len(want))
		for attentionID := range want {
			if _, exists := have[attentionID]; exists {
				continue
			}
			if _, resolved := aggregate.ResolvedAttention[attentionID]; resolved {
				continue
			}
			added = append(added, attentionID)
		}
		sort.Strings(added)
		for _, attentionID := range added {
			events = append(events, attentionQueuedEvent(id, attentionID, true))
		}
		// A pre-queue journal can also carry the legacy boolean with no rows to
		// derive it from. The bootstrap is the one migration boundary allowed to
		// lower it; steady-state production never emits the legacy event again.
		if len(aggregate.PendingAttention) == 0 && len(want) == 0 && aggregate.NeedsAttention {
			events = append(events, delegatestore.Event{
				Kind:       delegatestore.EventDelegateAttentionChanged,
				DelegateID: id,
				AttentionChanged: &delegatestore.DelegateAttentionChanged{
					NeedsAttention: false,
				},
			})
		}
	}
	if len(events) != 0 {
		if _, err := c.appendLocked(events...); err != nil {
			return fmt.Errorf("repair delegate attention projection: %w", err)
		}
		c.evidenceVersion++
	}
	// A delegate that still owes attention keeps its other per-delegate state —
	// drive turn, park and failure count — so a reconcile does not reset the
	// drive's scheduling; one that no longer owes any leaves the drive's line
	// entirely.
	rebuilt := make(map[string]*delegateAttentionState, len(pending))
	for id := range pending {
		if !delegatestore.AttentionProjectionEligible(c.durable, id) {
			continue
		}
		state := c.attention[id]
		if state == nil {
			state = &delegateAttentionState{}
		}
		rebuilt[id] = state
	}
	c.attention = rebuilt
	return nil
}

// openDelegateAttention serializes a transcript-derived open against accepted
// answer consumption for the same child. Delivery opens use their larger
// owner-open/source-ack batch; watch and quiet opens use this single-event path.
func (c *delegateTreeController) openDelegateAttention(delegateID, attentionID string) (bool, error) {
	release, err := c.beginRetirementMutation()
	if err != nil {
		return false, err
	}
	defer release()
	for {
		added, blocker, plan, emit, err := c.tryOpenDelegateAttention(delegateID, attentionID)
		if err != nil {
			return false, err
		}
		if blocker != nil {
			<-blocker
			continue
		}
		if emit {
			c.emitDelegateUpdate(plan)
		}
		return added, nil
	}
}

// tryOpenDelegateAttention performs one nonblocking projection-open attempt.
// A reservation blocker is returned to the caller without waiting so callers
// that hold a Session attention claim can release it before waiting.
// The admitted wake's superseded generation is returned with it, so a caller
// that holds session state ordered against that wake uses the journaled
// value rather than sampling one of its own.
func (c *delegateTreeController) tryOpenDelegateAttention(delegateID, attentionID string) (bool, <-chan struct{}, delegateUpdatePlan, bool, error) {
	if c == nil || delegateID == "" || attentionID == "" {
		return false, nil, delegateUpdatePlan{}, false, errDelegateNotControllable
	}
	release, err := c.beginRetirementMutation()
	if err != nil {
		return false, nil, delegateUpdatePlan{}, false, err
	}
	defer release()
	c.mu.Lock()
	added, blocker, plan, emit, err := c.openDelegateAttentionLocked(delegateID, attentionID)
	c.mu.Unlock()
	// A newly raised projection has to reach the root's drive: an arm that
	// lands while its delegate runs wakes no one by itself, and a lost raise
	// strands the row until some unrelated wake arrives.
	if err == nil && emit {
		c.retryDelegateAttentionLater()
	}
	return added, blocker, plan, emit, err
}

// openDelegateAttentionLocked is one admission attempt with the controller lock
// held. Callers must hold c.mu.
func (c *delegateTreeController) openDelegateAttentionLocked(delegateID, attentionID string) (bool, <-chan struct{}, delegateUpdatePlan, bool, error) {
	var blocker <-chan struct{}
	for _, record := range c.reservations {
		if record != nil && record.delegateID == delegateID && record.trigger == delegatestore.TriggerAttention && record.done != nil {
			blocker = record.done
			break
		}
	}
	if blocker != nil {
		return false, blocker, delegateUpdatePlan{}, false, nil
	}
	if c.durable[delegateID] == nil {
		return false, nil, delegateUpdatePlan{}, false, errDelegateNotControllable
	}
	// A row the queue holds but no wake has published is a rider a start commit
	// copied from the transcript: it still owes the arm's own wake. Only a
	// committed row is the duplicate. A committed row's wake also clears every
	// user-stop hold in the store, which is the only place the release lives.
	if c.delegateAttentionCommittedLocked(delegateID, attentionID) {
		return false, nil, delegateUpdatePlan{}, false, nil
	}
	raised := c.delegateAttentionWillRaiseLocked(delegateID)
	if _, err := c.appendLocked(attentionQueuedEvent(delegateID, attentionID, true)); err != nil {
		return false, nil, delegateUpdatePlan{}, false, err
	}
	c.resetDelegateAttentionAttemptsLocked(delegateID)
	var plan delegateUpdatePlan
	if raised {
		plan = c.capturedPlanLocked(delegateID)
	}
	c.evidenceVersion++
	return true, nil, plan, raised, nil
}

// attentionQueuedEvent builds the durable queue event that marks
// attentionID owed for delegateID. committed is true when the wake publishing
// the row is in this batch; a start commit copies riders with committed false
// and their own arm commits them later.
func attentionQueuedEvent(delegateID, attentionID string, committed bool) delegatestore.Event {
	return delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionQueued,
		DelegateID: delegateID,
		AttentionQueued: &delegatestore.AttentionQueued{
			AttentionID: attentionID,
			Committed:   committed,
		},
	}
}

// ensureDelegateAttentionQueuedIfUnresolved puts a durably appended child
// attention message into the delegate's queue without publishing its wake; the
// arm that publishes the wake commits the row (Committed true and the hold
// release) later. The store queue is the only authority for a child's pending
// attention. The call repairs the one window a durable transcript append can
// leave open: the message reached the transcript but the journal never took its
// queue event, because the append between the two failed. A resolved id is
// never repaired — the delegate already presented and answered that report —
// and a still-queued id needs nothing. A raise schedules the root's drive,
// because an arm landing while its delegate runs wakes no one by itself.
func (c *delegateTreeController) ensureDelegateAttentionQueuedIfUnresolved(delegateID, attentionID string) error {
	if c == nil || delegateID == "" || attentionID == "" {
		return nil
	}
	defer c.retirementChanged()
	c.mu.Lock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		c.mu.Unlock()
		return fmt.Errorf("delegate %q has no journal aggregate", delegateID)
	}
	if _, resolved := aggregate.ResolvedAttention[attentionID]; resolved {
		c.mu.Unlock()
		return nil
	}
	for _, row := range aggregate.PendingAttention {
		if row.AttentionID == attentionID {
			c.mu.Unlock()
			return nil
		}
	}
	raised := c.delegateAttentionWillRaiseLocked(delegateID)
	_, err := c.appendLocked(attentionQueuedEvent(delegateID, attentionID, false))
	c.mu.Unlock()
	if err == nil && raised {
		c.retryDelegateAttentionLater()
	}
	return err
}

func attentionResolvedEvent(delegateID, attentionID string) delegatestore.Event {
	return delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionResolved,
		DelegateID: delegateID,
		AttentionResolved: &delegatestore.AttentionResolved{
			AttentionID: attentionID,
		},
	}
}

// delegateAttentionCommittedLocked reports whether attentionID is a row whose
// wake has already published for delegateID.
func (c *delegateTreeController) delegateAttentionCommittedLocked(delegateID, attentionID string) bool {
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		return false
	}
	for _, row := range aggregate.PendingAttention {
		if row.AttentionID == attentionID {
			return row.Committed
		}
	}
	return false
}

// delegateAttentionWillRaiseLocked reports whether queueing one more unheld row
// would raise delegateID's NeedsAttention projection in the same Apply: the
// flag is down and the delegate is eligible to need attention. Held rows keep
// the flag down, so queueing one more row raises it there too.
func (c *delegateTreeController) delegateAttentionWillRaiseLocked(delegateID string) bool {
	aggregate := c.durable[delegateID]
	return aggregate != nil && !aggregate.NeedsAttention &&
		delegatestore.AttentionProjectionEligible(c.durable, delegateID)
}

// appendAttentionResolvedLocked removes ids from delegateID's durable queue in
// one journal batch. An identity the queue no longer holds appends nothing, so
// overlapping resolvers cannot duplicate an event. A successful batch also
// forgets the resolved ids' causes, so a later full-history turn cannot adopt
// lineage for attention that is already spent. keepCauses is for a transfer: the
// id is resolved here but the root receives the same identity, so its receiving
// turn still needs the cause.
func (c *delegateTreeController) appendAttentionResolvedLocked(delegateID string, ids map[string]struct{}, keepCauses bool) error {
	aggregate := c.durable[delegateID]
	if aggregate == nil || len(ids) == 0 {
		return nil
	}
	events := make([]delegatestore.Event, 0, len(ids))
	ordered := make([]string, 0, len(ids))
	for attentionID := range ids {
		if attentionID == "" {
			continue
		}
		if _, resolved := aggregate.ResolvedAttention[attentionID]; resolved {
			continue
		}
		ordered = append(ordered, attentionID)
	}
	sort.Strings(ordered)
	for _, attentionID := range ordered {
		events = append(events, attentionResolvedEvent(delegateID, attentionID))
	}
	if len(events) == 0 {
		return nil
	}
	if _, err := c.appendLocked(events...); err != nil {
		return err
	}
	if !keepCauses {
		c.attentionCauses.forget(ordered...)
	}
	c.clearTransientAttentionIfResolvedLocked(delegateID)
	return nil
}

// clearTransientAttentionIfResolvedLocked drops the process-local scheduling
// state once the durable queue holds no rows for delegateID. Parked, counted
// restore failures, the lane-held quiet and driveTurn all describe a retry for
// rows that no longer exist; leaving one behind would make a delegate that owes
// fresh attention look parked or tried.
func (c *delegateTreeController) clearTransientAttentionIfResolvedLocked(delegateID string) {
	if aggregate := c.durable[delegateID]; aggregate == nil || len(aggregate.PendingAttention) == 0 {
		delete(c.attention, delegateID)
	}
}

// forgetDelegateAttentionLocked resolves one queue row. An id the journal
// already resolved appends nothing, so a repeat forget is a no-op. An id the
// queue never held still records its resolution: a pre-upgrade transcript row
// is pending without a queue row, and the recorded resolution is what retires
// it.
func (c *delegateTreeController) forgetDelegateAttentionLocked(delegateID, attentionID string) error {
	if c == nil || delegateID == "" || attentionID == "" {
		return nil
	}
	return c.appendAttentionResolvedLocked(delegateID, map[string]struct{}{attentionID: {}}, false)
}

// attentionStateLocked returns delegateID's attention bookkeeping, creating it
// on first use. Callers must hold c.mu.
func (c *delegateTreeController) attentionStateLocked(delegateID string) *delegateAttentionState {
	state := c.attention[delegateID]
	if state == nil {
		state = &delegateAttentionState{}
		if c.attention == nil {
			c.attention = make(map[string]*delegateAttentionState)
		}
		c.attention[delegateID] = state
	}
	return state
}

// resetDelegateAttentionAttemptsLocked starts delegateID's delivery attempts
// over: no counted restore failures, and not parked.
func (c *delegateTreeController) resetDelegateAttentionAttemptsLocked(delegateID string) {
	state := c.attention[delegateID]
	if state == nil {
		return
	}
	state.parked = false
	state.restoreFailures = 0
}

// recordDelegateAttentionRestoreFailure records one more counted restore
// failure for delegateID and returns the consecutive count.
func (c *delegateTreeController) recordDelegateAttentionRestoreFailure(delegateID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.attentionStateLocked(delegateID)
	state.restoreFailures++
	return state.restoreFailures
}

// delegateAttentionRestored ends delegateID's run of counted failures.
func (c *delegateTreeController) delegateAttentionRestored(delegateID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.attention[delegateID]; state != nil {
		state.restoreFailures = 0
	}
}

// parkDelegateAttention stops the drive's cold restores of delegateID until
// new attention is queued for it or the daemon
// restarts (coldRestoreDriveCandidateLocked says what the park leaves). It
// reports whether this call parked it, so overlapping passes say so once.
func (c *delegateTreeController) parkDelegateAttention(delegateID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.attentionStateLocked(delegateID)
	state.restoreFailures = 0
	if state.parked {
		return false
	}
	state.parked = true
	return true
}

// giveUpAttentionPlan is the hand-over of delegateID's owed attention to the
// root, in the shape fenced attention escalates in, or false when it owes
// none.
func (c *delegateTreeController) giveUpAttentionPlan(delegateID string) (delegateFencedAttentionEscalation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil || len(c.delegateAttentionUnheldIDsLocked(delegateID)) == 0 {
		return delegateFencedAttentionEscalation{}, false
	}
	return c.escalationPlanLocked(delegateID, aggregate, c.delegateAttentionWakeIDsLocked(delegateID)), true
}

// escalationPlanLocked is the transfer of delegateID's owed attention ids to
// the root, in id order.
func (c *delegateTreeController) escalationPlanLocked(delegateID string, aggregate *delegatestore.Aggregate, ids map[string]struct{}) delegateFencedAttentionEscalation {
	attentionIDs := make([]string, 0, len(ids))
	for attentionID := range ids {
		attentionIDs = append(attentionIDs, attentionID)
	}
	sort.Strings(attentionIDs)
	var runtime *Session
	if live := c.live[delegateID]; live != nil {
		runtime = live.runtime
	}
	return delegateFencedAttentionEscalation{
		delegateID:    delegateID,
		transcriptRef: aggregate.Descriptor.TranscriptRef,
		attentionIDs:  attentionIDs,
		runtime:       runtime,
	}
}

// delegateAttentionUnheldIDsLocked returns the queue rows no user stop holds.
// Only these rows raise NeedsAttention, count as runnable wakes, or may be
// presented; a held row stays durably queued until a committed wake or an
// explicit send clears its hold in the same store.
func (c *delegateTreeController) delegateAttentionUnheldIDsLocked(delegateID string) map[string]struct{} {
	aggregate := c.durable[delegateID]
	if aggregate == nil || len(aggregate.PendingAttention) == 0 {
		return nil
	}
	ids := make(map[string]struct{}, len(aggregate.PendingAttention))
	for _, row := range aggregate.PendingAttention {
		if row.HeldThrough == 0 {
			ids[row.AttentionID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// delegateAttentionWakeIDsLocked returns a copy of delegateID's unheld durable
// queue IDs.
func (c *delegateTreeController) delegateAttentionWakeIDsLocked(delegateID string) map[string]struct{} {
	return c.delegateAttentionUnheldIDsLocked(delegateID)
}

// unheldDelegateAttentionIDs lists delegateID's pending rows no stop holds, in
// id order. It is the drive's selection list as well as the set of rows an
// automatic request may present.
func (c *delegateTreeController) unheldDelegateAttentionIDs(delegateID string) []string {
	if c == nil || delegateID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := c.delegateAttentionUnheldIDsLocked(delegateID)
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// heldDelegateAttentionIDs reports which of delegateID's queued rows a user
// stop holds. Their turns stay out of an automatic drive's model history.
func (c *delegateTreeController) heldDelegateAttentionIDs(delegateID string) map[string]struct{} {
	if c == nil || delegateID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		return nil
	}
	held := make(map[string]struct{})
	for _, row := range aggregate.PendingAttention {
		if row.HeldThrough != 0 {
			held[row.AttentionID] = struct{}{}
		}
	}
	if len(held) == 0 {
		return nil
	}
	return held
}

// owesDelegateAttention reports whether delegateID still has attention the
// driver has yet to deliver, escalate or forget.
func (c *delegateTreeController) owesDelegateAttention(delegateID string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.delegateAttentionUnheldIDsLocked(delegateID)) != 0
}

func (c *delegateTreeController) hasPendingDelegateAttention() bool {
	return c.hasOwedDelegateAttentionLocked(nil)
}

// hasDeliverableDelegateAttention is hasPendingDelegateAttention for the
// drain's give-up scans: attention held behind a foreign lane is owed and
// retried, but it is not deliverable work the one-shot drain can wait on.
func (c *delegateTreeController) hasDeliverableDelegateAttention() bool {
	return c.hasOwedDelegateAttentionLocked(func(state *delegateAttentionState) bool {
		return state.laneHeldQuiet
	})
}

// hasOwedDelegateAttentionLocked reports whether any delegate owes attention
// that skip does not hold back. parked is always held back: a parked delegate's
// own paths say why in their own warning.
func (c *delegateTreeController) hasOwedDelegateAttentionLocked(skip func(*delegateAttentionState) bool) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for delegateID, aggregate := range c.durable {
		if aggregate == nil || len(c.delegateAttentionUnheldIDsLocked(delegateID)) == 0 {
			continue
		}
		if state := c.attention[delegateID]; state != nil && (state.parked || (skip != nil && skip(state))) {
			continue
		}
		// Resumable alone excludes a closed delegate: every fold transition
		// into PhaseClosed requires !Resumable.
		if aggregate.Resumable && aggregate.PendingStopSeq == 0 {
			return true
		}
	}
	return false
}

// quietLaneHeldAttention marks delegateID's owed attention as held behind a
// lane another session owns, whether a launch refusal or a cold scheduled
// restore found it held. The state is created when absent so the refusal that
// dropped the committed start's wake and the reopen that restores it cannot
// land in either order and leave the drain counting the row.
func (c *delegateTreeController) quietLaneHeldAttention(delegateID string) {
	if c == nil || delegateID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.durable[delegateID] == nil {
		return
	}
	c.attentionStateLocked(delegateID).laneHeldQuiet = true
}

// clearLaneHeldAttention ends the quiet a successful lane revalidation
// resolves, so the next drive selects the owed row normally.
func (c *delegateTreeController) clearLaneHeldAttention(delegateID string) {
	if c == nil || delegateID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.attention[delegateID]; state != nil {
		state.laneHeldQuiet = false
	}
}

// delegateAttentionWakeEligibleLocked reports whether delegateID could accept
// an attention wake or an escalation right now, before the ancestor fence is
// consulted. ReserveAttention and every wake-cache scan start from it; the
// cold-restore drive then also skips parked delegates
// (coldRestoreDriveCandidateLocked), a park it does not know about, so a
// parked delegate's own runtime can still reserve and the escalation still
// reaches it. Every guard that makes this false must be classified in
// delegateAttentionRefusalLocked — as a refusal token, or as one of its
// documented deliberate silences — or that guard's refusals go invisible
// again (#6330).
func (c *delegateTreeController) delegateAttentionWakeEligibleLocked(delegateID string) bool {
	if !c.idleRestorableLocked(delegateID) {
		return false
	}
	if live := c.live[delegateID]; live != nil && (live.binding != nil || live.recoveryRequired) {
		return false
	}
	// A finished generation still finalizing hasn't announced its result: an
	// attention successor waits for the release, as a send does, so it can't
	// start ahead of that result. Every release wakes the root's drive for
	// attention it skipped here (releaseFinalizationLocked).
	if c.finalizingLocked(delegateID) != nil {
		return false
	}
	if record := c.reservationForDelegateLocked(delegateID); record != nil {
		return record.trigger == delegatestore.TriggerAttention
	}
	return true
}

// reservationForDelegateLocked returns the delegate's one start reservation,
// if any. Admission refuses a second reservation for the same delegate
// (ReserveAttention and the send path both decline when a record exists), so
// one record per delegate is an invariant both wake predicates rely on: the
// record found is the delegate's reservation.
func (c *delegateTreeController) reservationForDelegateLocked(delegateID string) *delegateStartRecord {
	for _, record := range c.reservations {
		if record != nil && record.delegateID == delegateID {
			return record
		}
	}
	return nil
}

// The guard predicates that refuse a delegate attention wake. These are stable
// machine tokens carried by the refusal warning's structured field, never
// prose: a lane stranded by a refusal must be distinguishable from a quiet
// one by which predicate held it (#6330), and tests pin the token.
const (
	// delegateAttentionRefusedBinding: the delegate's live runtime still
	// holds an open generation binding — a finishing generation that has not
	// released, or one the recovery latch holds.
	delegateAttentionRefusedBinding = "binding"
	// delegateAttentionRefusedRecoveryRequired: the delegate's last
	// generation failed its finish and is held behind the recovery latch.
	delegateAttentionRefusedRecoveryRequired = "recovery_required"
	// delegateAttentionRefusedFinalizing: a finished generation is still
	// finalizing on the delegate's runtime.
	delegateAttentionRefusedFinalizing = "finalizing"
	// delegateAttentionRefusedReservation: a non-attention start is
	// reserved for the delegate (a send), which owns its next generation.
	delegateAttentionRefusedReservation = "reservation"
	// delegateAttentionRefusedStartBlocked: the drive guard refused — the
	// delegate's runtime has a run, drive or finalizer in flight, or worktree
	// disposal holds it.
	delegateAttentionRefusedStartBlocked = "start_blocked"
	// delegateAttentionRefusedFatalRunGated: the delegate's runtime is gated
	// after a failed turn.
	delegateAttentionRefusedFatalRunGated = "fatal_run_gated"
)

// delegateAttentionRefusal is one delegate that owes attention and the guard
// predicate refusing its wake right now.
type delegateAttentionRefusal struct {
	delegateID string
	predicate  string
}

// pendingDelegateAttentionRefusals names every delegate that owes attention
// whose wake a guard predicate refuses, so the drive can warn which predicate
// holds each one (#6330). States that are deliberate or self-consuming are
// not refusals: a run still open is presenting what it owes, a closing,
// stopping, parked (the undeliverable warning already said so) or reclaimed
// delegate is owned by its teardown or escalation (which says so in its own
// warning), and an ancestor chain that is not restorable yet is a transitive
// wait. The caller holds no lock.
func (c *delegateTreeController) pendingDelegateAttentionRefusals() []delegateAttentionRefusal {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	refusals := make([]delegateAttentionRefusal, 0, len(c.durable))
	for delegateID, aggregate := range c.durable {
		if aggregate == nil || len(c.delegateAttentionUnheldIDsLocked(delegateID)) == 0 {
			continue
		}
		if state := c.attention[delegateID]; state != nil && state.parked {
			continue
		}
		if predicate := c.delegateAttentionRefusalLocked(delegateID); predicate != "" {
			refusals = append(refusals, delegateAttentionRefusal{delegateID: delegateID, predicate: predicate})
		}
	}
	sort.Slice(refusals, func(i, j int) bool { return refusals[i].delegateID < refusals[j].delegateID })
	return refusals
}

// delegateAttentionRefusalLocked names the guard predicate refusing
// delegateID's attention wake eligibility right now — one of the
// delegateAttentionRefused* tokens, or empty when nothing holds the wake. The
// deliberate silences: an eligible delegate (the leading check pins this
// classifier to delegateAttentionWakeEligibleLocked, so a guard later added
// there cannot leave a state this one warns about), a run still open that is
// presenting what it owes, a delegate owned by its teardown or escalation (a
// closing, stopping or reclaimed one — their own paths report), a delegate
// with neither a durable aggregate nor a live runtime (the post-restart
// window before generation recovery rebuilds them, a transitive wait), and a
// delegate the drive parked (the undeliverable warning already said so). The
// caller holds c.mu.
func (c *delegateTreeController) delegateAttentionRefusalLocked(delegateID string) string {
	if c.delegateAttentionWakeEligibleLocked(delegateID) {
		return ""
	}
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		return ""
	}
	live := c.live[delegateID]
	// A recovery latched below keeps its refusal visible even in states that
	// otherwise read as teardown or a consuming run: the durable generation
	// never settled, so nothing else will ever present the owed attention.
	recovery := live != nil && live.recoveryRequired
	if c.closing || aggregate.PendingStopSeq != 0 || !aggregate.Resumable || aggregate.Phase == delegatestore.PhaseClosed || aggregate.Phase == delegatestore.PhaseStopping || c.reclamationCoversLocked(delegateID) {
		if recovery {
			return delegateAttentionRefusedRecoveryRequired
		}
		return ""
	}
	if live != nil && live.binding != nil {
		if aggregate.CurrentRunOpen && !recovery {
			// An open run presents what it owes in its own requests.
			return ""
		}
		return delegateAttentionRefusedBinding
	}
	if recovery {
		return delegateAttentionRefusedRecoveryRequired
	}
	if c.finalizingLocked(delegateID) != nil {
		return delegateAttentionRefusedFinalizing
	}
	if record := c.reservationForDelegateLocked(delegateID); record != nil && record.trigger != delegatestore.TriggerAttention {
		return delegateAttentionRefusedReservation
	}
	// Nothing holds the wake: the delegate is idle and restorable, so the
	// drive selects it, or its teardown or escalation owns the attention.
	return ""
}

// hasRunnableDelegateAttention reports whether the root driver has actionable
// attention work: a wake it may commit, or attention it must escalate because
// a permanently closed ancestor fences the wake off forever. Attention parked
// under a transient ancestor stop, or under an ancestor chain its restore
// cannot make resident yet, is pending, not runnable.
func (c *delegateTreeController) hasRunnableDelegateAttention() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for delegateID, aggregate := range c.durable {
		if aggregate == nil || len(c.delegateAttentionUnheldIDsLocked(delegateID)) == 0 || !c.delegateAttentionWakeEligibleLocked(delegateID) {
			continue
		}
		// Attention a closed ancestor fenced off is the escalation's work,
		// parked or not.
		if _, closedAncestorID := c.attentionWakeAncestorGateLocked(delegateID); closedAncestorID != "" {
			return true
		}
		if c.coldRestoreDriveCandidateLocked(delegateID) {
			return true
		}
	}
	return false
}

func (c *delegateTreeController) retryDelegateAttentionLater() {
	if c == nil {
		return
	}
	c.mu.Lock()
	root := c.rootRuntime
	c.mu.Unlock()
	if root != nil {
		root.scheduleDelegateAttentionRetry()
	}
}

// selectDelegateAttentionWake is the wake driver's selection: it returns the
// next eligible delegate with pending attention and takes this pass's hold
// in one critical section, so no release claim can slip between selection
// and hold. The pass MUST release the hold on every exit; overlapping passes
// each hold their own reference.
func (c *delegateTreeController) selectDelegateAttentionWake() (string, string, bool) {
	if c == nil {
		return "", "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delegateID, attentionID, pending := c.nextIdleDelegateAttentionLocked()
	if !pending {
		return "", "", false
	}
	// Taking its turn sends the delegate to the back of the line, whatever
	// this pass then does with it.
	c.attentionDriveSeq++
	c.attentionStateLocked(delegateID).driveTurn = c.attentionDriveSeq
	c.holdAttentionRestoreLocked(delegateID)
	return delegateID, attentionID, true
}

// attentionWakeAncestorGateLocked is the ancestor side of an attention wake,
// shared by every wake-cache scan so the runnable check, selection, and
// escalation cannot disagree. ready means no ancestor fences the wake and the
// cold restore can make the owner chain resident; closedAncestorID names a
// permanently closed ancestor, whose fence escalates rather than waits.
func (c *delegateTreeController) attentionWakeAncestorGateLocked(delegateID string) (ready bool, closedAncestorID string) {
	parentID := c.durable[delegateID].Descriptor.ParentDelegateID
	blocked, closedAncestorID := c.ancestorFenceLocked(parentID)
	return !blocked && c.ancestorChainRestorableLocked(parentID), closedAncestorID
}

// coldRestoreDriveCandidateLocked reports whether the attention drive may
// cold-restore delegateID to deliver its wake-eligible attention: the drive
// has not parked it and the ancestor gate is ready. The park stops this
// drive's cold restores of a delegate that could not be restored or handed
// over, and only that: its own live runtime can still reserve its attention
// (ReserveAttention), and the fenced escalation still reaches it.
func (c *delegateTreeController) coldRestoreDriveCandidateLocked(delegateID string) bool {
	if state := c.attention[delegateID]; state != nil && state.parked {
		return false
	}
	ready, _ := c.attentionWakeAncestorGateLocked(delegateID)
	return ready
}

// ancestorChainRestorableLocked reports whether a child's cold restore can
// make its owner chain resident: walking up from parentID, each ancestor is
// either resident already or idle and restorable, the two conditions the
// restore itself applies (restoreColdDelegateOwnerRuntime). A running
// ancestor with no resident runtime (after a restart, until its generation
// is recovered) fails every restore beneath it target_busy, so its
// descendants wait rather than being selected pass after pass.
func (c *delegateTreeController) ancestorChainRestorableLocked(parentID string) bool {
	for ancestorID := parentID; ancestorID != ""; ancestorID = c.durable[ancestorID].Descriptor.ParentDelegateID {
		if c.residentDelegateRuntimeLocked(ancestorID) != nil {
			return true
		}
		if !c.idleRestorableLocked(ancestorID) {
			return false
		}
	}
	return true
}

func (c *delegateTreeController) nextIdleDelegateAttentionLocked() (string, string, bool) {
	delegateIDs := make([]string, 0, len(c.durable))
	for delegateID, aggregate := range c.durable {
		if aggregate == nil || len(c.delegateAttentionUnheldIDsLocked(delegateID)) == 0 || !c.delegateAttentionWakeEligibleLocked(delegateID) {
			continue
		}
		if !c.coldRestoreDriveCandidateLocked(delegateID) {
			continue
		}
		delegateIDs = append(delegateIDs, delegateID)
	}
	if len(delegateIDs) == 0 {
		return "", "", false
	}
	// Turns: a delegate the drive has not picked yet first, in id order,
	// then the one it picked longest ago. One delegate that can't be restored,
	// or whose drive keeps declining, must not hold every sibling back.
	delegateID := slices.MinFunc(delegateIDs, func(left, right string) int {
		return cmp.Or(cmp.Compare(c.attentionDriveTurnLocked(left), c.attentionDriveTurnLocked(right)), cmp.Compare(left, right))
	})
	rows := c.durable[delegateID].PendingAttention
	attentionIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.HeldThrough == 0 {
			attentionIDs = append(attentionIDs, row.AttentionID)
		}
	}
	sort.Strings(attentionIDs)
	return delegateID, attentionIDs[0], true
}

// attentionDriveTurnLocked is the delegate's last drive turn, zero before its
// first selection.
func (c *delegateTreeController) attentionDriveTurnLocked(delegateID string) uint64 {
	if state := c.attention[delegateID]; state != nil {
		return state.driveTurn
	}
	return 0
}

// delegateFencedAttentionEscalation is one delegate's pending attention that a
// permanently closed ancestor fences off forever. The root transfers each
// message to itself under the original identity and resolves the source.
type delegateFencedAttentionEscalation struct {
	delegateID    string
	transcriptRef string
	attentionIDs  []string
	runtime       *Session
	// parked: the drive gave up on this delegate, and its hand-over failed
	// once already. Its escalation reads the transcript strictly, so a
	// missing one keeps the attention owed instead of forgetting it.
	parked bool
}

// permanentlyFencedDelegateAttention lists pending attention wakes whose
// target sits under an ancestor that can never become resumable again. No
// generation can ever deliver them, so the root escalates and resolves them
// instead of retrying forever. Delegates that are not wake-eligible (in-flight
// generation, reservation, recovery, reclamation) are skipped and picked up on
// a later pass.
func (c *delegateTreeController) permanentlyFencedDelegateAttention() []delegateFencedAttentionEscalation {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	plans := make([]delegateFencedAttentionEscalation, 0)
	for delegateID, aggregate := range c.durable {
		if aggregate == nil || len(c.delegateAttentionUnheldIDsLocked(delegateID)) == 0 || !c.delegateAttentionWakeEligibleLocked(delegateID) {
			continue
		}
		if _, closedAncestorID := c.attentionWakeAncestorGateLocked(delegateID); closedAncestorID == "" {
			continue
		}
		plan := c.escalationPlanLocked(delegateID, aggregate, c.delegateAttentionWakeIDsLocked(delegateID))
		if state := c.attention[delegateID]; state != nil {
			plan.parked = state.parked
		}
		plans = append(plans, plan)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].delegateID < plans[j].delegateID })
	return plans
}

func (c *delegateTreeController) forgetDelegateAttention(delegateID string, attentionIDs ...string) error {
	return c.forgetDelegateAttentionCauses(false, delegateID, attentionIDs...)
}

// forgetTransferredDelegateAttention resolves a row whose message has been
// handed to the root under the same attention identity. The cause stays in the
// registry for the root's receiving turn; the root's own resolution forgets it.
func (c *delegateTreeController) forgetTransferredDelegateAttention(delegateID, attentionID string) error {
	return c.forgetDelegateAttentionCauses(true, delegateID, attentionID)
}

func (c *delegateTreeController) forgetDelegateAttentionCauses(keepCauses bool, delegateID string, attentionIDs ...string) error {
	defer c.retirementChanged()
	if c == nil {
		return nil
	}
	c.mu.Lock()
	wasNeeding := false
	if aggregate := c.durable[delegateID]; aggregate != nil {
		wasNeeding = aggregate.NeedsAttention
	}
	ids := make(map[string]struct{}, len(attentionIDs))
	for _, attentionID := range attentionIDs {
		if attentionID != "" {
			ids[attentionID] = struct{}{}
		}
	}
	err := c.appendAttentionResolvedLocked(delegateID, ids, keepCauses)
	var plan delegateUpdatePlan
	publish := false
	if err == nil && wasNeeding {
		if aggregate := c.durable[delegateID]; aggregate != nil && !aggregate.NeedsAttention {
			plan = c.capturedPlanLocked(delegateID)
			publish = true
		}
	}
	c.mu.Unlock()
	if publish {
		c.emitDelegateUpdate(plan)
	}
	return err
}

// holdDelegateAttention stamps a user-stop quiet on one queued row in the same
// store Apply that recomputes NeedsAttention, exactly as forgetDelegateAttention
// resolves a row. A held row stays durably pending but stops counting as a
// runnable wake, so the delegate retires cleanly instead of looping on a report
// its stop deliberately withheld. A generation of zero is a no-op: the store
// rejects it, because every hold must name the settled generation it stands for.
func (c *delegateTreeController) holdDelegateAttention(delegateID, attentionID string, generation uint64) error {
	defer c.retirementChanged()
	if c == nil || delegateID == "" || attentionID == "" || generation == 0 {
		return nil
	}
	c.mu.Lock()
	wasNeeding := false
	if aggregate := c.durable[delegateID]; aggregate != nil {
		wasNeeding = aggregate.NeedsAttention
	}
	event := delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionHeld,
		DelegateID: delegateID,
		AttentionHeld: &delegatestore.AttentionHeld{
			AttentionID: attentionID,
			Generation:  generation,
		},
	}
	_, err := c.appendLocked(event)
	var plan delegateUpdatePlan
	publish := false
	if err == nil && wasNeeding {
		if aggregate := c.durable[delegateID]; aggregate != nil && !aggregate.NeedsAttention {
			plan = c.capturedPlanLocked(delegateID)
			publish = true
		}
	}
	c.mu.Unlock()
	if publish {
		c.emitDelegateUpdate(plan)
	}
	return err
}

// holdAttentionRestore marks delegateID as owned by an attention wake pass:
// its runtime was restored cold and the pass is between that restore and its
// reservation decision. Release claims must not reap the runtime inside that
// span — the reservation would commit against a husk and the wake retry would
// pay a second cold restore. The pass releases the hold at exit, whichever way
// it decides. Holds count, so overlapping wake passes each keep their own
// reference and one pass's exit cannot drop another's hold.
func (c *delegateTreeController) holdAttentionRestore(delegateID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holdAttentionRestoreLocked(delegateID)
}

// holdAttentionRestoreLocked is the one definition of the hold mutation: the
// nil-map initialization plus the counted hold. Both hold sites share it — the
// wake driver's selection, which already holds c.mu, and the standalone hold,
// which takes the mutex itself — so the mutation semantics cannot diverge
// between what tests exercise and what the driver takes. Callers must hold
// c.mu.
func (c *delegateTreeController) holdAttentionRestoreLocked(delegateID string) {
	if c.attentionRestoreHolds == nil {
		c.attentionRestoreHolds = make(map[string]int)
	}
	c.attentionRestoreHolds[delegateID]++
}

// releaseAttentionRestoreHold drops this wake pass's hold. Releasing an
// unheld delegate is a no-op, so a declined or failed pass cannot leak the
// hold by clearing twice or out of order, and the last reference out clears
// the delegate for claims again.
func (c *delegateTreeController) releaseAttentionRestoreHold(delegateID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	releaseCountedLocked(c.attentionRestoreHolds, delegateID)
}

// attentionRestoreHeldLocked reports whether an attention wake pass holds
// delegateID between its cold restore and its reservation decision. Callers
// must hold c.mu.
func (c *delegateTreeController) attentionRestoreHeldLocked(delegateID string) bool {
	return c.attentionRestoreHolds[delegateID] > 0
}

// armColdDelegateAttention admits a wake only after re-folding the exact cold
// receiver transcript. It publishes directly into the controller's one
// unresolved-ID set; the caller retains responsibility for retrying a failed
// transcript read.
func (c *delegateTreeController) armColdDelegateAttention(delegateID, attentionID string) error {
	release, err := c.beginRetirementMutation()
	if err != nil {
		return err
	}
	defer release()
	armed, err := c.armColdDelegateAttentionOnce(delegateID, attentionID)
	c.mu.Lock()
	root := c.rootRuntime
	c.mu.Unlock()
	if err == nil && armed && root != nil {
		root.notify()
	}
	return err
}

func (c *delegateTreeController) armColdDelegateAttentionOnce(delegateID, attentionID string) (bool, error) {
	if c == nil || delegateID == "" || attentionID == "" {
		return false, errors.New("cold delegate attention identity is incomplete")
	}
	release, err := c.beginRetirementMutation()
	if err != nil {
		return false, err
	}
	defer release()
	c.mu.Lock()
	aggregate := c.durable[delegateID]
	stateDir := c.stateDir
	transcriptRef := ""
	if aggregate != nil {
		transcriptRef = aggregate.Descriptor.TranscriptRef
	}
	c.mu.Unlock()
	if transcriptRef == "" {
		return false, errDelegateNotControllable
	}
	path, sessionID, err := delegateTranscriptPathFromRef(stateDir, transcriptRef)
	if err != nil {
		return false, err
	}
	ids, err := readPendingDelegateAttention(path, sessionID)
	if err != nil {
		return false, err
	}
	if !slices.Contains(ids, attentionID) {
		return false, nil
	}
	return c.openDelegateAttention(delegateID, attentionID)
}

func (c *delegateTreeController) idleDelegateRestoreCommit(delegateID string) (delegateStartCommit, string, error) {
	if c == nil || delegateID == "" {
		return delegateStartCommit{}, "", errDelegateNotControllable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.idleRestorableLocked(delegateID) {
		return delegateStartCommit{}, "", errDelegateTargetBusy
	}
	aggregate := c.durable[delegateID]
	if c.laneHandoffCoversLocked(delegateID) {
		return delegateStartCommit{}, "", errDelegateTargetBusy
	}
	// The cold path never runs CommitStart, so it owns the ancestor check the
	// ordinary start paths get at commit: a closed ancestor must not be revived
	// into a lane that was already handed off.
	if blocked, _ := c.ancestorFenceLocked(aggregate.Descriptor.ParentDelegateID); blocked {
		return delegateStartCommit{}, "", errDelegateTargetBusy
	}
	descriptor := cloneDelegateStartDescriptor(aggregate.Descriptor)
	worktreePath := ""
	if descriptor.Isolation == "worktree" {
		worktreePath = descriptor.WorkingDir
	}
	return delegateStartCommit{
		lease:          delegateLease{delegateID: delegateID, generation: aggregate.Generation},
		ctx:            context.Background(),
		descriptor:     descriptor,
		transcriptPath: schema.SessionTranscriptPath(c.stateDir, descriptor.ChildSessionID),
		worktreePath:   worktreePath,
	}, descriptor.ParentDelegateID, nil
}

func (c *delegateTreeController) residentDelegateRuntime(delegateID string) *Session {
	if c == nil || delegateID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.residentDelegateRuntimeLocked(delegateID)
}

// residentDelegateRuntimeLocked is delegateID's live runtime when it is
// resident and neither closed nor stopping, else nil.
func (c *delegateTreeController) residentDelegateRuntimeLocked(delegateID string) *Session {
	aggregate := c.durable[delegateID]
	live := c.live[delegateID]
	if aggregate == nil || aggregate.Phase == delegatestore.PhaseClosed || aggregate.PendingStopSeq != 0 || live == nil {
		return nil
	}
	return live.runtime
}

// idleRestorableLocked reports whether delegateID can be restored cold for
// an attention wake: idle, resumable, not stopping, not being reclaimed, on
// a controller that is not closing.
func (c *delegateTreeController) idleRestorableLocked(delegateID string) bool {
	aggregate := c.durable[delegateID]
	return !c.closing && aggregate != nil && aggregate.Phase == delegatestore.PhaseIdle && aggregate.Resumable && aggregate.PendingStopSeq == 0 && !c.reclamationCoversLocked(delegateID)
}

// AttachIdleRuntime installs only the exact lazily restored runtime identity.
// It creates no generation and writes no durable controller event.
func (c *delegateTreeController) AttachIdleRuntime(delegateID string, runtime *Session) error {
	if c == nil || runtime == nil || delegateID == "" {
		return errDelegateTargetBusy
	}
	installation, err := c.beginIdleRuntimeInstallation(delegateID)
	if err != nil {
		return err
	}
	defer installation.release()
	return installation.attach(runtime)
}

// An installation owns admission across manager bind and publication. Its
// creator must release it only after the manager has dropped its owner lock.
type delegateIdleRuntimeInstallation struct {
	controller *delegateTreeController
	delegateID string
	release    func()
}

func (c *delegateTreeController) beginIdleRuntimeInstallation(delegateID string) (*delegateIdleRuntimeInstallation, error) {
	release, err := c.beginRetirementMutation()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	refused := c.closing || c.laneHandoffCoversLocked(delegateID)
	c.mu.Unlock()
	if refused {
		release()
		return nil, errDelegateTargetBusy
	}
	return &delegateIdleRuntimeInstallation{controller: c, delegateID: delegateID, release: release}, nil
}

func (installation *delegateIdleRuntimeInstallation) attach(runtime *Session) error {
	c, delegateID := installation.controller, installation.delegateID
	if c == nil || runtime == nil || delegateID == "" {
		return errDelegateTargetBusy
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if c.closing || aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || aggregate.PendingStopSeq != 0 || runtime.delegateController != c || runtime.owningDelegateID != delegateID || runtime.ID() != aggregate.Descriptor.ChildSessionID {
		return errDelegateTargetBusy
	}
	if c.laneHandoffCoversLocked(delegateID) {
		return errDelegateTargetBusy
	}
	live := c.live[delegateID]
	if live == nil {
		live = &delegateLiveState{}
		c.live[delegateID] = live
	}
	if live.binding != nil || live.recoveryRequired || live.runtime != nil && live.runtime != runtime {
		return errDelegateTargetBusy
	}
	ownerID, owner, err := c.runtimeOwnerLocked(runtime)
	if err != nil || owner != nil && owner != live || ownerID != "" && ownerID != delegateID {
		return errDelegateTargetBusy
	}
	c.setResidentRuntimeLocked(live, runtime)
	c.evidenceVersion++
	return nil
}
