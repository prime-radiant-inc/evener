package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/delegatestore"
)

// delegateIdleReleaseDelayDefault is how long a stable delegate stays warm
// after its generation finalizes before its resident runtime subtree is
// released non-terminally. The grace exists because the warm follow-up is a
// documented, load-bearing pattern: a completed delegate is routinely driven
// again moments after it finishes (delegate_send steering, attention
// callbacks), and "a caller that needs a drivable child must wait for
// quiescence first" would be meaningless if quiescence itself released the
// runtime. Thirty seconds covers those bursts while still
// bounding the leak this release exists for — a daemon's retained stdio MCP
// server processes previously lived as long as the daemon did (days), not
// half a minute. Tests override it via testOnly.delegateIdleReleaseDelay.
const delegateIdleReleaseDelayDefault = 30 * time.Second

type delegateRuntimeReclamationEntry struct {
	delegateID     string
	childSessionID string
	runtime        *Session
	ownerRuntime   *Session
	// depth is the member's depth in the durable tree, computed under the
	// controller mutex when the claim builds its entries. The idle
	// teardown groups members into depth waves — deepest first — so
	// same-depth members settle concurrently while a member never starts
	// before every deeper descendant has finished.
	depth int
}

type delegateRuntimeReclamationClaim struct {
	token   uint64
	roots   []delegateRuntimeReclamationEntry
	entries []delegateRuntimeReclamationEntry
}

type delegateRuntimeReclamationCandidate struct {
	root         delegateRuntimeReclamationEntry
	entries      []delegateRuntimeReclamationEntry
	closed       bool
	acknowledged bool
	endedAt      time.Time
}

// ClaimRuntimeReclamation reserves enough quiescent terminal runtime subtrees
// to admit required new resident runtimes. The durable delegate tree is not
// mutated; the claim only fences process-local runtime ownership while callers
// close the selected sessions outside the controller mutex.
//
// Unlike the idle release, capacity reclamation deliberately does not refuse
// a subtree that owns a shared task store: eviction here is terminal, closing
// the resumability of every member it tears down. A resolver elsewhere in the
// tree that names an evicted member as its shared-store owner fails its
// owner-residency check on the next resume — eviction behavior this path
// has always had — which the idle release's non-terminal teardown must not
// reproduce, hence the guard there and not here.
func (c *delegateTreeController) ClaimRuntimeReclamation(required int) (*delegateRuntimeReclamationClaim, error) {
	retirementRelease, retirementErr := c.beginRetirementMutation()
	if retirementErr != nil {
		return nil, retirementErr
	}
	defer retirementRelease()
	if c == nil {
		return nil, errors.New("delegate controller is unavailable")
	}
	if required <= 0 {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil, errDelegateTargetBusy
	}

	// A resident subtree another in-flight claim — an idle release closing a
	// just-finalized generation — is already settling and frees capacity the
	// moment it completes, so admission must not fail loudly for capacity that
	// is about to appear: count only what no claim is currently settling. An
	// aborted claim restores residency and a later spawn recomputes, so an
	// over-promise self-heals rather than stranding the spawn. Serializing
	// admission until the settling claim completes was rejected: it would put
	// a retirement-mutation wait on the spawn path, trading this self-healing
	// over-admission for a new refusal mode.
	resident := 0
	for id, aggregate := range c.durable {
		if !c.isResidentTerminalRuntimeLocked(id, aggregate) {
			continue
		}
		if _, settling := c.reclaiming[id]; settling {
			continue
		}
		resident++
	}
	needed := resident + required - c.maxRetainedTerminal
	if needed <= 0 {
		return nil, nil
	}

	candidatesByID := make(map[string]delegateRuntimeReclamationCandidate)
	for id, aggregate := range c.durable {
		if !c.isResidentTerminalRuntimeLocked(id, aggregate) {
			continue
		}
		entries, ok := c.claimableRuntimeSubtreeLocked(id)
		if !ok {
			continue
		}
		candidatesByID[id] = delegateRuntimeReclamationCandidate{
			root:         c.runtimeReclamationEntryLocked(id),
			entries:      entries,
			closed:       aggregate.Phase == delegatestore.PhaseClosed || !aggregate.Resumable,
			acknowledged: len(aggregate.PendingDeliveries) == 0,
			endedAt:      aggregate.LatestOutcome.EndedAt,
		}
	}

	// A claim closes a whole quiescent subtree. Keep only its highest resident
	// root so descendants are never selected twice or closed independently of a
	// reclaimable retained parent.
	candidates := make([]delegateRuntimeReclamationCandidate, 0, len(candidatesByID))
	for id, candidate := range candidatesByID {
		hasCandidateAncestor := false
		for parentID := c.durable[id].Descriptor.ParentDelegateID; parentID != ""; {
			if _, ok := candidatesByID[parentID]; ok {
				hasCandidateAncestor = true
				break
			}
			parent := c.durable[parentID]
			if parent == nil {
				break
			}
			parentID = parent.Descriptor.ParentDelegateID
		}
		if !hasCandidateAncestor {
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.closed != right.closed {
			return left.closed
		}
		if left.acknowledged != right.acknowledged {
			return left.acknowledged
		}
		if !left.endedAt.Equal(right.endedAt) {
			return left.endedAt.Before(right.endedAt)
		}
		return left.root.delegateID < right.root.delegateID
	})

	selected := make([]delegateRuntimeReclamationCandidate, 0, len(candidates))
	claimed := 0
	for _, candidate := range candidates {
		selected = append(selected, candidate)
		claimed += len(candidate.entries)
		if claimed >= needed {
			break
		}
	}
	if claimed < needed {
		return nil, fmt.Errorf("retained delegate limit reached (%d): no quiescent terminal runtime subtree can reclaim %d required slot(s)", c.maxRetainedTerminal, needed)
	}

	roots := make([]delegateRuntimeReclamationEntry, 0, len(selected))
	entries := make([]delegateRuntimeReclamationEntry, 0, claimed)
	for _, candidate := range selected {
		roots = append(roots, candidate.root)
		entries = append(entries, candidate.entries...)
	}
	return c.armRuntimeReclamationClaimLocked(roots, entries), nil
}

