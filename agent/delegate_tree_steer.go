package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

var errDelegateTranscriptUnavailable = errors.New("delegate transcript is unavailable")

type delegateCompletionDecision uint8

const (
	delegateCompletionUseExistingTerminal delegateCompletionDecision = iota
	delegateCompletionFinishNoAction
	delegateCompletionNeedsNudge
)

func (c *delegateTreeController) completionDecision(lease delegateLease) (delegateCompletionDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := c.reduceCompletionDecisionIntent(finishIntent{lease: lease})
	return decision.completion, decision.err
}

type delegateSteeringAdmission struct {
	entryID string
	// carriesAcrossGeneration marks an admission accepted under a covering stop.
	// Ordinary admissions belong to the generation that took them and are
	// dropped when it is released; this one's generation is already ending, and
	// the successor is the only turn that can ever consume it.
	carriesAcrossGeneration bool
}

type delegateSteeringClaim struct {
	token      uint64
	delegateID string
	lease      delegateLease
	runtime    *Session
	entryID    string
}

type delegateModelRequestClaim struct {
	token       uint64
	lease       delegateLease
	runtime     *Session
	steeringIDs []string
}

type delegateTranscriptEntry struct {
	entryID   string
	timestamp time.Time
}

func (c *delegateTreeController) Steer(ctx context.Context, actor delegateActor, delegateID, message string) (delegateMutationPlans, error) {
	if err := ctx.Err(); err != nil {
		return delegateMutationPlans{}, err
	}
	if strings.TrimSpace(message) == "" {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}

	claim, err := c.BeginSteerPersistence(actor, delegateID)
	if err != nil {
		return delegateMutationPlans{}, err
	}
	entry, err := claim.runtime.appendDelegateSteeringDurably(message, claim.entryID)
	if err != nil {
		_ = c.AbortSteerPersistence(claim)
		return delegateMutationPlans{}, err
	}
	return c.CompleteSteerPersistence(claim, entry)
}

func (c *delegateTreeController) BeginSteerPersistence(actor delegateActor, delegateID string) (*delegateSteeringClaim, error) {
	release, err := c.beginRetirementMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeMutationLocked(actor, delegateID); err != nil {
		return nil, err
	}
	live := c.live[delegateID]
	if live == nil || live.binding == nil || live.binding.runtime == nil {
		return nil, errDelegateTargetBusy
	}
	if _, _, err := c.admitLeaseLocked(live.binding.lease, delegatestore.PhaseRunning); err != nil {
		return nil, err
	}
	if c.hasSettlementClaimLocked(live.binding.lease) {
		return nil, errDelegateTargetBusy
	}
	c.nextToken++
	claim := &delegateSteeringClaim{
		token:      c.nextToken,
		delegateID: delegateID,
		lease:      live.binding.lease,
		runtime:    live.binding.runtime,
		entryID:    newQueueEntryID(),
	}
	c.steeringClaims[claim.token] = claim
	c.evidenceVersion++
	return claim, nil
}

func (c *delegateTreeController) CompleteSteerPersistence(claim *delegateSteeringClaim, entry delegateTranscriptEntry) (delegateMutationPlans, error) {
	defer c.retirementChanged()
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.steeringClaims[claim.token] != claim || entry.entryID == "" || entry.entryID != claim.entryID {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	stopFenced := false
	if c.stop != nil {
		_, stopFenced = c.stop.steeringClaims[claim.token]
	}
	c.releaseSteeringClaimLocked(claim.token)
	_, live, err := c.admitLeaseLocked(claim.lease, delegatestore.PhaseRunning)
	if err != nil {
		// A covering stop owns the earlier admission. Transcript fsync remains
		// its durable acceptance point even after the exact binding is released.
		//
		// The admission is still recorded: the successor's model claim binds
		// it like any steer its own generation took, which is what makes that
		// generation answer it (escalateCompletionRequirementLocked).
		if stopFenced && claim.delegateID == claim.lease.delegateID {
			live = c.live[claim.delegateID]
			if live != nil {
				live.pendingSteers = append(live.pendingSteers, delegateSteeringAdmission{
					entryID:                 entry.entryID,
					carriesAcrossGeneration: true,
				})
				if entry.timestamp.After(live.activityAt) {
					live.activityAt = entry.timestamp
				}
				// Mirror ReportActivityPhase: a steer at or after the current
				// activity instant re-baselines the quiet cadence.
				if !entry.timestamp.Before(live.activityAt) {
					live.rearmQuietCadenceLocked()
				}
				if entry.timestamp.After(live.productiveActivityAt) {
					live.productiveActivityAt = entry.timestamp
				}
			}
			c.evidenceVersion++
			return delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(claim.delegateID)}}, nil
		}
		c.evidenceVersion++
		return delegateMutationPlans{}, err
	}
	if live.binding.runtime != claim.runtime || claim.delegateID != claim.lease.delegateID {
		c.evidenceVersion++
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	live.pendingSteers = append(live.pendingSteers, delegateSteeringAdmission{entryID: entry.entryID})
	if entry.timestamp.After(live.activityAt) {
		live.activityAt = entry.timestamp
	}
	// Mirror ReportActivityPhase: a steer at or after the current activity
	// instant re-baselines the quiet cadence.
	if !entry.timestamp.Before(live.activityAt) {
		live.rearmQuietCadenceLocked()
	}
	if entry.timestamp.After(live.productiveActivityAt) {
		live.productiveActivityAt = entry.timestamp
	}
	c.evidenceVersion++
	return delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(claim.delegateID)}}, nil
}

