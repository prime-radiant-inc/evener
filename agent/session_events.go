package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// Events returns the session's receive-only channel of SessionEvent values.
//
// Delivery on it is BEST-EFFORT: a full buffer drops. That is correct for an
// observer -- a CLI printing NDJSON, a dev tool -- and correct for the many
// sessions that never read it at all. A consumer whose state would be
// permanently wrong if it missed an event must use ConsumeEventsLossless
// instead.
func (s *Session) Events() <-chan events.SessionEvent { return s.events }

// ConsumeEventsLossless makes consume the session's authoritative consumer:
// every event is delivered to it, in order, until the session closes.
//
// It exists because the daemon's in-memory projection is the sole authority for
// turn reads. An event the daemon never sees is absent from every thread/read
// for the life of the identity, and nothing re-derives it -- a page reload does
// not help, only a daemon restart. Best-effort delivery to that consumer is
// silent, permanent corruption.
//
// It RETURNS ONCE THE REGISTRATION IS IN EFFECT and drains on its own
// goroutine, so no event emitted after the call can be dropped. Returning
// before the mark took hold would leave a startup window in which the feed is
// still lossy -- narrow, since a session emits far fewer than a bufferful
// before its consumer attaches, but narrow-by-luck is what this design exists
// to replace. Callers must NOT wrap it in `go`; that puts the window back.
//
// The registration and the drain loop are ONE call for the same reason.
// Losslessness is a promise about the consumer, not a setting on the session: a
// session marked lossless with nobody reading wedges its emitters forever the
// moment its buffer fills. Making the mark inseparable from the loop means that
// state is unreachable rather than merely discouraged -- which matters because
// the sessions with no reader are the common case (every subagent and
// delegate), and because no test in this repo drives a session hard enough to
// notice.
//
// One consumer per session. A second would silently split the stream between
// them, so it panics rather than corrupting both.
func (s *Session) ConsumeEventsLossless(consume func(events.SessionEvent)) {
	s.eventsMu.Lock()
	if s.eventsClosed {
		s.eventsMu.Unlock()
		return
	}
	if s.authoritativeConsumer {
		s.eventsMu.Unlock()
		panic("agent: session already has an authoritative event consumer")
	}
	s.authoritativeConsumer = true
	s.eventsMu.Unlock()

	go func() {
		for ev := range s.events {
			consume(ev)
		}
	}()
}

// emitSessionStartEnvelope emits EventSessionStart and then flushes every
// buffer of construction-time diagnostics that accumulated before it. THE
// RULING (kata et0x): SESSION_START is a genuine ordering promise, not just
// "the first interesting event" — no consumer of Session.Events() is meant to
// observe a diagnostic for a session it has not yet been told exists. This is
// recorded here, in the one function that is supposed to be every
// construction-time diagnostic's sole door onto the stream, because an
// in-code comment on each individual buffer field was not enough to keep the
// next one from drifting: two call sites (NewSession's transcript-create
// warning, attachTranscript's held-turn flush warning) had already emitted
// directly instead of buffering before this kata fixed them.
//
// This matters beyond tidiness: a client only creates its per-thread state in
// response to SESSION_START's projection (see internal/appprojector's
// EventSessionStart case and cmd/serf-hub/frontend's thread/started handling),
// and a warning is never persisted to the transcript for a later snapshot to
// recover (cmd/serf-hub/frontend/src/protocol/reducer.ts's "warning" case
// comment: "warnings are not transcript-persisted at all ... the next
// snapshot would not carry it either"). A diagnostic that reaches the stream
// before SESSION_START therefore has no tracked thread to land on and is lost
// for good, not just reordered.
//
// Every future construction-time diagnostic MUST follow this same buffer-then-
// flush shape (add a pendingXWarnings-style field, append to it during
// construction, drain it in the loop below) rather than calling s.emit or
// s.emitDiagnosticWarning directly from inside initSessionState/NewSession/
// RestoreSessionFromMetaWithConfig. et0x fixed the two known offenders but did
// not audit every call site reachable before this function runs. Kata 57j8
// found and fixed the rest: resumeWorktreeReentry's re-entry-failure notice
// (resume only, called from RestoreSessionFromMetaWithConfig before
// initSessionState) AND applyInitInsideWorktreeLock's four warnings (called
// from initSessionState, so reachable on BOTH NewSession and
// RestoreSessionFromMetaWithConfig — a fresh session launched inside an
// existing managed worktree lane hits this too, not only a resume). All five
// now buffer into pendingTranscriptWarnings, same as the two et0x fixed.
// Consequence worth knowing: these five sites run before s.hookRunner is
// assigned, so a direct s.emit there never fired the Notification hook;
// buffering them here means they now fire it, for the first time, once
// hookRunner exists.
func (s *Session) emitSessionStartEnvelope(start events.SessionStartData, promptSources []promptSource) {
	s.emit(events.EventSessionStart, start)
	// Collected transcript-health failures (NewSession's transcript create, and
	// attachTranscript's held-turn flush) are genuine, model-facing warnings —
	// unlike the diagnostic buffers below, they run through emit (not
	// emitDiagnosticWarning), so they still fire the Notification hook exactly
	// as they did before kata et0x moved them off the direct-emit path.
	for _, w := range s.pendingTranscriptWarnings {
		s.emit(events.EventWarning, w)
	}
	s.pendingTranscriptWarnings = nil
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
	// Collected MCP connect/register failures (initMCP) ride the same
	// diagnostic path: visible on the stream, no Notification hook.
	for _, w := range s.pendingMCPWarnings {
		s.emitDiagnosticWarning(w)
	}
	s.pendingMCPWarnings = nil
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
	s.emitWithProvenance(kind, data, s.activeCausalProvenance())
}