// CompleteRuntimeReclamation clears only the exact runtime pointers that the
// caller reports closed. A replacement installed after the claim survives.
func (c *delegateTreeController) CompleteRuntimeReclamation(claim *delegateRuntimeReclamationClaim, closed map[string]*Session) error {
	defer c.retirementChanged()
	if c == nil {
		return errors.New("delegate controller is unavailable")
	}
	if claim == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reclamations[claim.token] != claim {
		return errDelegateStaleLease
	}
	for _, entry := range claim.entries {
		if closed[entry.delegateID] != entry.runtime {
			continue
		}
		if live := c.live[entry.delegateID]; live != nil && live.binding == nil && live.runtime == entry.runtime {
			live.runtime = nil
		}
	}
	c.releaseRuntimeReclamationLocked(claim)
	return nil
}

func (c *delegateTreeController) AbortRuntimeReclamation(claim *delegateRuntimeReclamationClaim) error {
	defer c.retirementChanged()
	if c == nil {
		return errors.New("delegate controller is unavailable")
	}
	if claim == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reclamations[claim.token] != claim {
		return errDelegateStaleLease
	}
	c.releaseRuntimeReclamationLocked(claim)
	return nil
}

func (c *delegateTreeController) releaseRuntimeReclamationLocked(claim *delegateRuntimeReclamationClaim) {
	delete(c.reclamations, claim.token)
	for _, entry := range claim.entries {
		if c.reclaiming[entry.delegateID] == claim.token {
			delete(c.reclaiming, entry.delegateID)
		}
	}
	c.evidenceVersion++
}

// armRuntimeReclamationClaimLocked mints the claim token, fences every claimed
// member under the reclaiming map, registers the claim, and bumps the evidence
// version. Both claim entrypoints — capacity selection over retained-terminal
// pressure and the named idle-release subtree — share it, so the claim-side
// invariants live in one place, mirroring releaseRuntimeReclamationLocked on
// the completion side.
func (c *delegateTreeController) armRuntimeReclamationClaimLocked(roots, entries []delegateRuntimeReclamationEntry) *delegateRuntimeReclamationClaim {
	c.nextToken++
	claim := &delegateRuntimeReclamationClaim{token: c.nextToken, roots: roots, entries: entries}
	for _, entry := range claim.entries {
		c.reclaiming[entry.delegateID] = claim.token
	}
	c.reclamations[claim.token] = claim
	c.evidenceVersion++
	return claim
}

func (c *delegateTreeController) isResidentTerminalRuntimeLocked(id string, aggregate *delegatestore.Aggregate) bool {
	if aggregate == nil || aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.Phase != delegatestore.PhaseIdle && aggregate.Phase != delegatestore.PhaseClosed {
		return false
	}
	if c.attentionRestoreHeldLocked(id) {
		// An attention wake pass owns this runtime between its cold restore
		// and its reservation decision; it is not plain terminal-idle yet.
		return false
	}
	live := c.live[id]
	return live != nil && live.binding == nil && live.runtime != nil
}

