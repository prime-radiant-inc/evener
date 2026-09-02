package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
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
	entryID    string
	provenance *provenance.Causal
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
	provenance *provenance.Causal
}

type delegateModelRequestClaim struct {
	token                uint64
	lease                delegateLease
	runtime              *Session
	steeringIDs          []string
	steeringProvenance   map[string]*provenance.Causal
	testBeforeProvenance func()
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

// SteerCaller routes one delegate's explicit caller message through its
// controlling runtime. A nested caller uses the stable parent's controller
// admission; a top-level caller uses the root Session's turn-boundary queue.
func (c *delegateTreeController) SteerCaller(ctx context.Context, actor delegateActor, message string, p *provenance.Causal) (delegateMutationPlans, error) {
	if err := ctx.Err(); err != nil {
		return delegateMutationPlans{}, err
	}
	if strings.TrimSpace(message) == "" {
		return delegateMutationPlans{}, errors.New("invalid_request: message is required")
	}
	claim, root, err := c.beginCallerSteerPersistence(actor, p)
	if err != nil {
		return delegateMutationPlans{}, err
	}
	if root != nil {
		if err := root.enqueueDelegateCallerSteeringDurably(message, p); err != nil {
			return delegateMutationPlans{}, err
		}
		return delegateMutationPlans{}, nil
	}
	entry, err := claim.runtime.appendDelegateCallerSteeringDurably(message, claim.entryID)
	if err != nil {
		_ = c.AbortSteerPersistence(claim)
		return delegateMutationPlans{}, err
	}
	return c.CompleteSteerPersistence(claim, entry)
}

func (c *delegateTreeController) BeginSteerPersistence(actor delegateActor, delegateID string) (*delegateSteeringClaim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeMutationLocked(actor, delegateID); err != nil {
		return nil, err
	}
	return c.beginSteerPersistenceLocked(delegateID, nil)
}

func (c *delegateTreeController) beginCallerSteerPersistence(actor delegateActor, p *provenance.Causal) (*delegateSteeringClaim, *Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if actor.lease == nil {
		return nil, nil, errors.New("invalid_request: caller is only available from a delegate")
	}
	actorAggregate, _, err := c.admitLeaseLocked(*actor.lease, delegatestore.PhaseRunning)
	if err != nil {
		return nil, nil, err
	}
	if c.hasSettlementClaimLocked(*actor.lease) {
		return nil, nil, errDelegateTargetBusy
	}
	parentID := actorAggregate.Descriptor.ParentDelegateID
	if parentID == "" {
		if actorAggregate.Descriptor.OwnerSessionID != c.rootSessionID || c.rootRuntime == nil {
			return nil, nil, errDelegateNotControllable
		}
		return nil, c.rootRuntime, nil
	}
	claim, err := c.beginSteerPersistenceLocked(parentID, p)
	return claim, nil, err
}

func (c *delegateTreeController) beginSteerPersistenceLocked(delegateID string, p *provenance.Causal) (*delegateSteeringClaim, error) {
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
		provenance: provenance.Clone(p),
	}
	c.steeringClaims[claim.token] = claim
	c.evidenceVersion++
	return claim, nil
}

func (c *delegateTreeController) CompleteSteerPersistence(claim *delegateSteeringClaim, entry delegateTranscriptEntry) (delegateMutationPlans, error) {
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
		// The admission is still recorded. The transcript replays the message,
		// but only an admission carries the steer's causal provenance into the
		// successor's model claim -- without one the successor runs the steer
		// with no idea which watch drove it, and the loss is invisible because
		// the text arrived.
		if stopFenced && claim.delegateID == claim.lease.delegateID {
			live = c.live[claim.delegateID]
			if live != nil {
				live.pendingSteers = append(live.pendingSteers, delegateSteeringAdmission{
					entryID:                 entry.entryID,
					provenance:              provenance.Clone(claim.provenance),
					carriesAcrossGeneration: true,
				})
				if entry.timestamp.After(live.activityAt) {
					live.activityAt = entry.timestamp
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
	live.pendingSteers = append(live.pendingSteers, delegateSteeringAdmission{
		entryID:    entry.entryID,
		provenance: provenance.Clone(claim.provenance),
	})
	if entry.timestamp.After(live.activityAt) {
		live.activityAt = entry.timestamp
	}
	if entry.timestamp.After(live.productiveActivityAt) {
		live.productiveActivityAt = entry.timestamp
	}
	c.evidenceVersion++
	return delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(claim.delegateID)}}, nil
}

func (c *delegateTreeController) AbortSteerPersistence(claim *delegateSteeringClaim) error {
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
	_, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
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
		if pending.provenance != nil {
			if claim.steeringProvenance == nil {
				claim.steeringProvenance = make(map[string]*provenance.Causal)
			}
			claim.steeringProvenance[pending.entryID] = provenance.Clone(pending.provenance)
		}
	}
	c.modelClaims[claim.token] = claim
	c.evidenceVersion++
	return claim, nil
}