func (c *delegateTreeController) AbortSteerPersistence(claim *delegateSteeringClaim) error {
	defer c.retirementChanged()
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.steeringClaims[claim.token] != claim {
		return errDelegateStaleLease
	}
	c.releaseSteeringClaimLocked(claim.token)
	c.evidenceVersion++
	return nil
}

func (c *delegateTreeController) BeginModelRequest(lease delegateLease) (*delegateModelRequestClaim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, live, err := c.admitLeaseWithStopErrorLocked(lease, context.Canceled, delegatestore.PhaseRunning)
	if err != nil {
		return nil, err
	}
	if live.binding.runtime == nil {
		return nil, errDelegateTargetBusy
	}
	if c.hasSettlementClaimLocked(lease) {
		return nil, errDelegateTargetBusy
	}
	for _, existing := range c.modelClaims {
		if existing.lease == lease {
			return nil, errDelegateTargetBusy
		}
	}
	c.nextToken++
	claim := &delegateModelRequestClaim{
		token:   c.nextToken,
		lease:   lease,
		runtime: live.binding.runtime,
	}
	for _, pending := range live.pendingSteers {
		claim.steeringIDs = append(claim.steeringIDs, pending.entryID)
	}
	c.modelClaims[claim.token] = claim
	c.evidenceVersion++
	return claim, nil
}

func (c *delegateTreeController) CompleteModelRequest(claim *delegateModelRequestClaim, history []schema.Turn, scope replayScope) ([]llm.Message, error) {
	defer c.retirementChanged()
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.modelClaims[claim.token] != claim {
		return nil, errDelegateStaleLease
	}
	_, live, err := c.admitLeaseWithStopErrorLocked(claim.lease, context.Canceled, delegatestore.PhaseRunning)
	if err != nil || live.binding.runtime != claim.runtime {
		c.releaseModelClaimLocked(claim.token)
		c.evidenceVersion++
		if err != nil {
			return nil, err
		}
		return nil, errDelegateStaleLease
	}
	pending := make([]delegateSteeringAdmission, 0, len(claim.steeringIDs))
	claimedIDs := make(map[string]struct{}, len(claim.steeringIDs))
	for _, entryID := range claim.steeringIDs {
		pending = append(pending, delegateSteeringAdmission{entryID: entryID})
		claimedIDs[entryID] = struct{}{}
	}
	lateIDs := make(map[string]struct{})
	for _, admission := range live.pendingSteers {
		if _, claimed := claimedIDs[admission.entryID]; !claimed {
			lateIDs[admission.entryID] = struct{}{}
		}
	}
	for _, steeringClaim := range c.steeringClaims {
		if steeringClaim != nil && steeringClaim.lease == claim.lease {
			if _, claimed := claimedIDs[steeringClaim.entryID]; !claimed {
				lateIDs[steeringClaim.entryID] = struct{}{}
			}
		}
	}
	history, bound := projectDelegatePendingSteers(history, pending, lateIDs)
	if len(bound) != 0 {
		if err := c.escalateCompletionRequirementLocked(claim.lease); err != nil {
			c.releaseModelClaimLocked(claim.token)
			c.evidenceVersion++
			return nil, err
		}
	}
	kept := live.pendingSteers[:0]
	for _, pending := range live.pendingSteers {
		if _, claimed := bound[pending.entryID]; !claimed {
			kept = append(kept, pending)
		}
	}
	live.pendingSteers = kept
	c.releaseModelClaimLocked(claim.token)
	c.evidenceVersion++
	return expandHistory(history, scope), nil
}

func (c *delegateTreeController) AbortModelRequest(claim *delegateModelRequestClaim) error {
	defer c.retirementChanged()
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.modelClaims[claim.token] != claim {
		return errDelegateStaleLease
	}
	c.releaseModelClaimLocked(claim.token)
	c.evidenceVersion++
	return nil
}

func (c *delegateTreeController) releaseSteeringClaimLocked(token uint64) {
	delete(c.steeringClaims, token)
	if c.stop != nil {
		if _, tracked := c.stop.steeringClaims[token]; tracked {
			delete(c.stop.steeringClaims, token)
			c.signalStopProgressLocked()
		}
	}
}

func (c *delegateTreeController) releaseModelClaimLocked(token uint64) {
	delete(c.modelClaims, token)
	if c.stop != nil {
		if _, tracked := c.stop.modelClaims[token]; tracked {
			delete(c.stop.modelClaims, token)
			c.signalStopProgressLocked()
		}
	}
}