func (c *delegateTreeController) claimableRuntimeSubtreeLocked(rootID string) ([]delegateRuntimeReclamationEntry, bool) {
	members := c.subtreeMembersLocked(rootID)
	if c.runtimeReclamationIntersectsProcessWorkLocked(members) {
		return nil, false
	}
	entries := make([]delegateRuntimeReclamationEntry, 0, len(members))
	for _, id := range c.memberIDsLeafFirstLocked(members) {
		aggregate := c.durable[id]
		if aggregate == nil || aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.Phase != delegatestore.PhaseIdle && aggregate.Phase != delegatestore.PhaseClosed {
			return nil, false
		}
		if c.attentionRestoreHeldLocked(id) {
			// A wake pass holds this member between its cold restore and its
			// reservation decision. Held members refuse the subtree before
			// the live-entry check: a member mid cold restore has no live
			// entry yet, and its absence must not let a claim sweep the
			// restored parent the chain restore is still using.
			return nil, false
		}
		if live := c.live[id]; live != nil {
			// Only steering a LIVE generation still owes counts as work in
			// flight. An admission carried across a covering stop is a parcel
			// for a successor, and stopping a delegate is normally terminal --
			// once resumability is closed ReserveStart refuses, so no successor
			// can ever consume it. Counting it here would pin this runtime, and
			// the bail below is subtree-wide, so it would pin every sibling too:
			// one stopped-and-forgotten delegate burning a maxRetainedTerminal
			// slot for the life of the process.
			if live.binding != nil || live.recoveryRequired || live.finalizationRecoveryRequired || live.recoveryRunnerPending || liveGenerationOwesSteering(live) || len(live.waiters) != 0 || live.quietClaim != nil {
				return nil, false
			}
			if live.runtime != nil {
				entries = append(entries, c.runtimeReclamationEntryLocked(id))
			}
		}
	}
	return entries, len(entries) != 0
}

func (c *delegateTreeController) runtimeReclamationEntryLocked(id string) delegateRuntimeReclamationEntry {
	aggregate := c.durable[id]
	live := c.live[id]
	entry := delegateRuntimeReclamationEntry{delegateID: id}
	if aggregate != nil {
		entry.childSessionID = aggregate.Descriptor.ChildSessionID
		entry.ownerRuntime = c.ownerRuntimeLocked(aggregate)
		entry.depth = c.delegateDepthLocked(id)
	}
	if live != nil {
		entry.runtime = live.runtime
	}
	return entry
}

func (c *delegateTreeController) runtimeReclamationIntersectsProcessWorkLocked(members map[string]struct{}) bool {
	if c.stop != nil {
		for id := range members {
			if _, ok := c.stop.members[id]; ok {
				return true
			}
		}
	}
	for id := range members {
		if _, ok := c.reclaiming[id]; ok {
			return true
		}
	}
	for _, reservation := range c.reservations {
		if reservation != nil && delegateIDInSet(reservation.delegateID, members) {
			return true
		}
	}
	for _, lease := range c.inputClaims {
		if delegateIDInSet(lease.delegateID, members) {
			return true
		}
	}
	for _, claim := range c.steeringClaims {
		if claim != nil && delegateIDInSet(claim.delegateID, members) {
			return true
		}
	}
	for _, claim := range c.modelClaims {
		if claim != nil && delegateIDInSet(claim.lease.delegateID, members) {
			return true
		}
	}
	for _, claim := range c.settlementClaims {
		if claim != nil && delegateIDInSet(claim.lease.delegateID, members) {
			return true
		}
	}
	for _, work := range c.work {
		if work != nil && delegateIDInSet(work.owner.delegateID, members) {
			return true
		}
	}
	for _, receipt := range c.deliveries {
		if receipt != nil && (delegateIDInSet(receipt.delegateID, members) || delegateIDInSet(receipt.ownerID, members)) {
			return true
		}
	}
	for _, claim := range c.deliveryClaims {
		if claim != nil && (delegateIDInSet(claim.delegateID, members) || delegateIDInSet(claim.ownerID, members)) {
			return true
		}
	}
	for _, claim := range c.quietClaims {
		if claim != nil && delegateIDInSet(claim.lease.delegateID, members) {
			return true
		}
	}
	for _, receipt := range c.watchEnqueues {
		if receipt != nil && (delegateIDInSet(receipt.sourceDelegateID, members) || delegateIDInSet(receipt.receiverDelegateID, members)) {
			return true
		}
	}
	for _, receipt := range c.watchDeliveries {
		if receipt != nil && (delegateIDInSet(receipt.sourceDelegateID, members) || delegateIDInSet(receipt.receiverDelegateID, members)) {
			return true
		}
	}
	for _, lease := range c.reconcileOrder {
		if delegateIDInSet(lease.delegateID, members) {
			return true
		}
	}
	return false
}

func delegateIDInSet(id string, ids map[string]struct{}) bool {
	if id == "" {
		return false
	}
	_, ok := ids[id]
	return ok
}

func (c *delegateTreeController) reclamationCoversLocked(delegateID string) bool {
	for id := delegateID; id != ""; {
		if _, ok := c.reclaiming[id]; ok {
			return true
		}
		aggregate := c.durable[id]
		if aggregate == nil {
			return false
		}
		id = aggregate.Descriptor.ParentDelegateID
	}
	return false
}

// ownerRuntimeLocked returns the resident session that tracks the delegate's
// runtime as a child: the root runtime for a root delegate, otherwise the
// parent delegate's runtime, or nil when the parent is not resident.
func (c *delegateTreeController) ownerRuntimeLocked(aggregate *delegatestore.Aggregate) *Session {
	parentID := aggregate.Descriptor.ParentDelegateID
	if parentID == "" {
		return c.rootRuntime
	}
	if parent := c.live[parentID]; parent != nil {
		return parent.runtime
	}
	return nil
}

