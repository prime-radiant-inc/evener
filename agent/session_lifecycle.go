package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

var errBareTextWithoutResultTool = errors.New("model returned bare text without calling result tool")
var errEmptyResponseExhausted = errors.New("model returned empty response")
var errStreamUnavailable = errors.New("stream unavailable")

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
)

// retryTracker counts the degenerate (empty / bare-text) model responses seen so
// far in one input turn. consecutiveEmpty and consecutiveBareText reset once the
// model produces tool calls; totalEmpty bounds burst-and-recover spirals across
// the whole turn.
type retryTracker struct {
	consecutiveEmpty    int
	totalEmpty          int
	consecutiveBareText int
}

// Close shuts the session down once (subsequent calls are no-ops via
// closeOnce). It marks the session closing/closed, cancels in-flight
// LLM calls, cleans up the environment (killing child processes), runs the
// SessionEnd hooks, emits EventSessionEnd with the final state, closes
// subagents, the MCP manager, and the transcript, exports the ATIF trajectory
// when configured for the root session, removes any embedded skills directory,
// waits for in-flight event emitters to finish, and closes the events channel.
func (s *Session) Close() {
	s.close(true)
}

func (s *Session) close(cleanupEnv bool) {
	s.closeOnce.Do(func() {
		s.responseSideEffectsMu.Lock()
		s.mu.Lock()
		turns := s.modelResponses
		emitEnd := !s.sessionEndEmitted
		s.sessionEndEmitted = true
		s.closing = true
		s.state = SessionClosed
		// Mark closing BEFORE draining so a spawn or namer launch racing teardown
		// is either registered here (and cancelled below) or observes closing and
		// refuses — there is no window for a late goroutine to escape the drain.
		// The map is cleared under the lock; children are closed OUTSIDE the lock
		// (sub.sess.Close() acquires its own mu).
		subs := s.subagents.drainForClose()
		s.mu.Unlock()
		s.responseSideEffectsMu.Unlock()
		s.subagents.waitForReconstructions()
		s.subagents.waitForReconstructionSideEffects()

		// Spec Appendix B graceful shutdown ordering:
		// 1. Cancel in-flight LLM calls.
		if s.cancelFunc != nil {
			s.cancelFunc()
		}

		// 2. Mark and stop durable jobs before environment cleanup can reap
		// process handles and make deliberate shutdown look like runtime failure.
		// Keep the parent store open while child sessions close; nested child
		// jobs may still need to forward their terminal events to the parent.
		var jobManagerCloseErr error
		if s.jobManager != nil {
			jobManagerCloseErr = s.jobManager.closeRuntimeState()
		}

		// 3. Close subagents before shared environment cleanup; child sessions
		// can own durable jobs whose process handles live in the parent env. The
		// parent owns cleanup of the shared env, so child closes skip env cleanup.
		for _, sub := range subs {
			sub.sess.close(false)
		}

		// Native worktree tools spec §9 step 4 + §5 close-unlock: dispose the
		// isolation delegate lanes this session created and unlock its own
		// occupied managed worktree. Both run AFTER child sessions close (a
		// mid-tree delegate parent disposes its own lanes first) and BEFORE the
		// store closes, because disposal's disposed mark is a durable append
		// (step 5). env Cleanup() below (step 4 of the ordering) kills any
		// residual lane process; a residual writer racing the clean check is
		// self-healing since disposal's `git worktree remove` runs without
		// --force and downgrades to keep on a dirty refusal.
		s.disposeDelegateLanesAtClose()
		s.unlockOwnManagedWorktreeAtClose()

		if s.jobManager != nil {
			jobManagerCloseErr = errors.Join(jobManagerCloseErr, s.jobManager.closeStoreOnly())
		}
		if jobManagerCloseErr != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("job manager close incomplete: %v", jobManagerCloseErr)})
		}

		// 4. Kill any remaining child processes (SIGTERM → wait 2s → SIGKILL).
		if cleanupEnv {
			s.currentEnv().Cleanup()
		}

		// SessionEnd hooks (best-effort, bounded timeout)
		if s.hookRunner != nil {
			hookCtx, hookCancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.hookRunner.RunSessionEnd(hookCtx, s.hookInput(plugin.HookSessionEnd))
			hookCancel()
		}

		// 5-6. Emit SESSION_END with final state.
		if emitEnd {
			s.emit(events.EventSessionEnd, events.SessionEndData{
				Reason: "session_closed",
				State:  string(SessionClosed),
				Turns:  turns,
			})
		}

		if s.mcpMgr != nil {
			s.mcpMgr.Close()
		}

		if s.transcript != nil {
			_ = s.transcript.Close()
		}

		// Export ATIF trajectory if configured (root session only, after transcript flush).
		if s.cfg.ExportATIFPath != "" && s.stateDir != "" && s.cfg.spawn.depth == 0 {
			tpath := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
			if err := exportATIF(tpath, s.cfg.ExportATIFPath, s.cfg.ExportATIFProviderHandles); err != nil {
				s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("ATIF export failed: %v", err)})
			}
		}

		if s.embeddedSkillsDir != "" {
			_ = os.RemoveAll(s.embeddedSkillsDir) // best-effort temp-dir cleanup during shutdown
		}

		// 8. Reassert closed in case an in-flight turn reached a late state transition.
		s.mu.Lock()
		s.state = SessionClosed
		s.mu.Unlock()
		s.toolEventsWG.Wait()
		// Join detached event emitters (subagent runs, session namer) so their
		// events are delivered before the channel closes. They are already
		// cancelled above (child Close + cancelFunc), so this returns promptly.
		s.sendersWG.Wait()
		// Close under eventsMu so a caller-owned emit() (Enqueue/DrainAsSteer or
		// the ProcessInput loop — goroutines the session cannot join) can never
		// send on the closed channel. This replaces the old emit() recover().
		s.eventsMu.Lock()
		s.eventsClosed = true
		close(s.events)
		s.eventsMu.Unlock()
	})
}