// emitWithProvenance is emit with an explicit causal provenance stamp. emit
// passes the session's active provenance; callers with a more specific origin
// (a watch delivery) pass it directly. The Notification-hook decision and the
// jobManager fan-out are identical to emit.
func (s *Session) emitWithProvenance(kind events.EventKind, data events.EventData, p *provenance.Causal) {
	if s == nil || s.events == nil {
		return
	}
	data, ev := s.sendEvent(kind, data, p)
	if s.jobManager != nil {
		s.jobManager.onSessionEvent(ev)
	}
	if kind == events.EventWarning {
		s.fireNotificationHook(warningHookMessage(data))
	}
}

// emitTurnFailure reports a terminal turn failure on both channels a client
// can reach it by: the live error event, and a TurnFailure transcript entry
// that survives reload. Both are built from the same events.ErrorData, so the
// failure a returning user reads can never disagree with the one a watching
// user saw. Turn cancellations do NOT come through here — they are not
// failures (the interrupted SessionEnd owns that turn's terminal state), and
// callers on the cancellation path emit the bare event instead.
func (s *Session) emitTurnFailure(data events.ErrorData) {
	s.emit(events.EventError, data)
	s.recordTurnFailure(data)
}

// recordTurnFailure persists the diagnostic of a failed turn as a TurnFailure
// entry. It enriches the data exactly as the event pipeline does, so the
// stored source/title/hint match what the live event carried.
func (s *Session) recordTurnFailure(data events.ErrorData) {
	enriched := enrichErrorData(data)
	info := schema.TurnFailureInfo{
		Message: strings.TrimSpace(enriched.Error),
		Source:  enriched.Source,
		Title:   enriched.Title,
		Hint:    enriched.Hint,
	}
	if enriched.Cause != nil {
		info.Cause = &schema.TurnFailureCause{
			Kind:     enriched.Cause.Kind,
			Provider: enriched.Cause.Provider,
			Model:    enriched.Cause.Model,
			Status:   enriched.Cause.Status,
		}
	}
	// The message also rides the turn's own text so renderers that read only
	// turn text still show the failure.
	turn := schema.NewTurn(schema.TurnFailure, llm.System(info.Message))
	turn.Error = &info
	s.recordTurn(turn, turn)
}

// emitHookCompleted reports a finished hook on both channels a client can
// reach it by: the live HOOK_END event, and a TurnHookCompleted transcript
// entry that survives reload. Both carry the same fields, so the hook line a
// returning reader sees cannot disagree with the one a watching reader saw.
//
// The entry is written unconditionally. Whether a hook line is SHOWN is a
// per-client display choice (Settings → Transcript's two hook-exit toggles,
// which live in browser storage and are applied at render time); recording
// only what some client currently wants to see would make one reader's
// preference destroy another's history, and would make the toggle retroactive
// — turning it on would reveal only hooks that ran afterwards, which is the
// same "switch that governs nothing" complaint this fixes (kata qm9y).
func (s *Session) emitHookCompleted(data events.HookEndData) {
	s.emit(events.EventHookEnd, data)
	info := schema.HookInfo{
		Event:      data.Event,
		HookType:   data.HookType,
		Matcher:    data.Matcher,
		PluginName: data.PluginName,
		ExitCode:   data.ExitCode,
		DurationMS: data.DurationMS,
	}
	// The announcement also rides the turn's own text so renderers that read
	// only turn text still show the hook.
	turn := schema.NewTurn(schema.TurnHookCompleted, llm.System(info.Announcement()))
	turn.Hook = &info

	// SessionStart hooks run inside initSessionState, before the transcript
	// writer exists (kata d4es). recordTurn holds the turn until it does; no
	// buffering is needed here.
	s.recordTurn(turn, turn)
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
	s.sendEvent(events.EventWarning, data, s.activeCausalProvenance())
}

// sendEvent enriches and delivers data on the event stream, stamping the given
// causal provenance onto the envelope, and returns the enriched payload alongside
// the delivered envelope. It performs no side effects beyond the send; the
// Notification hook decision belongs to the caller.
func (s *Session) sendEvent(kind events.EventKind, data events.EventData, p *provenance.Causal) (events.EventData, events.SessionEvent) {
	data = enrichDiagnosticData(kind, data)
	ev := events.New(data)
	ev.SessionID = s.id
	ev.Provenance = provenance.Clone(p)
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
			// The buffer is full, and what to do about it depends entirely on
			// whether anything is reading.
			//
			// With an authoritative consumer attached, a drop is permanent
			// corruption: the daemon's in-memory projection is the sole
			// authority for turn reads, so an event that never arrives is
			// absent from every thread/read for the life of the identity and
			// nothing re-derives it. Wait instead. The wait is bounded by the
			// consumer's own work, which is memory plus at most one jobs.jsonl
			// read per turn.
			//
			// With nothing reading, waiting is not backpressure, it is a
			// permanent wedge. That is the normal case, not an edge: every
			// subagent and delegate has an unread channel by design, because a
			// child reaches its parent through synchronous callbacks instead.
			// For them the drop is the only behaviour that is not a deadlock.
			if s.authoritativeConsumer {
				s.events <- ev
			}
		}
	}
	s.eventsMu.RUnlock()
	return data, ev
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
	result := s.hookRunner.RunNotification(s.apiLogContext(ctx), input)
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
		CWD:           s.currentEnv().WorkingDirectory(),
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
