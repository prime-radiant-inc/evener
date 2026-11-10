package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

var errBareTextWithoutResultTool = errors.New("model returned bare text without calling result tool")
var errEmptyResponseExhausted = errors.New("model returned empty response")
var errStreamUnavailable = errors.New("stream unavailable")

type sessionLifecycleFaultsKey struct{}

func sessionLifecycleFault(ctx context.Context, point string) error {
	faults, _ := ctx.Value(sessionLifecycleFaultsKey{}).(map[string]error)
	return faults[point]
}

type emptyResponseExhaustedError struct {
	retries int
}

func (e *emptyResponseExhaustedError) Error() string {
	return fmt.Sprintf("model returned empty response after %d retries", e.retries)
}

func (e *emptyResponseExhaustedError) Is(target error) bool {
	return target == errEmptyResponseExhausted
}

type bareTextWithoutResultToolError struct {
	toolName string
	retries  int
}

func (e *bareTextWithoutResultToolError) Error() string {
	return fmt.Sprintf("model returned bare text without calling %s after %d retries", e.toolName, e.retries)
}

func (e *bareTextWithoutResultToolError) Is(target error) bool {
	return target == errBareTextWithoutResultTool
}

// Retry budgets for degenerate model responses within a single input turn.
const (
	maxEmptyRetries        = 3
	maxTotalEmptyResponses = 8 // prevent repeated burst-and-recover spirals
	maxBareTextRetries     = 3
	// maxServerContinuations bounds one input turn's run of consecutive
	// pause_turn responses. A pause is a legitimate protocol continuation, so it
	// still refunds its tool round (see TestSession_PauseTurn_*), but a provider
	// that returns them forever must not keep a single turn alive indefinitely:
	// the chain is a distinct budget from the tool-round cap.
	maxServerContinuations = 8
)

// maxHookStopReasonRunes bounds a hook-supplied stop reason at the dispatch
// site. It becomes a user-visible warning AND a steering turn, so a shell
// stderr that happens to be one enormous line must not fill the transcript.
// The hooks package already reduces a reason to its first line and to this
// same width (maxHookFailureDetail); this is the second, independent bound at
// the place the text is actually delivered.
const maxHookStopReasonRunes = 160

// retryTracker counts the degenerate (empty / bare-text) model responses seen so
// far in one input turn. consecutiveEmpty and consecutiveBareText reset once the
// model produces tool calls; totalEmpty bounds burst-and-recover spirals across
// the whole turn.
type retryTracker struct {
	consecutiveEmpty    int
	totalEmpty          int
	consecutiveBareText int
}

// recordAbandonedEnvironmentLocked remembers an environment the swap has just
// installed over, when the session can no longer reach it: not the environment
// just installed, not the one an enter parked (worktreeRestoreEnv, which close
// retains separately), and not the live parent's own environment
// (parentSharedEnv) — a session on its parent's own environment never records
// it as one of its own abandoned clones, structurally rather than as a
// consequence of enter always parking it first. The clone between two enters
// is the case that matters otherwise — a switch does not re-park, so the
// environment the session came from is dropped from every session reference
// while a child spawned in it still holds the object and can mint a scratch
// there afterwards.
//
// The caller holds s.mu and has already run the swap's record, so
// worktreeRestoreEnv is the parked environment this swap decided on. An
// environment is recorded once: a clone is only ever installed once, and the
// scan keeps that true by construction rather than by trust, so the slice is
// bounded by the number of worktree switches a session actually made.
func (s *Session) recordAbandonedEnvironmentLocked(prior, next *execenv.LocalExecutionEnvironment) {
	if prior == nil || prior == next || prior == s.worktreeRestoreEnv || sameEnvironment(prior, s.parentSharedEnv) {
		return
	}
	if slices.Contains(s.abandonedEnvs, prior) {
		return
	}
	s.abandonedEnvs = append(s.abandonedEnvs, prior)
}

// disposeAbandonedEnvironmentScratch removes the scratch every environment this
// session swapped away from still owns. It never runs Cleanup, so no process
// table is touched.
//
// close calls it after the current environment's Cleanup: an abandoned
// environment shares the current clone's process table, so its processes are
// already reaped and running Cleanup on it would reap that table a second
// time. What is left on it is what a shared child minted after the session
// moved on, which nothing else will ever remove. teardownChildSession also
// calls it: a session whose environment is its live parent's own never runs
// cleanupEnv at all, so this is the only place that ever drains a worktree
// clone such a child built for itself and then swapped away from.
func (s *Session) disposeAbandonedEnvironmentScratch() {
	s.mu.Lock()
	abandoned := s.abandonedEnvs
	s.abandonedEnvs = nil
	s.mu.Unlock()
	for _, env := range abandoned {
		releaseOwnedChildEnvironment(env)
	}
}

// disposeParkedWorktreeEnvironmentScratch removes every scratch the environment
// parked by a worktree enter (worktreeRestoreEnv) still owns. Only close calls
// it, after the current environment's Cleanup: the parked environment shares
// that process table, so its processes are already reaped and running Cleanup
// on it would reap the table a second time.
func (s *Session) disposeParkedWorktreeEnvironmentScratch() {
	s.mu.Lock()
	parked := s.worktreeRestoreEnv
	s.mu.Unlock()
	if parked != nil {
		_ = parked.EndSessionScratch()
	}
}

// endOwnedCurrentScratch ends the current environment's scratch when this
// session owns that environment (EndSessionScratch). A child still holding its
// live parent's own environment owns none of it; the parent's close ends it.
func (s *Session) endOwnedCurrentScratch() {
	s.mu.Lock()
	current, parentShared := s.env, s.parentSharedEnv
	s.mu.Unlock()
	local, ok := current.(*execenv.LocalExecutionEnvironment)
	if !ok || sameEnvironment(current, parentShared) {
		return
	}
	if err := local.EndSessionScratch(); err != nil {
		s.emit(events.WarningData{Message: "session scratch removal incomplete: " + err.Error()})
	}
}

// discardRestoredCandidate tears down a restore candidate nothing ever adopted.
// The candidate's own environmentOwnedAtTeardown says whether its execution
// environment is one built FOR it (a working-dir re-root and/or a per-delegate
// box) rather than the parent's own: prepareSubagentEnvironment returns the
// parent's environment untouched when the delegate needs neither, and a shared
// environment belongs to the live parent still working in it. It is the same
// distinction close() makes before retaining a child's scratch, read here so an
// aborted candidate never deletes a scratch dir out from under its parent.
func (s *Session) discardRestoredCandidate() {
	s.closeOnce.Do(func() {
		s.responseSideEffectsMu.Lock()
		s.mu.Lock()
		s.suppressSessionEndLocked()
		s.closing = true
		s.state = SessionClosed
		subs := s.subagents.drainForClose()
		s.mu.Unlock()
		s.responseSideEffectsMu.Unlock()

		if s.cancelFunc != nil {
			s.cancelFunc()
		}
		s.stopLaneResidueSweepTimer()
		s.stopLaneReLockRetryTimer()

		if s.jobManager != nil && s.jobManager.store != nil {
			_ = s.jobManager.store.Close()
		}
		for _, sub := range subs {
			sub.sess.discardRestoredCandidate()
		}
		if s.ownsArtifactStore && s.artifactStore != nil {
			_ = s.artifactStore.Close()
		}
		_ = s.closeOwnedDelegateStore()
		// A discarded candidate is an ordinary end for its scratch: a named one
		// is kept for the session's next restore, a disposable one removed.
		env := s.environmentOwnedAtTeardown()
		endEnvironmentScratch(env)
		if s.mcpMgr != nil {
			s.mcpMgr.Close()
		}
		_ = s.closeAttachedTranscript()
		s.mu.Lock()
		s.state = SessionClosed
		s.mu.Unlock()
		s.toolEventsWG.Wait()
		s.sendersWG.Wait()
		s.releaseAPILogRoute()
		s.eventsMu.Lock()
		s.eventsClosed = true
		close(s.events)
		s.eventsMu.Unlock()
	})
}

// EntryKind classifies how an input enters the drain loop: a user-typed turn
// (EntryUserInput) or a system-framed continuation injected by the goal engine
// (EntryContinuation). It is exported because it crosses the go.work module
// boundary into the root module (server/cmd) on the InputMessage carried by the
// serve loop.
type EntryKind int

const (
	// EntryUserInput is a turn carrying input typed by the user. It is the zero
	// value so an unset Kind defaults to user input.
	EntryUserInput EntryKind = iota
	// EntryContinuation is a system-framed goal continuation turn.
	EntryContinuation
	// EntryNotification is a system-framed turn that drains pending job
	// notifications and surfaces them to the model as a steering reminder. An empty
	// queue makes it a no-op (no model request).
	EntryNotification
	// EntryDelegateAttention is a controller-owned stable delegate generation
	// whose exact model-bound input is already durable in the receiver transcript.
	EntryDelegateAttention
	// EntryRuntime is a daemon-authored recovery turn. It records no user turn:
	// the steering queued for it (a delegate's missing-report reminder) is the
	// model's whole prompt, so recovery cannot masquerade as caller instruction
	// in history, events, hooks, or compaction (#3734).
	EntryRuntime
	// entryKindCount is the number of EntryKind values. Keep it last: the
	// turn-opening audit (entrykind_audit_test.go) iterates up to it, so a kind
	// added above this line must declare how its turn acquires an identity
	// before the suite goes green.
	entryKindCount
)

// roundContentClass classifies a model round's assistant content. NoContent marks
// a round with no text and no tool calls (a model glitch that triggers the
// empty-retry path). HasPhase marks any content part carrying OpenAI Responses
// phase metadata (e.g. "final_answer"), which must be preserved in history even
// when otherwise empty so the model sees its own phase annotation and can
// course-correct. SkipHistory is a no-content round with no phase metadata — truly
// nothing to append.
type roundContentClass struct {
	NoContent   bool
	HasPhase    bool
	SkipHistory bool
}

// classifyRoundContent derives the round-content classification from the assistant
// text, the tool-call count, and the response content parts. It is pure: no
// locking, I/O, or mutation.
func classifyRoundContent(txt string, callCount int, content []llm.ContentPart) roundContentClass {
	noContent := strings.TrimSpace(txt) == "" && callCount == 0
	hasPhase := false
	for _, p := range content {
		if p.Phase != "" {
			hasPhase = true
			break
		}
	}
	return roundContentClass{
		NoContent:   noContent,
		HasPhase:    hasPhase,
		SkipHistory: noContent && !hasPhase,
	}
}

// noCallsRoute selects how processOneInput handles a round that produced no tool
// calls.
type noCallsRoute int

const (
	// finishIdle ends the turn idle without a retry: a bare-text (non-empty)
	// response to a notification turn is an acknowledgement, not a glitch, and no
	// user awaits a reply.
	finishIdle noCallsRoute = iota
	// runNoToolCalls routes through the empty/bare-text retry budget.
	runNoToolCalls
)

// routeNoToolCalls lets notification acknowledgements finish idle. After a
// terminal communicate, notification silence must not resurrect the run (#329).
func routeNoToolCalls(kind EntryKind, noContent bool, afterTerminalCommunicate bool) noCallsRoute {
	if kind == EntryNotification && (!noContent || afterTerminalCommunicate) {
		return finishIdle
	}
	return runNoToolCalls
}

// drainInputs is the snapshot the drain loop feeds selectDrainNextAction after a
// completed (non-error) turn: the kind of the turn that just ran, whether a goal
// continuation is already deferred, the popped queued message
// (its text plus image and selection counts), whether notification work is pending, and
// whether the turn just rested SessionAwaiting (spec §5.3's drain-ladder gate).
type drainInputs struct {
	RanKind              EntryKind
	HaveDeferredCont     bool
	QueuedText           string
	QueuedImages         int
	QueuedSkills         int
	QueuedCommands       int
	NotificationsPending bool
	Awaiting             bool
	// QueuedCarrier reports that the queued entry is the steering carrier
	// claimSteeringCarrierInput synthesized for user steering the turn that
	// just ran left undelivered -- a queue drained as steering after the
	// turn's last model call was already in flight. It carries no content of
	// its own and runs as queued input.
	QueuedCarrier bool
	// HookStoppedInput reports that a hook's continue:false ended the whole
	// input, so no autonomous turn (goal continuation, deferred continuation)
	// may run behind it.
	HookStoppedInput bool
	// SteeringParked reports that pending user steering is parked after a
	// failed attempt (Session.steeringParked): a carrier claim this ladder
	// could not persist, most often. No autonomous turn runs behind it -- a
	// notification or continuation would drain the steer under a turn id that
	// is not the receipt's -- so the input settles and the next external wake
	// carries the steer. User work (a queued message) still runs.
	SteeringParked bool
}

// drainAction is the next action the drain loop takes after a completed turn.
type drainAction int

const (
	// runQueued runs the popped queued user message -- or the steering carrier
	// claimed in its place -- as the next turn.
	runQueued drainAction = iota
	// armGoalGate marks that the goal-continuation gate folds this turn and, with no
	// notification pending, the resulting turn is the deferred continuation (if the
	// fold arms one) or idle.
	armGoalGate
	// runNotification runs a pending notification turn next.
	runNotification
	// runDeferredContInline runs an already-deferred goal continuation inline.
	runDeferredContInline
	// goIdle ends the input: settle any turn-tail goal and emit SESSION_END.
	goIdle
)