// unhookReclaimedRuntimeRecord removes a reclaimed member's record from its
// owner's subagent manager so the released runtime leaves no live record
// behind. Both reclamation paths — capacity eviction and the idle release —
// unhook identically; the ordering around the unhook differs and stays
// per-caller.
func unhookReclaimedRuntimeRecord(entry delegateRuntimeReclamationEntry) {
	if entry.ownerRuntime != nil && entry.ownerRuntime.subagents != nil {
		entry.ownerRuntime.subagents.removeSession(entry.childSessionID, entry.runtime)
	}
}

func (s *Session) reclaimDelegateRuntimeCapacity(required int) (err error) {
	if s == nil || s.delegateController == nil {
		return errors.New("delegate controller is unavailable")
	}
	claim, err := s.delegateController.ClaimRuntimeReclamation(required)
	if err != nil || claim == nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			err = errors.Join(err, s.delegateController.AbortRuntimeReclamation(claim))
		}
	}()

	closed := make(map[string]*Session, len(claim.entries))
	for _, entry := range claim.entries {
		if entry.runtime == nil {
			continue
		}
		unhookReclaimedRuntimeRecord(entry)
		if closeRuntime := s.cfg.testOnly.delegateRuntimeReclaimClose; closeRuntime != nil {
			closeRuntime(entry.runtime)
		} else {
			teardownChildSession(context.Background(), entry.runtime, retainChildScratch)
		}
		closed[entry.delegateID] = entry.runtime
	}
	if err := s.delegateController.CompleteRuntimeReclamation(claim, closed); err != nil {
		return err
	}
	completed = true
	return nil
}

// ClaimIdleRuntimeRelease reserves one named quiescent terminal delegate's
// resident runtime subtree for non-terminal idle release: the caller unhooks
// and releases those runtimes outside the controller mutex and reports them
// closed via CompleteRuntimeReclamation, which clears the live runtime
// pointers so future sends and drives take the cold restore path. It is the
// targeted counterpart of ClaimRuntimeReclamation, which selects whole
// subtrees by retained-terminal capacity pressure; the idle release after a
// generation finalizes wants exactly one named subtree.
//
// A nil claim with a nil error means "not releasable right now": the delegate
// is missing, not terminal-idle, holds no resident runtime, or its subtree
// intersects process work — pending deliveries, claims, watchers, recovery
// flags, waiters, or a shared task store owned inside the subtree — that must
// settle first. Callers skip without forcing; the delegate's next finalize or
// the capacity reclamation backstop will retry.
//
// The second return, retryable, splits that nil-claim class: transient
// refusals — a member holds process work, recovery flags, waiters, steering
// debt, or a descendant is still running, all of which settle on their own —
// tell the caller to re-arm one more grace window; terminal refusals — the
// delegate missing, not terminal-idle, already released, or a shared task
// store owner — wait for the next finalize or the capacity backstop.
func (c *delegateTreeController) ClaimIdleRuntimeRelease(delegateID string) (*delegateRuntimeReclamationClaim, bool, error) {
	retirementRelease, retirementErr := c.beginRetirementMutation()
	if retirementErr != nil {
		return nil, false, retirementErr
	}
	defer retirementRelease()
	if c == nil {
		return nil, false, errors.New("delegate controller is unavailable")
	}
	if delegateID == "" {
		return nil, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil, false, errDelegateTargetBusy
	}
	// A wake pass holds the runtime it just restored until its reservation
	// commits or the pass declines; that span is transient, so the grace
	// timer re-arms one more window instead of losing the release.
	if c.attentionRestoreHeldLocked(delegateID) {
		return nil, true, nil
	}
	aggregate := c.durable[delegateID]
	if aggregate == nil || !c.isResidentTerminalRuntimeLocked(delegateID, aggregate) {
		return nil, false, nil
	}
	entries, ok := c.claimableRuntimeSubtreeLocked(delegateID)
	if !ok {
		return nil, true, nil
	}
	if c.subtreeOwnsSharedTaskStoreLocked(entries) {
		return nil, false, nil
	}
	// Leaf-first ordering puts the named root last among the entries, so the
	// claim's roots entry is the entries' final element already. Copy it so
	// the claim's slices never alias a backing array both could append to.
	roots := append([]delegateRuntimeReclamationEntry(nil), entries[len(entries)-1])
	return c.armRuntimeReclamationClaimLocked(roots, entries), false, nil
}

// subtreeOwnsSharedTaskStoreLocked reports whether releasing the claim's
// subtree would strand a shared task store resolver: some aggregate — inside
// the subtree or anywhere else in the tree — names one of the subtree's
// sessions as its shared task store owner. A released owner has no resident
// runtime, so resolveStableSharedTaskStore's owner-residency requirement would
// fail the resolver's next resume.
func (c *delegateTreeController) subtreeOwnsSharedTaskStoreLocked(entries []delegateRuntimeReclamationEntry) bool {
	owned := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		owned[entry.childSessionID] = struct{}{}
	}
	for _, aggregate := range c.durable {
		if owner := aggregate.Descriptor.SharedTaskStoreOwnerSessionID; owner != "" {
			if _, ok := owned[owner]; ok {
				return true
			}
		}
	}
	return false
}