func (s *Session) discardRestoredCandidate() {
	s.closeOnce.Do(func() {
		s.responseSideEffectsMu.Lock()
		s.mu.Lock()
		s.sessionEndEmitted = true
		s.closing = true
		s.state = SessionClosed
		subs := s.subagents.drainForClose()
		s.mu.Unlock()
		s.responseSideEffectsMu.Unlock()

		if s.cancelFunc != nil {
			s.cancelFunc()
		}

		if s.jobManager != nil && s.jobManager.store != nil {
			_ = s.jobManager.store.Close()
		}
		for _, sub := range subs {
			sub.sess.discardRestoredCandidate()
		}
		if s.mcpMgr != nil {
			s.mcpMgr.Close()
		}
		if s.transcript != nil {
			_ = s.transcript.Close()
		}
		if s.embeddedSkillsDir != "" {
			_ = os.RemoveAll(s.embeddedSkillsDir)
		}
		s.mu.Lock()
		s.state = SessionClosed
		s.mu.Unlock()
		s.toolEventsWG.Wait()
		s.sendersWG.Wait()
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
	// EntryWatchDelivery is a watch-originated delegate turn. It may decide a
	// delivered watch frame needs no action; non-empty bare text is retained in
	// the child transcript as an internal disposition and does not require
	// communicate.
	EntryWatchDelivery
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
	// response to a watch-delivery or notification turn is an acknowledgement, not a
	// glitch, and no user awaits a reply.
	finishIdle noCallsRoute = iota
	// runNoToolCalls routes through the empty/bare-text retry budget.
	runNoToolCalls
)

// routeNoToolCalls decides the no-tool-calls route for a round from the input kind
// and whether the round had no content. It is pure and total over EntryKind: only
// a non-empty watch-delivery or notification turn finishes idle; everything else
// (including any empty round) routes through the retry budget.
func routeNoToolCalls(kind EntryKind, noContent bool) noCallsRoute {
	if (kind == EntryWatchDelivery || kind == EntryNotification) && !noContent {
		return finishIdle
	}
	return runNoToolCalls
}

// drainInputs is the snapshot the drain loop feeds selectDrainNextAction after a
// completed (non-error) turn: the kind of the turn that just ran, whether a goal
// continuation is already deferred, the popped follow-up text and queued message
// (its text plus image count), whether any job notifications are pending, and
// whether the turn just rested SessionAwaiting (spec §5.3's drain-ladder gate).
type drainInputs struct {
	RanKind              EntryKind
	HaveDeferredCont     bool
	FollowUp             string
	QueuedText           string
	QueuedImages         int
	NotificationsPending bool
	Awaiting             bool
}

// drainAction is the next action the drain loop takes after a completed turn.
type drainAction int

const (
	// runFollowUp runs the popped per-turn follow-up as the next turn.
	runFollowUp drainAction = iota
	// runQueued runs the popped queued user message as the next turn.
	runQueued
	// armGoalGate marks that the goal-continuation gate folds this turn and, with no
	// notification pending, the resulting turn is the deferred continuation (if the
	// fold arms one) or idle.
	armGoalGate
	// runNotification runs a pending job-notification turn next.
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
// drain-ladder gate): a turn that just posted a question holds its follow-up,
// notification, and goal rungs — none of them may drive the model past the
// unanswered questions — and only the queued-input rung stays live, because a
// message the user queued mid-turn IS the reply (they are demonstrably present).
//
// Otherwise, priority: a pending follow-up, then a queued user message, then a
// pending notification, then the goal gate's deferred continuation, then idle.
// The goal gate always folds BEFORE the notification turn runs: the wrapper arms
// on !skipGoalGate before acting on runNotification, so the fold stays ahead of
// the notification interleave even though the notification is the turn that runs
// next. armGoalGate is returned only when the gate is eligible AND no
// notification preempts, so the resulting turn depends on the (side-effectful)
// fold result.
func selectDrainNextAction(in drainInputs) (action drainAction, skipGoalGate bool) {
	skipGoalGate = in.RanKind == EntryNotification || in.HaveDeferredCont || in.Awaiting
	if in.Awaiting {
		if strings.TrimSpace(in.QueuedText) != "" || in.QueuedImages > 0 {
			return runQueued, skipGoalGate
		}
		return goIdle, skipGoalGate
	}
	switch {
	case strings.TrimSpace(in.FollowUp) != "":
		return runFollowUp, skipGoalGate
	case strings.TrimSpace(in.QueuedText) != "" || in.QueuedImages > 0:
		return runQueued, skipGoalGate
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
// then loops to drain per-turn follow-ups and queued user messages, running
// each as a further turn. On a cancelled turn it applies interrupt semantics —
// flipping the session back to idle (unless closed), appending a system-reminder
// interrupt marker, and optionally draining the queue head — then returns the
// partial output with the error. It emits EventSessionEnd for the input and
// returns an error when the session is closed or a turn fails.
func (s *Session) ProcessInputKind(ctx context.Context, input string, images []ImageAttachment, kind EntryKind) (string, error) {
	return s.processInputKindWithProvenance(ctx, input, images, kind, nil)
}

func (s *Session) processInputWithProvenance(ctx context.Context, input string, images []ImageAttachment, p *provenance.Causal) (string, error) {
	return s.processInputKindWithProvenance(ctx, input, images, EntryUserInput, p)
}

func (s *Session) processInputKindWithProvenance(ctx context.Context, input string, images []ImageAttachment, kind EntryKind, inputProvenance *provenance.Causal) (string, error) {
	// Reset so SESSION_END can fire at the end of this input's processing.
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return "", errors.New("session is closed")
	}
	// Entry gate (spec §5.3): while a question is pending, an autonomous wake —
	// EntryNotification, EntryContinuation, EntryWatchDelivery — is refused here,
	// before the Processing transition below (processOneInput's
	// setStateIfOpenLocked(SessionProcessing)) ever runs. No state flip, no event
	// emission: round 4's bug was that transition firing unconditionally before
	// dispatch, so a delegate or notification finishing while the user reads the
	// question silently turned the needs-you signal off. Refusing here leaves the
	// wake source's own durable queue (jobstore notifications, the goal engine,
	// the watch outbox) untouched, so it redelivers once the boundary drains
	// after the user's reply. EntryUserInput is always accepted — it is how the
	// reply resolves awaiting (spec §5.2).
	//
	// Gated on the pending set, not raw state (attention-status-model v5
	// reconciliation): SessionAwaiting used to imply "a question is pending" —
	// the only producer of the state before the merge — but the general
	// inbox-semantics upgrade (armAwaitingAtSettle) now also rests a session
	// awaiting after any clean, output-producing turn with nothing else in
	// flight, no ask involved. Their spec's own consequence for that case is
	// the opposite of a hold: "async wakes re-arm by design." Only a genuine
	// pending question is a stronger stop than the wake (a delegate finishing
	// while the user reads a QUESTION must not silently resolve it out from
	// under them); a general re-arm has nothing pending to protect.
	if len(s.askPending) > 0 && kind != EntryUserInput {
		s.mu.Unlock()
		return "", nil
	}
	s.sessionEndEmitted = false
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
	}()

	outputs := []string{}
	next := input
	nextImages := images
	nextKind := kind
	nextProvenance := provenance.Clone(inputProvenance)
	processCtx := ctx
	// A goal continuation decided at the gate is deferred across an interleaved
	// notification turn and then run INLINE (via continue), so the goal advances
	// without depending on the idle kick (which no-ops when kickFunc==nil, e.g.
	// one-shot `serf run`). haveDeferredCont guards both the "don't re-fold while a
	// continuation is already pending" case and the inline run below. The gate-time
	// render is NOT cached: the inline site re-reads the store and re-renders the
	// current objective, so a clear/retarget during the interleaved notification turn
	// cannot run a stale continuation.
	var haveDeferredCont bool
	for {
		// Capture the kind actually being processed this iteration before the
		// follow-up reset below; the goal gate needs it to know whether the turn
		// that just ran was a goal continuation (which accrues toward the
		// no-progress streak and iteration count) or a user/follow-up turn (which
		// does not — /par #4).
		ranKind := nextKind
		out, progressed, err := s.processOneInput(processCtx, next, nextImages, nextKind, nextProvenance)
		// Follow-up turns (after the first) carry no attachments and are
		// user-driven, not continuations.
		nextImages = nil
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
			if isTurnCancellation(processCtx, err) {
				s.finishProcessingAtBoundary(processCtx, SessionIdle)
				// An interrupted turn ends idle even if it posted questions (spec
				// §5.1): the user is demonstrably present. The cards stay rendered
				// from the transcript regardless; only the pending set — which
				// exists purely to drive the boundary check — clears.
				s.clearAskPending()
				s.mu.Lock()
				closed := s.closingOrClosedLocked()
				turns := s.modelResponses
				emitEnd := !s.sessionEndEmitted && !closed
				if emitEnd {
					s.sessionEndEmitted = true
				}
				s.mu.Unlock()

				if !closed {
					// Append a system-reminder turn so the next request to
					// the model includes the interrupt notice in history.
					// This is the user-visible "interrupted here" marker
					// in the transcript that consumers (TUI / hub) render.
					interruptMsg := "<SYSTEM-REMINDER>The user interrupted the previous turn before it completed. Any partial tool output above is incomplete. Wait for the user's next message before continuing.</SYSTEM-REMINDER>"
					s.appendTurn(schema.TurnSteering, llm.User(interruptMsg))
					s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: interruptMsg})
				}
				if emitEnd {
					s.emit(events.EventSessionEnd, events.SessionEndData{
						Reason:      "interrupted",
						State:       string(SessionIdle),
						Turns:       turns,
						Interrupted: true,
					})
				}
				if !closed {
					if queued := s.popQueueHead(); strings.TrimSpace(queued.Text) != "" || len(queued.Images) > 0 {
						if drainCtx, ok := queuedInputDrainContext(processCtx, err); ok {
							next = queued.Text
							nextImages = queued.Images
							processCtx = drainCtx
							s.mu.Lock()
							s.sessionEndEmitted = false
							s.mu.Unlock()
							continue
						}
						s.pushQueueHead(queued)
					}
				}
			}
			// The drain-loop gate below is unreachable on an error return, so a
			// goal that is still active when the turn fails must be terminated
			// here (spec §2/C11). terminateGoalOnError no-ops on a genuine user
			// interrupt, leaving the goal active to resume after the next turn.
			s.terminateGoalOnError(processCtx, err)
			s.finishProcessingAtBoundary(processCtx, SessionIdle)
			return strings.Join(outputs, "\n"), err
		}
		// Drain the next action after a completed (non-error) turn. The pops and the
		// goal-gate fold are side effects kept here; selectDrainNextAction is the pure
		// priority decision over their results. popQueueHead is reached only when no
		// follow-up is pending (it consumes a queued message), matching the original
		// short-circuit; peekNotifications is likewise consulted only at its ladder
		// rung and only for a non-notification, non-awaiting turn.
		//
		// awaiting reflects the boundary state the turn that just ran left behind
		// (spec §5.3's drain-ladder gate). The follow-up pop itself is skipped while
		// awaiting — not popped and then discarded — so a follow-up queued during
		// the asking turn (FollowUp is public API with no gating of its own) is left
		// intact in s.followups for the next drain cycle instead of being silently
		// dropped by an awaiting rest that won't run it.
		//
		// Proof this capture is equivalent to askPendingCount() > 0, even though
		// it reads raw state directly (unlike SetGoal/settleGoalOnIdle/Compact,
		// which key on the pending-ask set): processOneInput unconditionally
		// resets s.state to SessionProcessing and s.askPending to nil at entry
		// (above, "s.setStateIfOpenLocked(SessionProcessing)" / "s.askPending =
		// nil") for every accepted entry kind, so whatever this call's state was
		// before this iteration's processOneInput call is wiped before it runs.
		// The only path that can move it OUT of SessionProcessing before
		// processOneInput returns on the clean-completion path is
		// deliverIfCommunicated (session_tool_round.go), which writes
		// SessionAwaiting via finishProcessingAtBoundary exactly when
		// askedThisRound (this round grew askPending) — i.e. exactly when
		// askPendingCount() > 0. The general non-ask idle→awaiting upgrade,
		// armAwaitingAtSettle, is the only OTHER writer of SessionAwaiting
		// reachable from this loop, but it runs strictly later, at the terminal
		// settle (below, s.armAwaitingAtSettle), which is unconditionally
		// followed by this call's own return — it never runs before this
		// capture within the same ProcessInputKind call, and never more than
		// once per call. So at this exact point, s.State() == SessionAwaiting
		// holds if and only if askPendingCount() > 0 here; the two are
		// interchangeable at this capture only, not as a general rule elsewhere
		// in this file.
		awaiting := s.State() == SessionAwaiting
		var fu string
		if !awaiting {
			fu = s.popFollowUp()
		}
		var queued queuedInput
		if strings.TrimSpace(fu) == "" {
			// kata 111a / t5j6: each drained queued message becomes a distinct user
			// turn; its image attachments ride along as ContentImage parts.
			queued = s.popQueueHead()
		}
		noFollowUpOrQueued := strings.TrimSpace(fu) == "" &&
			strings.TrimSpace(queued.Text) == "" && len(queued.Images) == 0
		notificationsPending := false
		if noFollowUpOrQueued && !awaiting && ranKind != EntryNotification {
			notificationsPending = s.peekNotifications() > 0
		}
		action, skipGoalGate := selectDrainNextAction(drainInputs{
			RanKind:              ranKind,
			HaveDeferredCont:     haveDeferredCont,
			FollowUp:             fu,
			QueuedText:           queued.Text,
			QueuedImages:         len(queued.Images),
			NotificationsPending: notificationsPending,
			Awaiting:             awaiting,
		})

		switch action {
		case runFollowUp:
			next = fu
			s.mu.Lock()
			s.sessionEndEmitted = false
			s.mu.Unlock()
			continue
		case runQueued:
			next = queued.Text
			nextImages = queued.Images
			s.mu.Lock()
			s.sessionEndEmitted = false
			s.mu.Unlock()
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
		// Notification interleave (priority 3): a pending job notification runs
		// AFTER the fold above but BEFORE the deferred continuation, so it is
		// transparent to goal accounting (the just-finished continuation already
		// folded). The queue is consumed inside acceptNotificationInput when the
		// EntryNotification turn runs (an empty queue there is a no-op, but the peek
		// guards against it). After a notification turn this rung is skipped (selector
		// gate) because job notifications may have been requeued.
		if action == runNotification {
			next = ""
			nextImages = nil
			nextKind = EntryNotification
			continue
		}
		// Run the deferred continuation INLINE (via continue, not the idle kick) so
		// the goal advances even when kickFunc==nil (e.g. one-shot `serf run` with a
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
		if !awaiting && haveDeferredCont {
			haveDeferredCont = false
			// Re-validate against the goal store before running: the user may have
			// cleared (/goal clear) or retargeted (/goal <new>) the goal during the
			// interleaved notification turn above, making the render the gate computed
			// at fold time stale. currentGoalContinuation re-reads the store read-only
			// (the fold already happened at the gate — no RecordContinuation re-runs
			// here). If the goal is no longer active, drop the stale continuation and
			// fall through to settle + idle; if it is active, run a FRESH render of the
			// CURRENT objective so a retarget pursues the new goal.
			s.mu.Lock()
			if !s.closingOrClosedLocked() {
				s.sessionEndEmitted = false
			}
			s.mu.Unlock()
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
		goalKicked := s.settleGoalOnIdle()
		s.armAwaitingAtSettle(strings.TrimSpace(strings.Join(outputs, "\n")) != "", goalKicked)
		s.mu.Lock()
		if !s.sessionEndEmitted {
			s.sessionEndEmitted = true
			turns := s.modelResponses
			s.mu.Unlock()
			s.emit(events.EventSessionEnd, events.SessionEndData{
				Reason: "input_complete",
				State:  s.WireState(),
				Turns:  turns,
			})
		} else {
			s.mu.Unlock()
		}
		return strings.Join(outputs, "\n"), nil
	}
}

func (s *Session) processOneInput(ctx context.Context, input string, images []ImageAttachment, kind EntryKind, inputProvenance *provenance.Causal) (out string, progressed bool, err error) {
	s.setActiveEntryKind(kind)
	defer s.setActiveEntryKind(EntryUserInput)

	// Flush meta.json on every exit from this function — normal return, error
	// return, ctx cancellation, retry-budget exhaustion, or panic. Without
	// this, in-memory modelResponses bumps that happen between happy-path
	// autosaves (e.g. pause_turn, empty-response retries that exhaust) stay
	// stranded if any exit path is taken before the next tool round. Kata ztne.
	defer func() {
		if r := recover(); r != nil {
			s.maybeAutoSave()
			panic(r)
		}
		s.maybeAutoSave()
	}()

	// Derive a context that cancels when either the caller's ctx or the session ctx cancels.
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-s.sessionCtx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	defer cancel()

	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return "", false, errors.New("session is closed")
	}
	s.setStateIfOpenLocked(SessionProcessing)
	s.comm = communicateResult{}
	// Pending asks resolve with this accepted turn (spec §5.2): clear here,
	// beside comm's reset, under the lock already held (not via the
	// clearAskPending helper, which takes s.mu itself and would deadlock).
	s.askPending = nil
	s.watchCallbackDelivered = false
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.emit(events.EventError, errorDataFromError(ctx.Err()))
		s.finishProcessingAtBoundary(ctx, SessionIdle)
		return "", false, ctx.Err()
	default:
	}

	if kind == EntryContinuation {
		s.acceptContinuationInput(ctx, input)
	} else if kind == EntryNotification {
		if !s.acceptNotificationInput(ctx) {
			return "", false, nil
		}
	} else if !s.acceptUserInput(ctx, input, images, inputProvenance, kind == EntryUserInput) {
		return "", false, nil
	}

	var toolSigs []string
	var lastText string // accumulated assistant text for round-limit return
	ctxWarned := false
	contentFilterRetried := false // track whether we've already tried recovering from a content filter error
	var tracker retryTracker

	// Continuation (goal) turns clamp the per-input round cap to GoalTurnMaxRounds
	// (spec §2b/C13); a config of <0 is "unbounded", so a bare min would be wrong.
	roundCap := goalRoundCap(s.cfg.MaxToolRoundsPerInput, kind)

	for round := 0; roundCap < 0 || round < roundCap; round++ {
		roundStart := s.sclock().Now()
		// Snapshot the pending-ask count before this round's tool calls run, so
		// the delivery check below can compute the per-round delta (spec §5.1):
		// a round that posts a question ends the turn, and the delta is the
		// robust, self-contained way to detect that (rather than trusting the
		// global count, which a later round should never even reach).
		askBefore := s.askPendingCount()
		var timings events.RoundTimings
		timings.Round = round

		s.mu.Lock()
		s.totalRounds++
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			s.emit(events.EventError, errorDataFromError(ctx.Err()))
			s.finishProcessingAtBoundary(ctx, SessionIdle)
			return "", progressed, ctx.Err()
		default:
		}

		profile, sys, history, req, reqEffort := s.prepareModelRequest(ctx, round, &timings)
		if kind == EntryWatchDelivery {
			req.ToolChoice = &llm.ToolChoice{Mode: "auto"}
		}

		// --- Phase: LLMCall ---
		tPhaseStart := s.sclock().Now()

		s.noteParentJobActivity(jobPhaseAwaitingModel)
		modelResp, req, attempt, err := s.callModelWithFallback(ctx, profile, req, reqEffort, round)
		resp := modelResp.Response

		timings.LLMCall = time.Since(tPhaseStart)

		if err == nil {
			if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
				return "", progressed, abortErr
			}
		}

		s.logAPICall(round, roundStart, timings.LLMCall, sys, len(history), req, resp, err, attempt)

		if err != nil {
			retry, ferr := s.handleModelError(ctx, err, req, &contentFilterRetried)
			if retry {
				continue
			}
			return "", progressed, ferr
		}

		if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
			return "", progressed, abortErr
		}

		// Accumulate usage and record exact input token count for pressure calculation.
		s.recordResponseUsage(resp, req)

		// Context window awareness: emit a warning when we exceed ~80% of the profile's context window.
		if !ctxWarned {
			if s.maybeWarnContextUsage(profile, req) {
				ctxWarned = true
			}
		}

		// Best-effort nudge: when pressure crosses WarnThreshold, steer the agent to
		// self-compact at its next clean seam. recordResponseUsage above just refreshed
		// lastInputTokens, so pressure reflects the round that completed. The latch keeps
		// this one-shot until the next compaction; the nudge is queued as steering and
		// reaches the model before the next round's model call.
		s.maybeNudgeSelfCompact(len(sys))

		if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
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

		if abortErr := s.emitAssistantResponse(ctx, resp, modelResp, txt, skipHistory, attempt); abortErr != nil {
			return "", progressed, abortErr
		}
		// pause_turn: model needs another turn (e.g. server-side web search still running).
		if resp.Finish.Reason == llm.FinishReasonPauseTurn {
			round-- // Don't count pause_turn as a tool round.
			timings.TotalRound = time.Since(roundStart)
			timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
			s.emit(events.EventRoundTimings, timings)
			continue
		}

		// Reverse-map provider-specific tool names to canonical names for registry lookup.
		for i := range calls {
			calls[i].Name = s.canonicalToolName(calls[i].Name)
		}

		// Progress signal for the goal engine: a turn "progressed" iff it made a
		// real mutating tool call — !ReadOnly AND not the result/communicate tool
		// AND not task_list (plan-spam and "I'm done" messages must not count).
		// Accumulated as a turn-level OR across the turn's rounds (spec §2).
		progressed = progressed || s.callsMadeProgress(calls)

		if len(calls) == 0 {
			// A bare-text (non-empty) response to a watch-delivery or notification turn
			// is the agent acknowledging a frame it has nothing to act on (it often
			// already read the job's output itself). Finish idle instead of scolding it
			// toward a no-op communicate — the text is already in the transcript, and a
			// system-initiated turn carries no user awaiting a reply. A truly empty
			// (no-content) response is a model glitch and still routes through the
			// empty-retry path below.
			if routeNoToolCalls(kind, noContent) == finishIdle {
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
			s.emit(events.EventRoundTimings, timings)
			continue
		}

		// Model produced tool calls — reset retry counters.
		tracker.consecutiveEmpty = 0
		tracker.consecutiveBareText = 0

		if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
			if ctx.Err() != nil && !s.isClosingOrClosed() {
				s.appendCanceledToolResults(calls, nil, abortErr)
			}
			return "", progressed, abortErr
		}

		// --- Phase: ToolExec ---
		tPhaseStart = s.sclock().Now()

		// Execute tool calls (possibly in parallel) and send results back.
		s.noteParentJobActivity(jobPhaseToolRunning)
		results, execErr := s.execToolBatch(ctx, calls, profile)
		if execErr != nil {
			return "", progressed, execErr
		}

		timings.ToolExec = time.Since(tPhaseStart)

		// --- Phase: Persistence ---
		tPhaseStart = s.sclock().Now()

		if persistErr := s.persistToolResults(ctx, calls, results); persistErr != nil {
			return "", progressed, persistErr
		}

		timings.Persistence = time.Since(tPhaseStart)

		// --- Phase: AfterAction ---
		tPhaseStart = s.sclock().Now()

		// Notify the context strategy that a tool round completed.
		if afterErr := s.notifyStrategyAfterAction(ctx); afterErr != nil {
			return "", progressed, afterErr
		}

		timings.AfterAction = time.Since(tPhaseStart)

		yieldToObserverCallback, steerErr := s.injectPostToolSteering(ctx, calls, &toolSigs)
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
		s.emit(events.EventRoundTimings, timings)

		// communicate sets the flag, or this round posted question(s) (spec
		// §5.1) — either ends the turn; deliverIfCommunicated decides the
		// boundary state and composes them.
		askedThisRound := s.askPendingCount() > askBefore
		if done, text := s.deliverIfCommunicated(ctx, askedThisRound); done {
			return text, progressed, nil
		}
		if yieldToObserverCallback {
			s.finishProcessingAtBoundary(ctx, SessionIdle)
			return "", progressed, nil
		}
	}

	s.emit(events.EventTurnLimit, events.TurnLimitData{MaxToolRoundsPerInput: s.cfg.MaxToolRoundsPerInput})
	s.finishProcessingAtBoundary(ctx, SessionIdle)
	return lastText, progressed, nil
}