// selectDrainNextAction is the pure priority decision for the drain loop's tail.
// The wrapper performs the pops and the goal-gate fold (side effects) and feeds
// their results in; this function only decides. skipGoalGate reports whether the
// wrapper should SKIP folding the goal gate this turn — true after a notification
// turn (which must not advance or terminate the goal), while a continuation is
// already deferred (one fold per turn), or while the turn that just ran rests
// SessionAwaiting.
//
// Awaiting is checked first and overrides every other rung (spec §5.3's
// drain-ladder gate): a turn that just posted a question holds its
// notification and goal rungs — none of them may drive the model past the
// unanswered questions — and only the queued-input rung stays live, because a
// message the user queued mid-turn IS the reply (they are demonstrably present).
//
// The steering carrier is queued input for this purpose, awaiting or not: it
// is the user speaking too, and a queued message would have carried the
// steering itself (acceptUserInput drains it before the first model call), so
// the carrier is only ever claimed when nothing is queued. Ending the input
// instead would report the session idle while the user's drained text is
// still undispatched (issue #1308).
//
// Otherwise, priority: a queued user message (or the carrier), then a pending
// notification, then the goal gate's deferred
// continuation, then idle.
// The goal gate always folds BEFORE the notification turn runs: the wrapper arms
// on !skipGoalGate before acting on runNotification, so the fold stays ahead of
// the notification interleave even though the notification is the turn that runs
// next. armGoalGate is returned only when the gate is eligible AND no
// notification preempts, so the resulting turn depends on the (side-effectful)
// fold result.
func selectDrainNextAction(in drainInputs) (action drainAction, skipGoalGate bool) {
	skipGoalGate = in.RanKind == EntryNotification || in.HaveDeferredCont || in.Awaiting || in.SteeringParked
	queued := strings.TrimSpace(in.QueuedText) != "" || in.QueuedImages > 0 || in.QueuedSkills > 0 || in.QueuedCommands > 0 || in.QueuedCarrier
	if in.Awaiting {
		if queued {
			return runQueued, skipGoalGate
		}
		return goIdle, skipGoalGate
	}
	switch {
	case queued:
		return runQueued, skipGoalGate
	case in.HookStoppedInput:
		// The hook stopped the input, not only its turn. Nothing autonomous runs
		// behind it: a goal continuation would send the stopped prompt to the
		// model without the hooks that stopped it, so the input ends here.
		// Queued user work still runs (the case above).
		return goIdle, true
	case in.SteeringParked:
		return goIdle, skipGoalGate
	case in.RanKind != EntryNotification && in.NotificationsPending:
		return runNotification, skipGoalGate
	case !skipGoalGate:
		return armGoalGate, skipGoalGate
	case in.HaveDeferredCont:
		return runDeferredContInline, skipGoalGate
	default:
		return goIdle, skipGoalGate
	}
}

// ProcessInput processes a single user input (with optional image attachments)
// through to completion and returns the accumulated assistant output. It is a
// thin delegate to ProcessInputKind with kind EntryUserInput.
func (s *Session) ProcessInput(ctx context.Context, input string, images []ImageAttachment) (string, error) {
	return s.processInputKindWithProvenance(ctx, input, images, EntryUserInput, nil)
}

// ProcessInputKind processes a single input of the given EntryKind through to
// completion and returns the accumulated assistant output. It runs the input,
// then loops to drain queued user messages, running each as a further turn.
// On a cancelled turn it applies interrupt semantics — flipping the session
// back to idle (unless closed), appending a system-reminder interrupt marker,
// and optionally draining the queue head — then returns the partial output with the error. It emits EventSessionEnd for the input and
// returns an error when the session is closed or a turn fails.
func (s *Session) ProcessInputKind(ctx context.Context, input string, images []ImageAttachment, kind EntryKind) (string, error) {
	return s.processInputKindWithProvenance(ctx, input, images, kind, nil)
}

func (s *Session) processInputWithProvenance(ctx context.Context, input string, images []ImageAttachment, p *provenance.Causal) (string, error) {
	// This internal entry is used for harness nudges and stop-hook input, not
	// a client's message. Keep that evidence distinct from session placement.
	ctx = context.WithValue(ctx, internalTranscriptInputKey{}, true)
	return s.processInputKindWithProvenance(ctx, input, images, EntryUserInput, p)
}

// processRuntimeInput runs a daemon-authored recovery turn. It records no user
// input of its own: the steering queued for it is the model's whole prompt, so
// the turn cannot be mistaken for the caller speaking (#3734).
func (s *Session) processRuntimeInput(ctx context.Context, p *provenance.Causal) (string, error) {
	return s.processInputKindWithProvenance(ctx, "", nil, EntryRuntime, p)
}

