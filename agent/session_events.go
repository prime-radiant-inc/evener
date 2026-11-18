package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/toolname"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
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
//
// onDrained runs once the drain has finished, AFTER the last event has been
// handed to consume. Anything whose lifetime is shorter than the session's -- a
// log sink, a tee, a file handle the consumer writes to -- must tear down from
// there. Session.Close() closes the event channel but does NOT wait for the tail
// to be delivered, so "the session is closed" is not "the consumer is done".
//
// It is a CALLBACK rather than a returned channel on purpose. A channel can be
// discarded: Go permits ignoring a non-error return, errcheck does not cover it,
// and no linter here flags it, so `ConsumeEventsLossless(consume)` compiled
// cleanly and put the shutdown crash straight back. A parameter cannot be
// omitted, so no call site can be SILENT about the wait.
//
// That closes omission, not misuse, and the distinction is load-bearing rather
// than pedantic: passing `func() {}` here and tearing the sink down elsewhere
// compiles, and reproduces the original crash with a byte-identical stack.
// cmd/evener/serve.go is deliberately that shape -- one --verbose tee outlives N
// drains across /clear, so the session cannot own the sink's lifetime and the
// wait has to live at the caller that joins them. So unlike
// authoritativeConsumer above, the unsafe state here is still spellable; what
// the parameter buys is that choosing it is a visible decision at the call site
// instead of an absent return nobody reads.
//
// onDrained must not be nil. A caller with nothing to tear down passes an empty
// function, which is a visible decision; nil would be the discardable return in
// another costume.
func (s *Session) ConsumeEventsLossless(consume func(events.SessionEvent), onDrained func()) {
	if onDrained == nil {
		panic("agent: ConsumeEventsLossless requires an onDrained callback; pass func() {} if nothing needs tearing down")
	}
	s.eventsMu.Lock()
	if s.eventsClosed {
		s.eventsMu.Unlock()
		onDrained()
		return
	}
	if s.authoritativeConsumer {
		s.eventsMu.Unlock()
		panic("agent: session already has an authoritative event consumer")
	}
	s.authoritativeConsumer = true
	s.eventsMu.Unlock()

	go func() {
		defer onDrained()
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
// EventSessionStart case and cmd/evener-hub/frontend's thread/started handling),
// and a warning is never persisted to the transcript for a later snapshot to
// recover (appwire-client/typescript/reducer.ts's "warning" case
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
// initSessionState) AND applyInitInsideWorktreeLock's five warnings (called
// from initSessionState, so reachable on BOTH NewSession and
// RestoreSessionFromMetaWithConfig — a fresh session launched inside an
// existing managed worktree lane hits this too, not only a resume). All six
// now buffer into pendingTranscriptWarnings, same as the two et0x fixed.
// Consequence worth knowing: these six sites run before s.hookRunner is
// assigned, so a direct s.emit there never fired the Notification hook;
// buffering them here means they now fire it, for the first time, once
// hookRunner exists.
func (s *Session) emitSessionStartEnvelope(start events.SessionStartData, promptSources []promptSource) {
	frontier := s.LatestExecutionFrontier()
	start.LatestExecutionTurnID, start.LatestExecutionSequence = frontier.TurnID, frontier.Sequence
	// The sandbox request is fixed for the session's life; SessionStart
	// carries it so a subagent's thread reports its own access (S15).
	start.Sandbox = s.cfg.Sandbox
	start.SandboxNet = appwire.ClonePointer(s.cfg.SandboxNet)
	// Construction (including restored worktree re-entry) has installed the
	// environment before this seed. Do not use the cached launch directory.
	s.mu.Lock()
	start.WorkingDir = s.envInfo.WorkingDir
	s.mu.Unlock()
	store := s.getOrCreateTaskStore()
	_ = store.MutateAndPublish(func(epoch, revision uint64) error {
		// Sample current work only after entering the shared store's publication
		// order. Otherwise a newer mutation could publish between the sample and
		// this start seed, giving an old snapshot a newer revision.
		if start.CurrentWork == nil {
			start.CurrentWork = s.currentWorkSeedData()
		}
		if start.TaskStoreOwnerSessionID == "" {
			start.TaskStoreOwnerSessionID = s.taskStoreOwnerSessionID()
		}
		start.TaskPublicationEpoch = epoch
		start.TaskPublicationRevision = revision
		s.emit(start)
		return nil
	})
	s.mu.Lock()
	s.sessionStarted = true
	s.mu.Unlock()
	// Collected transcript-health failures (NewSession's transcript create, and
	// attachTranscript's held-turn flush) are genuine, model-facing warnings —
	// unlike the diagnostic buffers below, they run through emit (not
	// emitDiagnosticWarning), so they still fire the Notification hook exactly
	// as they did before kata et0x moved them off the direct-emit path.
	for _, w := range s.pendingTranscriptWarnings {
		s.emit(w)
	}
	s.pendingTranscriptWarnings = nil
	for _, data := range s.pendingPluginEvents {
		s.emit(data)
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
	// Instruction-doc truncation is a construction-time config diagnostic too:
	// visible on the stream, no Notification hook (#1048).
	for _, w := range s.pendingInstructionWarnings {
		s.emitDiagnosticWarning(w)
	}
	s.pendingInstructionWarnings = nil
	for _, src := range promptSources {
		s.emit(events.PromptLoadedData{Label: src.Label, Size: src.Size})
	}
}

// currentWorkSeedData captures the self-contained task and structured goal
// state used by SessionStart. The non-nil envelope makes goal absence an
// authoritative clear; task-load failure is represented independently by nil
// Tasks.
func (s *Session) currentWorkSeedData() *events.CurrentWorkSeedData {
	seed := &events.CurrentWorkSeedData{}
	if tasks, err := s.TasksWithError(); err == nil {
		state := taskStateData(task.Summarize(tasks))
		seed.Tasks = &state
	}
	if goal := s.Meta().Goal; goal != nil {
		seed.Goal = &events.GoalStateData{
			Objective:  goal.Objective,
			Status:     goal.Status,
			Iterations: goal.Iterations,
		}
	}
	return seed
}

// emit sends data on the session's event stream. The event's Kind is derived
// from the payload (events.KindOf), so Kind and payload can never disagree.
//
// An EventWarning emitted here is a genuine, model-facing warning (context-length,
// content-filter, transcript-write failure, …): it runs the Notification hook so
// the model is informed. Hook-CONFIGURATION diagnostics must NOT run the
// Notification hook and instead go through emitDiagnosticWarning.
func (s *Session) emit(data events.EventData) {
	// Task mutations are emitted by transport-neutral tool handlers. Stamp the
	// owning session at this Session boundary so shared descendants name the
	// durable root store without leaking Session into the handler dependency.
	if update, ok := data.(events.TaskUpdatedData); ok && update.TaskStoreOwnerSessionID == "" {
		update.TaskStoreOwnerSessionID = s.taskStoreOwnerSessionID()
		data = update
	}
	s.emitWithProvenance(data, s.activeCausalProvenance())
}

// emitContext is emit on a caller's bound: the same publication, made under a
// deadline the caller owns rather than the session's. The close drain uses it so
// the Notification hook it fires -- and that hook's own output -- shares the
// stop deadline instead of the close cascade's.
//
// It is deliberately NOT the body of emit. emit must keep calling
// activeCausalProvenance and emitWithProvenance must keep calling
// jobManager.onSessionEvent directly on the emitting goroutine: those calls are
// what make an emit under Session.mu or jobManager.mu self-deadlock on the spot
// instead of wedging the daemon silently, and TestEmitReacquiresSessionMu and
// TestEmitWithProvenanceReacquiresJobManagerMu pin both (see
// session_emit_lock_guard_test.go). Routing the unbounded path through this
// bounded one would move both calls behind an extra hop and delete that guard.
func (s *Session) emitContext(ctx context.Context, data events.EventData) {
	if ctx == nil {
		s.emit(data)
		return
	}
	if update, ok := data.(events.TaskUpdatedData); ok && update.TaskStoreOwnerSessionID == "" {
		update.TaskStoreOwnerSessionID = s.taskStoreOwnerSessionID()
		data = update
	}
	s.emitWithProvenanceContext(ctx, data, s.activeCausalProvenance())
}

// emitWithProvenance is emit with an explicit causal provenance stamp. emit
// passes the session's active provenance; callers with a more specific origin
// (a watch delivery) pass that origin's stamp. The Notification-hook decision
// and the jobManager fan-out are identical to emit.
//
// The envelope ADOPTS p rather than copying it, so the caller must own it: a
// caller holding a record something else can still read or mutate — a
// descriptor, a jobstore event, a recorded transcript turn — passes
// provenance.Clone of that record's stamp, never the record's own. See
// sendEventContext for why adoption is the contract and what it costs to
// copy the stamp a second time.
func (s *Session) emitWithProvenance(data events.EventData, p *provenance.Causal) {
	if s == nil || s.events == nil {
		return
	}
	data, ev := s.sendEvent(data, p)
	if s.jobManager != nil {
		s.jobManager.onSessionEvent(ev)
	}
	if events.KindOf(data) == events.EventWarning {
		s.fireNotificationHook(warningHookMessage(data), warningHookTitle(data))
	}
}

// emitWithProvenanceContext is emitWithProvenance on a caller's bound. It carries
// no nil case on purpose: the unbounded path is emitWithProvenance above, which
// keeps the two lock-re-acquiring calls the guards pin.
func (s *Session) emitWithProvenanceContext(ctx context.Context, data events.EventData, p *provenance.Causal) {
	if s == nil || s.events == nil {
		return
	}
	data, ev := s.sendEventOnContext(ctx, data, p)
	if s.jobManager != nil {
		// The observer's flush is SYNCHRONOUS with this send: a bounded
		// publication can take the last slot in a full stream, and the watch
		// activity flush that follows then runs on this goroutine. On the
		// unbounded path it waits out the whole close cascade, and the stop
		// deadline that released THIS send cannot release that one -- so the
		// budget teardown needs is spent immediately after a correctly bounded
		// publication. The bound is passed, not dropped here.
		s.jobManager.onSessionEventContext(ctx, ev)
	}
	if events.KindOf(data) == events.EventWarning {
		s.fireNotificationHookBounded(ctx, warningHookMessage(data), warningHookTitle(data))
	}
}

// emitTurnFailure reports a terminal turn failure on both channels a client
// can reach it by: the live error event, and a TurnFailure transcript entry
// that survives reload. Both are built from the same events.ErrorData, so the
// failure a returning user reads can never disagree with the one a watching
// user saw. Turn cancellations do NOT come through here — they are not
// failures (the interrupted SessionEnd owns that turn's terminal state), and
// callers on the cancellation path emit the bare event instead. The entry is
// recorded first, and the event is marked Recorded when it was: history then
// shows the failure, so a live view need not keep it.
func (s *Session) emitTurnFailure(data events.ErrorData) {
	data.Recorded = s.recordTurnFailure(data, false).Recorded
	s.emit(data)
}

// emitSteeringCarrierTurnFailure is emitTurnFailure's steering-carrier
// sibling: identical on the live channel, but tags the persisted TurnFailure
// with one of the two shapes (schema.TurnFailureInfo.SteeringCarrier's own
// doc comment) deriveRestoredAskPending/deriveRestoredState
// (session_tools_ask.go) may treat as a resolution boundary on its own. The
// ONLY caller of THIS function is acceptSteeringCarrierInput's
// carrierSteerUndelivered case (session_lifecycle.go), and only when the
// claimed steer answers the ask (steeringCarrierClaimAnswersAsk) — the turn
// whose acceptance already cleared askPending and then recorded nothing
// else; a human-note carrier's own append failure calls emitTurnFailure
// instead, since its entry clear never ran. The tag's other shape, a
// carrier-claim's own steer selection failure, is set directly by
// recordFailedSteeringSelection (session_queue.go) without going through
// this function, gated by the same answering check.
func (s *Session) emitSteeringCarrierTurnFailure(data events.ErrorData, clientMutationID string) {
	data.Recorded = s.recordTurnFailureWithAsk(data, true, s.clientMutationAskResolution(clientMutationID)).Recorded
	s.emit(data)
}

// recordTurnFailure persists the diagnostic of a failed turn as a TurnFailure
// entry. It enriches the data exactly as the event pipeline does, so the
// stored source/title/hint match what the live event carried. It reports the
// entry's record.
func (s *Session) recordTurnFailure(data events.ErrorData, steeringCarrier bool) transcript.Record {
	return s.recordTurnFailureWithAsk(data, steeringCarrier, nil)
}

func (s *Session) recordTurnFailureWithAsk(data events.ErrorData, steeringCarrier bool, resolution *schema.AskResolution) transcript.Record {
	enriched := enrichErrorData(data)
	info := schema.TurnFailureInfo{
		Message:         strings.TrimSpace(enriched.Error),
		Source:          enriched.Source,
		Title:           enriched.Title,
		Hint:            enriched.Hint,
		SteeringCarrier: steeringCarrier,
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
	turn.AskResolution = resolution
	if resolution != nil {
		s.mu.Lock()
		activeExecution := s.execution.turnID
		s.mu.Unlock()
		turn.AnswerToTurnIDs = askResolutionTurnIDs(resolution, activeExecution)
	}
	return s.recordTurn(turn, turn)
}

// installRunnerEventCallback wires the runner's lifecycle events onto the
// session's stream. It is one function rather than two closures in
// initSessionState so the policy below is a single thing a test can exercise.
//
// A hook dispatched on a publication bound of its own has its lifecycle events
// published on that bound. ONLY such a dispatch is. The dispatch context an
// ordinary hook runs under is its turn's, and applying that context here would
// drop a live completion the moment the turn is interrupted, even though the
// session stays open and the completion reached the transcript -- a reloaded
// reader would then see a hook exit no live reader ever saw. So the bound is
// opt-in, marked by the dispatcher that has one to impose and knows what the
// loss means: today that is the delegate close drain, which must not hold the
// teardown budget open on an unresponsive consumer. Every other dispatch takes
// the unbounded path exactly as it always has.
func (s *Session) installRunnerEventCallback(runner *hooks.Runner) {
	// The unbounded route. A completed hook additionally goes to disk (kata
	// qm9y): a hook exit that exists only as a live event leaves the
	// transcript's hook-exit toggles governing nothing for any session a reader
	// comes back to.
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		if end, ok := data.(events.HookEndData); ok && kind == events.EventHookEnd {
			s.emitHookCompleted(end)
			return
		}
		s.emit(data)
	})
	runner.SetEventCallbackContext(s.publishHookLifecycle)
}

// publishHookLifecycle is the policy described above, as one method: a dispatch
// that marked a publication bound publishes on it, and every other dispatch
// takes the session's ordinary delivery path.
func (s *Session) publishHookLifecycle(ctx context.Context, kind events.EventKind, data events.EventData) {
	end, isEnd := data.(events.HookEndData)
	isEnd = isEnd && kind == events.EventHookEnd
	bounded, ok := publicationBound(ctx)
	// Recorded at the decision rather than at the send: the send's own outcome
	// is a select among ready cases, so a test that watched it would agree with
	// a wrong policy some of the time. This reports which path the policy chose,
	// which is the thing that must not drift.
	if observe := s.testOnlyPublicationRoute; observe != nil {
		observe(ok)
	}
	if !ok {
		if isEnd {
			s.emitHookCompleted(end)
			return
		}
		s.emit(data)
		return
	}
	if isEnd {
		// The marked path is the close drain's, which is on the close-join
		// critical path, so its completion also bounds the ACQUISITION of the
		// transcript door rather than waiting on the close budget for it.
		s.emitHookCompletedBounded(bounded, end)
		return
	}
	s.emitContext(bounded, data)
}

// publicationBoundKey marks a hook dispatch whose lifecycle and output
// publications are made on the bound carried in the context, rather than on the
// session's own delivery path.
//
// It exists because the alternative -- applying every dispatch's context to
// every hook event -- is the defect it replaces. A turn's cancellation is the
// context an ordinary hook dispatch runs under, and an ordinary hook's
// completion must reach the stream even when that turn is interrupted: the
// completion is also recorded in the transcript, so dropping the live event
// leaves a reloaded session showing a hook exit that live readers never saw.
// The bound here is therefore supplied only by a caller that has one to impose
// and knows what to do with the loss, which today is the delegate close drain.
type publicationBoundKey struct{}

// withPublicationBound marks ctx as a dispatch bound the publisher must honour.
func withPublicationBound(ctx context.Context) context.Context {
	return context.WithValue(ctx, publicationBoundKey{}, true)
}

// publicationBound reports the bound a lifecycle publication must use, and
// whether the dispatcher supplied one at all.
func publicationBound(ctx context.Context) (context.Context, bool) {
	if !suppliedCallerBound(ctx) {
		return nil, false
	}
	if bounded, _ := ctx.Value(publicationBoundKey{}).(bool); !bounded {
		return nil, false
	}
	return ctx, true
}

// closeJoinBoundKey marks a publication context that belongs to the CLOSE-JOIN
// critical path, as distinct from any other caller that supplied a bound.
//
// The distinction earns its own key because "a bound exists" is not the question
// the wake path needs answered. An ordinary stop driver supplies a bound too, and
// it must keep waking the receiver whose acknowledgment it just retried: that
// wake is the delivery's own, it is not on a close join, and nothing is waiting
// on it to reach worktree disposal. Only the close drain's publications pay the
// price of being off that join.
type closeJoinBoundKey struct{}

// withCloseJoinBound marks ctx as belonging to the close-join critical path.
func withCloseJoinBound(ctx context.Context) context.Context {
	return context.WithValue(ctx, closeJoinBoundKey{}, true)
}

// closeJoinBound reports whether ctx is a close-join publication context. An
// unmarked context -- Background, a turn's, an ordinary stop driver's, the
// session's -- reports false.
func closeJoinBound(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(closeJoinBoundKey{}).(bool)
	return marked
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
	s.emit(data)
	s.recordHookCompletion(data, recordDiagnostics{warn: s.emitDiagnosticWarning})
}

// emitHookCompletedBounded is emitHookCompleted for a caller on the
// close-join critical path. It additionally bounds the ACQUISITION of the
// transcript door: the drain waits for this callback, so a completion that
// cannot take the door within the bound records nothing and announces the drop
// rather than parking on a lock the close budget owns.
func (s *Session) emitHookCompletedBounded(bound context.Context, data events.HookEndData) {
	s.emitContext(bound, data)
	s.recordHookCompletionBounded(bound, data, recordDiagnostics{
		bound: bound,
		warn:  func(w events.WarningData) { s.emitDiagnosticWarningContext(bound, w) },
	})
}

// recordHookCompletion writes the completion turn, and is shared by both
// publication paths above. warn carries the caller's bound and sink for the
// transcript-write diagnostics this record raises.
func (s *Session) recordHookCompletion(data events.HookEndData, diag recordDiagnostics) {
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
	//
	// Recording this completion can fail, and a transcript-write failure is an
	// EventWarning, which fires the Notification hook. That hook completes here
	// too, so the ordinary warning path would dispatch hook after hook for as
	// long as the writer keeps refusing, spending the shutdown budget one
	// dispatch at a time. This record's own failures are therefore published as
	// diagnostics: the failure still reaches every reader of the stream, it just
	// cannot re-enter the dispatcher that caused it. The sink is scoped to this
	// call rather than to the session, so a concurrent warning from another
	// goroutine keeps firing its hook as usual. The invariant is directional:
	// the warning path is what dispatches hooks, so the warning path must never
	// be reachable from a hook's own completion.
	//
	// The sink is also the caller's bound. A close drain launched its hook on
	// the stop context and is waiting for this record to return, so publishing
	// here on the full close budget is what let a stalled stream spend the
	// budget reserved for leaving the lane's worktree on disk. The bound is the
	// caller's, so a responsive consumer still receives the warning and the
	// record still succeeds.
	s.recordTurnTo(turn, turn, diag)
}

// recordHookCompletionBounded is recordHookCompletion with a bounded
// acquisition: the turn is built exactly as above, and the record is attempted
// only while the caller's bound still has time.
func (s *Session) recordHookCompletionBounded(bound context.Context, data events.HookEndData, diag recordDiagnostics) {
	info := schema.HookInfo{
		Event:      data.Event,
		HookType:   data.HookType,
		Matcher:    data.Matcher,
		PluginName: data.PluginName,
		ExitCode:   data.ExitCode,
		DurationMS: data.DurationMS,
	}
	turn := schema.NewTurn(schema.TurnHookCompleted, llm.System(info.Announcement()))
	turn.Hook = &info
	_, _ = s.recordTurnBounded(bound, turn, turn, diag)
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
	s.sendEvent(data, s.activeCausalProvenance())
}

// emitDiagnosticWarningContext is emitDiagnosticWarning on a caller's bound, so a
// hook the close drain launched publishes its own output on the drain's deadline
// instead of the full close budget.
func (s *Session) emitDiagnosticWarningContext(ctx context.Context, data events.WarningData) {
	if s == nil || s.events == nil {
		return
	}
	s.sendEventOnContext(ctx, data, s.activeCausalProvenance())
}

// sendEvent enriches and delivers data on the event stream, stamping the given
// causal provenance onto the envelope, and returns the enriched payload alongside
// the delivered envelope. Summary failure metadata is also retained in the
// process log; the Notification hook decision belongs to the caller.
//
// The envelope ADOPTS p rather than copying it, so the caller must own p: a
// value nothing else reads or mutates. Callers holding a shared record (a
// descriptor, a jobstore event, a transcript entry) pass provenance.Clone of
// it. See sendEventContext for why adoption is the contract.
func (s *Session) sendEvent(data events.EventData, p *provenance.Causal) (events.EventData, events.SessionEvent) {
	data, ev, _ := s.sendEventContext(context.Background(), data, p)
	return data, ev
}

// sendEventOnContext delivers on a caller's bound, which bounds the send in
// addition to whatever close budget the session has published.
func (s *Session) sendEventOnContext(ctx context.Context, data events.EventData, p *provenance.Causal) (events.EventData, events.SessionEvent) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, ev, _ := s.sendEventContext(ctx, data, p)
	return data, ev
}

// sendEventContext is the bounded variant used by shutdown's terminal
// boundary. An authoritative consumer normally provides backpressure so the
// event cannot be lost; when that consumer is wedged, the close deadline must
// release eventsMu so teardown can close the stream and preserve the durable
// closed state.
//
// The delivered envelope ADOPTS p: it is not copied again here. Copying it a
// second time cost every watch-driven event two identical stamps, 4 of its 8
// allocations (#6413). Adoption is safe because a provenance reaches this
// function already owned by this one event: activeCausalProvenance returns a
// fresh clone per call, so two events never share one, and no consumer mutates
// a delivered envelope's stamp. A caller holding a value anything else can read
// or mutate passes provenance.Clone of it instead.
func (s *Session) sendEventContext(ctx context.Context, data events.EventData, p *provenance.Causal) (events.EventData, events.SessionEvent, bool) {
	data = enrichDiagnosticData(data)
	ev := events.New(data)
	ev.SessionID = s.id
	ev.Provenance = p
	// eventsMu makes the send mutually exclusive with Close()'s close(s.events).
	// emit is called from caller-owned goroutines the session cannot join
	// (the client-mutation queue handlers, the ProcessInput loop), so the lock — not a recover()
	// — is what guarantees we never send on a closed channel. Delivery of detached
	// emitters' events before teardown is ensured separately by the WaitGroups.

	// closeSignal is minted once and never replaced, so the hot path only has
	// to read it. Taking the exclusive lock on every event to find it already
	// there serialized emission on the same lock close needs to publish the
	// shared deadline; only a session's first emitter upgrades to mint it.
	s.closeCtxMu.RLock()
	closeSignal := s.closeSignal
	s.closeCtxMu.RUnlock()
	if closeSignal == nil {
		s.closeCtxMu.Lock()
		if s.closeSignal == nil {
			s.closeSignal = make(chan struct{})
		}
		closeSignal = s.closeSignal
		s.closeCtxMu.Unlock()
	}
	s.eventsMu.RLock()
	open := !s.eventsClosed
	delivered := false
	if open {
		s.closeCtxMu.RLock()
		closeCtx := s.closeCtx
		s.closeCtxMu.RUnlock()
		// Until a close publishes its budget, the session's own lifetime is the
		// ONLY thing that can release a send parked below: the caller's context
		// is context.Background() for every ordinary emit, and closeSignal is
		// closed off that same unpublished budget. The goroutine parked there
		// can be the daemon's input loop, and shutdown waits for that loop
		// before it reaches the call that would publish either -- so without
		// this arm the wait and the publisher wait on each other.
		//
		// A budget published BEFORE this send arrives supersedes the lifetime
		// outright, and a nil channel never fires. A budget published while the
		// send is already parked is handled on the arm itself: close publishes
		// and then cancels the session context a few statements later, so an
		// already-parked send sees the lifetime end with a fresh budget in hand
		// and must come back to that budget rather than drop an event it still
		// has its whole window to deliver.
		// The caller's bound and the published close budget are INDEPENDENT
		// exit conditions, not one context that wins over the other. Merging
		// them by taking the earlier deadline silently discarded the caller's
		// cancellation whenever that caller had no deadline of its own: a stop
		// driver runs on a cancel-only context, so a send it made while a close
		// budget was published waited out the whole cascade even after the
		// driver was abandoned. Both arms are kept below, so whichever fires
		// first releases the send.
		//
		// An ordinary emit passes context.Background(), which never fires and
		// has no deadline, so the close arm is the only one that can release it
		// — exactly the close window it had before.
		var lifetimeDone, closeDone <-chan struct{}
		if closeCtx != nil {
			closeDone = closeCtx.Done()
		} else {
			lifetimeDone = s.sessionContext().Done()
		}
		// A delegate close drain caps every send made while it publishes,
		// including the Notification hook's own lifecycle events and output.
		// Those run on the hook runner's goroutines and carry no caller
		// context, so the cap has to travel through the session to reach them.
		//
		// This arm only bites because the close budget above COMBINES with the
		// caller's bound instead of replacing it. While a close budget
		// superseded the caller outright, a hook publishing on
		// context.Background() had no deadline for a caller to shorten and any
		// stop-scoped cap was discarded before this line -- the send then
		// parked until the close cascade's own deadline.
		select {
		case s.events <- ev:
			delivered = true
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
			//
			// SCOPE OF THE WAIT, because it is wider than it looks: this blocks
			// holding eventsMu.RLock, and Close() needs eventsMu.Lock to close
			// the channel. So a consumer that stops draining does not merely
			// stall emission — the session can no longer be TORN DOWN either,
			// and Close()'s sendersWG.Wait() never returns. That is the trade
			// against silent projection corruption, and it is why the consumer's
			// per-event work is bounded on purpose (server.BridgeEvent).
			if s.authoritativeConsumer {
				if observe := s.testOnlyBlockedSendEntered; observe != nil {
					observe()
				}
				select {
				case s.events <- ev:
					delivered = true
				case <-ctx.Done():
				case <-closeDone:
				case <-closeSignal:
				case <-lifetimeDone:
					// The lifetime ended under this send. If a close published
					// its budget in the meantime, that budget owns the event
					// exactly as it would for a send that arrived after the
					// publication, so re-park on it. At daemon shutdown there
					// is none and none is possible -- the call that would
					// publish one waits on the goroutine parked here -- so that
					// path still leaves immediately.
					s.closeCtxMu.RLock()
					published := s.closeCtx
					s.closeCtxMu.RUnlock()
					if published != nil {
						if observe := s.testOnlyBlockedSendEntered; observe != nil {
							observe()
						}
						select {
						case s.events <- ev:
							delivered = true
						case <-ctx.Done():
						case <-published.Done():
						case <-closeSignal:
						}
					}
				}
			}
		}
	}
	s.eventsMu.RUnlock()
	if open && events.KindOf(data) == events.EventWarning {
		s.logSummaryFailure(data)
	}
	forwarded := open && s.descendantEvent != nil
	if forwarded {
		s.descendantEvent(ev)
	}
	if ev.Kind == events.EventSessionStart {
		// The introduction must reach every synchronous route before held
		// first-record signals can follow it. An unread child channel may drop
		// even though its parent received the start. Release before job fan-out.
		s.publishFrontierStartup(open && (delivered || forwarded))
	}
	return data, ev, open && delivered
}

// SetDescendantEventFunc installs the callback inherited by subsequently
// spawned descendants. Root events continue through the authoritative event
// consumer; only child sessions invoke this callback.
func (s *Session) SetDescendantEventFunc(f func(events.SessionEvent)) {
	s.mu.Lock()
	s.cfg.spawn.descendantEvent = f
	s.mu.Unlock()
	if f != nil && s.delegateController != nil && s.ownsDelegateController {
		s.delegateController.emitDelegateUpdate(s.delegateController.Snapshot())
	}
}

// ObserveCloseDrainPublication is the context-independent observation seam for a
// bounded close publication: it sees the bound the send is made on, so a test can
// assert WHICH deadline caps it instead of measuring how long the send took. Nil
// in production, like ObserveCloseStopJoinBudget and for the same reason -- host
// load can stretch any wall-clock ceiling, but the deadline a pass chose is exact.
var ObserveCloseDrainPublication func(bound context.Context)

// emitCloseDrainWarnings publishes the warnings a delegate close drain settled,
// on the stop context that drain runs under. It is the closing session's whole
// publication surface: a warning is news the owner would otherwise never see --
// a delegate's result was lost -- and it costs one channel send. Nothing else
// the drain settled is published here, because a closing session has no turn to
// run a delivery in.
//
// The bound matters as much as the publication. When the root's authoritative
// consumer is wedged, a send parks until something releases it; without this
// context it would park until the close cascade's own deadline, spending the
// half that teardown needs and leaving collectible isolation lanes on disk. The
// stop bound releases the send early, dropping this one warning and leaving the
// rest of the cascade intact.
func (s *Session) emitCloseDrainWarnings(ctx context.Context, warnings []events.WarningData) {
	if s == nil || s.events == nil {
		return
	}
	// Recorded at the publication rather than at each caller: every bounded
	// close publication ends up here, so this is the one place that can say
	// which deadline actually capped the send.
	if observe := ObserveCloseDrainPublication; observe != nil {
		observe(ctx)
	}
	// The bound travels with the warning, not with the session. Every send this
	// publication causes -- the warning itself, the Notification hook it fires,
	// that hook's own lifecycle events and its output -- is made on the drain's
	// context. Nothing else on the root is: an unrelated tool event landing in the
	// same window keeps the shared close deadline and is still delivered to a
	// consumer that resumes after the stop's half has expired.
	for _, warning := range warnings {
		data, ev, delivered := s.sendEventContext(ctx, warning, s.activeCausalProvenance())
		if !delivered && s.forwardsEventsToParent() {
			// A resident delegate whose unread local channel is full still
			// FORWARDS every event to its parent synchronously, and that parent is
			// the authority this warning had to reach. Counting only the local
			// channel send as delivery made this publisher treat a warning that
			// already arrived as failed: it skipped the Notification hook and
			// retained the warning, and the retry forwarded a SECOND copy to
			// AppWire. A warning that reached the stream by either route is
			// published, so the hook fires here and nothing is retained.
			delivered = true
		}
		if !delivered {
			// The bound expired before the warning reached the stream. The
			// receipt that produced this warning is already abandoned and gone
			// from the store, so no later reconciliation can settle it again:
			// dropping it here is the loss this whole change exists to prevent.
			// Retain it for one retry on the budget left after teardown.
			s.retainUndeliveredCloseWarning(warning)
			continue
		}
		if s.jobManager != nil {
			// The bound, not the ordinary path: this flush runs on the drain's
			// goroutine, synchronous with the warning that just filled the last
			// slot, so on the ordinary path it would wait out the whole cascade.
			s.jobManager.onSessionEventContext(ctx, ev)
		}
		s.fireNotificationHookBounded(ctx, warningHookMessage(data), warningHookTitle(data))
	}
}

// forwardsEventsToParent reports whether this session forwards every event it
// emits to its parent, which is how a resident delegate reaches the authoritative
// stream when its own unread channel cannot take another event.
//
// The callback is read under the session mutex, the same way emitDelegateUpdate
// reads it: installed at spawn, cleared at teardown. A close does not move it.
func (s *Session) forwardsEventsToParent() bool {
	if s == nil {
		return false
	}
	// Read under s.mu like every other read of the field, though it is immutable
	// after spawn; the test seam sets it through SetDescendantEventFunc.
	s.mu.Lock()
	forward := s.descendantEvent
	s.mu.Unlock()
	return forward != nil
}

// retainUndeliveredCloseWarning holds a lost-result warning whose publication
// expired, so retryCloseDrainWarnings can deliver it once on what the cascade
// has left after the worktree removals have run.
//
// It is deliberately NOT a durable record. The warning names a receipt that is
// already gone, so there is nothing to reconcile against later: the only chance
// to report it is this close, and the only budget that can carry it is the
// remainder the removals did not spend.
func (s *Session) retainUndeliveredCloseWarning(warning events.WarningData) {
	for {
		current := s.closeDrainWarnings.Load()
		next := append([]events.WarningData(nil), warning)
		if current != nil {
			next = append(append([]events.WarningData(nil), (*current)...), warning)
		}
		if s.closeDrainWarnings.CompareAndSwap(current, &next) {
			return
		}
	}
}

// retryCloseDrainWarnings publishes the warnings whose first attempt expired, on
// what is left of the close cascade. It runs after the worktree removals, which
// is the same placement as retryCloseFailClosed and for the same reason: the
// budget it spends must be budget the teardown no longer needs.
//
// The retry does NOT redispatch the Notification hook. A hook is a side effect a
// user configured for the first publication, and running it again from the
// close's tail would let a warning's own dispatch fail the shutdown it was
// reported during.
func (s *Session) retryCloseDrainWarnings(ctx context.Context) {
	if observe := s.testOnlyCloseWarningRetry; observe != nil {
		observe(ctx)
	}
	held := s.closeDrainWarnings.Swap(nil)
	if held == nil || len(*held) == 0 {
		return
	}
	for _, warning := range *held {
		_, ev, delivered := s.sendEventContext(ctx, warning, s.activeCausalProvenance())
		if !delivered && s.forwardsEventsToParent() {
			// Same rule as the first attempt: the warning reached the parent's
			// stream, so it is published and must not be dropped here.
			delivered = true
		}
		if !delivered {
			// Nothing is left to spend and no later pass can rebuild the receipt.
			// This is the one case the close cannot report, and it is known and
			// stated rather than silently assumed away: the cascade budget, not
			// this warning, ran out. See the PR body's known-unfixed item.
			continue
		}
		if s.jobManager != nil {
			s.jobManager.onSessionEventContext(ctx, ev)
		}
	}
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
//
// The hook runs SYNCHRONOUSLY on whichever goroutine emitted the warning, and in
// the daemon that goroutine is the input loop shutdown waits for before it closes
// the session. So the base context is the session's own lifetime, not a
// background one: a hook already running when shutdown starts is interrupted by
// the same cancellation instead of holding the shutdown budget for its full
// timeout. A close that has already published its budget supersedes that, and an
// exhausted deadline of either kind declines the hook rather than launching one
// nothing is left to wait for.
func (s *Session) fireNotificationHook(message, title string) {
	ctx := s.notificationHookBaseContext()
	if ctx.Err() != nil {
		return
	}
	s.runNotificationHook(ctx, message, title)
}

// fireNotificationHookBounded is fireNotificationHook for a caller that carries
// its own bound, which stays in force on top of the session's: the delegate
// close drain publishes on the stop context, and a Notification hook launched
// there must not outlive it.
func (s *Session) fireNotificationHookBounded(ctx context.Context, message, title string) {
	base := s.notificationHookBaseContext()
	if base.Err() != nil {
		return
	}
	if ctx == nil {
		ctx = base
	}
	bounded, cancel := context.WithCancel(ctx)
	defer cancel()
	// The hook ends at whichever bound expires first: the caller's, the
	// session's lifetime, or a published close budget.
	stop := context.AfterFunc(base, cancel)
	defer stop()
	if bounded.Err() != nil {
		return
	}
	// Marked so the runner's lifecycle publisher, and the output publication
	// inside runNotificationHook, honour this bound instead of the session's
	// delivery path: the drain is waiting on this hook, so an unresponsive
	// consumer must release it at the stop deadline. Unmarked dispatches -- every
	// ordinary hook -- are unaffected.
	s.runNotificationHook(withPublicationBound(bounded), message, title)
}

// notificationHookBaseContext is the Notification hook's own bound: the
// session's lifetime, superseded by a close budget once one is published. A
// hook already running when shutdown starts is interrupted by the same
// cancellation instead of holding the shutdown budget for its full timeout.
func (s *Session) notificationHookBaseContext() context.Context {
	ctx := s.sessionContext()
	s.closeCtxMu.RLock()
	closeCtx := s.closeCtx
	s.closeCtxMu.RUnlock()
	if closeCtx != nil {
		ctx = closeCtx
	}
	return ctx
}

// warningData is the warning payload an event carries, and whether it carries
// one at all. A nil *WarningData is "no payload": both readers treat it as not a
// warning.
func warningData(data events.EventData) (events.WarningData, bool) {
	switch v := data.(type) {
	case events.WarningData:
		return v, true
	case *events.WarningData:
		if v != nil {
			return *v, true
		}
	}
	return events.WarningData{}, false
}

// warningHookMessage is the warning's message text, or the event's own printing
// when the event is not a warning. It is the Notification hook's "message" field.
func warningHookMessage(data events.EventData) string {
	if v, ok := warningData(data); ok {
		return v.Message
	}
	return fmt.Sprint(data)
}

// warningHookTitle is the warning's own title, empty when it carries none. It is
// the Notification hook's "title" field, so a hook renders the same heading the
// user sees.
func warningHookTitle(data events.EventData) string {
	v, _ := warningData(data)
	return v.Title
}

// notificationHookType is the Claude notification_type evener emits. Evener has
// exactly one user-visible notification kind — a diagnostic warning — so this is
// evener-native rather than Claude's permission_prompt/idle_prompt vocabulary,
// and the runner matches on the value the input carries.
const notificationHookType = "warning"

func (s *Session) runNotificationHook(ctx context.Context, message, title string) {
	if s.hookRunner == nil {
		return
	}
	// The hook's OUTPUT publication is scoped to a caller that supplied a bound.
	// A bound is a publication deadline handed in by a caller that has a stall
	// budget of its own -- the delegate close drain is the only one. Every other
	// dispatcher passes a context that merely tracks the session, and reusing
	// that one as the publication bound turned a canceled session context into a
	// delivery deadline: an ordinary hook finishing as Close cancels the session
	// dropped its user message off a full buffer at once, even though the
	// consumer frees a slot inside the close window the event was entitled to.
	//
	// An unmarked dispatch therefore publishes on NIL, which sendEventContext
	// reads as "the session's own publication path": no caller deadline, so each
	// send observes whatever close budget is current AT THE MOMENT IT SENDS. That
	// is the difference the previous shape missed. Capturing the session lifetime
	// here instead froze the answer at hook-start: a hook already running when
	// Close canceled the lifetime carried that cancellation into a buffer the
	// consumer could still drain inside the published window, and the message was
	// dropped with the window wide open.
	// One assignment, because publicationBound already answers both questions:
	// it returns the bound when the dispatcher marked one, and nil -- the
	// session's own publication path -- when none exists. The nil is the Medium 2
	// fix and is load-bearing: an unmarked dispatch must not inherit a captured
	// lifetime as its delivery deadline.
	output, _ := publicationBound(ctx)
	input := s.hookInput(plugin.HookNotification)
	input.Message = message
	input.Reason = message
	input.NotificationType = notificationHookType
	input.Title = title
	result := s.hookRunner.RunNotification(s.apiLogContext(ctx), input)
	for _, m := range result.ModelContext {
		if err := s.deliverHookContext(m); err != nil {
			s.emitDiagnosticWarningContext(output, events.WarningData{Message: fmt.Sprintf("steering admission failed: %v", err)})
		}
	}
	for _, m := range result.UserMessages {
		s.deliverHookUserMessageContext(output, m)
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
	// A subagent's hook reports the child's own identity, never the parent's.
	// Root sessions leave both empty, as Claude does.
	parentSessionID := s.cfg.spawn.parentSessionID
	agentType := s.cfg.spawn.agentType
	s.mu.Unlock()
	if effort != "" {
		input.Effort = &hooks.Effort{Level: effort}
	}
	if parentSessionID != "" {
		input.AgentID = s.id
		input.AgentType = agentType
	}
	// PermissionMode is intentionally left unset: evener has no permission-mode field
	// on Session today. Do NOT fabricate a value the hook would act on.
	return input
}

// toolHookInput starts an event's hook input with the fields every tool event
// carries: the Claude tool name, the call id, and the parsed tool_input. A call
// whose raw arguments were refused keeps its unvalidated bytes out of
// tool_input, exactly as the event it describes never had them applied. Each
// caller then sets only its own event-specific fields (tool_response, error,
// duration_ms, and so on).
func (s *Session) toolHookInput(event plugin.HookEvent, call llm.ToolCallData, rawRejected bool) hooks.Input {
	hi := s.hookInput(event)
	hi.ToolName = toolname.EvenerToClaude(call.Name)
	hi.ToolUseID = call.ID
	if !rawRejected && len(call.Arguments) > 0 {
		// Decode through the registered tool's own decoder, exactly as dispatch
		// does: a tool that preserves exact integers (uint64 ids, int64 bounds)
		// must hand the hook the same values it hands the tool, not the float64
		// a plain json.Unmarshal produces.
		registered := s.reg.Get(call.Name)
		if registered == nil {
			registered = &tool.RegisteredTool{}
		}
		hi.UnmarshalToolArguments = registered.UnmarshalArguments
		_ = registered.UnmarshalArguments(call.Arguments, &hi.ToolInput)
	}
	return hi
}

// settleFailedRound persists what a terminally failed round produced, so the
// work the provider ate survives into the next round instead of being thrown
// away: up to two model-visible turns, the salvaged assistant draft followed by
// the failure steering that explains it. See the spec's "Component 3:
// partial-preserving settlement".
//
// It runs on the turn goroutine, the only goroutine that appends to the round
// recorder's groups, so reading them here needs no further synchronization.
func (s *Session) settleFailedRound(terminalErr error) {
	rec := s.roundSalvageRecorder()
	kind := classifySettlement(rec, terminalErr)
	if kind == settleNone {
		return
	}
	// The group's model/provider are read out here, while BestSalvage's interior
	// pointer is known live, so nothing downstream holds a pointer into the
	// recorder's groups.
	var salvaged, model, provider string
	if kind == settleSalvageAndSteering {
		if partial, from := rec.BestSalvage(); from != nil {
			salvaged, model, provider = salvageText(partial), from.Model, from.Provider
		}
	}
	// Composed before anything is persisted: a round with nothing a template can
	// honestly describe settles nothing at all, rather than leaving a salvaged
	// draft standing with no explanation of where it came from.
	steering := composeFailureSteering(rec, terminalErr, len(salvaged), s.resultToolName())
	if steering == "" {
		return
	}
	if salvaged != "" && !s.persistAndEmitSalvage(salvaged, model, provider) {
		return
	}
	s.appendSteeringTurn(steering, events.SteeringKindProviderFailure)
}

// persistAndEmitSalvage records the salvaged draft and replays it on the event
// bus, reporting whether it reached the transcript. Both settlement paths gate
// their steering turn on it: a draft that did not reach the transcript is a
// draft the steering's "the content above" would point at nothing, so the pair
// settles together or not at all.
func (s *Session) persistAndEmitSalvage(salvaged, model, provider string) bool {
	if err := s.persistSalvagedTurn(salvaged, model, provider); err != nil {
		return false
	}
	s.emitSalvagedTurn(salvaged, model)
	return true
}

// interruptSalvageSteering explains a salvaged draft that a user interrupt, not
// a provider failure, cut short. It is deliberately one line and deliberately
// silent about causes and next steps: the user stopped the round, so claiming
// the provider broke would be a lie, and pushing "continue" would override the
// very decision the interrupt expressed.
const interruptSalvageSteering = "This response was interrupted; the content above was produced before the interruption and was not delivered."

// settleInterruptedRound preserves what the round had already produced when a
// user interrupt cut it short: the salvaged draft plus the one-line interrupt
// steering that says where it came from. See the spec's "Component 3:
// partial-preserving settlement", interrupt carve-out.
//
// It runs on the turn goroutine — handleModelError's cancellation branch, which
// is also the only goroutine that appends to the round recorder's groups — so
// reading them here needs no further synchronization. The interrupt itself
// arrives asynchronously as a context cancellation; the turn goroutine is where
// it is OBSERVED, and that is the read this settlement does.
//
// A round with nothing salvaged persists nothing, leaving the round loop's own
// interrupt marker as the sole record, exactly as before.
func (s *Session) settleInterruptedRound() {
	s.mu.Lock()
	closed := s.closingOrClosedLocked()
	s.mu.Unlock()
	// A session being torn down has no next round to read the draft, and the
	// round loop skips its own interrupt marker on the same grounds.
	if closed {
		return
	}
	// The group's model/provider are read out while BestSalvage's interior
	// pointer is known live, so nothing downstream holds a pointer into the
	// recorder's groups.
	partial, from := s.roundSalvageRecorder().BestSalvage()
	if from == nil {
		return
	}
	// The spec's content-filter exclusion is unconditional, and the interrupt
	// carve-out spares a round only its FAILURE STEERING. Persisting the content
	// that tripped the filter would pin it in a compaction-atomic, recent-tail-
	// protected turn and defeat the ForceCompact recovery that exists to remove
	// it — which the next turn re-arms, so an ended turn defers that recovery
	// rather than retiring it. Keyed on the salvage-producing group, exactly as
	// classifySettlement keys it.
	if contentFilterKilledGroup(from) {
		return
	}
	salvaged, model, provider := salvageText(partial), from.Model, from.Provider
	if salvaged == "" {
		return
	}
	if !s.persistAndEmitSalvage(salvaged, model, provider) {
		return
	}
	s.appendSteeringTurn(interruptSalvageSteering, events.SteeringKindInterruptedSalvage)
}

// persistSalvagedTurn records the round's best partial as a normal assistant
// turn stamped with model/provider provenance ONLY.
//
// It must never carry Responses-continuation metadata: continuation anchoring
// scans backward to the most recent assistant turn, and IDs from a response the
// provider never finalized would poison every subsequent round with
// previous_response_not_found. The zero ModelAttemptMetadata is what keeps every
// one of those fields empty.
//
// On success it latches markSalvagedTurnPersisted, the signal a delegating
// parent reads (hasSalvageFromFinalRound) after this session fails as a child
// to decide whether its result should point at resuming the draft.
func (s *Session) persistSalvagedTurn(salvaged, model, provider string) error {
	if err := s.appendAssistantTurn(llm.Response{
		Message:  llm.Assistant(salvaged),
		Model:    model,
		Provider: provider,
	}, ModelAttemptMetadata{}); err != nil {
		return err
	}
	s.markSalvagedTurnPersisted()
	return nil
}

// emitSalvagedTurn replays the salvaged draft on the event bus so a watching
// client renders what the transcript stores. The reset is what reconciles them:
// the screen holds whichever partial streamed LAST, while settlement persists
// the LARGEST. The sequence mirrors a normal assistant text round
// (consumeModelStream's start/delta, emitAssistantResponse's end).
func (s *Session) emitSalvagedTurn(salvaged, model string) {
	s.emit(events.AssistantTextResetData{})
	s.emit(events.AssistantTextStartData{Model: model})
	s.emit(events.AssistantTextDeltaData{Delta: salvaged})
	s.emit(events.AssistantTextEndData{Text: salvaged, Model: model})
}
