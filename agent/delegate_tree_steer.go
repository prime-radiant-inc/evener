package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

var errDelegateTranscriptUnavailable = errors.New("delegate transcript is unavailable")

type delegateSteeringAdmission struct {
	entryID string
}

type delegateSteeringClaim struct {
	token      uint64
	delegateID string
	lease      delegateLease
	runtime    *Session
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
	entry, err := claim.runtime.appendDelegateSteeringDurably(message)
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
	live := c.live[delegateID]
	if live == nil || live.binding == nil || live.binding.runtime == nil {
		return nil, errDelegateTargetBusy
	}
	if _, _, err := c.admitLeaseLocked(live.binding.lease, delegatestore.PhaseRunning); err != nil {
		return nil, err
	}
	c.nextToken++
	claim := &delegateSteeringClaim{
		token:      c.nextToken,
		delegateID: delegateID,
		lease:      live.binding.lease,
		runtime:    live.binding.runtime,
	}
	c.steeringClaims[claim.token] = claim
	c.evidenceVersion++
	return claim, nil
}

func (c *delegateTreeController) CompleteSteerPersistence(claim *delegateSteeringClaim, entry delegateTranscriptEntry) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.steeringClaims[claim.token] != claim || entry.entryID == "" {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	delete(c.steeringClaims, claim.token)
	_, live, err := c.admitLeaseLocked(claim.lease, delegatestore.PhaseRunning)
	if err != nil || live.binding.runtime != claim.runtime || claim.delegateID != claim.lease.delegateID {
		c.evidenceVersion++
		if err != nil {
			return delegateMutationPlans{}, err
		}
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	live.pendingSteers = append(live.pendingSteers, delegateSteeringAdmission{entryID: entry.entryID})
	if entry.timestamp.After(live.activityAt) {
		live.activityAt = entry.timestamp
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
	delete(c.steeringClaims, claim.token)
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

func (c *delegateTreeController) CompleteModelRequest(claim *delegateModelRequestClaim, history []schema.Turn) ([]llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.modelClaims[claim.token] != claim {
		return nil, errDelegateStaleLease
	}
	delete(c.modelClaims, claim.token)
	_, live, err := c.admitLeaseLocked(claim.lease, delegatestore.PhaseRunning)
	if err != nil || live.binding.runtime != claim.runtime {
		c.evidenceVersion++
		if err != nil {
			return nil, err
		}
		return nil, errDelegateStaleLease
	}
	pending := make([]delegateSteeringAdmission, 0, len(claim.steeringIDs))
	for _, entryID := range claim.steeringIDs {
		pending = append(pending, delegateSteeringAdmission{entryID: entryID})
	}
	history, bound := projectDelegatePendingSteers(history, pending)
	kept := live.pendingSteers[:0]
	for _, pending := range live.pendingSteers {
		if _, claimed := bound[pending.entryID]; !claimed {
			kept = append(kept, pending)
		}
	}
	live.pendingSteers = kept
	c.evidenceVersion++
	return expandHistory(history, replayScope{}), nil
}

func (c *delegateTreeController) AbortModelRequest(claim *delegateModelRequestClaim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.modelClaims[claim.token] != claim {
		return errDelegateStaleLease
	}
	delete(c.modelClaims, claim.token)
	c.evidenceVersion++
	return nil
}

func (c *delegateTreeController) dropRuntimeClaimsForMembersLocked(members map[string]struct{}) {
	for token, claim := range c.steeringClaims {
		if claim != nil {
			if _, covered := members[claim.delegateID]; !covered {
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
		delete(c.modelClaims, token)
	}
}

func projectDelegatePendingSteers(history []schema.Turn, pending []delegateSteeringAdmission) ([]schema.Turn, map[string]struct{}) {
	order := make(map[string]int, len(pending))
	for i, admission := range pending {
		order[admission.entryID] = i
	}
	projected := make([]schema.Turn, 0, len(history))
	steers := make([]schema.Turn, len(pending))
	found := make([]bool, len(pending))
	for _, turn := range history {
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

func (s *Session) appendDelegateSteeringDurably(message string) (delegateTranscriptEntry, error) {
	s.mu.Lock()
	ready := s.transcriptReady && s.transcript != nil
	s.mu.Unlock()
	if !ready {
		return delegateTranscriptEntry{}, errDelegateTranscriptUnavailable
	}
	turn := schema.NewTurn(schema.TurnSteering, llm.User(message))
	turn.Timestamp = s.sclock().Now().UTC()
	turn.SteeringSource = "user"
	turn.StableTurnID = newQueueEntryID()
	if err := s.writeTranscriptDurable(turn); err != nil {
		return delegateTranscriptEntry{}, err
	}
	s.mu.Lock()
	s.history = append(s.history, turn)
	s.mu.Unlock()
	return delegateTranscriptEntry{entryID: turn.StableTurnID, timestamp: turn.Timestamp}, nil
}

func (s *Session) delegateModelHistorySnapshot() []schema.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.Turn(nil), s.history...)
}