func (s *Session) processInputKindWithProvenance(ctx context.Context, input string, images []ImageAttachment, kind EntryKind, inputProvenance *provenance.Causal) (string, error) {
	release, admissionErr := s.beginRetirementMutation("turn")
	if admissionErr != nil {
		return "", admissionErr
	}
	defer release()
	s.inputsInFlight.Add(1)
	defer s.inputsInFlight.Add(-1)
	// A SessionStart hook's continue:false is recorded while an input is being
	// accepted and consumed by processOneInput's take. An input refused before
	// that -- an unhealthy transcript, a closed session, a failed user-turn
	// append -- returns without ever reaching the take, so this clear is what
	// keeps a stop recorded for an input that never ran from swallowing the
	// NEXT, unrelated prompt. Idempotent on the path that did consume it.
	//
	// It applies to USER input only, because only a user turn takes the stop:
	// a notification, continuation or delegate-attention turn that ran first
	// must leave it for the user's first prompt (the take's own gate).
	if kind == EntryUserInput {
		defer s.clearSessionStartHookStop()
	}
	// A delegate child's turn keeps the marker parent's activity evidence
	// fresh for its whole life, silent stretches included; see
	// startLaneEvidenceTicker. Stopped before the retirement release, so no
	// tick outlives the turn.
	stopEvidenceTicker := s.startLaneEvidenceTicker(ctx)
	defer stopEvidenceTicker()
	// Stamp the session id for the whole turn so LLM side calls made from tools
	// (web fetch summaries, naming, in-turn compaction) attribute to this
	// session in the per-session API log. callModelWithFallback layers its
	// per-attempt metadata on top before provider dispatch.
	ctx = llm.WithAPILogContext(ctx, s.id)
	// Reset so SESSION_END can fire at the end of this input's processing.
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		// A steering carrier's claim is this run's to hand back, and this is
		// the one exit before the drain loop's release.
		s.releaseSteeringCarrierClaim(queuedClientMutationFromContext(ctx))
		return "", errors.New("session is closed")
	}
	// Entry gate (spec §5.3): while a question is pending, an autonomous wake —
	// EntryNotification or EntryContinuation — is refused here,
	// before the Processing transition below (processOneInput's
	// setStateIfOpenLocked(SessionProcessing)) ever runs. No state flip, no event
	// emission: round 4's bug was that transition firing unconditionally before
	// dispatch, so a delegate or notification finishing while the user reads the
	// question silently turned the needs-you signal off. Refusing here leaves the
	// wake source's own durable queue (jobstore notifications, the goal engine,
	// the watch outbox) untouched, so it redelivers once the boundary drains
	// after the user's reply. EntryUserInput is always accepted — it is how the
	// reply resolves awaiting (spec §5.2); the steering carrier enters as
	// queued user input for the same reason a queued message does: the
	// steering it carries is the user speaking, not an autonomous wake, so it
	// must reach the model rather than wait behind a question the user has
	// already moved past.
	//
	// Gated on the pending set, not raw state: armAwaitingAtSettle also rests
	// a session awaiting after a turn that ended on needs_response, no ask
	// involved, and an async wake may move that session on. Only a genuine
	// pending question is a stronger stop than the wake (a delegate finishing
	// while the user reads a QUESTION must not silently resolve it out from
	// under them); a needs_response rest has nothing pending to protect.
	if len(s.askPending) > 0 && kind != EntryUserInput {
		s.mu.Unlock()
		return "", nil
	}
	// The same refusal while user steering is parked after a failed attempt
	// (Session.steeringParked): an autonomous turn would drain the parked
	// steer under its own turn id, not the receipt's. Its wake source keeps
	// its work, and the ladder behind the external wake that carries the
	// steer runs it.
	if s.steeringParked && kind != EntryUserInput && s.hasPendingUserSteeringLocked() {
		s.mu.Unlock()
		return "", nil
	}
	s.rearmSessionEndLocked()
	// Mark the session as in-turn for the duration of this input. SetGoal/ClearGoal
	// read this under s.mu to coordinate the idle kick against the drain-loop gate
	// (spec §7): while it is set, an idle kick is suppressed (the gate backs the
	// goal); it is cleared as the last act of this call (the deferred clear below),
	// mutually exclusive on s.mu with "set goal + read flag" and "clear goal".
	s.goalInTurn = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.goalInTurn = false
		s.mu.Unlock()
		// The last act before going idle: a switch queued while this input ran
		// whose moment already passed (the pick landed after the final turn
		// returned, so no further turn of this input applies it) would
		// otherwise wait for a turn that nothing schedules. The session is idle
		// here, so the switch applies now and its marker is persisted before
		// the process can retire. Between-turn picks are the loop site above's.
		s.applyPendingModelSwitch()
	}()

	// A test-only seam: fail the turn before its user entry is recorded, the
	// pre-incorporation, non-transcript failure the start path's give-back keys
	// on. Nil in production, where it is inert.
	if fail := s.cfg.testOnly.failTurnBeforeRecording; fail != nil {
		return "", fail()
	}

	outputs := []string{}
	next := input
	nextImages := images
	nextKind := kind
	nextProvenance := provenance.Clone(inputProvenance)
	processCtx := ctx
	// A goal continuation decided at the gate is deferred across an interleaved
	// notification turn and then run INLINE (via continue), so the goal advances
	// without depending on the idle kick (which no-ops when kickFunc==nil, e.g.
	// one-shot `evener run`). haveDeferredCont guards both the "don't re-fold while a
	// continuation is already pending" case and the inline run below. The gate-time
	// render is NOT cached: the inline site re-reads the store and re-renders the
	// current objective, so a clear/retarget during the interleaved notification turn
	// cannot run a stale continuation.
	var haveDeferredCont bool
	for {
		// Fail closed on a transcript that has stopped accepting records, before
		// every turn rather than once at admission. A turn from here would run
		// in memory and be lost on the next restart, with only a warning per
		// dropped record to show for it; recordTurn's warn-and-continue is right
		// for a write that failed once and wrong for a writer that will refuse
		// everything. The check belongs here rather than at admission because a
		// turn's own buffered record can poison the writer while it runs, and
		// this loop then carries straight on into the follow-up or drained
		// message behind it. The writer's own lock is taken outside s.mu (an
		// append holds it across a write and an fsync). A writer an attention
		// reopen replaced is not the session's writer any more: the gate
		// revalidates a refusal against the attached writer, and a close or
		// poison that lands after a healthy decision is the stale read the next
		// round's gate re-samples.
		if err := s.refuseTurnOnUnhealthyTranscript(processCtx); err != nil {
			// A steering carrier claimed at the tail below owns the active-turn
			// slot until something hands it back; a refusal here is before the
			// release that follows processOneInput, exactly the stranded claim
			// ProcessPendingUserInput's deferred release guards against.
			identity := queuedClientMutationFromContext(processCtx)
			s.releaseSteeringCarrierClaim(identity)
			// A queued message the drain claimed but has not run goes back to
			// the queue. Its claim's poison check and commit are on different
			// resources, so a poisoning can land between them and this gate then
			// refuses; left claimed, the message is out of the queue with
			// ActiveTurnID pinned until restart recovery.
			//
			// Only when the claim has not been incorporated. An incorporated
			// turn's transcript entry is already on disk, so settling it here
			// would mark a durably recorded turn terminal and drop it instead of
			// leaving it for restart recovery to run. ProcessClientMutationStart
			// and ProcessPendingUserInput key their give-back on the same fact.
			if identity.ClientMutationID != "" && !identity.SteeringCarrier &&
				!s.clientMutationUserTranscriptIncorporated(identity.ClientMutationID, identity.StableTurnID) {
				if restoreErr := s.completeClientMutationTurn(identity.ClientMutationID); restoreErr != nil {
					err = errors.Join(err, fmt.Errorf("return claimed input: %w", restoreErr))
				}
			}
			return strings.Join(outputs, "\n"), err
		}
		// Capture the kind actually being processed this iteration before the
		// follow-up reset below; the goal gate needs it to know whether the turn
		// that just ran was a goal continuation (which accrues toward the
		// no-progress streak and iteration count) or a user/follow-up turn (which
		// does not — /par #4).
		ranKind := nextKind
		ranClientMutation := queuedClientMutationFromContext(processCtx)
		// A model switch queued during the previous turn - or picked in the
		// window after that turn settled but before this one starts - applies
		// here, and only here: the previous processOneInput has returned, so
		// its deferred settlement (attention finalizers included) already ran
		// and cannot clobber the switch's own recovery, and this turn has not
		// been admitted yet, so it opens on the picked model with the switch
		// marker between the two turns.
		s.applyPendingModelSwitch()
		out, progressed, err := s.processOneInput(processCtx, next, nextImages, nextKind, nextProvenance)
		// The durable name of the turn that just ran, read before this turn's
		// completion below releases it. An interrupt's marker is that turn's
		// last record, written after this function returns, so it must name
		// the turn it cut short rather than read the slot once the completion
		// has cleared it -- which is what left the marker unowned and the
		// projections grouping it as standalone metadata.
		interruptedTurnOwner := s.activeTurnOwner()
		// Hand a steering carrier's claim back on every exit, the way
		// ProcessPendingUserInput does: a queued message's slot is cleared by
		// its completion below, but nothing settles a carrier's identity --
		// the steer it carried is incorporated at the drain, not at the turn's
		// end. A no-op when the slot already names something else.
		s.releaseSteeringCarrierClaim(ranClientMutation)
		// True when the completion below finalized an interrupt fence naming
		// this turn: a Stop is what ended it, and the drain branch further down
		// must leave the queue head parked (wms7's ruling) rather than run the
		// very message the user just stopped.
		stopSettledThisTurn := false
		if ranClientMutation.ClientMutationID != "" {
			terminalState := "terminal"
			if err != nil {
				terminalState = "failed"
			}
			stopFinalized, completeErr := s.completeClientMutationTurnWithState(
				ranClientMutation.ClientMutationID,
				terminalState,
			)
			if completeErr != nil {
				err = errors.Join(err, fmt.Errorf("complete client mutation turn: %w", completeErr))
			}
			stopSettledThisTurn = stopFinalized
		}
		processCtx = context.WithValue(processCtx, queuedClientMutationContextKey{}, queuedClientMutationIdentity{})
		// The durable selection belonged to the turn that just ran; a
		// follow-up turn must not re-admit it.
		processCtx = withDurableSkillSelection(processCtx, nil)
		// Follow-up turns (after the first) are user-driven, not
		// continuations; each drain rung sets the attachments it carries.
		nextKind = EntryUserInput
		nextProvenance = nil
		if strings.TrimSpace(out) != "" {
			outputs = append(outputs, out)
		}
		if err != nil {
			// Interrupt semantics (kata 0ax1): a cancelled turn cuts the
			// in-flight LLM/tool round short but keeps the session alive
			// so the user can send a follow-up immediately. Matches
			// Claude Code / codex. The transcript records an interrupt
			// marker so the model sees, on the next turn, that the
			// previous round was cut short.
			interruptMarkerRejected := false
			if isTurnCancellation(processCtx, err) {
				s.mu.Lock()
				closed := s.closingOrClosedLocked()
				// Recorded BEFORE the settle below makes the session look idle:
				// a later shutdown close must be able to tell that this turn was
				// cut short, not that the user was sitting at an idle prompt.
				s.turnInterrupted = true
				s.mu.Unlock()

				markerAccepted := false
				if !closed {
					// Append a system-reminder turn so the next request to
					// the model includes the interrupt notice in history.
					// This is the user-visible "interrupted here" marker
					// in the transcript that consumers (TUI / hub) render.
					interruptMsg := systemReminderBlock("The user interrupted the previous turn before it completed. Any partial tool output above is incomplete. Wait for the user's next message before continuing.")
					if markerErr := s.appendSteeringTurnDurablyForOwner(interruptMsg, events.SteeringKindInterrupted, interruptedTurnOwner); markerErr != nil {
						interruptMarkerRejected = true
						// The cancellation is still the original turn error, but a
						// marker that was not admitted cannot resolve the ask or
						// open the autonomous drain. The generic failure tail settles
						// the live boundary from the still-pending ask.
						err = errors.Join(err, fmt.Errorf("record interrupt marker: %w", markerErr))
					} else {
						markerAccepted = true
						// An interrupted turn ends idle even if it posted questions
						// (spec §5.1): the user is demonstrably present. The cards
						// stay rendered from the transcript regardless; only the
						// pending set — which exists purely to drive the boundary
						// check — clears after the marker is accepted.
						s.clearAskPending()
						s.finishProcessingAtBoundary(processCtx, SessionIdle)
						s.emit(events.SteeringInjectedData{Text: interruptMsg, Kind: events.SteeringKindInterrupted})
					}
				} else {
					// A closing session cannot publish a marker. Preserve the
					// existing close guard: settle the turn and discard its
					// in-memory ask without announcing another boundary.
					s.finishProcessingAtBoundary(processCtx, SessionIdle)
					s.clearAskPending()
				}
				// The interrupt marker was the execution's last entry.
				s.completeExecution(schema.TurnInterrupted)
				if markerAccepted {
					s.mu.Lock()
					turns := s.modelResponses
					emitEnd := s.claimSessionEndLocked(false)
					s.mu.Unlock()
					if emitEnd {
						s.emit(events.SessionEndData{
							Reason: "interrupted",
							// The boundary above settled the session; WireState says
							// whether queued work will move it on.
							State:       s.WireState(),
							Turns:       turns,
							Interrupted: true,
						})
					}
				}
				// A turn ended by a Stop parks the queue head instead of running
				// it (wms7's ruling): the user stopped this session's work, so
				// nothing auto-starts, and the message waits, parked, until a
				// user-initiated run releases the hold. interruptDrainConfig
				// cannot make that call -- it is a pure function of this turn's
				// context and error, and a Stop's cancellation is
				// indistinguishable there from a bare host cancellation -- and
				// the fence that would mark the Stop was already finalized by
				// the completion above, which is why the completion reports it
				// here.
				if markerAccepted && !closed && !stopSettledThisTurn {
					// Decide, then claim. popQueueHead is a durable commit, and
					// whether this interrupt may drain depends only on the turn's
					// context and error -- never on the queue -- so there is
					// nothing to learn by taking the head out first. Doing it in
					// the other order costs a second commit to put the entry back,
					// and the state between the two is a queue no client ever saw:
					// a turn/promoteQueuedAsSteer landing there commits against the
					// wrong entry (kata 9f5x).
					if cfg, drainable := interruptDrainConfig(processCtx, err); drainable {
						// The pop below is the same durable claim the
						// completed-turn drain refuses before taking: the
						// message it takes runs on the next iteration, past the
						// gate at the top of this loop, which refuses it --
						// leaving the message in no transcript, no queue and no
						// session. Left queued, it is waiting when the restart
						// recovers the writer. The refusal joins this turn's
						// error because this branch returns it: without that the
						// caller hears only the interrupt and nothing says the
						// transcript is what stopped the drain.
						//
						// The check is unconditional: an interrupt whose own
						// record poisoned the writer still owes the caller that
						// reason when nothing is claimable to drain. The claim
						// re-decides it on the generation it commits against,
						// which covers a poisoning that lands in this window.
						if refusal := s.refuseBeforeClaimingOnUnhealthyTranscript(); refusal != nil {
							err = errors.Join(err, refusal)
						} else if queued, claimRefusal := s.popQueueHeadRefusingPoison(); claimRefusal != nil {
							err = errors.Join(err, claimRefusal)
						} else if inputHasContent(queued.Text, queued.Images, queued.SkillNames, queued.CommandNames) {
							next = queued.Text
							nextImages = queued.Images
							processCtx = s.claimedInputContext(cfg.nextTurnContext(), queued)
							s.rearmSessionEnd()
							continue
						}
					}
				}
			}
			// handleModelError owns terminal provider recovery: it emits the
			// failed turn, blocks the active goal, and settles through the
			// pending-aware failure boundary. Keep this generic tail for
			// non-provider failures (and budget exhaustion), but do not duplicate
			// provider goal/provenance effects.
			if !isProviderTerminalError(err) {
				if _, exhausted := budgetExhaustionFromError(err); !exhausted {
					s.terminateGoalOnError(processCtx, err)
				}
				// A human-note carrier's own append failure (carrierSteerUndelivered)
				// leaves askPending set -- its entry clear never ran
				// (steeringCarrierClaimAnswersAsk) -- so settling idle here would
				// report a session with a genuinely unanswered question as idle,
				// diverging from restore's deriveRestoredState for the identical
				// transcript and, via WireState, telling a live client nothing is
				// waiting on them.
				if interruptMarkerRejected {
					s.finishProcessingAtRestoredFailureBoundary(processCtx)
				} else {
					s.finishProcessingAtFailureBoundary(processCtx)
				}
			}
			// Every OTHER terminal boundary in this loop tells a live subscriber
			// the corrected status: the cancellation branch above emits
			// EventSessionEnd(Reason=interrupted), and the successful-settle tail
			// below emits EventSessionEnd(Reason=input_complete). A genuine
			// (non-cancelled) failure fell through both and emitted neither
			// (kata hen0): turn/completed(Failed) told a live subscriber the turn
			// failed, but nothing told it the thread is idle again, so its belief
			// that a turn was still running leaked until it left and re-entered
			// the session.
			s.completeExecution(schema.TurnFailed) // no-op after an interrupt already completed it
			s.endInputAtTurnFailure()
			return strings.Join(outputs, "\n"), err
		}
		s.completeExecution(schema.TurnCompleted)
		// Drain the next action after a completed (non-error) turn. The pops and the
		// goal-gate fold are side effects kept here; selectDrainNextAction is the pure
		// priority decision over their results. Notification work is consulted only
		// at its ladder rung and only for a non-notification, non-awaiting turn.
		//
		// awaiting reflects the boundary state the turn that just ran left behind
		// (spec §5.3's drain-ladder gate). A steering-carrier entry whose steer does
		// not answer the ask (a human-note update, steeringCarrierClaimAnswersAsk)
		// skips the entry clear of askPending, so a round that merely acknowledges
		// the note leaves askPending nonzero while deliverIfCommunicated settles it
		// SessionIdle at this capture point. State alone would then read as not
		// awaiting while ask1 sits unanswered, so askPendingCount() > 0 is checked
		// alongside it; awaitingOrHasPendingAsk samples both under one lock so a
		// concurrent state transition or askPending mutation can't land between
		// the two reads.
		awaiting := s.awaitingOrHasPendingAsk()
		var queued queuedInput
		// The pop is durable and the turn it feeds runs on the next
		// iteration, past the gate at the top of this loop. Refuse before
		// taking the message rather than after: a message popped for a turn
		// that is then refused is in no transcript, no queue and no session.
		// Left queued, it is waiting when the restart recovers the writer.
		if err := s.refuseTurnOnUnhealthyTranscript(processCtx); err != nil {
			return strings.Join(outputs, "\n"), err
		}
		// kata 111a / t5j6: each drained queued message becomes a distinct user
		// turn; its image attachments ride along as ContentImage parts.
		//
		// The claim refuses poison itself, on the generation it commits
		// against, so a poisoning that lands after the check above still
		// cannot pop a message for a turn the transcript will refuse. That
		// refusal ends the input exactly as the check would.
		var popRefusal error
		queued, popRefusal = s.popQueueHeadRefusingPoison()
		if popRefusal != nil {
			return strings.Join(outputs, "\n"), s.refuseTurnOnUnhealthyTranscript(processCtx)
		}
		// User steering the turn that just ran left behind -- a queue
		// drained as steering after its last model call was in flight --
		// has no turn of its own, and the input is not over until it has
		// run. Claim its carrier here, as the queued entry, so it runs
		// inline rather than after this call has reported the session
		// idle and a wake reopens it (issue #1308). Only when nothing is
		// queued: a queued message drains the steering itself. The claim
		// refuses a held rail (a Stop parked the steer) and an occupied
		// slot, the same gate the wake's claim uses, and it is not made
		// while steering is parked (steeringParkedNow): a steer this
		// input already tried and failed to append -- at a carrier, which
		// ends the input, or at a running turn's own boundary drain,
		// which goes on and completes -- is one attempt per external
		// wake, and this rung is not one. The selector sees the park too
		// and takes goIdle ahead of the autonomous rungs.
		if !inputHasContent(queued.Text, queued.Images, queued.SkillNames, queued.CommandNames) && !s.steeringParkedNow() && s.hasPendingUserSteering() {
			carrier, carrierRefusal := s.claimSteeringCarrierInput()
			if carrierRefusal != nil {
				return strings.Join(outputs, "\n"), s.refuseTurnOnUnhealthyTranscript(processCtx)
			}
			if carrier.SteeringCarrier {
				queued = carrier
				if s.cfg.testOnly.steeringCarrierClaimed != nil {
					s.cfg.testOnly.steeringCarrierClaimed(carrier.StableTurnID)
				}
			}
		}
		noQueued := !inputHasContent(queued.Text, queued.Images, queued.SkillNames, queued.CommandNames) && !queued.SteeringCarrier
		notificationsPending := false
		// After a terminal communicate, notification work is left to the one-shot
		// drain rather than run here: a completion the model was never shown is
		// delivered there, so its reply REPLACES the run's answer instead of
		// being joined onto it as a further output of this call (#865). Every
		// one-shot turn path is followed by DrainJobTree (cmd/evener run,
		// drainForFinalization), and the drain's own turn gate reads the same
		// two signals.
		if noQueued && !awaiting && ranKind != EntryNotification && !s.hasAcceptedTerminalCommunicate() {
			notificationsPending = s.peekNotifications() > 0 || s.pendingRootDelegateAttention()
		}
		// A hook's continue:false owns this input: no autonomous turn may run
		// behind it. Read once here so the decision and the idle goal kick below
		// cannot disagree.
		hookStoppedInput := s.hookStoppedTheInput()
		action, skipGoalGate := selectDrainNextAction(drainInputs{
			RanKind:              ranKind,
			HaveDeferredCont:     haveDeferredCont,
			QueuedText:           queued.Text,
			QueuedImages:         len(queued.Images),
			QueuedSkills:         len(queued.SkillNames),
			QueuedCommands:       len(queued.CommandNames),
			NotificationsPending: notificationsPending,
			Awaiting:             awaiting,
			QueuedCarrier:        queued.SteeringCarrier,
			SteeringParked:       s.steeringParkedNow(),
			HookStoppedInput:     hookStoppedInput,
		})

		if action == runQueued {
			next = queued.Text
			nextImages = queued.Images
			processCtx = s.claimedInputContext(processCtx, queued)
			s.rearmSessionEnd()
			continue
		}

		// Goal continuation gate (priority 4, strictly below user input): runs in
		// its NORMAL tail position so the just-finished continuation folds its
		// progress signal (armGoalContinuation → RecordContinuation → the
		// no-progress breaker accrues) BEFORE any notification can preempt it.
		// Folding here, not after the notification, is what keeps the breaker — the
		// goal engine's ONLY automatic stop — accruing under sustained notifications.
		//
		// The decided next continuation is DEFERRED (not run yet) so a pending
		// notification can interleave ahead of it; it is then run inline below.
		// skipGoalGate is true when the turn that just ran was a notification (a
		// notification must neither advance nor terminate the goal — it already folded
		// at its own gate), while a continuation is already deferred (one fold per
		// turn), or when the turn that just ran rests SessionAwaiting (spec §5.3: arm,
		// don't kick — this alone covers armGoalContinuation for the goal-hold
		// requirement, since it is this call's only site; settleGoalOnIdle and the
		// deferred-continuation inline-run below need their own awaiting guards
		// because both are reached even when this fold is skipped). On a
		// terminal/stop decision the gate emits the terminal report and leaves
		// haveDeferredCont false, so we fall through to EventSessionEnd (idle).
		if !skipGoalGate {
			// The gate's fold + terminal-report side effects (RecordContinuation, the
			// no-progress breaker, EventGoalEnded) happen here; the rendered prompt it
			// returns is discarded because the inline-run site below re-renders the
			// current objective from the store (so a clear/retarget during the
			// interleaved notification turn cannot run a stale continuation).
			if _, ok := s.armGoalContinuation(progressed, ranKind == EntryContinuation); ok {
				haveDeferredCont = true
			}
		}
		// Notification interleave (priority 3): pending notification work runs
		// AFTER the fold above but BEFORE the deferred continuation, so it is
		// transparent to goal accounting (the just-finished continuation already
		// folded). Job notifications and root delegate attention are consumed inside
		// acceptNotificationInput when the EntryNotification turn runs. After a
		// notification turn this rung is skipped (selector gate) because notification
		// work may have been requeued.
		if action == runNotification {
			next = ""
			nextImages = nil
			nextKind = EntryNotification
			continue
		}
		// Run the deferred continuation INLINE (via continue, not the idle kick) so
		// the goal advances even when kickFunc==nil (e.g. one-shot `evener run` with a
		// restored active goal whose model spawned a subagent).
		//
		// Gated on !awaiting (spec §5.3): haveDeferredCont can still be true here
		// even though THIS turn rests awaiting — it was set at an EARLIER turn's
		// tail (this turn's own fold above is skipped by skipGoalGate, which does
		// not clear an already-deferred flag), when a pending notification
		// interleaved ahead of the deferred continuation and that notification's
		// own turn is the one that just asked. Running the continuation here would
		// drive it straight past that unanswered question. Held, haveDeferredCont
		// is simply left set-then-abandoned (this call is about to fall through to
		// settleGoalOnIdle and return; the goal itself is untouched in the store)
		// — the normal resume fold picks the still-active goal up again once the
		// reply resolves the ask.
		// Held, not dropped, while steering is parked (Session.steeringParked):
		// the continuation's acceptance drains steering, and the parked steer
		// belongs to the carrier the next external wake runs; the goal is
		// untouched in the store and the settle after that carrier kicks it.
		if !awaiting && haveDeferredCont && !hookStoppedInput && !s.steeringParkedNow() {
			haveDeferredCont = false
			// Re-validate against the goal store before running: the user may have
			// cleared (/goal clear) or retargeted (/goal <new>) the goal during the
			// interleaved notification turn above, making the render the gate computed
			// at fold time stale. currentGoalContinuation re-reads the store read-only
			// (the fold already happened at the gate — no RecordContinuation re-runs
			// here). If the goal is no longer active, drop the stale continuation and
			// fall through to settle + idle; if it is active, run a FRESH render of the
			// CURRENT objective so a retarget pursues the new goal.
			s.rearmSessionEndUnlessClosing()
			if cont, ok := s.currentGoalContinuation(); ok {
				next = cont
				nextImages = nil
				nextKind = EntryContinuation
				continue
			}
		}
		// Idle transition: clear the in-turn flag and kick a goal that was set in the
		// turn-tail window (after the gate's store read) so it is not stranded until
		// the next user message (spec §7). No-op when no fresh goal is pending.
		if err := s.flushPendingDelegateDeliveries(); err != nil {
			s.emit(events.WarningData{Message: "delegate delivery retry at processing boundary failed: " + err.Error()})
		}
		// A hook-stopped input arms no goal: the idle kick would queue the very
		// continuation that runs without the stopping hook and sends the stopped
		// prompt to the model. Settling still clears the in-turn flag below.
		goalKicked := false
		if !hookStoppedInput {
			goalKicked = s.settleGoalOnIdle()
		}
		// A steer this input recorded whose store mark the store refused gets
		// its mark now, through the steering table; nothing when none waits.
		s.reconcileRecordedSteering()
		s.armAwaitingAtSettle(strings.TrimSpace(strings.Join(outputs, "\n")) != "", goalKicked)
		s.mu.Lock()
		turns := s.modelResponses
		emitEnd := s.claimSessionEndLocked(false)
		s.mu.Unlock()
		if emitEnd {
			s.emit(events.SessionEndData{
				Reason: "input_complete",
				State:  s.WireState(),
				Turns:  turns,
			})
		}
		return strings.Join(outputs, "\n"), nil
	}
}

