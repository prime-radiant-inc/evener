package agent

import (
	"context"
	"fmt"
	"time"
)

func (s *Session) Events() <-chan SessionEvent { return s.events }

func (s *Session) emitSessionStartEnvelope(start SessionStartData, promptSources []PromptSource) {
	s.emit(EventSessionStart, start)
	for _, data := range s.pendingPluginEvents {
		s.emit(EventPluginLoaded, data)
	}
	s.pendingPluginEvents = nil
	for _, src := range promptSources {
		s.emit(EventPromptLoaded, PromptLoadedData{Label: src.Label, Size: src.Size})
	}
}

func (s *Session) emit(kind EventKind, data any) {
	if s == nil || s.events == nil {
		return
	}
	data = enrichDiagnosticData(kind, data)
	ev := SessionEvent{
		Kind:      kind,
		Timestamp: time.Now().UTC(),
		SessionID: s.id,
		Data:      data,
	}
	// Detached event emitters (subagent runs, the session namer) are joined by
	// Close() via sendersWG before it closes the channel, so they cannot race the
	// close. The remaining concurrent emitter is the caller-owned ProcessInput
	// goroutine during an abort-Close: the session cannot join a goroutine it does
	// not own, so guard that best-effort send against a closed channel here.
	defer func() { _ = recover() }()
	select {
	case s.events <- ev:
	default:
		// Drop events if consumer is too slow; v1 is best-effort.
	}
	if kind == EventWarning {
		s.runNotificationHook(context.Background(), warningHookMessage(data))
	}
}

func warningHookMessage(data any) string {
	switch v := data.(type) {
	case WarningData:
		return v.Message
	case *WarningData:
		if v != nil {
			return v.Message
		}
	case map[string]any:
		if msg, ok := v["message"].(string); ok {
			return msg
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

// hookInput creates a HookInput with the session's ID and working directory pre-filled.
func (s *Session) hookInput(event HookEvent) HookInput {
	return HookInput{
		SessionID:     s.id,
		CWD:           s.env.WorkingDirectory(),
		HookEventName: string(event),
	}
}