// releaseIdleRuntimeAfterFinalize non-terminally releases this just-finalized
// stable delegate's resident runtime subtree. It unhooks each member's record
// from its owner's subagent manager, releases every member's runtime under
// the retirement policy — settling process-local resources, stdio MCP server
// processes above all, while durable identity, transcripts, scratch pins, and
// resumability stay intact — and clears the controller's live runtime
// pointers so the next send or drive takes the cold restore path.
//
// The caller is the finished child's own run goroutine at the end of the
// finalize tail: the generation outcome is durably committed, remaining
// delegate attention is re-armed, and quiescence is reported. Every quiescence
// precondition is checked BEFORE any teardown runs, because releaseRuntime
// consumes the session's single teardown pass: a refusal arriving mid-release
// would leave a half-settled runtime that can never be warm-resumed.
//
// It returns false — leaving the runtime warm — when any gate fails, and
// never forces. Terminal refusals — the delegate missing, not resident, or
// a shared task store owner — retry only at the next finalize or through the
// capacity reclamation backstop; the transient kinds — subtree members
// holding process work or a descendant still running, pre-gate residue that
// has not settled, a whole-tree retirement that has not decided — re-arm one
// more grace window so a one-shot refusal cannot lose the release. A closing
// controller is final and never retried. A root session (no owning delegate
// identity) hosts the tree and must stay resident; a non-stable child has no
// cold-restore path, so it is out of scope by construction (its parent is a
// stable delegate, and releasing that parent drains it as a subtree member).
// Manager records the durable subtree never tracked — no production spawn
// creates them today — settle with their member's teardown instead of
// outliving it.
func (s *Session) releaseIdleRuntimeAfterFinalize() bool {
	if s == nil || s.delegateController == nil || s.owningDelegateID == "" {
		return false
	}
	claim, retryable, err := s.delegateController.ClaimIdleRuntimeRelease(s.owningDelegateID)
	if err != nil {
		switch {
		case errors.Is(err, errDelegateTargetBusy):
			// Closing is final: the tree's own teardown owns everything now,
			// and a grace timer deliberately outlives the Close, so this
			// expected fire stays silent.
		case errors.Is(err, ErrRetirementUnavailable):
			// A whole-tree retirement owns this delegate's runtime while it
			// holds. If it commits, the tree's release settles everything;
			// if it aborts, the re-armed retry below picks the idle release
			// back up.
			s.rescheduleIdleRuntimeReleaseRetry()
		default:
			s.emit(events.EventWarning, warningDataFromError("idle runtime release claim failed", err))
		}
		return false
	}
	if claim == nil {
		// Transient refusals settle on their own, so one more grace window
		// bounds the retry; terminal ones wait for the next finalize or the
		// capacity backstop.
		if retryable {
			s.rescheduleIdleRuntimeReleaseRetry()
		}
		return false
	}
	completed := false
	defer func() {
		// Abort only while the claim can still be meaningful: once the pointers
		// are cleared the claim is released and Abort would just report a stale
		// lease; every path below either completes the claim or fails before
		// any controller mutation, so the abort path never leaves a half-done
		// release.
		if !completed {
			_ = s.delegateController.AbortRuntimeReclamation(claim)
		}
	}()

	// Gate EVERY member before unhooking or tearing ANY: these are the
	// conditions releaseQuiescentRuntime refuses on mid-release (running jobs,
	// pending terminal flush) plus the session-local residue a released
	// runtime could strand — queued notifications, pending watch sends,
	// delegate-delivery parcels, a closing manager, and settling restore
	// side effects. Claim entries are resident by construction —
	// claimableRuntimeSubtreeLocked admits only live runtimes — so no member
	// needs a nil skip.
	for _, entry := range claim.entries {
		if !entry.runtime.idleReleasePregatesClear() {
			// The residue a pre-gate refuses on is transient by definition —
			// it settles when its consumer runs — so one more grace window
			// bounds the retry instead of leaving the runtime warm until an
			// unrelated finalize or capacity pressure.
			s.rescheduleIdleRuntimeReleaseRetry()
			return false
		}
	}

	// Unhook first — record removal plus live-pointer clear — and only then run
	// the teardowns, WITHOUT holding the reclamation fence across them. The
	// fence exists to keep process work off a runtime that is being DESTROYED;
	// this release is non-terminal, so a send racing the teardown must find a
	// non-resident idle delegate and cold-restore a fresh runtime, exactly as
	// it would after a daemon restart, instead of being refused target_busy by
	// the fence. CompleteRuntimeReclamation clears only the exact pointers this
	//	claim captured, so a replacement runtime installed by such a racing send
	//	survives. Work a callback registers between the pre-gate snapshot and the
	//	teardown is the one race this ordering accepts: it lands on a runtime
	//	whose release is already committed and gets the same refusals and
	//	settlement warnings any abandoned work gets. Joining every callback
	//	source first would put unbounded waits back into a bounded release,
	//	which the retirement teardown's close budget exists to avoid.
	closed := make(map[string]*Session, len(claim.entries))
	for _, entry := range claim.entries {
		closed[entry.delegateID] = entry.runtime
		unhookReclaimedRuntimeRecord(entry)
	}
	if err := s.delegateController.CompleteRuntimeReclamation(claim, closed); err != nil {
		s.emit(events.EventWarning, warningDataFromError("idle runtime release completion failed", err))
		return false
	}
	completed = true

	// Members settle in depth waves, deepest first, with same-depth members
	// torn down concurrently under a bounded cap. A member teardown is
	// wait-dominated — signaling processes and honoring bounded closes,
	// where a stdio MCP member's close can take seconds — and the members
	// share no locks here: the unhook and the claim completion above are
	// already done, and each member's body keeps exactly the context and
	// close budget the serial loop gave it (one Background context, its
	// own close cascade). Batches inside a wave cap how many of those
	// waits overlap; a wave drains completely before the next one starts,
	// which is what keeps a member from beginning before every descendant
	// of a deeper wave has settled.
	limit := s.idleTeardownConcurrency()
	for _, wave := range reclamationTeardownWaves(claim.entries) {
		for start := 0; start < len(wave); start += limit {
			var wg sync.WaitGroup
			for _, entry := range wave[start:min(start+limit, len(wave))] {
				wg.Go(func() {
					s.teardownReclaimedRuntimeEntry(entry)
				})
			}
			wg.Wait()
		}
	}
	return true
}