// endInputAtTurnFailure tells a live subscriber that this input is over, and
// where the session stands now that it is. It is the emit-once dance the
// cancellation branch owns, reusing its sessionEndEmitted gate, so a completion
// that reaches this twice — or reaches it after the cancellation branch already
// fired — emits once.
//
// The state is read rather than asserted, the way the settled tail below reads
// it. The failure exit settles through the pending-aware boundary before
// calling this; a refusal can arrive with a question pending or a message
// still queued, and a client told idle there would show a thread that is
// finished with neither.
func (s *Session) endInputAtTurnFailure() {
	s.mu.Lock()
	closed := s.closingOrClosedLocked()
	turns := s.modelResponses
	emitEnd := !closed && s.claimSessionEndLocked(false)
	s.mu.Unlock()
	if emitEnd {
		s.emit(events.SessionEndData{
			Reason: "turn_failed",
			State:  s.WireState(),
			Turns:  turns,
		})
	}
}

// refuseTurnOnUnhealthyTranscript reports why no further turn may run when the
// transcript will not accept records -- poisoned, or closed -- and ends the input
// the way a failed turn ends one: settle the processing boundary, then emit.
// Admission cleared the emit-once gate on its way in, so a refusal that returns
// without the emission leaves the session looking mid-input to every client on
// the event stream; and a refusal that emits without settling claims an idle the
// session is not in, since a turn may already have run in this loop. Settling is
// a no-op when no turn did, which is the first iteration's case.
//
// Both facts, because the claims that reach here refuse both: a gate that knew
// only the poisoned one would answer a closed writer's claim refusal as success,
// telling the caller the input finished normally while its message or steer stays
// unclaimed.
//
// A writer an attention reopen replaced is not the session's writer any more.
// The gate discards a refusal decided on a writer that is no longer attached
// and decides again on the replacement (refuseOnCurrentUnhealthyTranscript), so
// the closed or repaired handle a reopen left behind cannot refuse a healthy
// session. A genuinely closed or poisoned current writer still refuses.
func (s *Session) refuseTurnOnUnhealthyTranscript(ctx context.Context) error {
	s.failClosedOnTranscriptCreation()
	refusal := s.refuseOnCurrentUnhealthyTranscript()
	if refusal == nil {
		return nil
	}
	s.announceFailClosed()
	s.finishProcessingAtFailureBoundary(ctx)
	s.endInputAtTurnFailure()
	return refusal
}

func delegateEntryRequiresReport(kind EntryKind) bool {
	switch kind {
	case EntryUserInput, EntryContinuation, EntryRuntime:
		return true
	default:
		return false
	}
}

