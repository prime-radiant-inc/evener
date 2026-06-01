package agent

import (
	"context"
	"fmt"

	"primeradiant.com/serf/agent/events"
)

// Events returns the session's receive-only channel of SessionEvent values.
func (s *Session) Events() <-chan SessionEvent { return s.events }

func (s *Session) emitSessionStartEnvelope(start SessionStartData, promptSources []promptSource) {
	s.emit(EventSessionStart, start)
	for _, data := range s.pendingPluginEvents {
		s.emit(EventPluginLoaded, data)
	}
	s.pendingPluginEvents = nil
	for _, src := range promptSources {
		s.emit(EventPromptLoaded, PromptLoadedData{Label: src.Label, Size: src.Size})
	}
}

// emit sends data on the session's event stream. The kind argument is retained
// for the contextStrategy/strategyHost interface contract (strategies call
// emit/Emit with an explicit kind), but the event's Kind is authoritative from
// the payload: events.New derives it via data.eventKind(), so Kind and payload
// can never disagree even if a caller passes a mismatched kind.
func (s *Session) emit(kind EventKind, data EventData) {
	if s == nil || s.events == nil {
		return
	}
	data = enrichDiagnosticData(kind, data)
	ev := events.New(data)
	ev.SessionID = s.id
	// eventsMu makes the send mutually exclusive with Close()'s close(s.events).
	// emit is called from caller-owned goroutines the session cannot join
	// (Enqueue/DrainAsSteer, the ProcessInput loop), so the lock — not a recover()
	// — is what guarantees we never send on a closed channel. Delivery of detached
	// emitters' events before teardown is ensured separately by the WaitGroups.
	s.eventsMu.RLock()
	if !s.eventsClosed {
		select {
		case s.events <- ev:
		default:
			// Drop events if the consumer is too slow; v1 is best-effort.
		}
	}
	s.eventsMu.RUnlock()
	if kind == EventWarning {
		s.runNotificationHook(context.Background(), warningHookMessage(data))
	}
}

func warningHookMessage(data EventData) string {
	switch v := data.(type) {
	case WarningData:
		return v.Message
	case *WarningData:
		if v != nil {
			return v.Message
		}
	}
	return fmt.Sprint(data)
}

func (s *Session) runNotificationHook(ctx context.Context, message string) {
	if s.hookRunner == nil {
		return
	}
	input := s.hookInput(HookNotification)
	input.Message = message
	input.Reason = message
	result := s.hookRunner.RunNotification(ctx, input)
	for _, msg := range result.SystemMessages {
		s.Steer(msg)
	}
}

// hookInput creates a hookInput with the session's ID and working directory pre-filled.
func (s *Session) hookInput(event HookEvent) hookInput {
	return hookInput{
		SessionID:     s.id,
		CWD:           s.env.WorkingDirectory(),
		HookEventName: string(event),
	}
}
