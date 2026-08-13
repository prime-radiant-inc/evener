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

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeMutationLocked(actor, delegateID); err != nil {
		return delegateMutationPlans{}, err
	}
	live := c.live[delegateID]
	if live == nil || live.binding == nil || live.binding.runtime == nil {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	if _, _, err := c.admitLeaseLocked(live.binding.lease, delegatestore.PhaseRunning); err != nil {
		return delegateMutationPlans{}, err
	}
	entry, err := live.binding.runtime.appendDelegateSteeringDurably(message)
	if err != nil {
		return delegateMutationPlans{}, err
	}
	live.pendingSteers = append(live.pendingSteers, delegateSteeringAdmission{entryID: entry.entryID})
	if entry.timestamp.After(live.activityAt) {
		live.activityAt = entry.timestamp
	}
	return delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(delegateID)}}, nil
}

func (c *delegateTreeController) BeginModelRequest(lease delegateLease) ([]llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err != nil {
		return nil, err
	}
	if live.binding.runtime == nil {
		return nil, errDelegateTargetBusy
	}
	history := live.binding.runtime.delegateModelHistorySnapshot()
	history, bound := projectDelegatePendingSteers(history, live.pendingSteers)
	kept := live.pendingSteers[:0]
	for _, pending := range live.pendingSteers {
		if _, ok := bound[pending.entryID]; !ok {
			kept = append(kept, pending)
		}
	}
	live.pendingSteers = kept
	return expandHistory(history, replayScope{}), nil
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