// acceptUserInput records a new user input at the start of an input turn: it
// repairs any orphaned tool results, enforces MaxTurns, and returns proceed=false
// (the caller returns "", nil) when the turn limit is already reached. Once the
// turn is accepted, it increments the turn counter. For real external user
// input only, it drains any pending resume SessionStart hook output before
// appending the user turn. It then emits EventUserInput, appends the user turn,
// launches the session namer, runs UserPromptSubmit hooks, drains any pending
// steering, and returns proceed=true.
func (s *Session) acceptUserInput(ctx context.Context, input string, images []ImageAttachment, inputProvenance *provenance.Causal, drainResumeSessionStart bool) (proceed bool) {
	// A new top-level input starts a fresh causal context: replace active
	// provenance with the input's provenance, or empty provenance for ordinary
	// external user input.
	s.replaceActiveProvenance(inputProvenance)
	s.repairOrphanedToolResults("before accepting new input")

	// Count conversation turns (user input -> model response pairs), not LLM round-trips.
	// Check the limit before incrementing so MaxTurns=N allows exactly N inputs.
	s.mu.Lock()
	turns := s.turns
	s.mu.Unlock()

	if s.cfg.MaxTurns > 0 && turns >= s.cfg.MaxTurns {
		s.emit(events.EventTurnLimit, events.TurnLimitData{MaxTurns: s.cfg.MaxTurns})
		s.finishProcessingAtBoundary(ctx, SessionIdle)
		return false
	}

	s.mu.Lock()
	s.turns++
	userInputTurn := len(s.history) + 1
	s.mu.Unlock()

	if drainResumeSessionStart {
		// Resume SessionStart hooks are intentionally lazy: they are recorded during
		// restore, but their model-facing output must join the first accepted real user
		// turn, never a MaxTurns-rejected input or an autonomous notification,
		// continuation, or watch turn. Drain them after the proceed gate accepts the
		// turn and before recording the user turn so their model context precedes the
		// prompt it applies to in the first resumed model request.
		s.drainPendingSessionStartHooksForUserTurn(ctx)
	}

	s.emit(events.EventUserInput, events.UserInputData{
		Text:   input,
		Images: userInputImagesFromAttachments(images),
		Turn:   userInputTurn,
	})
	s.appendTurn(schema.TurnUserInput, buildUserInputMessage(input, images))
	s.launchInitialPromptNamer(s.sessionCtx, input)

	// UserPromptSubmit hooks
	if s.hookRunner != nil {
		hi := s.hookInput(plugin.HookUserPromptSubmit)
		// Send the official "prompt" field and the legacy "user_prompt" alias with
		// the same value: Claude-style hooks read "prompt".
		hi.Prompt = input
		hi.UserPrompt = input
		result := s.hookRunner.RunUserPromptSubmit(ctx, hi)
		for _, m := range result.ModelContext {
			s.deliverHookContext(m)
		}
		for _, m := range result.UserMessages {
			s.deliverHookUserMessage(m)
		}
	}

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	for _, msg := range s.drainSteeringForTurn() {
		s.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
		s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}
	return true
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
func (s *Session) acceptContinuationInput(ctx context.Context, input string) {
	// A goal continuation is a fresh top-level input: reset active provenance so
	// the continuation turn's events do not inherit a prior watch origin.
	s.replaceActiveProvenance(nil)
	s.repairOrphanedToolResults("before accepting goal continuation")

	// Surface only a compact marker to the UI, not the full rendered continuation
	// prompt: the appwire projection turns EventGoalContinuation into a systemMessage,
	// so emitting the whole ~2.5KB prompt floods the thread every iteration (/par B6).
	// The model still receives the full prompt via the steering turn below.
	marker := goalContinuationMarker
	if snap, ok := s.getOrCreateGoalStore().Snapshot(); ok && snap.Objective != "" {
		marker = "Continuing toward: " + snap.Objective
	}
	s.emit(events.EventGoalContinuation, events.GoalContinuationData{Text: marker})
	s.appendTurn(schema.TurnSteering, llm.User(input))

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	for _, msg := range s.drainSteeringForTurn() {
		s.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
		s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}
}

// acceptNotificationInput records a job-completion notification turn at the
// start of an input turn. It mirrors acceptContinuationInput's framing — the
// drained queue is delivered to the model as a schema.TurnSteering reminder (a
// user-role message that expandHistory passes through without rendering a user
// bubble), so prepareModelRequest rebuilds the request from s.history and the
// reminder reaches the model THIS turn. Unlike acceptUserInput it skips the
// namer, the UserPromptSubmit hooks, the MaxTurns check, and the s.turns++
// accounting (a notification is not a user turn).
//
// It returns proceed=false on an empty queue, having set s.sessionEndEmitted so
// the drain loop's idle tail suppresses the phantom SESSION_END{input_complete} —
// an empty notification turn is a true no-op that makes no model request.
func (s *Session) acceptNotificationInput(ctx context.Context) (proceed bool) {
	// Drive signal (b) (spec §3): a child driven on its pending caller-targeted
	// watch sends may have no token queued yet (the drive was launched on the
	// hasPendingWatchSends signal, not a token enqueue). Enqueue the OWN session's
	// pending caller tokens onto its rail before draining so the frame renders in
	// this same drive turn. Caller-only — sidecar delivery stays at the boundary.
	s.enqueueOwnCallerWatchSendTokens()
	jobNotifs, retryJobNotifs, injectedJobNotifs := s.filterDeliverableJobNotifications(s.drainJobNotifications())
	hasSteering := s.hasPendingSteering()
	s.requeueJobNotifications(retryJobNotifs)
	injectedFailures := s.markJobNotificationsDelivered(injectedJobNotifs)
	s.requeueJobNotifications(injectedFailures)
	if len(jobNotifs) == 0 && !hasSteering {
		if len(retryJobNotifs) == 0 && len(injectedFailures) == 0 {
			s.resetJobNotificationRetry()
		}
		s.finishNotificationNoop()
		return false
	}

	s.repairOrphanedToolResults("before accepting notification")

	// A notification turn adopts the union of the provenance carried by the
	// notifications it delivers, so events the turn emits (and any watch it
	// retriggers) stamp the origin watch's lineage. The echo still delivers and is
	// classified self-influenced by the breaker (bounded by the runaway fuse), not
	// suppressed.
	var notificationProvenance *provenance.Causal
	for _, n := range jobNotifs {
		notificationProvenance = provenance.Union(notificationProvenance, n.notification.Provenance)
	}
	s.replaceActiveProvenance(notificationProvenance)

	if len(jobNotifs) > 0 {
		reminder := s.formatJobNotificationReminder(jobNotifs)
		if err := s.appendTurnDurably(schema.TurnSteering, llm.User(reminder)); err != nil {
			s.requeueJobNotifications(jobNotifications(jobNotifs))
			s.finishNotificationNoop()
			return false
		}
		s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: reminder})
	}
	deliveredFailures := s.markJobNotificationsDelivered(jobNotifs)
	s.requeueJobNotifications(deliveredFailures)
	if len(deliveredFailures) == 0 {
		s.resetJobNotificationRetry()
	}
	// Settle caller-targeted watch sends only after the durable reminder turn
	// persisted (above): the durable pending survives an appendTurnDurably
	// failure for re-token (at-least-once contract, spec §4.3).
	for _, d := range jobNotifs {
		if d.watchJM == nil {
			continue
		}
		if err := d.watchJM.settleWatchSendDelivered(d.watchCfg, d.watchState); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: "watch send settle failed: " + err.Error()})
		}
	}

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	for _, msg := range s.drainSteeringForTurn() {
		s.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
		s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}
	return true
}