// delegateTeardownConcurrencyDefault caps how many same-depth members the
// idle release tears down at once. The wait-dominated teardown means a
// modest bound already collapses a wide subtree's wall clock — the members
// overlap their waits rather than add work — without contending the machine.
// Tests override it via testOnly.idleTeardownConcurrency.
const delegateTeardownConcurrencyDefault = 8

// idleTeardownConcurrency is how many same-depth members the idle release
// may tear down concurrently.
func (s *Session) idleTeardownConcurrency() int {
	if override := s.cfg.testOnly.idleTeardownConcurrency; override != nil && *override > 0 {
		return *override
	}
	return delegateTeardownConcurrencyDefault
}

// reclamationTeardownWaves groups reclamation entries into depth waves:
// members are sorted deepest-first with delegate-id order breaking ties —
// the same ordering memberIDsLeafFirstLocked pins for the claim — and then
// split on depth boundaries. Within a wave every member sits at the same
// depth, so tearing the wave's members down concurrently cannot let a
// member start before a descendant settles: every descendant lives in a
// deeper wave, which the caller drains to completion first.
func reclamationTeardownWaves(entries []delegateRuntimeReclamationEntry) [][]delegateRuntimeReclamationEntry {
	sorted := make([]delegateRuntimeReclamationEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].depth != sorted[j].depth {
			return sorted[i].depth > sorted[j].depth
		}
		return sorted[i].delegateID < sorted[j].delegateID
	})
	var waves [][]delegateRuntimeReclamationEntry
	for start := 0; start < len(sorted); {
		end := start + 1
		for end < len(sorted) && sorted[end].depth == sorted[start].depth {
			end++
		}
		waves = append(waves, sorted[start:end])
		start = end
	}
	return waves
}

// teardownReclaimedRuntimeEntry settles one reclaimed member's process-local
// resources after the claim completed: the runtime's manager-held children
// the durable subtree never tracked, then the runtime itself, non-terminally.
func (s *Session) teardownReclaimedRuntimeEntry(entry delegateRuntimeReclamationEntry) {
	if hook := s.cfg.testOnly.idleTeardownMemberStarted; hook != nil {
		hook(entry.runtime)
	}
	defer func() {
		if hook := s.cfg.testOnly.idleTeardownMemberSettled; hook != nil {
			hook(entry.runtime)
		}
	}()
	// A released runtime's manager can hold children the durable
	// subtree never tracked. No production spawn creates such records
	// today — every manager insertion is the stable-delegate machinery —
	// but a record landing there by any other route must not outlive the
	// release: settle it the way this runtime's own close would have,
	// terminal teardown with the scratch retained for the handoff.
	if entry.runtime.subagents != nil {
		for _, sub := range entry.runtime.subagents.drainForClose() {
			teardownChildSession(context.Background(), sub.sess, retainChildScratch)
		}
	}
	// A teardown error after the pre-gates leaves the pass spent — this
	// runtime instance can never be warm-resumed — so unhooking is the
	// consistent outcome either way: durable state and scratch pins survive
	// for a cold restore, and the teardown body emits its own warnings for
	// the settlements that did not complete, the same contract
	// reclaimDelegateRuntimeCapacity's teardowns already follow.
	_ = entry.runtime.releaseChildRuntimeForRetirement(context.Background())
}

