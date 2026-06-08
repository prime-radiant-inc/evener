package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/plugin"
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

		// Spec Appendix B graceful shutdown ordering:
		// 1. Cancel in-flight LLM calls.
		if s.cancelFunc != nil {
			s.cancelFunc()
		}

		// 2-4. Kill running child processes (SIGTERM → wait 2s → SIGKILL).
		s.env.Cleanup()

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

		// 7. Close subagents.
		for _, sub := range subs {
			sub.sess.Close()
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
			if err := exportATIF(tpath, s.cfg.ExportATIFPath); err != nil {
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
	// EntryNotification is a system-framed turn that drains the pending
	// subagent-completion queue and surfaces it to the model as a steering
	// reminder. An empty queue makes it a no-op (no model request).
	EntryNotification
)

// ProcessInput processes a single user input (with optional image attachments)
// through to completion and returns the accumulated assistant output. It is a
// thin delegate to ProcessInputKind with kind EntryUserInput.
func (s *Session) ProcessInput(ctx context.Context, input string, images []ImageAttachment) (string, error) {
	return s.ProcessInputKind(ctx, input, images, EntryUserInput)
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
	// Reset so SESSION_END can fire at the end of this input's processing.
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return "", errors.New("session is closed")
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
		out, progressed, err := s.processOneInput(processCtx, next, nextImages, nextKind)
		// Follow-up turns (after the first) carry no attachments and are
		// user-driven, not continuations.
		nextImages = nil
		nextKind = EntryUserInput
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
				s.mu.Lock()
				// Only flip back to idle when the session isn't already
				// closed (e.g. daemon shutdown raced the interrupt).
				if !s.closingOrClosedLocked() {
					s.state = SessionIdle
				}
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
			return strings.Join(outputs, "\n"), err
		}
		fu := s.popFollowUp()
		if strings.TrimSpace(fu) != "" {
			next = fu
			s.mu.Lock()
			s.sessionEndEmitted = false
			s.mu.Unlock()
			continue
		}
		// kata 111a: when the per-turn followup queue is empty, drain the
		// next user-typed queued message and run it as a fresh user turn.
		// Each drain consumes exactly one message so each becomes a
		// distinct user turn in the transcript. Image attachments on the
		// queued entry are forwarded as ContentImage parts on that turn
		// (kata t5j6).
		if queued := s.popQueueHead(); strings.TrimSpace(queued.Text) != "" || len(queued.Images) > 0 {
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
		// Skipped when the turn that just ran was a notification (a notification
		// must neither advance nor terminate the goal — it already folded at its own
		// gate), and while a continuation is already deferred (one fold per turn).
		// On a terminal/stop decision the gate emits the terminal report and leaves
		// haveDeferredCont false, so we fall through to EventSessionEnd (idle).
		if ranKind != EntryNotification && !haveDeferredCont {
			// The gate's fold + terminal-report side effects (RecordContinuation, the
			// no-progress breaker, EventGoalEnded) happen here; the rendered prompt it
			// returns is discarded because the inline-run site below re-renders the
			// current objective from the store (so a clear/retarget during the
			// interleaved notification turn cannot run a stale continuation).
			if _, ok := s.armGoalContinuation(progressed, ranKind == EntryContinuation); ok {
				haveDeferredCont = true
			}
		}
		// Notification interleave (priority 3): a pending subagent-completion
		// notification runs AFTER the fold above but BEFORE the deferred
		// continuation, so it is transparent to goal accounting (the just-finished
		// continuation already folded). This is a non-draining length check; the
		// queue is consumed inside acceptNotificationInput when the EntryNotification
		// turn runs (an empty queue there is a no-op, but the peek guards against it).
		if s.peekNotifications() > 0 {
			next = ""
			nextImages = nil
			nextKind = EntryNotification
			continue
		}
		// Run the deferred continuation INLINE (via continue, not the idle kick) so
		// the goal advances even when kickFunc==nil (e.g. one-shot `serf run` with a
		// restored active goal whose model spawned a subagent).
		if haveDeferredCont {
			haveDeferredCont = false
			// Re-validate against the goal store before running: the user may have
			// cleared (/goal clear) or retargeted (/goal <new>) the goal during the
			// interleaved notification turn above, making the render the gate computed
			// at fold time stale. currentGoalContinuation re-reads the store read-only
			// (the fold already happened at the gate — no RecordContinuation re-runs
			// here). If the goal is no longer active, drop the stale continuation and
			// fall through to settle + idle; if it is active, run a FRESH render of the
			// CURRENT objective so a retarget pursues the new goal.
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
		s.settleGoalOnIdle()
		s.mu.Lock()
		if !s.sessionEndEmitted {
			s.sessionEndEmitted = true
			turns := s.modelResponses
			state := s.state
			s.mu.Unlock()
			s.emit(events.EventSessionEnd, events.SessionEndData{
				Reason: "input_complete",
				State:  string(state),
				Turns:  turns,
			})
		} else {
			s.mu.Unlock()
		}
		return strings.Join(outputs, "\n"), nil
	}
}

func (s *Session) processOneInput(ctx context.Context, input string, images []ImageAttachment, kind EntryKind) (out string, progressed bool, err error) {
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
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.emit(events.EventError, errorDataFromError(ctx.Err()))
		s.mu.Lock()
		if s.state == SessionProcessing && !s.closingOrClosedLocked() {
			s.state = SessionIdle
		}
		s.mu.Unlock()
		return "", false, ctx.Err()
	default:
	}

	if kind == EntryContinuation {
		s.acceptContinuationInput(ctx, input)
	} else if kind == EntryNotification {
		if !s.acceptNotificationInput(ctx) {
			return "", false, nil
		}
	} else if !s.acceptUserInput(ctx, input, images) {
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
		roundStart := time.Now()
		var timings events.RoundTimings
		timings.Round = round

		s.mu.Lock()
		s.totalRounds++
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			s.emit(events.EventError, errorDataFromError(ctx.Err()))
			s.mu.Lock()
			if s.state == SessionProcessing && !s.closingOrClosedLocked() {
				s.state = SessionIdle
			}
			s.mu.Unlock()
			return "", progressed, ctx.Err()
		default:
		}

		profile, sys, history, req := s.prepareModelRequest(ctx, round, &timings)

		// --- Phase: LLMCall ---
		tPhaseStart := time.Now()

		modelResp, req, err := s.callModelWithFallback(ctx, profile, req, round)
		resp := modelResp.Response

		timings.LLMCall = time.Since(tPhaseStart)

		if err == nil {
			if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
				return "", progressed, abortErr
			}
		}

		s.logAPICall(round, roundStart, timings.LLMCall, sys, len(history), req, resp, err)

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
		s.recordResponseUsage(resp)

		// Context window awareness: emit a warning when we exceed ~80% of the profile's context window.
		if !ctxWarned {
			if s.maybeWarnContextUsage(profile, req.Messages) {
				ctxWarned = true
			}
		}

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
		noContent := strings.TrimSpace(txt) == "" && len(calls) == 0
		hasPhase := false
		for _, p := range resp.Message.Content {
			if p.Phase != "" {
				hasPhase = true
				break
			}
		}
		skipHistory := noContent && !hasPhase

		if abortErr := s.emitAssistantResponse(ctx, resp, modelResp, txt, skipHistory); abortErr != nil {
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
		tPhaseStart = time.Now()

		// Execute tool calls (possibly in parallel) and send results back.
		results, execErr := s.execToolBatch(ctx, calls, profile)
		if execErr != nil {
			return "", progressed, execErr
		}

		timings.ToolExec = time.Since(tPhaseStart)

		// --- Phase: Persistence ---
		tPhaseStart = time.Now()

		if persistErr := s.persistToolResults(ctx, calls, results); persistErr != nil {
			return "", progressed, persistErr
		}

		timings.Persistence = time.Since(tPhaseStart)

		// --- Phase: AfterAction ---
		tPhaseStart = time.Now()

		// Notify the context strategy that a tool round completed.
		if afterErr := s.notifyStrategyAfterAction(ctx); afterErr != nil {
			return "", progressed, afterErr
		}

		timings.AfterAction = time.Since(tPhaseStart)

		if steerErr := s.injectPostToolSteering(ctx, calls, &toolSigs); steerErr != nil {
			return "", progressed, steerErr
		}

		// Emit round timings before checking result delivery.
		timings.TotalRound = time.Since(roundStart)
		timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall - timings.ToolExec - timings.Persistence - timings.AfterAction
		s.emit(events.EventRoundTimings, timings)

		// communicate sets the flag; exit the loop with the communicated message.
		if done, text := s.deliverIfCommunicated(ctx); done {
			return text, progressed, nil
		}
	}

	s.emit(events.EventTurnLimit, events.TurnLimitData{MaxToolRoundsPerInput: s.cfg.MaxToolRoundsPerInput})
	s.mu.Lock()
	s.setStateIfOpenLocked(SessionIdle)
	s.mu.Unlock()
	return lastText, progressed, nil
}