func (s *Session) finishNotificationNoop() {
	s.finishProcessingAtBoundary(context.Background(), SessionIdle)
	s.mu.Lock()
	s.sessionEndEmitted = true
	s.mu.Unlock()
}

func (s *Session) filterDeliverableJobNotifications(raw []jobNotification) ([]deliverableJobNotification, []jobNotification, []deliverableJobNotification) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	survivors := make([]deliverableJobNotification, 0, len(raw))
	durableRaw := make([]jobNotification, 0, len(raw))
	seenTok := make(map[jobstore.WatchSendKey]bool)
	for _, n := range raw {
		if n.WatchSend != nil {
			jm, cfg, state, ok := s.resolveWatchSendToken(n.WatchSend)
			if !ok {
				continue // stale token: replaced, cleared, dropped, or settled
			}
			if seenTok[n.WatchSend.Key] {
				continue
			}
			seenTok[n.WatchSend.Key] = true
			n.watchSendFrame = state.Frame
			n.Provenance = provenance.Union(n.Provenance, state.Provenance)
			survivors = append(survivors, deliverableJobNotification{notification: n, watchJM: jm, watchCfg: cfg, watchState: state})
			continue
		}
		if n.Status == jobNotificationEventWatch {
			survivors = append(survivors, deliverableJobNotification{notification: n})
			continue
		}
		durableRaw = append(durableRaw, n)
	}
	if len(durableRaw) == 0 {
		return survivors, nil, nil
	}
	if s.jobManager == nil {
		return survivors, durableRaw, nil
	}
	recs, err := s.jobManager.store.Load()
	if err != nil {
		return survivors, durableRaw, nil
	}
	alreadyInjected := make(map[string]bool, len(durableRaw))
	for _, n := range durableRaw {
		rec := recs[n.JobID]
		if rec == nil || !jobstore.ShouldDeliver(rec) {
			continue
		}
		if _, ok := alreadyInjected[rec.JobID]; !ok {
			alreadyInjected[rec.JobID] = s.jobNotificationAlreadyInjected(rec.JobID)
		}
	}
	durableSurvivors, injected := classifyDurableNotifications(durableRaw, recs, alreadyInjected)
	survivors = append(survivors, durableSurvivors...)
	return survivors, nil, injected
}