func (s *Session) processOneInput(ctx context.Context, input string, images []ImageAttachment, kind EntryKind, inputProvenance *provenance.Causal) (out string, progressed bool, err error) {
	communicatePreviewCalls := map[string]struct{}{}
	// A speculative document strip that this input never resolves must not
	// outlive it: cancellation, a round cap, or a recovery branch can exit
	// before the retry that would commit or roll it back (#4465).
	defer s.rollbackSpeculativeToolResultDocuments()
	defer func() {
		for callID := range communicatePreviewCalls {
			s.emit(events.CommunicatePreviewResetData{CallID: callID})
		}
	}()
	// Only an input that reached its round loop was admitted as a turn; a wake
	// that stood down earlier (an unnameable turn, a parked rail) does not
	// count toward an owed completion's undispatched turns.
	admittedToRounds := false
	defer func() { s.endTurnForJobNotificationsAwaitingRequest(admittedToRounds) }()
	var rootAttentionIDs []string
	rootAttentionAccepted := false
	// Consume the deliveries this turn's settled rounds presented, armed
	// before or during the turn, at every exit that ran the turn. A delivery
	// injected as steering rode the turn's requests; leaving it for the wake it
	// armed runs a redundant notification turn whose request carries nothing
	// new.
	//
	// Two rules govern the failure path (see finishRootDelegateAttentionTurn
	// for the coverage contract): a resolution failure joins the turn's error
	// only when there IS a begin snapshot — an accepted notification turn's
	// established contract — and every other coverage-only failure warns and
	// leaves the item pending, because coverage is an optimization over the
	// wake path and must not fail an otherwise successful turn.
	//
	// Register BEFORE the autosave/recover defer below so that on panic the
	// recover unwinds first and marks err: a panicking turn never settled, and
	// this defer must read a non-nil err to skip consuming coverage on the
	// success path.
	defer func() {
		if kind == EntryNotification && !rootAttentionAccepted {
			return
		}
		finishErr := s.finishAttentionTurn(rootAttentionIDs, err)
		if finishErr == nil {
			return
		}
		// rootAttentionAccepted is set only in the notification branch, so this
		// predicate is exactly "an accepted notification turn with a non-empty
		// begin snapshot" — the begin-snapshot contract that joins failures.
		if rootAttentionAccepted && len(rootAttentionIDs) != 0 {
			err = errors.Join(err, finishErr)
			return
		}
		s.emit(warningDataFromError("mid-turn delegate attention consumption failed; left pending for its wake", finishErr))
	}()
	// Flush meta.json on every exit from this function — normal return, error
	// return, ctx cancellation, retry-budget exhaustion, or panic. Without
	// this, in-memory modelResponses bumps that happen between happy-path
	// autosaves (e.g. pause_turn, empty-response retries that exhaust) stay
	// stranded if any exit path is taken before the next tool round. Kata ztne.
	//
	// On panic, mark err before re-panicking. The attention-finish defer above
	// unwinds right after this one and must read a non-nil err: a panicking
	// turn never settled, and consuming its covered attention on the success
	// path would durably resolve items the model never finished answering.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("session turn panicked: %v", r)
			s.maybeAutoSave()
			panic(r)
		}
		s.maybeAutoSave()
	}()
	if fault := sessionLifecycleFault(ctx, "panic"); fault != nil {
		panic(fault)
	}

	// Slash-command interception (design §10, P3). Gated on EntryUserInput so
	// only genuine user-typed text can invoke a plugin command: a queued
	// follow-up is still EntryUserInput once the drain loop dequeues it (so it
	// is expanded too), but a goal continuation's or notification's synthesized
	// text never is.
	//
	// A durable client skill selection owns this input's skill route instead:
	// it was prepared at claim time against the input's durable identity, the
	// prose stays the user's own text, and slash expansion is skipped so a
	// slash-looking prose token cannot execute a command while a structured
	// selection rides the same input.
	//
	// Skill routes keep the original input as the turn's text; their prepared
	// activations admit below, after the input turn is recorded. A known skill
	// failure is a visible failed input, never ordinary chat fall-through.
	var skillSelection *schema.SkillInputRecord
	var skillBatch *skillActivationBatch
	var skillRouteErr error
	if durable := durableSkillSelectionFromContext(ctx); durable != nil && kind == EntryUserInput {
		skillSelection = durable.Selection
		skillBatch = durable.Batch
		skillRouteErr = durable.Err
	} else if kind == EntryUserInput {
		result := s.expandSlashCommand(ctx, input)
		if result.Handled {
			input = result.Text
			skillSelection = result.Selection
			skillBatch = result.Activations
			skillRouteErr = result.Err
		}
	}

	// Derive a context that cancels when either the caller's ctx or the session
	// ctx cancels, or the session fails closed.
	ctx, cancel := context.WithCancel(ctx)
	defer s.trackExecutionCancel(cancel)()
	go func() {
		select {
		case <-s.sessionCtx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	defer cancel()

	// A steering-carrier entry (a queued human-note or answering steer with
	// no turn of its own) only resolves the pending ask when the steer it is
	// about to carry actually answers it: computed before the lock below
	// since it reads the client-mutation journal, not session state.
	queuedIdentity := queuedClientMutationFromContext(ctx)
	carrierAnswersAsk := s.steeringCarrierClaimAnswersAsk(queuedIdentity)
	carrierAskResolution := s.clientMutationAskResolution(queuedIdentity.ClientMutationID)

	// restMu: a needs_response rest arming right now announces first.
	if hook := s.cfg.testOnly.turnStartBeforeRestMu; hook != nil {
		hook()
	}
	s.restMu.Lock()
	s.delegateDeliveryMu.Lock()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		s.delegateDeliveryMu.Unlock()
		s.restMu.Unlock()
		return "", false, errors.New("session is closed")
	}
	s.restBeforeInput = s.state
	// The generation match is what keeps a rest owed only until the next turn
	// or settle: a turn that accepted needs_response and then failed or was
	// interrupted before its settle leaves the end reason set but owes nothing.
	s.quietRestBeforeInput = s.restOwedGeneration != 0 && s.restStillPendingLocked(s.restOwedGeneration)
	s.endReasonBeforeInput = s.comm.endReason
	// A resume the last settle never consumed (it returned early for a
	// pending question, or the drain loop returned before settling) must not
	// carry into this input's settle.
	s.resumeQuietRest = false
	s.setStateIfOpenLocked(SessionProcessing)
	// A turn starting ends any needs_response quiet period still running:
	// whichever boundary this turn rests on, the older rest no longer applies.
	s.restGeneration++
	s.restMu.Unlock()
	s.turnStartedAt = s.sclock().Now()
	// A housekeeping input — a job notification, a goal continuation, or the
	// runtime recovery — runs inside the same delegate generation as the
	// non-terminal update it follows. Preserve that evidence so a later
	// missing-report reminder still knows the delegate reached its parent; a new
	// caller input or attention generation starts fresh.
	preserveUpdate := kind != EntryUserInput && kind != EntryDelegateAttention
	updated := s.comm.updated
	s.comm = communicateResult{}
	if preserveUpdate {
		s.comm.updated = updated
	}
	// stop_hook_active is per turn: a continuation a Stop hook blocked into this
	// turn must not leak into the next one.
	s.stopHookActive.Store(false)
	// A tool post-hook's continue:false belongs to the round that raised it; the
	// round loop consumes it, and a new turn must not inherit an unconsumed one.
	s.toolHookStop = false
	s.toolHookStopReason = ""
	// Likewise a UserPromptSubmit stop belongs to the input that raised it.
	s.promptHookStop = ""
	// A new turn is new work; the previous turn's interrupt no longer says
	// anything about this session's idleness.
	s.turnInterrupted = false
	// The stopped-input marker belongs to the input whose prompt the hook
	// stopped. The drain reads it for that input; the next input starts clean.
	s.hookStoppedInput = false
	s.hookStopReason = ""
	// A Stop hook's replacement reply belongs to the turn that raised it.
	s.stopHookReplacedReply = false
	// A claimed answering steering carrier has already crossed its durable
	// admission boundary before this carrier turn runs, so preserve its
	// established entry-clear behavior. Ordinary user input clears only after
	// acceptUserInputWithSkillSelection has durably admitted its user turn;
	// failed MaxTurns, environment, or transcript admission must leave the
	// pending ask available for the next real reply and for restore.
	if carrierAnswersAsk && queuedIdentity.SteeringCarrier {
		s.clearAskPendingResolutionLocked(carrierAskResolution)
	}
	s.mu.Unlock()
	s.delegateDeliveryMu.Unlock()

	// Attention coverage credits only what this turn's requests present.
	// Clear a previous turn's marks before any path below can finish this one.
	s.resetAttentionCoverage()

	select {
	case <-ctx.Done():
		s.emit(errorDataFromError(ctx.Err()))
		// Leave the processing boundary for the outer cancellation handler. It
		// must first admit the interrupt marker, then settle Awaiting when a
		// pending ask survives a rejected marker; settling Idle here would make
		// finishProcessingAtFailureBoundary a no-op and diverge from restore.
		return "", false, ctx.Err()
	default:
	}

	// The running turn's durable name, handed back on every exit from here.
	// Registered before anything mints so no path can return between taking
	// the name and releasing it; the closure reads the variable at unwind
	// time, so this one defer covers whichever branch below does the minting.
	var runningTurnID string
	defer func() { s.releaseRunningTurnID(runningTurnID) }()

	if kind == EntryNotification {
		// A Stop parked this rail: stand down BEFORE minting or consuming
		// any wake state — the same ordering the name refusal below keeps.
		// Job notifications are not the user's to stop mid-delivery, so a
		// wake that carries any still runs.
		if s.rootAttentionRailParked() && s.peekNotifications() == 0 {
			s.finishNotificationNoop()
			return "", false, nil
		}
		// Take the name first, and in ONE atomic take-or-refuse against the
		// durable store. Asking whether the name is free and then taking it
		// are two operations with a gap, and a turn/start running on an RPC
		// goroutine can claim it in that gap -- after which this wake would
		// proceed unnamed, which is precisely the failure this work exists to
		// prevent. mintRunningTurnID either takes the name or reports it
		// taken, with nothing in between.
		//
		// Deciding here, before beginRootDelegateAttentionTurn, is the other
		// half. That call consumes the process-local wake: it clears
		// rootAttentionWake and cancels any scheduled retry, and only a wake
		// that is ACCEPTED re-arms them. A stand-down decided after it would
		// strand the very attention it was woken for.
		var refusal turnNameRefusal
		runningTurnID, refusal = s.mintRunningTurnID()
		if s.servedByDaemon() && runningTurnID == "" {
			// Settle the SessionProcessing transition this call already made
			// (above), the way every other refusal on this path does. Without
			// it the session reports itself busy with nothing running.
			s.finishNotificationNoop()
			// Ask for another wake. Nothing else will: the EntryNotification
			// that got us here is consumed, and the drain loop deliberately does
			// not run another notification immediately after an EntryNotification
			// turn. For delegate attention, the wake flag we deliberately did NOT
			// consume is itself what suppresses a new one --
			// armRootDelegateAttention notifies only when the flag is clear, and
			// the retry scheduler returns early while it is set.
			//
			// The wake is guaranteed rather than best-effort, but it is PACED
			// rather than immediate (kata ajg5). An immediate notify() pushes
			// into a one-slot channel the serve loop is about to read, so it is
			// a hot loop for as long as its condition holds: dequeue, stand
			// down, notify, dequeue. That was accepted because the condition
			// was assumed brief -- a client turn/start about to hand the name
			// back. A mutation store failing writes makes it permanent, since
			// the turn holding the name can then neither be claimed nor
			// released, and the owner check below reads it as owned throughout.
			// scheduleRunningTurnNameRetry keeps the guarantee and backs off.
			//
			// Which of the three refusals arrived decides between waiting and
			// giving up:
			//
			// A store that would not take the write says NOTHING about the
			// name -- it may be free. Reading that as a stale name is what left
			// delegate attention stranded: the stand-down warned and did not
			// re-arm, while rootAttentionWake stayed set and suppressed every
			// other source of a wake. So retry, and let mintRunningTurnID's own
			// warning be the diagnostic.
			//
			// A name a pending execution owns is coming back, whether promptly
			// or once the disk recovers. So is a slot an accepted interrupt has
			// fenced -- and a fence over a turn the daemon never named carries
			// the EMPTY name that turn had, so the owner check answers false for
			// it and it needs its own arm. Retry both.
			//
			// A name NO pending execution owns is not coming back at all: it
			// was left by a crash between reserving and releasing, or by a
			// failed release write, and only forgetRunningTurnNoOneOwns at load
			// clears it. Waiting on that is the hot loop with a timer in front
			// of it, so say so once and let the wake wait for a real one.
			switch {
			case refusal == turnNameStoreFailed:
				s.scheduleRunningTurnNameRetry(turnNameStoreRetryMaxDelay)
			case refusal == turnNameFenced, s.runningTurnNameHasOwner():
				s.scheduleRunningTurnNameRetry(jobNotificationRetryMaxDelay)
			default:
				s.emit(events.WarningData{
					Message: "a turn name is held with no pending mutation to release it; deferring this wake would never resume",
				})
			}
			return "", false, nil
		}
		// Named: drop any backoff this session accumulated standing down.
		s.resetRunningTurnNameRetry()
		rootAttentionIDs = s.beginRootDelegateAttentionTurn()
	}

	// Name the turn before the event that opens it, so the AppWire projection
	// adopts this id in stream order rather than minting a turn_<n> of its own
	// that no mutation precondition accepts. User input arrives already named
	// by its own durable reservation; a goal continuation and a notification
	// wake have nothing to name them.
	//
	// A continuation is named here because acceptContinuationInput cannot
	// refuse. A notification wake took its name above, before it consumed any
	// wake state, so that a wake it cannot name stands down while standing
	// down is still free.
	if kind == EntryContinuation {
		runningTurnID, _ = s.mintRunningTurnID()
	}

	// Admit the execution before its first entry: a continuation or a
	// notification runs under the name minted above, user input under the
	// name its durable claim reserved, anything else under a fresh id.
	executionName := runningTurnID
	if executionName == "" {
		executionName = queuedIdentity.StableTurnID
	}
	s.beginExecution(executionName)

	if kind == EntryContinuation {
		s.acceptContinuationInput(ctx, input, runningTurnID)
	} else if kind == EntryNotification {
		if !s.acceptNotificationInput(ctx, runningTurnID) {
			// The name taken above is released by the deferred handback, which
			// was registered before the mint precisely so this path cannot
			// leak it.
			return "", false, nil
		}
		rootAttentionAccepted = true
	} else if kind == EntryDelegateAttention {
		s.acceptDelegateAttentionInput()
	} else if kind == EntryRuntime {
		s.acceptRuntimeInput(inputProvenance)
	} else if err := s.acceptUserInputWithSkillSelection(ctx, input, images, inputProvenance, kind == EntryUserInput, skillSelection); err != nil {
		if errors.Is(err, errSteeringCarrierStoodDown) {
			// A carrier that stood down ran no model turn. Record it so the
			// claim's caller can report ran=false truthfully (issue #185 item 3).
			s.markSteeringCarrierStoodDown(queuedIdentity.StableTurnID)
			return "", false, nil
		}
		return "", false, err
	}
	// A UserPromptSubmit hook's continue:false ends the turn before its first
	// model request: its reason was delivered to the user while the input was
	// accepted, and no model work dispatches. A SessionStart hook's stop does the
	// same for the first USER turn; both are consumed unconditionally here so a
	// short-circuit cannot leave the other flag set for a later turn.
	//
	// The startup stop waits for that user turn. A notification, continuation or
	// delegate-attention turn can arrive first (a child's update, a watched job),
	// and letting one of those consume the flag would swallow the user's first
	// prompt's stop: the reason names start-up, and the input it guards is the
	// user's.
	promptStopReason := s.takePromptHookStop()
	startStopReason := ""
	if kind == EntryUserInput {
		startStopReason = s.takeSessionStartHookStop()
	}
	stopReason := promptStopReason + startStopReason
	if stopReason != "" {
		// The stop owns the whole input, not only this turn: the drain would
		// otherwise fold the goal gate and schedule a continuation, which runs
		// without these hooks and sends the stopped prompt to the model.
		s.mu.Lock()
		s.hookStoppedInput = true
		s.mu.Unlock()
		// The reason is kept for a delegate's terminal packet, which would
		// otherwise report a completed run with an empty message. The input's
		// own return stays empty: a stopped turn delivers nothing to the model.
		s.mu.Lock()
		s.hookStopReason = stopReason
		s.mu.Unlock()
		s.finishProcessingAtBoundary(ctx, SessionIdle)
		return "", false, nil
	}
	if skillRouteErr != nil {
		// A known but failed skill route is a visible failed input: the
		// original request is recorded above for correction and explicit
		// retry, and no dependent model work dispatches.
		s.emitTurnFailure(errorDataFromError(skillRouteErr))
		s.finishProcessingAtBoundary(ctx, SessionIdle)
		return "", false, nil
	}
	if skillBatch != nil {
		if err := s.admitSkillActivationBatch(skillBatch); err != nil {
			s.emitTurnFailure(errorDataFromError(err))
			s.finishProcessingAtBoundary(ctx, SessionIdle)
			return "", false, nil
		}
	}
	if delegateEntryRequiresReport(kind) && s.delegateController != nil {
		if lease, ok := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease); ok {
			if err := s.delegateController.escalateCompletionRequirement(lease); err != nil {
				return "", false, err
			}
		}
	}

	var toolSigs []string
	var toolSigFailed []bool
	var lastText string // accumulated assistant text for round-limit return
	ctxWarned := false
	contentFilterRetried := false  // track whether we've already tried recovering from a content filter error
	documentRejectRetried := false // track whether we've already retried without a provider-rejected tool-result document
	localAdmissionCompacted := false
	providerContextRecovered := false
	var tracker retryTracker
	serverContinuations := 0

	// Continuation (goal) turns clamp the per-input round cap to GoalTurnMaxRounds
	// (spec §2b/C13); a config of <0 is "unbounded", so a bare min would be wrong.
	goalControlsCap := goalControlsRoundCap(s.cfg.MaxToolRoundsPerInput, kind)
	roundCap := goalRoundCap(s.cfg.MaxToolRoundsPerInput, kind)

	admittedToRounds = true
	for round := 0; roundCap < 0 || round < roundCap; round++ {
		// The same admission rule the drain loop applies before a turn, applied
		// before every round after the first: an input does not stop being
		// admitted once it has started. A record this input already made can
		// stop the writer mid-input -- a tool-result line that landed partially
		// is the ordinary way -- and every round behind it would make another
		// model request and another batch of tool executions whose results the
		// transcript already refuses, leaving work that exists in memory and
		// nowhere else. Round 0 is not re-checked: the drain loop's gate and
		// this input's own user-input record already stand in front of it.
		if round > 0 {
			if err := s.refuseTurnOnUnhealthyTranscript(ctx); err != nil {
				return "", progressed, err
			}
		}
		roundStart := s.sclock().Now()
		// Snapshot the pending-ask count before this round's tool calls run, so
		// the delivery check below can compute the per-round delta (spec §5.1):
		// a round that posts a question ends the turn, and the delta is the
		// robust, self-contained way to detect that (rather than trusting the
		// global count, which a later round should never even reach).
		askBefore := s.askPendingCount()
		// Install this round's salvage recorder before anything can fail, so a
		// round that never reaches its model call cannot settle on the previous
		// round's partial output.
		s.beginRoundRecorder()
		var timings events.RoundTimings
		timings.Round = round

		s.mu.Lock()
		s.totalRounds++
		s.mu.Unlock()

		if sessionLifecycleFault(ctx, "round_cancel") != nil {
			cancel()
		}
		select {
		case <-ctx.Done():
			s.emit(errorDataFromError(ctx.Err()))
			// Leave the processing boundary for the outer cancellation handler.
			// A cancellation between rounds has the same marker-admission and
			// pending-ask boundary as cancellation before the first round.
			return "", progressed, ctx.Err()
		default:
		}

		var (
			profile, usedProfile *provider.Profile
			sys, reqEffort       string
			req                  llm.Request
			fullHistory          []llm.Message
			modelResp            sessionModelResponse
			attempt              ModelAttemptMetadata
			tPhaseStart          time.Time
			err, prepareErr      error
		)
		// A call that waited out a usage limit (waitOutUsageLimit) makes the
		// round's request again: prepared afresh, so it runs on a model
		// switched to meanwhile and carries steering and notifications that
		// arrived meanwhile, and within the same round, so waiting spends no
		// round.
		for {
			// Notifications that arrived since the opener's drain or the
			// previous request join this request, including a pause_turn
			// continuation, which does not advance the round. They count as
			// delivered only once a request is dispatched (below), so any exit
			// before that leaves them owed.
			if kind == EntryNotification {
				s.injectLaterJobNotifications(ctx)
			}
			profile, sys, _, req, fullHistory, reqEffort, prepareErr = s.prepareModelRequestWithError(ctx, round, &timings)
			if prepareErr != nil {
				break
			}
			// --- Phase: LLMCall ---
			tPhaseStart = s.sclock().Now()
			quotaWakes := s.quotaWakeCount()
			s.noteParentJobActivity(jobPhaseAwaitingModel)
			// Completions awaiting a request settle only once one built from
			// the transcript holding their reminders has actually been handed
			// to the provider; the client reports that moment. A provider
			// error after it still settles them (the turn fails visibly, or
			// waits and retries from the same transcript), while a Stop, an
			// admission refusal or any other failure before it leaves them
			// owed.
			var requestSent atomic.Bool
			sentCtx := llm.WithRequestSentObserver(ctx, func() { requestSent.Store(true) })
			modelResp, req, attempt, usedProfile, err = s.callModelWithFallback(sentCtx, profile, req, fullHistory, reqEffort, round)
			if requestSent.Load() {
				s.settleJobNotificationsCarriedByRequest(ctx)
			}
			for _, callID := range modelResp.CommunicatePreviewCallIDs {
				communicatePreviewCalls[callID] = struct{}{}
			}
			retry, waitErr := s.waitOutUsageLimit(ctx, err, req.Model, quotaWakes)
			if waitErr != nil {
				err = waitErr
			}
			if !retry {
				break
			}
			s.resetCommunicatePreviews(communicatePreviewCalls)
		}
		if prepareErr != nil {
			if isLocalContextCompactionError(prepareErr) && !localAdmissionCompacted && s.contextMgr != nil {
				s.emit(warningDataFromError(
					"Local context admission failed; compacting context and retrying", prepareErr))
				localAdmissionCompacted = true
				s.forceCompactForModelRecovery(ctx)
				continue
			}
			return "", progressed, prepareErr
		}
		resp := modelResp.Response

		timings.LLMCall = time.Since(tPhaseStart)

		if err == nil {
			// A speculative document strip whose retry succeeded was the fix:
			// keep those payloads out of later requests (#4465).
			s.commitSpeculativeToolResultDocuments()
			if abortErr := errors.Join(s.abortResponseProcessing(ctx), sessionLifecycleFault(ctx, "abort_after_model")); abortErr != nil {
				return "", progressed, abortErr
			}
		}

		if err != nil {
			if isLocalContextCompactionError(err) && !localAdmissionCompacted && s.contextMgr != nil {
				s.emit(warningDataFromError(
					"Local context admission failed; compacting context and retrying", err))
				localAdmissionCompacted = true
				s.forceCompactForModelRecovery(ctx)
				continue
			}
			if isLocalContextBudgetError(err) {
				return "", progressed, err
			}
			// A request-size rejection (413) on a request carrying tool-result
			// documents is bounded document recovery, not context pressure:
			// compaction preserves the newest tool results, so the oversized
			// payload would survive it and fail again (#4465).
			if isDocumentPayloadSizeRejection(err) && !documentRejectRetried {
				if ids := toolResultDocumentCallIDs(req); len(ids) > 0 {
					documentRejectRetried = true
					s.speculateRejectedToolResultDocuments(ids)
					s.emit(bareWarningDataFromError(
						"Provider rejected the request size for a tool-result document — retrying without the document payload", err))
					s.resetCommunicatePreviews(communicatePreviewCalls)
					continue
				}
			}
			if isProviderContextLengthError(err) && !providerContextRecovered && s.contextMgr != nil {
				failedProvider, failedModel := strings.TrimSpace(req.Provider), strings.TrimSpace(req.Model)
				if failedProvider == "" {
					failedProvider = profile.ID()
				}
				if failedModel == "" {
					failedModel = profile.Model()
				}
				s.emit(bareWarningDataFromError(
					"Context length exceeded: Provider context disagreement for "+failedProvider+"/"+failedModel+"; compacting context and retrying", err))
				providerContextRecovered = true
				s.forceCompactForModelRecovery(ctx)
				continue
			}
			retry, ferr := s.handleModelError(ctx, err, req, &contentFilterRetried, &documentRejectRetried, providerContextRecovered)
			if retry {
				s.resetCommunicatePreviews(communicatePreviewCalls)
				continue
			}
			return "", progressed, ferr
		}
		// The round settled, so its staged attention is now covered. A round
		// that failed — or was retried after compaction folded the steering
		// turn away — never reaches here, so coverage always means
		// "presented in a settled call of this turn".
		s.promoteStagedAttentionCoverage(req)

		if abortErr := errors.Join(s.abortResponseProcessing(ctx), sessionLifecycleFault(ctx, "abort_after_log")); abortErr != nil {
			return "", progressed, abortErr
		}

		// Accumulate usage and record exact input token count for pressure calculation.
		s.recordResponseUsage(resp, req, usedProfile)

		// Context window awareness: emit a warning when we exceed ~80% of the
		// context window. A fallback that answered owns the round's window: the
		// context accounting already switched to it, and the warning must not
		// report the primary's window for a request the fallback served.
		if !ctxWarned {
			if sessionLifecycleFault(ctx, "warn") != nil || s.maybeWarnContextUsage(usedProfile, req) {
				ctxWarned = true
			}
		}

		// Best-effort nudge: when pressure crosses WarnThreshold, steer the agent to
		// self-compact at its next clean seam. recordResponseUsage above just refreshed
		// lastInputTokens, so pressure reflects the round that completed. The latch keeps
		// this one-shot until the next compaction; the nudge is queued as steering and
		// reaches the model before the next round's model call.
		s.maybeNudgeSelfCompact(len(sys))

		if abortErr := errors.Join(s.abortResponseProcessing(ctx), sessionLifecycleFault(ctx, "abort_after_usage")); abortErr != nil {
			return "", progressed, abortErr
		}

		txt := resp.Text()
		lastText = txt
		calls := resp.ToolCalls()

		// Two concepts of "empty":
		// 1. noContent: no text and no tool calls — triggers retry logic
		// 2. skipHistory: no text, no tool calls, AND no phase metadata —
		//    truly nothing to append. Responses with phase annotations
		//    (e.g., "final_answer") must be preserved in history so the
		//    model sees its own phase metadata and can course-correct.
		rc := classifyRoundContent(txt, len(calls), resp.Message.Content)
		noContent := rc.NoContent
		skipHistory := rc.SkipHistory
		// A pause_turn response is the provider's partial state. Even when it
		// carries only server-side tool blocks (a web search still running) and
		// no text or client tool calls, the continuation must see it again, so
		// keep it in history instead of dropping it as a no-content round.
		if resp.Finish.Reason == llm.FinishReasonPauseTurn && len(resp.Message.Content) > 0 {
			skipHistory = false
		}

		if abortErr := errors.Join(s.emitAssistantResponse(ctx, resp, modelResp, txt, skipHistory, attempt), sessionLifecycleFault(ctx, "emit_assistant")); abortErr != nil {
			return "", progressed, abortErr
		}
		// pause_turn: model needs another turn (e.g. server-side web search still running).
		if resp.Finish.Reason == llm.FinishReasonPauseTurn {
			round-- // Don't count pause_turn as a tool round.
			serverContinuations++
			timings.TotalRound = time.Since(roundStart)
			timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
			s.emit(timings)
			if serverContinuations >= maxServerContinuations {
				// Stop the chain on its own budget; the pause responses
				// already emitted stay in history, so the turn is resumable.
				exhausted := &budgetExhaustionError{
					Budget:    exhaustedBudgetServerContinuations,
					Limit:     maxServerContinuations,
					Resumable: true,
				}
				s.emitBudgetExhaustionFailure(exhausted)
				return lastText, progressed, exhausted
			}
			continue
		}
		serverContinuations = 0

		// Reverse-map provider-specific tool names to canonical names for registry lookup.
		for i := range calls {
			calls[i].Name = s.canonicalToolName(calls[i].Name)
		}

		if len(calls) == 0 {
			// A bare-text (non-empty) response to a notification turn
			// is the agent acknowledging a frame it has nothing to act on (it often
			// already read the job's output itself). Finish idle instead of scolding it
			// toward a no-op communicate — the text is already in the transcript, and a
			// system-initiated turn carries no user awaiting a reply. A truly empty
			// (no-content) response is a model glitch and still routes through the
			// empty-retry path below — EXCEPT after a terminal communicate, where
			// silence means "nothing to add to a finished run" and retrying would
			// resurrect it (issue #329).
			if kind == EntryDelegateAttention && !noContent && s.delegateController != nil {
				if lease, ok := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease); ok {
					allowDelegateNoAction, recordErr := s.delegateController.recordAttentionNoAction(lease)
					if recordErr != nil {
						return "", progressed, recordErr
					}
					if allowDelegateNoAction {
						s.finishProcessingAtBoundary(ctx, SessionIdle)
						return "", progressed, nil
					}
				}
			}
			route := routeNoToolCalls(kind, noContent, s.hasAcceptedTerminalCommunicate())
			if route == finishIdle {
				s.finishProcessingAtBoundary(ctx, SessionIdle)
				return "", progressed, nil
			}
			retry, ferr := s.handleNoToolCalls(noContent, &tracker)
			if !retry {
				return "", progressed, ferr
			}
			round-- // Don't count empty/bare-text retries as tool rounds.
			timings.TotalRound = time.Since(roundStart)
			timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
			s.emit(timings)
			continue
		}

		// Model produced tool calls — reset retry counters.
		tracker.consecutiveEmpty = 0
		tracker.consecutiveBareText = 0

		if sessionLifecycleFault(ctx, "abort_before_tools_cancel") != nil {
			cancel()
		}
		if abortErr := errors.Join(s.abortResponseProcessing(ctx), sessionLifecycleFault(ctx, "abort_before_tools")); abortErr != nil {
			if ctx.Err() != nil && !s.isClosingOrClosed() {
				s.appendCanceledToolResults(calls, nil, abortErr)
			}
			return "", progressed, abortErr
		}

		// --- Phase: ToolExec ---
		tPhaseStart = s.sclock().Now()

		// Execute tool calls (possibly in parallel) and send results back.
		s.noteParentJobActivity(jobPhaseToolRunning)
		s.beginToolRound()
		results, execErr := s.execToolBatch(ctx, calls, profile, resp.Finish.Reason)
		execErr = errors.Join(execErr, sessionLifecycleFault(ctx, "exec_tools"))
		if execErr != nil {
			return "", progressed, execErr
		}

		// Progress signal for the goal engine: a turn "progressed" iff it
		// executed a real mutating tool call — !ReadOnly, not the
		// result/communicate tool, not task_list (plan-spam and "I'm done"
		// messages must not count), and not a failed or prevalidation-only
		// result. Credited from the executed results, not the proposed calls,
		// so an unknown name or a rejected write never resets the no-progress
		// streak. Accumulated as a turn-level OR across the turn's rounds.
		progressed = progressed || s.toolResultsMadeProgress(results)

		timings.ToolExec = time.Since(tPhaseStart)

		// --- Phase: Persistence ---
		tPhaseStart = s.sclock().Now()

		if persistErr := errors.Join(s.persistToolResults(ctx, calls, results), sessionLifecycleFault(ctx, "persist_tools")); persistErr != nil {
			return "", progressed, persistErr
		}

		timings.Persistence = time.Since(tPhaseStart)

		// PostToolBatch hooks, fired before injectPostToolSteering so their
		// additionalContext is queued when that function's injectDrainedSteering
		// -- the last drain before the next request -- runs, and so reaches the
		// SAME next request. Firing after it would deliver one request late.
		//
		// The block is recorded here and applied only after
		// deliverIfCommunicated reports done == false: a round that also called
		// communicate keeps its reply, and one that posted ask_user stays
		// awaiting. A batch of nothing never fires (no tool_calls to describe).
		// A PostToolUse/PostToolUseFailure continue:false is recorded by the
		// round's own tool executions and read here, where the results are
		// already durable. It joins PostToolBatch's block in the same turn-ending
		// tail below, so a round that also communicated keeps its reply.
		s.mu.Lock()
		toolHookStopped := s.toolHookStop
		toolHookStopReason := s.toolHookStopReason
		s.toolHookStop = false
		s.toolHookStopReason = ""
		s.mu.Unlock()
		batchBlocked := toolHookStopped
		batchStop := toolHookStopReason
		// A session whose plugins register no PostToolBatch hook skips building
		// and parsing every call this round; the gate is the same one the
		// dispatch would apply, in front of the work it needs.
		if s.hookRunner != nil && s.hookRunner.Has(plugin.HookPostToolBatch) && len(calls) > 0 {
			hi := s.hookInput(plugin.HookPostToolBatch)
			hi.ToolCalls = s.batchToolCalls(calls, results)
			batchRes := s.hookRunner.RunPostToolBatch(s.apiLogContext(ctx), hi)
			for _, m := range batchRes.ModelContext {
				if err := s.deliverHookContext(m); err != nil {
					return "", progressed, err
				}
			}
			for _, m := range batchRes.UserMessages {
				s.deliverHookUserMessage(m)
			}
			if batchRes.Stopped {
				batchBlocked = true
				if stop := strings.TrimSpace(batchRes.StopReason); batchStop == "" {
					batchStop = stop
				}
			}
		}

		// --- Phase: AfterAction ---
		tPhaseStart = s.sclock().Now()

		// Notify the context strategy that a tool round completed.
		if afterErr := errors.Join(s.notifyStrategyAfterAction(ctx), sessionLifecycleFault(ctx, "after_action")); afterErr != nil {
			return "", progressed, afterErr
		}

		timings.AfterAction = time.Since(tPhaseStart)

		yieldToObserverCallback, steerErr := s.injectPostToolSteering(ctx, calls, results, &toolSigs, &toolSigFailed)
		steerErr = errors.Join(steerErr, sessionLifecycleFault(ctx, "post_tool_steer"))
		if steerErr != nil {
			return "", progressed, steerErr
		}

		// Agent-requested self-compaction runs here: after AfterAction/steering (so a
		// strategy's AfterAction sees pre-compaction history) and before delivery (so a
		// compact+communicate round compacts in-activation, not at the next user turn).
		s.applyPendingForceCompact(ctx)

		// Emit round timings before checking result delivery.
		timings.TotalRound = time.Since(roundStart)
		timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall - timings.ToolExec - timings.Persistence - timings.AfterAction
		s.emit(timings)

		// communicate sets the flag, or this round posted question(s) (spec
		// §5.1) — either ends the turn; deliverIfCommunicated decides the
		// boundary state and composes them.
		askedThisRound := s.askPendingCount() > askBefore
		done, text, deliverErr := s.deliverIfCommunicated(ctx, askedThisRound)
		if deliverErr != nil {
			return "", progressed, deliverErr
		}
		if done {
			return text, progressed, nil
		}
		// The round did not produce a reply or a question, so the recorded stop
		// owns the turn: a PostToolUse/PostToolUseFailure continue:false, or
		// PostToolBatch's block. Deliver its reason, record it as steering, and
		// end through the same failure boundary the other pre-request stops use.
		// The tool results are already persisted.
		//
		// A Stop hook that vetoed one of this round's communicate calls lands
		// here too (deliverIfCommunicated returned not-done), so the batch block
		// yields to nothing and ends the turn. Reaching another model request
		// would be the alternative, and the loop the previous behavior could run.
		// firstLineClamp bounds the reason: it becomes a warning AND a steering
		// turn, and multi-line shell stderr must not fill the transcript.
		if batchBlocked {
			// A block with no stated reason still ends the turn with a legible
			// line, rather than a silent stop.
			batchStop = s.hookStopText(firstLineClamp(batchStop, maxHookStopReasonRunes), "a tool hook stopped this round")
			s.deliverHookUserMessage(batchStop)
			s.appendSteeringTurn(batchStop, events.SteeringKindHookContext)
			s.finishProcessingAtFailureBoundary(ctx)
			return "", progressed, nil
		}
		if yieldToObserverCallback || sessionLifecycleFault(ctx, "yield_observer") != nil {
			s.finishProcessingAtFailureBoundary(ctx)
			return "", progressed, nil
		}
		// A managed job can finish while this tool batch is running. An ordinary
		// turn yields before another model round so the outer drain delivers that
		// completion as an EntryNotification turn. A notification turn is that
		// delivery already: it folds the arrival in at the start of its next
		// round, once that round is certain to make a model request.
		if kind != EntryNotification && s.peekNotifications() > 0 {
			s.finishProcessingAtFailureBoundary(ctx)
			return "", progressed, nil
		}
	}

	s.recordNotice(schema.NoticeInfo{Kind: schema.NoticeTurnLimit, TurnLimit: &schema.TurnLimitNotice{MaxToolRoundsPerInput: s.cfg.MaxToolRoundsPerInput}})
	s.emit(events.TurnLimitData{MaxToolRoundsPerInput: s.cfg.MaxToolRoundsPerInput})
	s.finishProcessingAtFailureBoundary(ctx)
	if goalControlsCap {
		return lastText, progressed, nil
	}
	return lastText, progressed, &budgetExhaustionError{
		Budget:    exhaustedBudgetToolRounds,
		Limit:     s.cfg.MaxToolRoundsPerInput,
		Resumable: true,
	}
}