// acceptUserInput records a new user input at the start of an input turn: it
// repairs any orphaned tool results, emits EventUserInput, appends the user turn,
// launches the session namer, and runs UserPromptSubmit hooks. It enforces
// MaxTurns, returning proceed=false (the caller returns "", nil) when the turn
// limit is already reached; otherwise it increments the turn counter, drains any
// pending steering, and returns proceed=true.
func (s *Session) acceptUserInput(ctx context.Context, input string, images []ImageAttachment) (proceed bool) {
	s.repairOrphanedToolResults("before accepting new input")

	s.mu.Lock()
	userInputTurn := len(s.history) + 1
	s.mu.Unlock()
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

	// Count conversation turns (user input -> model response pairs), not LLM round-trips.
	// Check the limit before incrementing so MaxTurns=N allows exactly N inputs.
	s.mu.Lock()
	turns := s.turns
	s.mu.Unlock()

	if s.cfg.MaxTurns > 0 && turns >= s.cfg.MaxTurns {
		s.emit(events.EventTurnLimit, events.TurnLimitData{MaxTurns: s.cfg.MaxTurns})
		s.mu.Lock()
		s.setStateIfOpenLocked(SessionIdle)
		s.mu.Unlock()
		return false
	}

	s.mu.Lock()
	s.turns++
	s.mu.Unlock()

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	for _, msg := range s.drainSteering() {
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
	for _, msg := range s.drainSteering() {
		s.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
		s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}
}

// acceptNotificationInput records a subagent-completion notification turn at the
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
	notifs := s.filterDeliverableNotifications(s.drainNotifications())
	if len(notifs) == 0 {
		s.mu.Lock()
		s.sessionEndEmitted = true
		s.mu.Unlock()
		return false
	}

	s.repairOrphanedToolResults("before accepting notification")

	reminder := formatNotificationReminder(notifs)
	s.appendTurn(schema.TurnSteering, llm.User(reminder))
	s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: reminder})

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	for _, msg := range s.drainSteering() {
		s.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
		s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}
	return true
}

