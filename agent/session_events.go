package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// emitContext is emit on a caller's publication bound. A nil bound is the
// session's ordinary path, unchanged.
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

// emitWithProvenanceContext is emitWithProvenance on a caller's bound: the send,
// the observer flush that follows it, and any Notification hook the event
// dispatches all run on that bound rather than on the session's delivery path.
//
// The bound is passed to the observer flush rather than dropped there because
// the flush is SYNCHRONOUS with the send: a bounded publication can take the
// last slot in a full stream, and the flush that follows would then wait out the
// whole close cascade even though the stop deadline that released this send
// already fired. The budget teardown needs would be spent immediately after a
// correctly bounded publication.
func (s *Session) emitWithProvenanceContext(ctx context.Context, data events.EventData, p *provenance.Causal) {
	if s == nil || s.events == nil {
		return
	}
	data, ev := s.sendEventOnContext(ctx, data, p)
	if s.jobManager != nil {
		s.jobManager.onSessionEventContext(ctx, ev)
	}
	if events.KindOf(data) == events.EventWarning {
		s.fireNotificationHookBounded(ctx, ctx, warningHookMessage(data), warningHookTitle(data))
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
	s.recordHookCompletion(data)
}

// emitHookCompletedBounded is emitHookCompleted on a caller's publication bound.
// The record still happens: history must show a hook that ran, whatever the
// stream could take. Only the PUBLICATIONS take the bound, so a stalled consumer
// releases the close drain that is waiting on this hook instead of holding the
// teardown budget open.
func (s *Session) emitHookCompletedBounded(bound context.Context, data events.HookEndData) {
	s.emitContext(bound, data)
	s.recordHookCompletionOn(bound, data)
}

// recordHookCompletion writes the completion turn shared by both publication
// paths above.
func (s *Session) recordHookCompletion(data events.HookEndData) {
	s.recordHookCompletionTurn(noBound, func(w events.WarningData) { s.emit(w) }, data)
}

// recordHookCompletionOn writes the completion turn, reporting a recording
// failure on bound through the DIAGNOSTIC path.
//
// The completion is being recorded on a close publication bound because a
// Notification hook is waiting on it. A write failure is an EventWarning, and
// the ordinary warning path would fire that same Notification hook again -- on
// the very send that is failing, for as long as the writer refuses, spending the
// close budget one dispatch at a time. emitDiagnosticWarningContext publishes
// the failure without dispatching a hook, so the failure still reaches every
// reader and cannot re-enter the dispatcher that caused it.
func (s *Session) recordHookCompletionOn(bound context.Context, data events.HookEndData) {
	s.recordHookCompletionTurn(bound, func(w events.WarningData) {
		s.emitDiagnosticWarningContext(bound, w)
	}, data)
}

// recordHookCompletionTurn builds the completion turn and records it, reporting
// a write failure through warn. Both publication paths differ only in that sink.
func (s *Session) recordHookCompletionTurn(bound context.Context, warn func(events.WarningData), data events.HookEndData) {
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
	s.recordTurnOn(bound, warn, turn, turn)
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

// emitDiagnosticWarningContext is emitDiagnosticWarning on a caller's bound. A
// nil bound is the session's ordinary publication path, unchanged: no caller
// deadline, so the send observes whatever close budget is current when it sends.
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

// noBound is the explicit "no caller supplied a stall budget" value for the
// publication path. It is a real context rather than nil so a caller that has no
// bound and a caller that forgot to pass one are distinguishable at the seam
// that decides whether a send may be dropped.
var noBound context.Context = unboundedContext{}

type unboundedContext struct{}

func (unboundedContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (unboundedContext) Done() <-chan struct{}       { return nil }
func (unboundedContext) Err() error                  { return nil }
func (unboundedContext) Value(key any) any           { return nil }

// suppliedCallerBound reports whether bound is anything other than one of the
// two encodings of "no bound": a nil context and noBound.
func suppliedCallerBound(bound context.Context) bool {
	return bound != nil && bound != noBound
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

// withPublicationBound marks ctx as a dispatch whose lifecycle and output
// publications are made on publication. The two are the same context for an
// ordinary bounded dispatch. They differ for a close-time hook that must still
// RUN after its publication cutoff expired: the hook's command gets the live
// cascade, so it is not declined over a deadline the failure it reports may
// itself have caused, while the events it publishes -- HOOK_START, HOOK_END, its
// output -- stay on the cutoff, so an unresponsive consumer releases them
// without holding the close's remaining budget.
func withPublicationBound(ctx, publication context.Context) context.Context {
	return context.WithValue(ctx, publicationBoundKey{}, publication)
}

// publicationBound reports the bound a lifecycle publication must use, and
// whether the dispatcher supplied one at all. A mark carrying no usable context
// is "no bound", so an unmarked dispatch keeps the ordinary path.
func publicationBound(ctx context.Context) (context.Context, bool) {
	if !suppliedCallerBound(ctx) {
		return nil, false
	}
	bound, _ := ctx.Value(publicationBoundKey{}).(context.Context)
	if !suppliedCallerBound(bound) {
		return nil, false
	}
	return bound, true
}

// installRunnerEventCallback wires the runner's lifecycle events onto the
// session's stream. It is one function rather than a closure in initPlugins so
// the policy below is a single thing a test can exercise.
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
	runner.SetEventCallbackContext(s.publishHookLifecycle)
}

// publishHookLifecycle is the policy described above, as one method: a dispatch
// that marked a publication bound publishes on it, and every other dispatch
// takes the session's ordinary delivery path.
func (s *Session) publishHookLifecycle(ctx context.Context, kind events.EventKind, data events.EventData) {
	end, isEnd := data.(events.HookEndData)
	isEnd = isEnd && kind == events.EventHookEnd
	bounded, ok := publicationBound(ctx)
	// Recorded at the decision rather than at the send: the send's own outcome is
	// a select among ready cases, so a test that watched it would agree with a
	// wrong policy some of the time. This reports which path the policy chose,
	// which is the thing that must not drift.
	if observe := ObserveCloseDrainPublicationRoute; observe != nil {
		observe(ok)
	}
	if !ok {
		// The unbounded route. A completed hook additionally goes to disk (kata
		// qm9y): a hook exit that exists only as a live event leaves the
		// transcript's hook-exit toggles governing nothing for any session a
		// reader comes back to.
		if isEnd {
			s.emitHookCompleted(end)
			return
		}
		s.emit(data)
		return
	}
	if isEnd {
		// The marked path is the close drain's, and it is waiting on this
		// publication. Recording the completion goes through the same bound so the
		// drain is not held open by a transcript write that has its own budget --
		// but the RECORD still happens: history must show a hook that ran.
		s.emitHookCompletedBounded(bounded, end)
		return
	}
	s.emitContext(bounded, data)
}

// ObserveCloseDrainPublicationRoute is the context-independent observation seam
// for the lifecycle publication policy: it reports whether a hook's lifecycle
// event was published on a caller's bound. Nil in production, like
// ObserveCloseDrainPublication and for the same reason -- the route chosen is
// exact, while how long a send took is not.
var ObserveCloseDrainPublicationRoute func(bounded bool)

// ObserveCloseDrainRetryBudget is the context-independent observation seam for
// the post-disposal warning retry: it reports whether the retry was handed a live
// budget. Nil in production, like the observers above and for the same reason --
// whether a send landed depends on the buffer, while the budget the retry received
// is exact.
var ObserveCloseDrainRetryBudget func(live bool)

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
		// A published budget SUPERSEDES the lifetime as the wait's own ceiling,
		// but it never replaces the CALLER's cancellation: a caller that supplied
		// a bound -- the close drain, a hook's lifecycle send, a watch
		// invalidation -- must be able to release its own send, or a stalled
		// consumer holds a caller whose budget already expired. Replacing the
		// caller's context with the close budget is what let a bounded
		// publication spend the whole cascade. So the caller's Done stays armed
		// on every wait below, and the budget only takes over the LIFETIME's
		// seat.
		//
		// A budget published while the send is already parked is handled on the
		// arm itself: close publishes and then cancels the session context a few
		// statements later, so an already-parked send sees the lifetime end with
		// a fresh budget in hand and must come back to that budget rather than
		// drop an event it still has its whole window to deliver.
		// callerDone is armed on EVERY wait below. budgetDone is the published
		// close budget, nil when none is published; lifetimeDone is the session's
		// own lifetime, which stands in for a budget until one is published. Both
		// are nil channels when not in force, and a nil channel never fires.
		callerDone := ctx.Done()
		var budgetDone, lifetimeDone <-chan struct{}
		if closeCtx != nil {
			budgetDone = closeCtx.Done()
		} else {
			lifetimeDone = s.sessionContext().Done()
		}
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
				case <-callerDone:
				case <-budgetDone:
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
						case <-callerDone:
						case <-published.Done():
						case <-closeSignal:
						}
					}
				case <-closeSignal:
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
//
// hook decides WHEN the warning's configured Notification hook is dispatched,
// which is not the same question as whether the channel took the event. A
// lost-result warning is news the drain recovered, so a hook the cutoff declined
// is an accepted limit (hookOnPublication). An incomplete-close warning is a
// failure the session always reported, and the path it used before this
// publisher dispatched its hook independently of channel delivery, so that
// notification must not become a consequence of a free slot (hookOnAttempt).
//
// On the hookOnAttempt path the hook runs on hookCtx, NOT on ctx: the warning's
// send is capped at the cascade's publication cutoff, and once that has expired
// the send cannot land -- but the cascade itself is still live, and the previous
// close path dispatched this notification there. Running the hook on the expired
// publication cutoff would skip it for exactly the failure it reports. It still
// runs at most once: the post-removal retry publishes the channel event only,
// and nothing redispatches the hook.
//
// That dispatch runs ASYNCHRONOUSLY, on a goroutine this session joins in
// waitCloseDrainHooks. A matching hook is a user's command and may run for its
// whole configured timeout; running it inline on this goroutine held the caller
// (warnIncompleteCloseBeforeDisposal) until the cascade budget expired, so every
// worktree removal that followed started on a spent budget and left collectible
// lanes on disk even though the consumer was healthy. Returning here lets those
// removals proceed concurrently on the live cascade. releasePublish, when
// non-nil, is called once the hook's goroutine has finished, so the caller's
// publication context cannot be torn down under a send still using it.
func (s *Session) emitCloseDrainWarnings(ctx context.Context, hookCtx context.Context, warnings []events.WarningData, hook closeWarningHook) {
	s.emitCloseDrainWarningsReleasing(ctx, hookCtx, warnings, hook, nil)
}

// emitCloseDrainWarningsReleasing is emitCloseDrainWarnings with releasePublish
// called once the asynchronously dispatched hook (if any) has finished. It owns
// that release so a caller's publication context outlives exactly the work
// published on it, and so it is released on every path -- including one where
// the session publishes nothing at all.
func (s *Session) emitCloseDrainWarningsReleasing(ctx context.Context, hookCtx context.Context, warnings []events.WarningData, hook closeWarningHook, releasePublish func()) {
	if s == nil || s.events == nil {
		if releasePublish != nil {
			releasePublish()
		}
		return
	}
	// Recorded at the publication rather than at each caller: every bounded
	// close publication ends up here, so this is the one place that can say
	// which deadline actually capped the send.
	if observe := ObserveCloseDrainPublication; observe != nil {
		observe(ctx)
	}
	// Deferred until the loop is over so one tracked goroutine can join every
	// hook this publication dispatches and then release the caller's context:
	// releasing here would cancel it under a hook this call had already started.
	var hooks []func()
	// The bound travels with the warning, not with the session. Every send this
	// publication causes -- the warning itself, the Notification hook it fires,
	// that hook's own lifecycle events and its output -- is made on the drain's
	// context. Nothing else on the root is.
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
			if hook == hookOnPublication {
				continue
			}
		} else if s.jobManager != nil {
			// The bound, not the ordinary path: this flush runs on the drain's
			// goroutine, synchronous with the warning that just filled the last
			// slot, so on the ordinary path it would wait out the whole cascade.
			s.jobManager.onSessionEventContext(ctx, ev)
		}
		message, title := warningHookMessage(data), warningHookTitle(data)
		dispatchCtx := ctx
		if hook == hookOnAttempt {
			dispatchCtx = hookCtx
		}
		// A declined dispatch on the hookOnPublication path is the accepted limit
		// stated above. On the hookOnAttempt path the caller has already given the
		// hook the live cascade, so a decline there means the cascade itself is
		// over -- there is no budget left to run anything on.
		// The PUBLICATION is always ctx: the hook's lifecycle and output sends stay
		// on the cascade's cutoff even when its command runs on the live cascade.
		if hook == hookOnAttempt {
			// Started, not awaited: the command is the user's and may run for its
			// whole timeout, which is exactly the budget the removals that follow
			// this publication need.
			hooks = append(hooks, func() {
				s.fireNotificationHookBounded(dispatchCtx, ctx, message, title)
			})
			continue
		}
		s.fireNotificationHookBounded(dispatchCtx, ctx, message, title)
	}
	if len(hooks) == 0 {
		if releasePublish != nil {
			releasePublish()
		}
		return
	}
	// One goroutine owns every hook this call dispatched, so the publication
	// context is released exactly once and only after the last of them stops
	// publishing on it. It is tracked by closeDrainHooksWG, so a close that joins
	// that group also waits out the release.
	s.closeDrainHooksWG.Go(func() {
		defer func() {
			if releasePublish != nil {
				releasePublish()
			}
		}()
		// In publication order, as the synchronous loop dispatched them: a hook
		// that has to finish before the next one starts still does.
		for _, fire := range hooks {
			fire()
		}
	})
}