// acceptUserInput records a new user input at the start of an input turn: it
// repairs any orphaned tool results, enforces MaxTurns, and returns typed
// exhaustion when the turn limit is already reached. Once the
// turn is accepted, it increments the turn counter. For real external user
// input only, it drains any pending resume SessionStart hook output before
// appending the user turn. It then emits EventUserInput, appends the user turn,
// launches the session namer, runs UserPromptSubmit hooks, drains any pending
// steering, and returns nil.
// returnAcceptedUserTurn gives back everything acceptUserInput claimed for an
// input it is about to refuse: the provisional turn count, and whichever
// mutation claim this input arrived on -- a direct input's reservation, a
// client start's, or a queued entry's, which goes back to the queue. One shape
// for every refusal in this function, so a new refusal cannot return a
// different subset of the same claim than the ones beside it.
func (s *Session) returnAcceptedUserTurn(queuedIdentity queuedClientMutationIdentity) error {
	s.mu.Lock()
	s.turns--
	s.mu.Unlock()
	if queuedIdentity.ClientMutationID == "" {
		if err := s.returnClaimedDirectClientMutationTurn(); err != nil {
			return fmt.Errorf("return claimed direct user turn: %w", err)
		}
		return nil
	}
	pending := s.clientMutations.snapshot().PendingExecutions[queuedIdentity.ClientMutationID]
	if pending.Method == clientMutationMethodStart {
		if err := s.returnClaimedClientMutationStart(queuedIdentity.ClientMutationID); err != nil {
			return fmt.Errorf("return claimed client start: %w", err)
		}
		return nil
	}
	if err := s.completeClientMutationTurn(queuedIdentity.ClientMutationID); err != nil {
		return fmt.Errorf("return queued input: %w", err)
	}
	return nil
}