// filterDeliverableNotifications drops a drained notification that should no
// longer wake the model at delivery time: the child's record is absent (the
// manager get returns nil — GC-reclaimed), the record is closed, or its
// result was already consumed by a blocking wait. The notification is armed
// unconditionally at terminal run end (so arming never races a concurrent
// wait/close); suppression happens here, at the moment the parent's turn would
// surface it. A notification turn is a wake, not a consume — surviving entries
// are NOT marked consumed — so suppression keys only on what wait/close already
// did.
//
// Lock discipline: the queue is already drained (under pendingNotifsMu) before
// this runs. For each entry it takes the manager mutex once (the single get) and
// then reads status/resultConsumed under sub.mu briefly, per-entry. It never
// holds pendingNotifsMu or the manager mutex while reading sub.mu, and never
// nests sub.mu inside another sub's lock.
func (s *Session) filterDeliverableNotifications(raw []subagentNotification) []subagentNotification {
	survivors := make([]subagentNotification, 0, len(raw))
	for _, n := range raw {
		sub := s.subagents.get(n.AgentID)
		if sub == nil {
			continue
		}
		sub.mu.Lock()
		drop := sub.closed || sub.resultConsumed
		sub.mu.Unlock()
		if drop {
			continue
		}
		survivors = append(survivors, n)
	}
	return survivors
}

// formatNotificationReminder renders one <subagent-notification ...> block per
// drained entry, joined by newlines. Each block carries the entry's metadata and
// a human line pointing the model at wait()/subagent_output() to read the result.
func formatNotificationReminder(notifs []subagentNotification) string {
	blocks := make([]string, 0, len(notifs))
	for _, n := range notifs {
		blocks = append(blocks, fmt.Sprintf(
			"<subagent-notification agent_id=%q status=%q turns_used=%q transcript_ref=%q>\n"+
				"Subagent %s finished (%s). Read its result with wait(%q) or subagent_output(%q, view=result).\n"+
				"</subagent-notification>",
			n.AgentID, n.Status, strconv.Itoa(n.TurnsUsed), n.TranscriptRef,
			n.AgentID, n.Status, n.AgentID, n.AgentID,
		))
	}
	return strings.Join(blocks, "\n")
}
