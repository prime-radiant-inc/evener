package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/llm"
)

// oodaStrategy extends sessionLogStrategy by injecting the session log as a
// steering message before each LLM call (the Orient phase of the OODA loop).
type oodaStrategy struct {
	*sessionLogStrategy // embed for ManageContext layers, AfterAction, Tools
}

// newOODAStrategy creates an oodaStrategy backed by the given contextManager
// and host.
func newOODAStrategy(cm *contextManager, host strategyHost) (*oodaStrategy, error) {
	sls, err := newSessionLogStrategy(cm, host)
	if err != nil {
		return nil, err
	}
	return &oodaStrategy{
		sessionLogStrategy: sls,
	}, nil
}

// Name returns the strategy's identifier, "ooda".
func (s *oodaStrategy) Name() string { return "ooda" }

// ManageContext applies normal compaction layers from sessionLogStrategy,
// then injects the session log as an orientation message at the end of history.
func (s *oodaStrategy) ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	// Apply normal compaction layers.
	if err := s.sessionLogStrategy.ManageContext(ctx, history, sysPromptChars, emitFn); err != nil {
		return err
	}

	// Orient phase: inject session log as context if it has entries.
	if s.log.Len() == 0 {
		return nil
	}

	// Build the orient message.
	logText := s.log.String()

	// Don't inject if the log is too large (> ~20k tokens ≈ 80k chars).
	// In that case, the agent can use the recall tool.
	const maxLogChars = 80_000
	if len(logText) > maxLogChars {
		logText = logText[:maxLogChars] + "\n... [session log truncated, use recall tool for details]"
	}

	orient := fmt.Sprintf("[SESSION ORIENTATION]\nHere is a log of your session actions so far. Use the recall tool if you need details about any entry.\n\n%s\n[END ORIENTATION]", logText)

	// Remove any previous orient turns so they don't accumulate
	// across repeated ManageContext calls.
	filtered := (*history)[:0]
	for _, t := range *history {
		if t.Kind == TurnSteering && strings.Contains(t.Message.Text(), "[SESSION ORIENTATION]") {
			continue
		}
		filtered = append(filtered, t)
	}
	*history = filtered

	// Append the orient message to the end of history.
	// The model will see it just before generating its next response.
	orientTurn := NewTurn(TurnSteering, llm.User(orient))
	*history = append(*history, orientTurn)

	return nil
}