func (c *delegateTreeController) dropRuntimeClaimsForMembersLocked(members map[string]struct{}) {
	for token, claim := range c.steeringClaims {
		if claim != nil {
			if _, covered := members[claim.delegateID]; !covered {
				continue
			}
		}
		if c.stop != nil {
			if _, tracked := c.stop.steeringClaims[token]; tracked {
				continue
			}
		}
		delete(c.steeringClaims, token)
	}
	for token, claim := range c.modelClaims {
		if claim != nil {
			if _, covered := members[claim.lease.delegateID]; !covered {
				continue
			}
		}
		if c.stop != nil {
			if _, tracked := c.stop.modelClaims[token]; tracked {
				continue
			}
		}
		delete(c.modelClaims, token)
	}
	for token, claim := range c.settlementClaims {
		if claim != nil {
			if _, covered := members[claim.lease.delegateID]; !covered {
				continue
			}
		}
		delete(c.settlementClaims, token)
	}
}

func projectDelegatePendingSteers(history []schema.Turn, pending []delegateSteeringAdmission, excluded map[string]struct{}) ([]schema.Turn, map[string]struct{}) {
	order := make(map[string]int, len(pending))
	for i, admission := range pending {
		order[admission.entryID] = i
	}
	projected := make([]schema.Turn, 0, len(history))
	steers := make([]schema.Turn, len(pending))
	found := make([]bool, len(pending))
	for _, turn := range history {
		if turn.Kind == schema.TurnSteering {
			if _, exclude := excluded[turn.StableTurnID]; exclude {
				continue
			}
		}
		index, waiting := order[turn.StableTurnID]
		if waiting && turn.Kind == schema.TurnSteering && !found[index] {
			steers[index] = turn
			found[index] = true
			continue
		}
		projected = append(projected, turn)
	}
	bound := make(map[string]struct{}, len(pending))
	for i, admission := range pending {
		if !found[i] {
			continue
		}
		projected = append(projected, steers[i])
		bound[admission.entryID] = struct{}{}
	}
	return projected, bound
}

func (c *delegateTreeController) BeginTool(lease delegateLease) error {
	return c.beginRuntimeBoundary(lease)
}

// parentMessageAttribution opens each message a delegate's parent steers into
// its running turn, so the delegate's model knows who is speaking.
const parentMessageAttribution = "Message from your parent agent:\n\n"

// parentSteeringTurn is the steering turn a delegate_send message to a running
// delegate appends as: from its parent, attribution first.
func parentSteeringTurn(message, stableTurnID string, now time.Time) schema.Turn {
	turn := schema.NewTurn(schema.TurnSteering, llm.User(parentMessageAttribution+message))
	turn.Timestamp = now.UTC()
	turn.SteeringSource = events.SteeringSourceParent
	turn.StableTurnID = stableTurnID
	return turn
}

func (s *Session) appendDelegateSteeringDurably(message, stableTurnID string) (delegateTranscriptEntry, error) {
	s.mu.Lock()
	ready := s.transcriptReady && s.transcript != nil
	s.mu.Unlock()
	if !ready {
		return delegateTranscriptEntry{}, errDelegateTranscriptUnavailable
	}
	turn := parentSteeringTurn(message, stableTurnID, s.sclock().Now())
	if err := s.appendTurnAfterTranscriptWrite(
		turn,
		func() error {
			// Delegate steering reaches this session from another session's
			// goroutine, like attention: the running execution's, or a
			// delivery turn of its own.
			_, err := s.recordTranscriptLocked(turn, transcript.DoorSynced, transcript.PlaceAsync)
			return err
		},
		func() { s.history = append(s.history, turn) },
	); err != nil {
		return delegateTranscriptEntry{}, err
	}
	return delegateTranscriptEntry{entryID: turn.StableTurnID, timestamp: turn.Timestamp}, nil
}

func (s *Session) delegateModelHistorySnapshot() []schema.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.Turn(nil), s.history...)
}

// liveGenerationOwesSteering reports whether any admission belongs to a
// generation that can still consume it. An admission carried across a covering
// stop is excluded: it is held for a successor, not owed by the run that took
// it, so it must not read as work in flight.
func liveGenerationOwesSteering(live *delegateLiveState) bool {
	for _, admission := range live.pendingSteers {
		if !admission.carriesAcrossGeneration {
			return true
		}
	}
	return false
}

// generationOwesSteering reports whether delegateID's currently open generation
// holds an admitted steering message no model request has consumed yet. A run
// that is parked in its finalization drain reads this to run a turn so the
// steering is acted on instead of sitting unread until an owned job ends
// (#2796). The check is scoped to the same open generation that admitted the
// steering: an admission carried across a covering stop belongs to a successor
// and liveGenerationOwesSteering already excludes it.
func (c *delegateTreeController) generationOwesSteering(delegateID string) bool {
	if c == nil || delegateID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	live := c.live[delegateID]
	return aggregate != nil && aggregate.CurrentRunOpen && live != nil && liveGenerationOwesSteering(live)
}