// scheduleIdleRuntimeRelease arms the idle release for this just-finalized
// stable delegate: after the grace period (delegateIdleReleaseDelayDefault,
// or the testOnly override), the resident runtime subtree releases
// non-terminally. The timer routes through the session clock — the one seam
// every delayed callback uses — so clock-controlled tests observe the
// scheduled release like any other timer.
//
// finalizedGeneration guards staleness: a later generation that starts or
// finalizes before this timer fires supersedes it — that generation's own
// finalize tail arms a fresh timer — so the stale fire stands down instead
// of cutting the newer generation's grace short. Grace is measured from the
// most recent finalize, never from an earlier one.
//
// At most one grace timer per delegate is ever armed: the arm swaps the
// delegate's outstanding handle and stops the replaced one, so the finalize
// tail's timer and each refusal's retry collapse into a single timer instead
// of stacking one per refusal. An arm paused between creating its timer and
// installing it cannot displace a newer generation's install, and a same-
// generation arm cannot displace a newer retry of its own generation — the
// displaced live timer would leave the delegate with no grace window at all
// when the displacer has already fired (the stale timer is stopped instead).
// An arm whose callback already ran before the install step is never
// installed: a spent timer must not become the delegate's outstanding handle,
// because its own retire found no entry to drop and nothing else would retire
// it. A closing controller rejects every arm — the close's sweep empties the
// map exactly once, so a post-sweep install would arm a callback the teardown
// already promised to stop. The fired callback retires its own entry, so a
// spent handle never pins the arm's closure — and the *Session it captures —
// for the life of the process. The handle lives on the controller keyed by
// delegate: a cold restore replaces the *Session, and the new generation's
// arm must still retire the window the previous runtime armed, which a
// session-keyed map could not find. It is
// deliberately NOT joined by any WaitGroup a session's Close joins: Close's
// bounded joins must never wait out a grace period, and a timer that fires
// after the session or tree has closed is harmless — the stale-generation
// guard and the release claim refuse on a closing controller, a superseded
// generation, or an already-released runtime, and every gate re-checks state
// at fire time.
func (s *Session) scheduleIdleRuntimeRelease(finalizedGeneration uint64) {
	if s == nil || s.delegateController == nil {
		return
	}
	delay := delegateIdleReleaseDelayDefault
	if override := s.cfg.testOnly.delegateIdleReleaseDelay; override != nil {
		delay = *override
	}
	arm := s.delegateController.nextIdleReleaseArmID()
	fired := new(atomic.Bool)
	timer := s.sclock().AfterFunc(delay, func() {
		// Mark this arm's handle spent before anything else: a callback that
		// runs before its arm finishes installing must leave the install step
		// nothing to install — the already-fired timer would otherwise be
		// installed as the delegate's outstanding handle and pin this closure
		// (and the *Session it captures) until some later arm displaced it.
		fired.Store(true)
		// A fired callback retires its own installed handle so the entry
		// cannot outlive the arm it belongs to.
		s.delegateController.retireIdleReleaseTimer(s.owningDelegateID, arm)
		if !s.delegateController.idleReleaseGenerationCurrent(s.owningDelegateID, finalizedGeneration) {
			return
		}
		_ = s.releaseIdleRuntimeAfterFinalize()
	})
	// Stop the displaced timer outside the controller mutex: Timer.Stop takes
	// the clock seam's own lock, and callbacks dispatch on their own
	// goroutines, so holding c.mu across the stop buys nothing. The displaced
	// timer is whichever of the two lost the swap: the window this arm
	// replaced, or this arm's own timer when a newer generation already
	// installed — the stale arm's callback would decline on the generation
	// guard, leaving the newer generation with no timer at all.
	if displaced := s.delegateController.swapIdleReleaseTimer(s.owningDelegateID, timer, finalizedGeneration, arm, fired); displaced != nil {
		displaced.Stop()
	}
}

// idleReleaseTimerHandle is one delegate's installed idle-release grace
// window: the armed timer, the generation whose finalize armed it, and the arm
// identity its fired callback uses to retire the entry.
type idleReleaseTimerHandle struct {
	timer      clock.Timer
	generation uint64
	arm        uint64
}

