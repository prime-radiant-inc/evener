package agent

import (
	"context"
	"fmt"

	"primeradiant.com/serf/llm"
)

// OODAStrategy extends SessionLogStrategy by injecting the session log as a
// steering message before each LLM call (the Orient phase of the OODA loop).
type OODAStrategy struct {
	*SessionLogStrategy // embed for ManageContext layers, AfterAction, Tools
}

// NewOODAStrategy creates an OODAStrategy backed by the given ContextManager
// and Session.
func NewOODAStrategy(cm *ContextManager, session *Session) *OODAStrategy {
	return &OODAStrategy{
		SessionLogStrategy: NewSessionLogStrategy(cm, session),
	}
}

func (s *OODAStrategy) Name() string { return "ooda" }

// ManageContext applies normal compaction layers from SessionLogStrategy,
// then injects the session log as an orientation message at the end of history.
func (s *OODAStrategy) ManageContext(ctx context.Context, history *[]Turn, pressure float64, sysPromptChars int, emitFn func(EventKind, any)) error {
	// Apply normal compaction layers.
	if err := s.SessionLogStrategy.ManageContext(ctx, history, pressure, sysPromptChars, emitFn); err != nil {
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

	// Append the orient message to the end of history.
	// The model will see it just before generating its next response.
	orientTurn := NewTurn(TurnSteering, llm.User(orient))
	*history = append(*history, orientTurn)

	return nil
}