// appendUserInputTurnRefusingPoison records the USER_INPUT turn as the
// history/transcript pair recordTurn makes, but writes BEFORE it publishes, so
// a write that stops the writer leaves no history entry to take back out.
//
// A poisoned writer refuses every later append, so a turn whose own input
// record poisoned it has already made the last record this session can make:
// announcing the input and calling the model past that point runs a whole turn
// -- assistant answer, tool calls, their results -- whose every record is lost,
// for an input no transcript holds either. The drain loop applies exactly this
// admission rule, but at the TOP of each iteration, which is one turn too late
// for the turn that did the poisoning. The error carries the poisoned guards'
// shared answer so the callers keyed on that sentinel -- the queued restore in
// ProcessPendingUserInput -- recognize this refusal as one of theirs.
//
// A retained whole line is adopted by the shared pair helper, while any write
// that recorded nothing is returned before the input is announced or the model
// runs. This direct user-input door owns the stronger admission contract: the
// pending ask cannot be cleared until the reply is recorded. A retained,
// unsynced record is still adopted as the authoritative line rather than
// appended again, which is the transcript pair's explicit retained-record
// contract.
//
// Both of the writer's refusals count, not poison alone: a closed writer's plain
// append is a silent nil no-op, so a path that knew only poison recorded the turn
// in memory and carried on with no transcript record at all -- the failure this
// refusal exists to prevent. The synced door reports that refusal itself, and a
// partial append that breaks the writer returns its cause rather than a sentinel,
// so the decision below is asked over both the write and the writer.
//
// Any OTHER write failure keeps recordTurn's warn-and-continue: a writer that
// failed once still accepts the next record, and whether that is right for
// every producer is the audit in #1181, not this path's rule to settle.
func (s *Session) appendUserInputTurnRefusingPoison(turn schema.Turn) error {
	turn.AnswerToTurnIDs = s.pendingAskTurnIDs()
	err := s.appendTurnAfterTranscriptWrite(
		turn,
		func() error { return s.writeTranscriptSyncedLocked(turn) },
		func() { s.history = append(s.history, turn) },
	)
	if err != nil {
		// The writer's own refusal, whichever of the two facts it is, is not a
		// write that failed: it is the transcript refusing this turn, so it takes
		// the refusal answer rather than the warn-and-continue below.
		if refusal := s.transcriptRefusalForWrite(err); refusal != nil {
			return errors.Join(err, refusal)
		}
		s.emit(events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	return err
}

func (s *Session) acceptUserInput(ctx context.Context, input string, images []ImageAttachment, inputProvenance *provenance.Causal, drainResumeSessionStart bool) error {
	return s.acceptUserInputWithSkillSelection(ctx, input, images, inputProvenance, drainResumeSessionStart, nil)
}

// takePromptHookStop consumes a UserPromptSubmit hook's recorded continue:false.
// It reports whether the turn must end before its first model request. The
// reason was already delivered to the user when the input was accepted.
func (s *Session) takePromptHookStop() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason := s.promptHookStop
	s.promptHookStop = ""
	return reason
}

// takeSessionStartHookStop consumes a SessionStart hook's recorded
// continue:false. It reports whether the first turn must end before its model
// request. The reason was already delivered to the user when the hook ran.
func (s *Session) takeSessionStartHookStop() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason := s.sessionStartHookStop
	s.sessionStartHookStop = ""
	return reason
}

// clearSessionStartHookStop drops a SessionStart hook's recorded continue:false
// when the input that raised it never ran. Without it the flag would survive to
// the next prompt, which is not the input the hook stopped. Guarded by mu.
func (s *Session) clearSessionStartHookStop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionStartHookStop = ""
}

// hookStoppedTheInput reports whether a hook's continue:false ended the whole
// input. Guarded by mu.
func (s *Session) hookStoppedTheInput() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hookStoppedInput
}

// acceptUserInputWithSkillSelection is acceptUserInput plus the typed skill
// selection for a genuine user skill route: the recorded input turn keeps the
// original text and carries the selection record in its SkillState.
func (s *Session) acceptUserInputWithSkillSelection(ctx context.Context, input string, images []ImageAttachment, inputProvenance *provenance.Causal, drainResumeSessionStart bool, skillInput *schema.SkillInputRecord) error {
	queuedIdentity := queuedClientMutationFromContext(ctx)
	if queuedIdentity.SteeringCarrier {
		// Queued input with no content of its own: the steering it carries is
		// drained the way every accepted input drains it, and there is no
		// user turn to record, count or announce.
		return s.acceptSteeringCarrierInput(ctx, queuedIdentity)
	}
	// A new top-level input starts a fresh causal context: replace active
	// provenance with the input's provenance, or empty provenance for ordinary
	// external user input.
	s.replaceActiveProvenance(inputProvenance)
	s.repairOrphanedToolResults(context.Background(), "before accepting new input")

	// Count conversation turns (user input -> model response pairs), not LLM round-trips.
	// Check the limit before incrementing so MaxTurns=N allows exactly N inputs.
	var acceptedTurnsFloor uint64
	if queuedIdentity.ClientMutationID == "" {
		s.mu.Lock()
		acceptedTurnsFloor = uint64(s.turns)
		s.mu.Unlock()
		err := s.claimDirectClientMutationTurn(acceptedTurnsFloor)
		if err != nil {
			if _, exhausted := budgetExhaustionFromError(err); !exhausted {
				return fmt.Errorf("reserve user turn budget: %w", err)
			}
			s.recordNotice(schema.NoticeInfo{Kind: schema.NoticeTurnLimit, TurnLimit: &schema.TurnLimitNotice{MaxTurns: s.cfg.MaxTurns}})
			s.emit(events.TurnLimitData{MaxTurns: s.cfg.MaxTurns})
			s.finishProcessingAtFailureBoundary(ctx)
			return &budgetExhaustionError{
				Budget:    exhaustedBudgetTurns,
				Limit:     s.cfg.MaxTurns,
				Resumable: false,
			}
		}
	}

	s.queueTurnBudgetWarning()

	if queuedIdentity.ClientMutationID != "" &&
		s.clientMutationUserTranscriptIncorporated(queuedIdentity.ClientMutationID, queuedIdentity.StableTurnID) {
		s.injectDrainedSteering()
		s.clearAskPendingResolution(s.clientMutationAskResolution(queuedIdentity.ClientMutationID))
		return nil
	}

	s.mu.Lock()
	s.turns++
	s.mu.Unlock()

	preseededInput := delegateInputWasPreseeded(ctx, s.id, input) && len(images) == 0 && queuedIdentity.ClientMutationID == ""
	// Shared-notes context rides beside the goal continuation-prompt rendering
	// at turn start: the current notes plus URL list enter the model context
	// as a fresh projection of the persisted source of truth. Empty state
	// appends nothing, so a fresh session's history is byte-identical.
	if !preseededInput {
		if err := s.maybeAppendEnvironmentContext(); err != nil {
			if returnErr := s.returnAcceptedUserTurn(queuedIdentity); returnErr != nil {
				return errors.Join(err, returnErr)
			}
			return fmt.Errorf("append environment context: %w", err)
		}
		s.maybeAppendNotesContext()
	}

	if drainResumeSessionStart {
		// Persist the environment context before consuming deferred resume hooks so
		// a failed environment write leaves the hook pending for the retry.
		s.drainPendingSessionStartHooksForUserTurn(ctx)
	}

	// userInputTurn is computed AFTER any SessionStart-hook and environment-context
	// turns above so it reports the USER_INPUT turn's actual history position,
	// not a position stale by however many turns those inserted ahead of it.
	// With a transcript, the entry index the USER_INPUT entry was recorded at
	// replaces it (recordedUserInputTurn): the transcript also holds entries
	// history never does.
	s.mu.Lock()
	userInputTurn := len(s.history) + 1
	if preseededInput {
		userInputTurn = len(s.history)
	}
	s.mu.Unlock()

	// Persist attachments before the turn is built so the message can name
	// their durable paths (agent/image_persist.go).
	images = s.persistInputImages(images)

	if queuedIdentity.ClientMutationID == "" {
		if !preseededInput {
			turn := schema.NewTurn(schema.TurnUserInput, buildSelectedUserInputMessage(input, images, skillInputNames(skillInput)))
			turn.ConversationInputIDs = s.conversationInputIDs("")
			if internal, _ := ctx.Value(internalTranscriptInputKey{}).(bool); !internal {
				turn.Authorship = humanTranscriptAuthorship()
			}
			if selected := durableSkillSelectionFromContext(ctx); selected != nil {
				turn.CommandInput = selected.CommandInput
				turn.Message = appendSelectedCommands(turn.Message, selected.CommandInput, selected.CommandBodies)
			}
			if skillInput != nil {
				turn.SkillState = &schema.SkillTurnState{Input: skillInput}
			}
			if err := s.appendUserInputTurnRefusingPoison(turn); err != nil {
				if returnErr := s.returnAcceptedUserTurn(queuedIdentity); returnErr != nil {
					return errors.Join(err, returnErr)
				}
				return fmt.Errorf("append user input: %w", err)
			}
		}
	} else {
		turn := schema.NewTurn(schema.TurnUserInput, buildSelectedUserInputMessage(input, images, skillInputNames(skillInput)))
		turn.ConversationInputIDs = s.conversationInputIDs(queuedIdentity.ClientMutationID)
		turn.Authorship = humanTranscriptAuthorship()
		if selected := durableSkillSelectionFromContext(ctx); selected != nil {
			turn.CommandInput = selected.CommandInput
			turn.Message = appendSelectedCommands(turn.Message, selected.CommandInput, selected.CommandBodies)
		}
		turn.ClientMutationID = queuedIdentity.ClientMutationID
		attachVoicePresentation(&turn, queuedIdentity.VoicePresentation, queuedIdentity.ClientMutationID)
		turn.StableTurnID = queuedIdentity.StableTurnID
		turn.Actor = s.clientMutationActor(queuedIdentity.ClientMutationID)
		if skillInput != nil {
			turn.SkillState = &schema.SkillTurnState{Input: skillInput}
		}
		pending := s.clientMutations.snapshot().PendingExecutions[queuedIdentity.ClientMutationID]
		if pending.Method == clientMutationMethodStart && s.clientMutationPreAppendFailure != nil {
			if failure := s.clientMutationPreAppendFailure(turn); failure != nil {
				if err := s.beginClientMutationFailure(queuedIdentity.ClientMutationID, failure); err != nil {
					return errors.Join(failure, fmt.Errorf("persist client start failure intent: %w", err))
				}
				recoveryErr := s.recoverClientMutationFailures(true)
				userRecorded := s.clientMutationTranscriptItems(queuedIdentity.ClientMutationID, queuedIdentity.StableTurnID).User
				if userRecorded {
					s.clearAskPendingResolution(s.clientMutationAskResolution(queuedIdentity.ClientMutationID))
				}
				if recoveryErr != nil {
					return errors.Join(failure, fmt.Errorf("record client start failure: %w", recoveryErr))
				}
				if !userRecorded {
					return failure
				}
				s.emit(events.UserInputData{
					Text:             input,
					Images:           userInputImagesFromAttachments(images),
					ClientMutationID: queuedIdentity.ClientMutationID,
					StableTurnID:     queuedIdentity.StableTurnID,
					Turn:             s.recordedUserInputTurn(userInputTurn),
				})
				s.emit(errorDataFromError(failure))
				return failure
			}
		}
		s.setClientMutationAskEvidence(&turn, queuedIdentity.ClientMutationID, "")
		if err := s.appendTurnAfterTranscriptWrite(
			turn,
			func() error { return s.appendClientMutationTranscriptLocked(turn) },
			func() { s.history = append(s.history, turn) },
		); err != nil {
			if returnErr := s.returnAcceptedUserTurn(queuedIdentity); returnErr != nil {
				return errors.Join(err, returnErr)
			}
			return fmt.Errorf("append claimed user input: %w", err)
		}
		// The claimed user turn is now durable. Resolve the pending ask only
		// after this admission point; failures before it must preserve the ask.
		s.clearAskPendingResolution(turn.AskResolution)
		if err := s.markClaimedUserTranscriptIncorporated(queuedIdentity.ClientMutationID); err != nil {
			return fmt.Errorf("incorporate claimed user input: %w", err)
		}
	}
	if queuedIdentity.ClientMutationID == "" {
		// The direct user turn is durable (or was durably preseeded) at this
		// point. Earlier admission failures intentionally leave askPending set.
		s.clearAskPending()
	}
	s.emit(events.UserInputData{
		Text:             input,
		Images:           userInputImagesFromAttachments(images),
		ClientMutationID: queuedIdentity.ClientMutationID,
		StableTurnID:     queuedIdentity.StableTurnID,
		Turn:             s.recordedUserInputTurn(userInputTurn),
	})
	// UserPromptSubmit hooks
	if s.hookRunner != nil {
		hi := s.hookInput(plugin.HookUserPromptSubmit)
		// Send the official "prompt" field and the legacy "user_prompt" alias with
		// the same value: Claude-style hooks read "prompt".
		hi.Prompt = input
		hi.UserPrompt = input
		result := s.hookRunner.RunUserPromptSubmit(s.apiLogContext(ctx), hi)
		for _, m := range result.ModelContext {
			if err := s.deliverHookContext(m); err != nil {
				return err
			}
		}
		for _, m := range result.UserMessages {
			s.deliverHookUserMessage(m)
		}
		// continue:false here means the prompt never reaches the model. The stop
		// reason is delivered to the user now and carried to the turn loop, which
		// ends the turn before its first request (see promptHookStop).
		if result.Stopped {
			reason := s.hookStopText(result.StopReason, "UserPromptSubmit hook stopped this prompt")
			s.deliverHookUserMessage(reason)
			s.mu.Lock()
			s.promptHookStop = reason
			s.mu.Unlock()
		} else {
			// Naming is a model call, so it waits for the hook's verdict: a
			// stopped prompt must not reach the naming provider either.
			s.launchInitialPromptNamerUnlessStopped(input)
		}
	} else {
		s.launchInitialPromptNamerUnlessStopped(input)
	}

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	s.injectDrainedSteering()
	return nil
}