// swapIdleReleaseTimer installs handle as delegateID's one outstanding
// idle-release grace timer and returns the timer the caller must stop: the
// one this arm displaced, or the newly armed one when the install is stale.
// Every arm site — the finalize tail and each transient-refusal retry —
// funnels through this swap, so a delegate holds at most one armed grace
// timer. Arming and installing are two steps, so an older arm paused between
// them can resume after something newer installed its own: a stale arm must
// not displace it — a newer generation's callback would decline on the
// generation guard, and a same-generation arm's displacement of a newer retry
// leaves the delegate with no live window when the displacer has already
// fired — so the stale timer is the one stopped instead. An arm whose callback
// already ran never installs, and a closing controller rejects every arm: the
// close's sweep empties the map once, and a post-sweep install would arm a
// callback nothing would stop.
func (c *delegateTreeController) swapIdleReleaseTimer(delegateID string, handle clock.Timer, generation, arm uint64, fired *atomic.Bool) clock.Timer {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return handle
	}
	if fired != nil && fired.Load() {
		return handle
	}
	installed := c.idleReleaseTimers[delegateID]
	if installed.timer != nil && (installed.generation > generation ||
		(installed.generation == generation && installed.arm > arm)) {
		return handle
	}
	c.idleReleaseTimers[delegateID] = idleReleaseTimerHandle{timer: handle, generation: generation, arm: arm}
	return installed.timer
}

// nextIdleReleaseArmID mints the identity of one grace-timer arm. The fired
// callback carries it so the entry it retires is provably its own, even when
// a same-generation arm has since replaced it.
func (c *delegateTreeController) nextIdleReleaseArmID() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idleReleaseArmSeq++
	return c.idleReleaseArmSeq
}

// retireIdleReleaseTimer drops delegateID's installed arm when it is still
// this one, so a fired or stood-down callback leaves no handle behind: a
// spent clock timer can otherwise pin the arm's callback closure — and the
// *Session it captures — for the life of the process.
func (c *delegateTreeController) retireIdleReleaseTimer(delegateID string, arm uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if installed := c.idleReleaseTimers[delegateID]; installed.timer != nil && installed.arm == arm {
		delete(c.idleReleaseTimers, delegateID)
	}
}

// takeIdleReleaseTimersLocked empties the idle-release timer map on a
// controller that is closing. The caller stops the returned handles outside
// c.mu — Timer.Stop takes the clock seam's lock. A timer that slips a fire in
// before the stop is refused by the closing controller's claim guard, so the
// sweep is hygiene, not correctness.
func (c *delegateTreeController) takeIdleReleaseTimersLocked() []idleReleaseTimerHandle {
	taken := make([]idleReleaseTimerHandle, 0, len(c.idleReleaseTimers))
	for _, handle := range c.idleReleaseTimers {
		taken = append(taken, handle)
	}
	c.idleReleaseTimers = make(map[string]idleReleaseTimerHandle)
	return taken
}

// idleReleaseGenerationCurrent reports whether the delegate's durable
// generation is still the one whose finalize armed a pending idle-release
// timer — false once any later generation has started or finalized.
func (c *delegateTreeController) idleReleaseGenerationCurrent(delegateID string, generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	return aggregate != nil && aggregate.Generation == generation
}

// currentDelegateGeneration returns the delegate's current durable
// generation, zero when it has no aggregate. A retry timer armed with it
// stands down as soon as any newer generation finalizes — that finalize tail
// armed a fresh timer of its own.
func (c *delegateTreeController) currentDelegateGeneration(delegateID string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if aggregate := c.durable[delegateID]; aggregate != nil {
		return aggregate.Generation
	}
	return 0
}

// rescheduleIdleRuntimeReleaseRetry re-arms the idle release after a refusal
// that is transient by construction: pre-gate residue that has not settled,
// or a whole-tree retirement that has not decided. The fixtures that opt out
// of the release entirely opt out of its retries too.
func (s *Session) rescheduleIdleRuntimeReleaseRetry() {
	if s.cfg.testOnly.disableDelegateIdleRelease {
		return
	}
	s.scheduleIdleRuntimeRelease(s.delegateController.currentDelegateGeneration(s.owningDelegateID))
}

// idleReleasePregatesClear reports whether every precondition that must hold
// BEFORE a non-terminal teardown holds for this session: no queued
// notifications, no pending watch sends, no job-manager runtime obligations
// (running jobs or a pending terminal flush) that releaseQuiescentRuntime
// would refuse on mid-release, no manager child still running — an
// opportunistic release must not abandon a subagent mid-execution — and
// none of the session-local residue retirement refuses on: a
// delegate-delivery parcel the pump has not consumed, a manager already
// closing, or restore side effects still settling on this manager's
// children. In-flight notification and delivery consumers are absent here
// deliberately: they hold the tree's shared retirement mutation, which
// ClaimIdleRuntimeRelease takes before any pre-gate runs, so the claim
// itself refuses while one is executing.
func (s *Session) idleReleasePregatesClear() bool {
	if s.peekNotifications() != 0 {
		return false
	}
	if s.jobManager != nil && (s.jobManager.hasPendingWatchSends() || s.jobManager.hasRuntimeObligations()) {
		return false
	}
	if s.subagents != nil && s.subagents.hasRunningChildren() {
		return false
	}
	if s.delegateDeliveryResiduePending() {
		return false
	}
	if s.subagents != nil {
		s.subagents.mu.Lock()
		residue := s.subagents.closeOrRestoreResiduePending()
		s.subagents.mu.Unlock()
		if residue {
			return false
		}
	}
	return true
}
