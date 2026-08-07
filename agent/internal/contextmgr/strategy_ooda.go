package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// OODAStrategy extends SessionLogStrategy by injecting the session log as a
// steering message before each LLM call (the Orient phase of the OODA loop).
type OODAStrategy struct {
	*SessionLogStrategy // embed for ManageContext layers, AfterAction, Tools
}

// NewOODAStrategy creates an OODAStrategy backed by the given Manager
// and host.
func NewOODAStrategy(cm *Manager, host Host) (*OODAStrategy, error) {
	sls, err := NewSessionLogStrategy(cm, host)
	if err != nil {
		return nil, err
	}
	return &OODAStrategy{
		SessionLogStrategy: sls,
	}, nil
}

// Name returns the strategy's identifier, "ooda".
func (s *OODAStrategy) Name() string { return "ooda" }

// ManageContext applies normal compaction layers from SessionLogStrategy,
// then injects the session log as an orientation message at the end of history.
func (s *OODAStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	// Apply normal compaction layers.
	if err := s.SessionLogStrategy.ManageContext(ctx, history, sysPromptChars, emitFn); err != nil {
		return err
	}

	// Orient phase: inject session log as context if it has entries.
	if s.log.Len() == 0 {
		return nil
	}

	// Build the orient message.
	logText := s.log.String()

	// Don't inject if the log is too large (> ~20k tokens ≈ 80k chars).
	// In that case, the full detail stays in the transcript, which the agent
	// reaches with whatever transcript tools it has.
	const maxLogChars = 80_000
	recovery := s.transcriptRecoverySentence()
	if len(logText) > maxLogChars {
		truncation := "\n... [session log truncated; the full detail is in this session's transcript]"
		if recovery != "" {
			truncation = "\n... [session log truncated; the full detail is in this session's transcript." + recovery + "]"
		}
		logText = logText[:maxLogChars] + truncation
	}

	orient := fmt.Sprintf("[SESSION ORIENTATION]\nHere is a log of your session actions so far.%s\n\n%s\n[END ORIENTATION]", recovery, logText)

	// Remove any previous orient turns so they don't accumulate
	// across repeated ManageContext calls.
	filtered := (*history)[:0]
	for _, t := range *history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), "[SESSION ORIENTATION]") {
			continue
		}
		filtered = append(filtered, t)
	}
	*history = filtered

	// Append the orient message to the end of history.
	// The model will see it just before generating its next response.
	orientTurn := schema.NewTurn(schema.TurnSteering, llm.User(orient))
	*history = append(*history, orientTurn)

	return nil
}