// launchInitialPromptNamerUnlessStopped names the prompt only when no hook stop
// is already recorded for it. A SessionStart continue:false is recorded during
// session init and consumed by the turn loop after this function returns, so
// waiting on UserPromptSubmit's verdict alone would still name a stopped prompt.
func (s *Session) launchInitialPromptNamerUnlessStopped(input string) {
	s.mu.Lock()
	stopped := s.sessionStartHookStop != "" || s.promptHookStop != ""
	s.mu.Unlock()
	if stopped {
		return
	}
	s.launchInitialPromptNamer(s.sessionCtx, input)
}

// goalContinuationMarker is the compact fallback text surfaced for a goal
// continuation turn when no objective is available. The full rendered continuation
// prompt is delivered to the model as a steering turn but is NOT surfaced to the UI
// — it is ~2.5KB of scaffolding that would flood the thread every iteration (/par B6).
const goalContinuationMarker = "Continuing toward the goal."

// acceptContinuationInput records a goal-engine continuation at the start of an
// input turn. It mirrors acceptUserInput's history-repair and steering-drain
// behavior, but frames the input as a system/steering turn rather than the user
// speaking: it appends schema.TurnSteering (so expandHistory delivers it to the
// model as a user-role message without rendering a user bubble), emits
// EventGoalContinuation rather than EventUserInput, and skips the namer, the
// MaxTurns check, and the s.turns++ accounting (goal turns are bounded by the
// no-progress breaker, not the session's user-input ceiling; SESSION_END.Turns
// reads the separate modelResponses counter, so skipping s.turns++ is safe).
func (s *Session) acceptContinuationInput(_ context.Context, input, stableTurnID string) {
	// A goal continuation is a fresh top-level input: reset active provenance so
	// the continuation turn's events do not inherit a prior watch origin.
	s.replaceActiveProvenance(nil)
	s.repairOrphanedToolResults(context.Background(), "before accepting goal continuation")

	// Surface only a compact marker to the UI, not the full rendered continuation
	// prompt: the appwire projection turns EventGoalContinuation into a systemMessage,
	// so emitting the whole ~2.5KB prompt floods the thread every iteration (/par B6).
	// The model still receives the full prompt via the steering turn below.
	marker := goalContinuationMarker
	if snap, ok := s.getOrCreateGoalStore().Snapshot(); ok && snap.Objective != "" {
		marker = "Continuing toward: " + snap.Objective
	}
	s.emit(events.GoalContinuationData{Text: marker, StableTurnID: stableTurnID})
	turn := schema.NewTurn(schema.TurnSteering, llm.User(input))
	turn.GoalContinuation = &schema.GoalContinuationInfo{Text: marker}
	turn.StableTurnID = stableTurnID
	s.recordTurn(turn, turn)

	// On resume the agent re-reads the current notes beside the continuation
	// prompt, so a human edit that landed while the goal loop ran is visible.
	s.maybeAppendNotesContext()

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	s.injectDrainedSteering()
}

func (s *Session) acceptDelegateAttentionInput() {
	s.replaceActiveProvenance(nil)
	s.repairOrphanedToolResults(context.Background(), "before accepting delegate attention")
}

// acceptRuntimeInput starts a daemon-authored recovery turn. It records no
// turn of its own: the steering already queued for this turn carries the
// runtime reminder, and draining it is what gives the model its prompt.
func (s *Session) acceptRuntimeInput(p *provenance.Causal) {
	s.replaceActiveProvenance(p)
	s.repairOrphanedToolResults(context.Background(), "before accepting runtime input")
	s.injectDrainedSteering()
}

// acceptSteeringCarrierInput opens a turn whose only job is to carry
// already-accepted user steering that has no turn of its own to land in. It
// is acceptUserInput's notification-style sibling for this one shape: no
// MaxTurns check, no s.turns++, no TurnUserInput transcript append, because
// nothing about this turn is user-typed text -- the input already ran through
// turn/steer's (or drain's, or promote's) accept path, and only needs
// somewhere to land.
//
// identity is the steer's: its client mutation id, and the turn id
// claimSteeringCarrierInput reserved and published to ActiveTurnID before this
// call, handed to the daemon via onRunnable before the model call starts. It
// is the pending steer's own reserved id -- not a freshly minted one -- so the
// id the client was told in its Applied receipt is the id that actually runs,
// which is what lets an interrupt's fence match the turn it cancels.
//
// It returns errSteeringCarrierStoodDown when there is nothing for a model
// request to carry -- a race with a turn that drained the steering first, or
// a steer whose skill selection could not be prepared, whose failure the drain
// recorded and announced -- so processOneInput stands down, and any other
// error when the steer's append failed: the turn is failed and the input
// ends, the same policy a queued message whose append fails gets
// (acceptUserInput), with the steer still accepted, parked runnable, for the
// next external wake to carry.
func (s *Session) acceptSteeringCarrierInput(ctx context.Context, identity queuedClientMutationIdentity) error {
	turnID := identity.StableTurnID
	// A steering carrier is a top-level accept path like every sibling
	// accept*Input: reset active/completed provenance up front so the carrier
	// turn never inherits the previous turn's lineage. It runs before the
	// stand-down check too, so a carrier that does not proceed still does not
	// leave a stale completed set for subagent.followUpProvenance to read
	// (issue #185 item 2). The drain below re-unions the carried steer's own
	// provenance, so nothing legitimate is erased.
	s.replaceActiveProvenance(nil)
	if !s.hasPendingUserSteering() {
		s.finishNotificationNoop()
		return errSteeringCarrierStoodDown
	}
	s.repairOrphanedToolResults(context.Background(), "before accepting steering carrier")
	// The announce precedes every content event of the turn, the same as
	// acceptNotificationInput's boundary: content emitted before it would be
	// attributed to the turn before this one.
	if s.servedByDaemon() {
		s.emit(events.TurnStartedData{TurnID: turnID})
	}
	// The carrier turn runs on resume semantics too: the drained human-note
	// steer it carries is already in the store, and this projection puts the
	// full current notes beside it.
	s.maybeAppendNotesContext()
	// Recorded for the duration of the drain so a skill-selection failure for
	// THIS client mutation id (recordFailedSteeringSelection, session_queue.go)
	// can tag its TurnFailure as a resolution boundary too — this turn's mere
	// acceptance already cleared askPending before the drain ever ran. Cleared
	// by defer so a panic mid-drain cannot leak the claim id: a leaked id
	// would let an unrelated later recordFailedSteeringSelection mistag its
	// own TurnFailure SteeringCarrier and wrongly resolve an ask on restore.
	s.setSteeringCarrierClaimDrain(identity.ClientMutationID)
	defer s.setSteeringCarrierClaimDrain("")
	delivered := s.injectDrainedSteering()
	switch s.carrierSteerOutcome(identity) {
	case carrierSteerUndelivered:
		// The steer this turn exists to carry is back in the queue: its
		// transcript append failed. A model request now would carry nothing,
		// and a clean completion would let the drain ladder claim the same
		// steer again, and again. Fail the turn already announced above and
		// end the input. (A steer whose append landed and whose store mark
		// did not is delivered, and the turn proceeds.)
		err := fmt.Errorf("steering carrier %s: its steering was not recorded and stays queued", turnID)
		// Tagged only when the claimed steer itself answers the ask
		// (steeringCarrierClaimAnswersAsk, the same journal-kind check the
		// entry clear uses): a human-note carrier's entry clear already left
		// askPending set, so its failure must not be a resolution boundary
		// either, or restore would diverge from live.
		if s.steeringCarrierClaimAnswersAsk(identity) {
			s.emitSteeringCarrierTurnFailure(errorDataFromError(err), identity.ClientMutationID)
		} else {
			s.emitTurnFailure(errorDataFromError(err))
		}
		return err
	case carrierSteerFailed:
		// The drain recorded the selection failure of the steer this turn was
		// claimed for and announced it on this turn (recordFailedSteeringSelection's
		// error event); that steer is retired. The drain went on past it, so
		// the turn stands down only when it delivered nothing at all: a
		// steer queued behind the failed one is in the transcript now, and
		// this is the request that reads it.
		if delivered {
			return nil
		}
		s.finishProcessingAtBoundary(ctx, SessionIdle)
		return errSteeringCarrierStoodDown
	}
	return nil
}

// errSteeringCarrierStoodDown is acceptSteeringCarrierInput's answer when the
// steering it was claimed for is already gone: not a failure, a turn that has
// nothing to do.
var errSteeringCarrierStoodDown = errors.New("steering carrier: nothing left to carry")

// releaseSteeringCarrierClaim hands back the active-turn slot a steering
// carrier's claim published, and is a no-op for any other queued identity: a
// queued message's slot is cleared by its own completion, or kept for restore
// to re-run the claimed entry under.
func (s *Session) releaseSteeringCarrierClaim(identity queuedClientMutationIdentity) {
	if identity.SteeringCarrier {
		s.releaseRunningTurnID(identity.StableTurnID)
	}
}