// closeWarningHook decides when a close publication dispatches the warning's
// Notification hook. It is a named choice rather than a bare bool because the
// two answers are different obligations, and a caller must state which one it
// means.
type closeWarningHook int

const (
	// hookOnPublication dispatches the hook only for a warning that reached the
	// stream. A hook the cutoff declined is a stated limit for news the drain
	// recovered.
	hookOnPublication closeWarningHook = iota
	// hookOnAttempt dispatches the hook even when the send expired. It preserves
	// the guarantee the ordinary emit path had, for a warning whose notification
	// must not depend on a free slot.
	hookOnAttempt
)

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
// what is left of the close cascade. It runs after the worktree removals, because
// the budget it spends must be budget the teardown no longer needs.
//
// The retry does NOT redispatch the Notification hook. A hook is a side effect a
// user configured for the first publication, and running it again from the
// close's tail would let a warning's own dispatch fail the shutdown it was
// reported during.
func (s *Session) retryCloseDrainWarnings(ctx context.Context) {
	held := s.closeDrainWarnings.Swap(nil)
	if held == nil || len(*held) == 0 {
		return
	}
	// The budget the retry actually received, recorded at the entry rather than
	// inferred from whether a warning landed: a send can succeed on a buffer with
	// room no matter what context it was made on, so the context's own state is the
	// only exact answer to whether the retry had budget to spend.
	if observe := ObserveCloseDrainRetryBudget; observe != nil {
		observe(ctx.Err() == nil)
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
			// this warning, ran out.
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
// there must not outlive it. It reports whether the dispatch STARTED, so the
// caller can hold the dispatch for the close-time retry when the cutoff had
// already expired.
//
// dispatch and publication are the same context for every caller but one. They
// differ for the incomplete-close hook, which must still RUN when the
// publication cutoff the failure may itself have caused has expired: its command
// gets the live cascade, while the events it publishes -- its lifecycle and its
// output -- stay on publication. Marking only the dispatch context would put
// those sends on the cascade, and the runner publishes HOOK_START BEFORE it
// invokes the command, so a stalled consumer would park the drain on the
// cascade's whole remaining budget and leave collectible lanes on disk.
func (s *Session) fireNotificationHookBounded(dispatch, publication context.Context, message, title string) bool {
	base := s.notificationHookBaseContext()
	if base.Err() != nil {
		return false
	}
	if dispatch == nil {
		dispatch = base
	}
	if publication == nil {
		publication = dispatch
	}
	bounded, cancel := context.WithCancel(dispatch)
	defer cancel()
	// The hook ends at whichever bound expires first: the dispatcher's, the
	// session's lifetime, or a published close budget.
	stop := context.AfterFunc(base, cancel)
	defer stop()
	if bounded.Err() != nil {
		return false
	}
	// Marked so the runner's lifecycle publisher, and the output publication
	// inside runNotificationHook, honour publication instead of the session's
	// delivery path: the drain is waiting on this hook, so an unresponsive
	// consumer must release it at the cutoff rather than at the cascade's end.
	// Unmarked dispatches -- every ordinary hook -- are unaffected.
	s.runNotificationHook(withPublicationBound(bounded, publication), message, title)
	return true
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
	// send observes whatever close budget is current AT THE MOMENT IT SENDS.
	// One assignment, because publicationBound already answers both questions:
	// it returns the bound when the dispatcher marked one, and nil -- the
	// session's own publication path -- when none exists. The nil is load-bearing:
	// an unmarked dispatch must not inherit a captured lifetime as its delivery
	// deadline.
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