func (c *delegateTreeController) CompleteModelRequest(claim *delegateModelRequestClaim, history []schema.Turn, scope replayScope) ([]llm.Message, error) {
	c.mu.Lock()
	if claim == nil || c.modelClaims[claim.token] != claim {
		c.mu.Unlock()
		return nil, errDelegateStaleLease
	}
	_, live, err := c.admitLeaseLocked(claim.lease, delegatestore.PhaseRunning)
	if err != nil || live.binding.runtime != claim.runtime {
		c.releaseModelClaimLocked(claim.token)
		c.evidenceVersion++
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, errDelegateStaleLease
	}
	pending := make([]delegateSteeringAdmission, 0, len(claim.steeringIDs))
	claimedIDs := make(map[string]struct{}, len(claim.steeringIDs))
	for _, entryID := range claim.steeringIDs {
		pending = append(pending, delegateSteeringAdmission{
			entryID:    entryID,
			provenance: provenance.Clone(claim.steeringProvenance[entryID]),
		})
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
			c.mu.Unlock()
			return nil, err
		}
	}
	var consumedProvenance *provenance.Causal
	for entryID := range bound {
		consumedProvenance = provenance.Union(consumedProvenance, claim.steeringProvenance[entryID])
	}
	kept := live.pendingSteers[:0]
	for _, pending := range live.pendingSteers {
		if _, claimed := bound[pending.entryID]; !claimed {
			kept = append(kept, pending)
		}
	}
	live.pendingSteers = kept
	c.evidenceVersion++
	expanded := expandHistory(history, scope)
	runtime := claim.runtime
	c.mu.Unlock()
	if claim.testBeforeProvenance != nil {
		claim.testBeforeProvenance()
	}
	runtime.unionActiveProvenance(consumedProvenance)
	c.mu.Lock()
	if c.modelClaims[claim.token] != claim {
		c.mu.Unlock()
		return nil, errDelegateStaleLease
	}
	c.releaseModelClaimLocked(claim.token)
	c.evidenceVersion++
	c.mu.Unlock()
	return expanded, nil
}

func (c *delegateTreeController) AbortModelRequest(claim *delegateModelRequestClaim) error {
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

func (s *Session) appendDelegateSteeringDurably(message, stableTurnID string) (delegateTranscriptEntry, error) {
	return s.appendDelegateSteeringDurablyWithMetadata(message, stableTurnID, "user", "")
}

func (s *Session) appendDelegateCallerSteeringDurably(message, stableTurnID string) (delegateTranscriptEntry, error) {
	return s.appendDelegateSteeringDurablyWithMetadata(message, stableTurnID, "", events.SteeringKindAgentMessage)
}

func (s *Session) appendDelegateSteeringDurablyWithMetadata(message, stableTurnID, source, kind string) (delegateTranscriptEntry, error) {
	s.mu.Lock()
	ready := s.transcriptReady && s.transcript != nil
	s.mu.Unlock()
	if !ready {
		return delegateTranscriptEntry{}, errDelegateTranscriptUnavailable
	}
	turn := schema.NewTurn(schema.TurnSteering, llm.User(message))
	turn.Timestamp = s.sclock().Now().UTC()
	turn.SteeringSource = source
	turn.SteeringKind = kind
	turn.StableTurnID = stableTurnID
	if err := s.appendTurnAfterTranscriptWrite(
		func() error { return s.writeTranscriptDurableLocked(turn) },
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
