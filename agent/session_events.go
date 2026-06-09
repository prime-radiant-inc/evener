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
	// or recognized-but-unsupported event names, and invalid matchers), emitted
	// after SESSION_START so they ride the same stream the CLI/TUI/web render.
	// Collected in initPlugins. These are CONFIG diagnostics: they go through the
	// diagnostic path so they render but do NOT run the Notification hook.
	for _, w := range s.pendingHookWarnings {
		s.emitDiagnosticWarning(w)
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
//
// An EventWarning emitted here is a genuine, model-facing warning (context-length,
// content-filter, transcript-write failure, …): it runs the Notification hook so
// the model is informed. Hook-CONFIGURATION diagnostics must NOT run the
// Notification hook and instead go through emitDiagnosticWarning.
func (s *Session) emit(kind events.EventKind, data events.EventData) {
	if s == nil || s.events == nil {
		return
	}
	data = s.sendEvent(kind, data)
	if s.jobManager != nil {
		s.jobManager.onSessionEvent(kind, data)
	}
	if kind == events.EventWarning {
		s.fireNotificationHook(warningHookMessage(data))
	}
}

// emitDiagnosticWarning emits a hook-configuration/matcher diagnostic so the
// CLI/TUI/hub render it (they inspect WarningData on the stream), but WITHOUT
// running the Notification hook. These diagnostics are about the plugin's own
// configuration, not notifications meant for the model; routing them through emit
// would (a) spuriously fire the Notification command hook at session start and
// (b) — for the invalid-matcher diagnostic — recurse, since the Notification
// dispatch re-enters the matcher that produced the warning.
func (s *Session) emitDiagnosticWarning(data events.WarningData) {
	if s == nil || s.events == nil {
		return
	}
	s.sendEvent(events.EventWarning, data)
}

// sendEvent enriches and delivers data on the event stream and returns the
// enriched payload. It performs no side effects beyond the send; the Notification
// hook decision belongs to the caller.
func (s *Session) sendEvent(kind events.EventKind, data events.EventData) events.EventData {
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
	return data
}

// fireNotificationHook runs the Notification hook for a genuine, model-facing
// warning. It carries no re-entrancy guard, and deliberately so: a warning→hook→
// warning recursion is structurally impossible because nothing inside a
// Notification dispatch emits an EventWarning synchronously.
//
//   - Invalid-matcher diagnostics are validated once at load (Runner.Validate) and
//     MatchHooks skips invalid matchers SILENTLY at dispatch — no dispatch-time
//     emit.
//   - Hook-CONFIG diagnostics route through emitDiagnosticWarning, which never
//     fires the Notification hook.
//   - The only events RunNotification → runAll emits are HookStart/HookEnd (never
//     EventWarning). The Notification hook's own output is delivered via
//     deliverHookContext (Steer — emits nothing) and deliverHookUserMessage
//     (emitDiagnosticWarning — sends an EventWarning but, unlike emit, does NOT
//     fire the Notification hook), so no synchronous EventWarning re-enters here.
//
// A session-wide guard here would not prevent any real recursion but WOULD drop a
// genuine, independent warning emitted concurrently from another goroutine (it
// would lose the compare-and-swap for the whole synchronous hook duration). So the
// hook fires unconditionally; the recursion regression tests (recursion_test.go,
// TestNewSession_InvalidMatcherNotificationHookDoesNotRecurse) guard against a
// future synchronous warning-emitter being introduced inside the dispatch path.
func (s *Session) fireNotificationHook(message string) {
	s.runNotificationHook(context.Background(), message)
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
	for _, m := range result.ModelContext {
		s.deliverHookContext(m)
	}
	for _, m := range result.UserMessages {
		s.deliverHookUserMessage(m)
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