// classifyDurableNotifications is the pure durable-notification classification
// core: given the drained durable notifications, the loaded job records, and a
// per-job "already injected into history" snapshot, it partitions the durable
// entries into fresh survivors and already-injected replays. It applies the
// ShouldDeliver gate and the terminal-generation dedupe (DedupeKey), so an exact
// duplicate terminal event contributes at most one deliverable. It takes only
// data snapshots and performs no I/O, locking, mutation, or event emission.
func classifyDurableNotifications(
	durableRaw []jobNotification,
	recs map[string]*jobstore.JobRecord,
	alreadyInjected map[string]bool,
) (survivors, injected []deliverableJobNotification) {
	seen := make(map[jobstore.DedupeKey]bool)
	for _, n := range durableRaw {
		rec := recs[n.JobID]
		if rec == nil || !jobstore.ShouldDeliver(rec) {
			continue
		}
		key := rec.DedupeKey()
		if seen[key] {
			continue
		}
		seen[key] = true
		deliverable := deliverableJobNotification{
			notification: jobNotificationFromRecord(rec),
			terminalGen:  rec.TerminalGen,
		}
		if alreadyInjected[rec.JobID] {
			injected = append(injected, deliverable)
			continue
		}
		survivors = append(survivors, deliverable)
	}
	return survivors, injected
}

