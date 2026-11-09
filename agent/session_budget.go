package agent

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

type exhaustedBudget string

const (
	exhaustedBudgetTurns               exhaustedBudget = "max_turns"
	exhaustedBudgetToolRounds          exhaustedBudget = "max_tool_rounds_per_input"
	exhaustedBudgetServerContinuations exhaustedBudget = "max_server_continuations"
)

const childTurnBudgetWarning = "You have 5 turns remaining in this session. Report your current status and evidence to your parent soon, and ask for direction if the task cannot be completed safely within the remaining budget."

const rootTurnBudgetWarning = "You have 5 turns remaining in this session. Report your current status and evidence soon, and ask for direction if the task cannot be completed safely within the remaining budget."

type budgetExhaustionError struct {
	Budget    exhaustedBudget
	Limit     int
	Resumable bool
}

func (e *budgetExhaustionError) Error() string {
	return fmt.Sprintf("%s exhausted at limit %d", e.Budget, e.Limit)
}

func (e *budgetExhaustionError) reason() string {
	switch e.Budget {
	case exhaustedBudgetTurns:
		return "turn_budget_exhausted"
	case exhaustedBudgetServerContinuations:
		return "server_continuation_budget_exhausted"
	default:
		return "tool_round_budget_exhausted"
	}
}

func budgetExhaustionFromError(err error) (*budgetExhaustionError, bool) {
	var exhausted *budgetExhaustionError
	if !errors.As(err, &exhausted) {
		return nil, false
	}
	return exhausted, true
}

// emitBudgetExhaustionFailure persists and emits a mid-turn budget exhaustion as
// a terminal TurnFailure, so a returning client sees why the turn ended and can
// offer retry instead of only learning it from a daemon log. Cause.Kind carries
// the typed reason so consumers can branch on it rather than the message.
func (s *Session) emitBudgetExhaustionFailure(exhausted *budgetExhaustionError) {
	data := errorDataFromError(exhausted)
	data.Cause = &events.ErrorCause{Kind: exhausted.reason()}
	s.emitTurnFailure(data)
}

func turnBudgetWarningInHistory(history []schema.Turn) bool {
	for _, turn := range history {
		if turn.Kind != schema.TurnSteering {
			continue
		}
		text := turn.Message.Text()
		if text == childTurnBudgetWarning || text == rootTurnBudgetWarning {
			return true
		}
	}
	return false
}

func (s *Session) queueTurnBudgetWarning() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.MaxTurns <= 0 || s.turnBudgetWarningEmitted {
		return
	}
	remaining := s.cfg.MaxTurns - s.turns
	if remaining > 5 {
		return
	}
	text := rootTurnBudgetWarning
	if s.cfg.spawn.parentSessionID != "" || s.restoredMetaIsSubagent {
		text = childTurnBudgetWarning
	}
	s.steeringQueue = append(s.steeringQueue, steeringMessage{Text: text})
	s.turnBudgetWarningEmitted = true
}
