package agent

import (
	"context"
	"fmt"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
)

// Events returns the session's receive-only channel of SessionEvent values.
func (s *Session) Events() <-chan events.SessionEvent { return s.events }

func (s *Session) emitSessionStartEnvelope(start events.SessionStartData, promptSources []promptSource) {
	s.emit(events.EventSessionStart, start)
	for _, data := range s.pendingPluginEvents {
		s.emit(events.EventPluginLoaded, data)
	}
	s.pendingPluginEvents = nil
	// Loud, visible warnings for misconfigured plugin hook declarations (unknown
	// or recognized-but-unsupported event names), emitted after SESSION_START so
	// they ride the same stream the CLI/TUI/web render. Collected in initPlugins.
	for _, w := range s.pendingHookWarnings {
		s.emit(events.EventWarning, w)
	}
	s.pendingHookWarnings = nil
	for _, src := range promptSources {
		s.emit(events.EventPromptLoaded, events.PromptLoadedData{Label: src.Label, Size: src.Size})
	}
}

// emit sends data on the session's event stream. The kind argument is retained
// for the contextmgr.Host interface contract (strategies call Emit with an
// explicit kind via the ctxHost adapter), but the event's Kind is authoritative
// from the payload: events.New derives it via data.eventKind(), so Kind and
// payload can never disagree even if a caller passes a mismatched kind.
func (s *Session) emit(kind events.EventKind, data events.EventData) {
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
	if kind == events.EventWarning {
		s.runNotificationHook(context.Background(), warningHookMessage(data))
	}
}

func warningHookMessage(data events.EventData) string {
	switch v := data.(type) {
	case events.WarningData:
		return v.Message
	case *events.WarningData:
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
	input := s.hookInput(plugin.HookNotification)
	input.Message = message
	input.Reason = message
	result := s.hookRunner.RunNotification(ctx, input)
	for _, msg := range result.SystemMessages {
		s.Steer(msg)
	}
	// TODO(phase-B): additionalContext is model-context; route distinctly from
	// user-visible systemMessage once a context channel exists.
	for _, msg := range result.AdditionalContext {
		s.Steer(msg)
	}
}

// hookInput creates a hooks.Input with the session's ID, working directory, and
// available official Claude-compatible fields pre-filled.
func (s *Session) hookInput(event plugin.HookEvent) hooks.Input {
	input := hooks.Input{
		SessionID:     s.id,
		CWD:           s.env.WorkingDirectory(),
		HookEventName: string(event),
	}
	// TranscriptPath is empty when persistence is off; omitempty silences it.
	input.TranscriptPath = s.TranscriptPath()
	// Effort is empty when no effort has been configured; omitempty silences it.
	s.mu.Lock()
	effort := s.cfg.ReasoningEffort
	s.mu.Unlock()
	if effort != "" {
		input.Effort = effort
	}
	// PermissionMode is intentionally left unset: serf has no permission-mode field
	// on Session today. Do NOT fabricate a value the hook would act on.
	return input
}
