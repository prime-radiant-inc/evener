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
	s.mu.Unlock()

	outputs := []string{}
	next := input
	nextImages := images
	nextKind := kind
	processCtx := ctx
	for {
		out, err := s.processOneInput(processCtx, next, nextImages, nextKind)
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
			return strings.Join(outputs, "\n"), err
		}
		fu := s.popFollowUp()
		if strings.TrimSpace(fu) != "" {
			next = fu
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
			continue
		}
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

func (s *Session) processOneInput(ctx context.Context, input string, images []ImageAttachment, kind EntryKind) (string, error) {
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
		return "", errors.New("session is closed")
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
		return "", ctx.Err()
	default:
	}

	if kind == EntryContinuation {
		s.acceptContinuationInput(ctx, input)
	} else if !s.acceptUserInput(ctx, input, images) {
		return "", nil
	}

	var toolSigs []string
	var lastText string // accumulated assistant text for round-limit return
	ctxWarned := false
	contentFilterRetried := false // track whether we've already tried recovering from a content filter error
	var tracker retryTracker

	for round := 0; s.cfg.MaxToolRoundsPerInput < 0 || round < s.cfg.MaxToolRoundsPerInput; round++ {
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
			return "", ctx.Err()
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
				return "", abortErr
			}
		}

		s.logAPICall(round, roundStart, timings.LLMCall, sys, len(history), req, resp, err)

		if err != nil {
			retry, ferr := s.handleModelError(ctx, err, req, &contentFilterRetried)
			if retry {
				continue
			}
			return "", ferr
		}

		if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
			return "", abortErr
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
			return "", abortErr
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
			return "", abortErr
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

		if len(calls) == 0 {
			retry, ferr := s.handleNoToolCalls(noContent, &tracker)
			if !retry {
				return "", ferr
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
			return "", abortErr
		}

		// --- Phase: ToolExec ---
		tPhaseStart = time.Now()

		// Execute tool calls (possibly in parallel) and send results back.
		results, execErr := s.execToolBatch(ctx, calls, profile)
		if execErr != nil {
			return "", execErr
		}

		timings.ToolExec = time.Since(tPhaseStart)

		// --- Phase: Persistence ---
		tPhaseStart = time.Now()

		if persistErr := s.persistToolResults(ctx, calls, results); persistErr != nil {
			return "", persistErr
		}

		timings.Persistence = time.Since(tPhaseStart)

		// --- Phase: AfterAction ---
		tPhaseStart = time.Now()

		// Notify the context strategy that a tool round completed.
		if afterErr := s.notifyStrategyAfterAction(ctx); afterErr != nil {
			return "", afterErr
		}

		timings.AfterAction = time.Since(tPhaseStart)

		if steerErr := s.injectPostToolSteering(ctx, calls, &toolSigs); steerErr != nil {
			return "", steerErr
		}

		// Emit round timings before checking result delivery.
		timings.TotalRound = time.Since(roundStart)
		timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall - timings.ToolExec - timings.Persistence - timings.AfterAction
		s.emit(events.EventRoundTimings, timings)

		// communicate sets the flag; exit the loop with the communicated message.
		if done, text := s.deliverIfCommunicated(ctx); done {
			return text, nil
		}
	}

	s.emit(events.EventTurnLimit, events.TurnLimitData{MaxToolRoundsPerInput: s.cfg.MaxToolRoundsPerInput})
	s.mu.Lock()
	s.setStateIfOpenLocked(SessionIdle)
	s.mu.Unlock()
	return lastText, nil
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
		hi.UserPrompt = input
		result := s.hookRunner.RunUserPromptSubmit(ctx, hi)
		for _, msg := range result.SystemMessages {
			s.Steer(msg)
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

// acceptContinuationInput records a goal-engine continuation at the start of an
// input turn. It mirrors acceptUserInput's history-repair and steering-drain
// behavior, but frames the input as a system/steering turn rather than the user
// speaking: it appends schema.TurnSteering (so expandHistory delivers it to the
// model as a user-role message without rendering a user bubble), emits
// EventGoalContinuation rather than EventUserInput, and skips the namer, the
// MaxTurns check, and the s.turns++ accounting (goal turns are bounded by the
// engine's iteration cap, not the session's user-input ceiling; SESSION_END.Turns
// reads the separate modelResponses counter, so skipping s.turns++ is safe).
func (s *Session) acceptContinuationInput(ctx context.Context, input string) {
	s.repairOrphanedToolResults("before accepting goal continuation")

	s.emit(events.EventGoalContinuation, events.GoalContinuationData{Text: input})
	s.appendTurn(schema.TurnSteering, llm.User(input))

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	for _, msg := range s.drainSteering() {
		s.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
		s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}
}