func (s *Session) jobNotificationAlreadyInjected(jobID string) bool {
	if jobID == "" {
		return false
	}
	needle := fmt.Sprintf("job_id=%q", jobID)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.history {
		if durableJobNotificationAlreadyInjected(turn.Message.Text(), needle) {
			return true
		}
	}
	return false
}

func durableJobNotificationAlreadyInjected(text, needle string) bool {
	for {
		start := strings.Index(text, "<job-notification")
		if start < 0 {
			return false
		}
		text = text[start:]
		end := strings.Index(text, "</job-notification>")
		block := text
		if end >= 0 {
			block = text[:end]
		}
		if strings.Contains(block, needle) &&
			!strings.Contains(block, `event="watch"`) &&
			!strings.Contains(block, `status="watch"`) {
			return true
		}
		if end < 0 {
			return false
		}
		text = text[end+len("</job-notification>"):]
	}
}

func (s *Session) markJobNotificationsDelivered(notifs []deliverableJobNotification) []jobNotification {
	if len(notifs) == 0 || s.jobManager == nil {
		return nil
	}
	var failed []jobNotification
	for _, n := range notifs {
		if n.terminalGen == "" {
			continue
		}
		if err := s.jobManager.appendEvent(jobstore.Event{
			Kind:        jobstore.EventJobNotificationDelivered,
			TS:          s.jobManager.now(),
			JobID:       n.notification.JobID,
			TerminalGen: n.terminalGen,
		}); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("job notification delivery mark failed: %v", err)})
			failed = append(failed, n.notification)
			continue
		}
		// Forward the settle up so the parent's forwarded COPY of a child-owned job
		// settles too (spec §3): the child forwards only the Pending up, so without
		// this the parent's copy stays NotifyPending after the child self-delivers
		// and is later falsely escalated as "child unreachable:". Best-effort and
		// no-op for root sessions and non-forwarded terminals (forwardSnapshot
		// guards on forward/parentJobID); the parent intake (forwardEvent) settles
		// the copy without enqueueing, so no spurious render.
		if ferr := s.jobManager.forwardSnapshot(jobstore.Event{
			Kind:        jobstore.EventJobNotificationDelivered,
			TS:          s.jobManager.now(),
			JobID:       n.notification.JobID,
			TerminalGen: n.terminalGen,
		}); ferr != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("job notification delivery forward failed: %v", ferr)})
		}
	}
	return failed
}

func jobNotifications(notifs []deliverableJobNotification) []jobNotification {
	if len(notifs) == 0 {
		return nil
	}
	out := make([]jobNotification, 0, len(notifs))
	for _, n := range notifs {
		out = append(out, n.notification)
	}
	return out
}

// formatJobNotificationReminder renders notification blocks for a notification
// turn, joined by newlines. Terminal job_finished blocks carry a bounded result
// excerpt (shell tail / delegate head) re-read from the job record at render
// time, so the durable-replay path stays consistent with the live path.
func (s *Session) formatJobNotificationReminder(jobNotifs []deliverableJobNotification) string {
	blocks := make([]string, 0, len(jobNotifs))
	for _, n := range jobNotifs {
		blocks = append(blocks, formatJobNotificationBlock(n.notification, s.terminalNotificationExcerpt(n.notification)))
	}
	return strings.Join(blocks, "\n")
}
