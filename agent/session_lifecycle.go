package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

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

// Compact forces context compaction regardless of current pressure.
// Runs all compaction layers (observation masking, thinking clearing,
// checkpoint, and LLM summarization). Safe to call while idle.
func (s *Session) Compact(ctx context.Context) error {
	if s.contextMgr == nil {
		return fmt.Errorf("context manager not initialized")
	}

	s.contextMgr.Meta = s.buildCompactionMeta()

	s.mu.Lock()
	histCopy := append([]Turn{}, s.history...)
	s.mu.Unlock()

	emitFn, flushCompactionHooks := s.compactionEmitFunc(ctx, &histCopy)
	s.contextMgr.ForceCompact(ctx, &histCopy, emitFn)
	flushCompactionHooks()

	s.mu.Lock()
	s.history = histCopy
	s.mu.Unlock()

	s.maybeAutoSave()
	return nil
}

type steeringTurnRecord struct {
	turn Turn
	text string
}

func (s *Session) compactionEmitFunc(ctx context.Context, history *[]Turn) (func(EventKind, any), func()) {
	preCompactRan := false
	var pendingSteering []steeringTurnRecord
	emitFn := func(kind EventKind, data any) {
		if kind == EventContextCompaction && !preCompactRan {
			preCompactRan = true
			pendingSteering = append(pendingSteering, s.runPreCompactHook(ctx, history)...)
		}
		s.emit(kind, data)
	}
	flush := func() {
		s.flushSteeringTurnRecords(pendingSteering)
	}
	return emitFn, flush
}

func (s *Session) runPreCompactHook(ctx context.Context, history *[]Turn) []steeringTurnRecord {
	if s.hookRunner == nil || history == nil {
		return nil
	}
	result := s.hookRunner.RunPreCompact(ctx, s.hookInput(HookPreCompact))
	return appendSteeringMessagesToHistory(history, result.SystemMessages)
}

func appendSteeringMessagesToHistory(history *[]Turn, messages []string) []steeringTurnRecord {
	var records []steeringTurnRecord
	for _, msg := range messages {
		if strings.TrimSpace(msg) == "" {
			continue
		}
		turn := NewTurn(TurnSteering, llm.User(msg))
		*history = append(*history, turn)
		records = append(records, steeringTurnRecord{turn: turn, text: msg})
	}
	return records
}

func (s *Session) flushSteeringTurnRecords(records []steeringTurnRecord) {
	for _, record := range records {
		if s.transcript != nil {
			if err := s.transcript.Append(record.turn); err != nil {
				s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
			}
		}
		s.emit(EventSteeringInjected, SteeringInjectedData{Text: record.text})
	}
}

// buildCompactionMeta gathers session-level metadata for enriching compaction summaries.
func (s *Session) buildCompactionMeta() CompactionMeta {
	meta := CompactionMeta{}

	// Transcript path.
	if s.stateDir != "" {
		meta.TranscriptPath = filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
	}

	return meta
}

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
			s.hookRunner.RunSessionEnd(hookCtx, s.hookInput(HookSessionEnd))
			hookCancel()
		}

		// 5-6. Emit SESSION_END with final state.
		if emitEnd {
			s.emit(EventSessionEnd, SessionEndData{
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
		if s.cfg.ExportATIFPath != "" && s.stateDir != "" && s.cfg.Depth == 0 {
			tpath := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
			if err := ExportATIF(tpath, s.cfg.ExportATIFPath); err != nil {
				s.emit(EventWarning, WarningData{Message: fmt.Sprintf("ATIF export failed: %v", err)})
			}
		}

		if s.embeddedSkillsDir != "" {
			os.RemoveAll(s.embeddedSkillsDir)
		}

		// 8. Reassert closed in case an in-flight turn reached a late state transition.
		s.mu.Lock()
		s.state = SessionClosed
		s.mu.Unlock()
		s.toolEventsWG.Wait()
		// Join detached event emitters (subagent runs, session namer) so their
		// emit() sends complete before the channel is closed. They are already
		// cancelled above (child Close + cancelFunc), so this returns promptly.
		s.sendersWG.Wait()
		close(s.events)
	})
}

func (s *Session) ProcessInput(ctx context.Context, input string, images []ImageAttachment) (string, error) {
	// Reset so SESSION_END can fire at the end of this input's processing.
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return "", fmt.Errorf("session is closed")
	}
	s.sessionEndEmitted = false
	s.mu.Unlock()

	outputs := []string{}
	next := input
	nextImages := images
	processCtx := ctx
	for {
		out, err := s.processOneInput(processCtx, next, nextImages)
		// Follow-up turns (after the first) carry no attachments.
		nextImages = nil
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
					s.appendTurn(TurnSteering, llm.User(interruptMsg))
					s.emit(EventSteeringInjected, SteeringInjectedData{Text: interruptMsg})
				}
				if emitEnd {
					s.emit(EventSessionEnd, SessionEndData{
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
			s.emit(EventSessionEnd, SessionEndData{
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

func (s *Session) maybeWarnContextUsage(msgs []llm.Message) bool {
	if s == nil || s.profile == nil {
		return false
	}
	cw := s.profile.ContextWindowSize()
	if cw <= 0 {
		return false
	}

	totalChars := 0
	for _, m := range msgs {
		totalChars += messageCharCount(m)
	}
	approxTokens := float64(totalChars) / 4.0
	threshold := float64(cw) * 0.8
	if approxTokens <= threshold {
		return false
	}

	pct := int(math.Round((approxTokens / float64(cw)) * 100.0))
	msg := fmt.Sprintf("Context usage at ~%d%% of context window", pct)
	s.emit(EventWarning, WarningData{
		Message:           msg,
		ApproxTokens:      int(math.Round(approxTokens)),
		ContextWindowSize: cw,
		Percent:           pct,
	})
	return true
}

func messageCharCount(m llm.Message) int {
	n := 0
	n += len(m.Name)
	n += len(m.ToolCallID)
	for _, p := range m.Content {
		switch p.Kind {
		case llm.ContentText:
			n += len(p.Text)
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				n += len(p.ToolCall.ID)
				n += len(p.ToolCall.Name)
				n += len(p.ToolCall.Arguments)
			}
		case llm.ContentToolResult:
			if p.ToolResult != nil {
				n += len(p.ToolResult.ToolCallID)
				n += len(p.ToolResult.Name)
				switch x := p.ToolResult.Content.(type) {
				case string:
					n += len(x)
				case []byte:
					n += len(x)
				default:
					b, _ := json.Marshal(x)
					n += len(b)
				}
			}
		case llm.ContentThinking, llm.ContentRedThinking:
			if p.Thinking != nil {
				n += len(p.Thinking.Text)
				n += len(p.Thinking.Signature)
			}
		default:
			// Fallback to a best-effort JSON encoding.
			b, _ := json.Marshal(p)
			n += len(b)
		}
	}
	return n
}

func (s *Session) processOneInput(ctx context.Context, input string, images []ImageAttachment) (string, error) {
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
		return "", fmt.Errorf("session is closed")
	}
	s.setStateIfOpenLocked(SessionProcessing)
	s.communicated = false
	s.communicateAwaitReply = false
	s.communicateText = ""
	s.communicateReply = ""
	s.communicateOutput = ""
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.emit(EventError, errorDataFromError(ctx.Err()))
		s.mu.Lock()
		if s.state == SessionProcessing && !s.closingOrClosedLocked() {
			s.state = SessionIdle
		}
		s.mu.Unlock()
		return "", ctx.Err()
	default:
	}

	s.repairOrphanedToolResults("before accepting new input")

	s.mu.Lock()
	userInputTurn := len(s.history) + 1
	s.mu.Unlock()
	s.emit(EventUserInput, UserInputData{
		Text:   input,
		Images: userInputImagesFromAttachments(images),
		Turn:   userInputTurn,
	})
	s.appendTurn(TurnUserInput, buildUserInputMessage(input, images))
	s.launchInitialPromptNamer(s.sessionCtx, input)

	// UserPromptSubmit hooks
	if s.hookRunner != nil {
		hi := s.hookInput(HookUserPromptSubmit)
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
		s.emit(EventTurnLimit, TurnLimitData{MaxTurns: s.cfg.MaxTurns})
		s.mu.Lock()
		s.setStateIfOpenLocked(SessionIdle)
		s.mu.Unlock()
		return "", nil
	}

	s.mu.Lock()
	s.turns++
	s.mu.Unlock()

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	for _, msg := range s.drainSteering() {
		s.appendTurn(TurnSteering, steeringMessageToLLM(msg))
		s.emit(EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}

	var toolSigs []string
	var lastText string // accumulated assistant text for round-limit return
	ctxWarned := false
	contentFilterRetried := false // track whether we've already tried recovering from a content filter error
	consecutiveEmptyResponses := 0
	totalEmptyResponses := 0
	consecutiveBareTextResponses := 0
	const maxEmptyRetries = 3
	const maxTotalEmptyResponses = 8 // prevent repeated burst-and-recover spirals
	const maxBareTextRetries = 3

	for round := 0; s.cfg.MaxToolRoundsPerInput < 0 || round < s.cfg.MaxToolRoundsPerInput; round++ {
		roundStart := time.Now()
		var timings RoundTimings
		timings.Round = round

		s.mu.Lock()
		s.totalRounds++
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			s.emit(EventError, errorDataFromError(ctx.Err()))
			s.mu.Lock()
			if s.state == SessionProcessing && !s.closingOrClosedLocked() {
				s.state = SessionIdle
			}
			s.mu.Unlock()
			return "", ctx.Err()
		default:
		}

		// --- Phase: SystemPrompt ---
		tPhaseStart := time.Now()

		sys := s.cachedSystemPrompt

		timings.SystemPrompt = time.Since(tPhaseStart)

		// --- Phase: ContextMgmt ---
		tPhaseStart = time.Now()

		// Copy history once for both context management and message expansion.
		s.mu.Lock()
		historyTurns := append([]Turn{}, s.history...)
		s.mu.Unlock()
		if repaired, repairs := repairOrphanedToolResults(historyTurns); repairs > 0 {
			historyTurns = repaired
			s.mu.Lock()
			s.history = repaired
			s.mu.Unlock()
			s.emit(EventWarning, WarningData{Message: fmt.Sprintf("Recovered %d interrupted tool call(s) before model request", repairs)})
			s.maybeAutoSave()
		}

		// Apply context management before each LLM request.
		if s.strategy != nil {
			// Populate compaction metadata so checkpoint/summarize have session context.
			s.contextMgr.Meta = s.buildCompactionMeta()

			emitFn, flushCompactionHooks := s.compactionEmitFunc(ctx, &historyTurns)
			if err := s.strategy.ManageContext(ctx, &historyTurns, len(sys), emitFn); err != nil {
				s.emit(EventWarning, warningDataFromError("context strategy error: "+err.Error(), err))
			}
			flushCompactionHooks()

			s.mu.Lock()
			s.history = historyTurns
			s.mu.Unlock()
		}

		timings.ContextMgmt = time.Since(tPhaseStart)

		// --- Phase: HistoryExpand ---
		tPhaseStart = time.Now()

		// Reuse historyTurns from context management — no redundant copy.
		history := expandHistory(historyTurns)

		timings.HistoryExpand = time.Since(tPhaseStart)

		// --- Phase: ToolDefs ---
		tPhaseStart = time.Now()
		toolDefs := s.allToolDefinitions(round)
		timings.ToolDefs = time.Since(tPhaseStart)

		req := s.buildModelRequest(sys, history, toolDefs)

		// --- Phase: LLMCall ---
		tPhaseStart = time.Now()

		modelResp, req, err := s.callModelWithFallback(ctx, req, round)
		resp := modelResp.Response

		timings.LLMCall = time.Since(tPhaseStart)

		if err == nil {
			if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
				return "", abortErr
			}
		}

		s.logAPICall(round, roundStart, timings.LLMCall, sys, len(history), req, resp, err)

		if err != nil {
			if isTurnCancellation(ctx, err) {
				return "", err
			}

			// Content filter recovery: compaction often removes the offending
			// content, allowing the next request to succeed. Try once.
			var cfe *llm.ContentFilterError
			if errors.As(err, &cfe) && !contentFilterRetried && s.contextMgr != nil {
				contentFilterRetried = true
				s.emit(EventWarning, warningDataFromError("Content filter hit — compacting context and retrying", err))
				s.mu.Lock()
				histCopy := append([]Turn{}, s.history...)
				s.mu.Unlock()
				emitFn, flushCompactionHooks := s.compactionEmitFunc(ctx, &histCopy)
				s.contextMgr.ForceCompact(ctx, &histCopy, emitFn)
				flushCompactionHooks()
				s.mu.Lock()
				s.history = histCopy
				s.mu.Unlock()
				continue
			}

			errData := errorDataFromError(err)
			errData.Cause = providerCauseFromError(err, req.Model)
			s.emit(EventError, errData)

			// Spec: context overflow should emit a warning (no automatic compaction).
			var cle *llm.ContextLengthError
			if errors.As(err, &cle) {
				s.emit(EventWarning, warningDataFromError("Context length exceeded", err))
			}
			// Spec: non-retryable/unrecoverable errors transition the session to closed.
			var le llm.Error
			if errors.As(err, &le) && !le.Retryable() {
				s.Close()
			}
			// Recoverable LLM errors (retry policy exhausted, stream-ended,
			// timeouts, etc.) bail out of the run loop without compacting or
			// closing — but we still need to leave active. Without this
			// flip the session sits in active forever from the daemon's
			// /status endpoint, the hub disables steer/send, and the user has
			// no recovery path short of restarting the daemon (kata r6y9).
			// meta.json flush happens via the deferred flush at the top of
			// processOneInput (kata ztne).
			s.mu.Lock()
			if s.state == SessionProcessing && !s.closingOrClosedLocked() {
				s.state = SessionIdle
			}
			s.mu.Unlock()
			// Wrap with "provider error" so callers (toil, runners) can
			// distinguish a provider/LLM failure from agent quiescence
			// (empty output + nil err) without parsing adapter-specific
			// error message formats. The original error is preserved via
			// errors.Unwrap for callers that need the underlying detail
			// (kata 3xbh).
			return "", fmt.Errorf("provider error: %w", err)
		}

		if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
			return "", abortErr
		}

		// Accumulate usage and record exact input token count for pressure calculation.
		if s.contextMgr != nil {
			s.contextMgr.AddUsage(resp.Usage)

			// Detect server-side web search in the response. Anthropic makes
			// multiple forward passes for server tools, reporting combined usage
			// (~2x actual). Skip recording the inflated baseline; the previous
			// lastInputTokens value remains valid for pressure estimation.
			hasServerWebSearch := false
			for _, p := range resp.Message.Content {
				if p.Kind == llm.ContentWebSearch {
					hasServerWebSearch = true
					break
				}
			}
			if !hasServerWebSearch {
				totalInput := resp.Usage.InputTokens
				if resp.Usage.CacheReadTokens != nil {
					totalInput += *resp.Usage.CacheReadTokens
				}
				if resp.Usage.CacheWriteTokens != nil {
					totalInput += *resp.Usage.CacheWriteTokens
				}
				if totalInput > 0 {
					s.mu.Lock()
					hLen := len(s.history)
					s.mu.Unlock()
					s.contextMgr.RecordInputTokens(totalInput, hLen)
				}
			}
		}

		// Context window awareness: emit a warning when we exceed ~80% of the profile's context window.
		if !ctxWarned {
			if s.maybeWarnContextUsage(req.Messages) {
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

		if abortErr := s.withResponseSideEffects(ctx, func() {
			if !modelResp.StreamedAssistant {
				s.emit(EventAssistantTextStart, AssistantTextStartData{
					Model: resp.Model,
				})
			}
			if !skipHistory {
				s.appendAssistantTurn(resp)
			}
			if !modelResp.StreamedAssistant && strings.TrimSpace(txt) != "" {
				s.emit(EventAssistantTextDelta, AssistantTextDeltaData{Delta: txt})
			}
			textEndData := AssistantTextEndData{
				Text:         txt,
				Usage:        resp.Usage,
				FinishReason: resp.Finish.Reason,
				Model:        resp.Model,
			}
			if reasoning := resp.ReasoningText(); reasoning != "" {
				textEndData.Reasoning = reasoning
			}
			s.emit(EventAssistantTextEnd, textEndData)
			s.mu.Lock()
			s.modelResponses++
			s.mu.Unlock()
		}); abortErr != nil {
			return "", abortErr
		}
		// pause_turn: model needs another turn (e.g. server-side web search still running).
		if resp.Finish.Reason == llm.FinishReasonPauseTurn {
			round-- // Don't count pause_turn as a tool round.
			timings.TotalRound = time.Since(roundStart)
			timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
			s.emit(EventRoundTimings, timings)
			continue
		}

		// Reverse-map provider-specific tool names to canonical names for registry lookup.
		for i := range calls {
			calls[i].Name = s.canonicalToolName(calls[i].Name)
		}

		if len(calls) == 0 {
			// Empty response (no text, no tool calls): likely a model glitch
			// (e.g. gpt-5.3-codex null-content). Inject escalating steering and retry.
			// Note: phase-only responses are still retried — they carry metadata
			// but the model hasn't produced any useful output.
			if noContent {
				consecutiveEmptyResponses++
				totalEmptyResponses++
				if consecutiveEmptyResponses <= maxEmptyRetries && totalEmptyResponses <= maxTotalEmptyResponses {
					s.emit(EventWarning, WarningData{
						Message: fmt.Sprintf("empty response from model (retry %d/%d)", consecutiveEmptyResponses, maxEmptyRetries),
					})
					var steering string
					switch consecutiveEmptyResponses {
					case 1:
						steering = "Your previous response was empty. Please continue working on the task."
					case 2:
						steering = "Your response was empty again. If you're stuck, write what you've tried so far " +
							"to notes.txt, then try a different approach. You have plenty of rounds left."
					default:
						steering = "You've produced multiple empty responses. You MUST either call a tool to continue " +
							"working, or call " + s.resultToolName() + " with your best effort so far. Take a breath — you've " +
							"got this. Try a completely different approach."
					}
					s.appendTurn(TurnSteering, llm.User(steering))
					s.emit(EventSteeringInjected, SteeringInjectedData{Text: steering})
					round-- // Don't count empty-response retries as tool rounds.
					timings.TotalRound = time.Since(roundStart)
					timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
					s.emit(EventRoundTimings, timings)
					continue
				}
				err := &emptyResponseExhaustedError{retries: maxEmptyRetries}
				s.emit(EventError, errorDataFromError(err))
				s.mu.Lock()
				s.setStateIfOpenLocked(SessionIdle)
				s.mu.Unlock()
				return "", err
			} else {
				// Non-empty bare text without tool calls violates the contract:
				// all user-facing messages must go through communicate.
				consecutiveEmptyResponses = 0
				consecutiveBareTextResponses++
				if consecutiveBareTextResponses <= maxBareTextRetries {
					s.emit(EventWarning, WarningData{
						Message: fmt.Sprintf("bare text response without tool call (retry %d/%d)", consecutiveBareTextResponses, maxBareTextRetries),
					})
					steering := "You responded with bare text instead of a tool call. " +
						"All user-facing messages MUST use " + s.resultToolName() + ". " +
						"If that bare text was meant for the user, call " + s.resultToolName() + " now with that text in message, set await_reply=true only if you need user input, and include the output envelope. " +
						"Otherwise call your next tool and keep working."
					s.appendTurn(TurnSteering, llm.User(steering))
					s.emit(EventSteeringInjected, SteeringInjectedData{Text: steering})
					round-- // Don't count bare-text retries as tool rounds.
					timings.TotalRound = time.Since(roundStart)
					timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
					s.emit(EventRoundTimings, timings)
					continue
				}
				err := &bareTextWithoutResultToolError{
					toolName: s.resultToolName(),
					retries:  maxBareTextRetries,
				}
				s.emit(EventError, errorDataFromError(err))
				s.mu.Lock()
				s.setStateIfOpenLocked(SessionIdle)
				s.mu.Unlock()
				return "", err
			}
		}

		// Model produced tool calls — reset retry counters.
		consecutiveEmptyResponses = 0
		consecutiveBareTextResponses = 0

		if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
			if ctx.Err() != nil && !s.isClosingOrClosed() {
				s.appendCanceledToolResults(calls, nil, abortErr)
			}
			return "", abortErr
		}

		// --- Phase: ToolExec ---
		tPhaseStart = time.Now()

		// Execute tool calls (possibly in parallel) and send results back.
		results := make([]ToolExecResult, len(calls))
		if s.profile.SupportsParallelToolCalls() && len(calls) > 1 {
			// Ordered-group algorithm: batch consecutive read-only calls for
			// parallel execution; serialize everything else.
			flushReadBatch := func(batch []int) error {
				switch len(batch) {
				case 0:
					// nothing to do
				case 1:
					if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
						return abortErr
					}
					results[batch[0]] = s.execTool(ctx, calls[batch[0]])
				default:
					var wg sync.WaitGroup
					var panicValue any
					var panicMu sync.Mutex
					wg.Add(len(batch))
					for _, i := range batch {
						go func() {
							defer func() {
								if r := recover(); r != nil {
									panicMu.Lock()
									if panicValue == nil {
										panicValue = r
									}
									panicMu.Unlock()
								}
								wg.Done()
							}()
							if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
								panic(abortErr)
							}
							results[i] = s.execTool(ctx, calls[i])
						}()
					}
					wg.Wait()
					if panicValue != nil {
						if err, ok := panicValue.(error); ok {
							return err
						}
						panic(panicValue)
					}
				}
				return nil
			}

			var readBatch []int
			for i, call := range calls {
				t := s.reg.Get(call.Name)
				if t != nil && t.ReadOnly {
					readBatch = append(readBatch, i)
				} else {
					if err := flushReadBatch(readBatch); err != nil {
						if ctx.Err() != nil && !s.isClosingOrClosed() {
							s.appendCanceledToolResults(calls, results, err)
						}
						return "", err
					}
					readBatch = readBatch[:0]
					if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
						if ctx.Err() != nil && !s.isClosingOrClosed() {
							s.appendCanceledToolResults(calls, results, abortErr)
						}
						return "", abortErr
					}
					results[i] = s.execTool(ctx, call)
				}
			}
			if err := flushReadBatch(readBatch); err != nil {
				if ctx.Err() != nil && !s.isClosingOrClosed() {
					s.appendCanceledToolResults(calls, results, err)
				}
				return "", err
			}
		} else {
			for i := range calls {
				if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
					if ctx.Err() != nil && !s.isClosingOrClosed() {
						s.appendCanceledToolResults(calls, results, abortErr)
					}
					return "", abortErr
				}
				results[i] = s.execTool(ctx, calls[i])
			}
		}

		if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
			if ctx.Err() != nil && !s.isClosingOrClosed() {
				s.appendCanceledToolResults(calls, results, abortErr)
			}
			return "", abortErr
		}

		timings.ToolExec = time.Since(tPhaseStart)

		// --- Phase: Persistence ---
		tPhaseStart = time.Now()

		// Aggregate all tool results into a single TurnToolResults turn.
		var parts []llm.ContentPart
		for _, r := range results {
			parts = append(parts, llm.ContentPart{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID:     r.CallID,
					Name:           r.ToolName,
					Content:        r.Output,
					IsError:        r.IsError,
					DurationMS:     r.DurationMS,
					ImageData:      r.ImageData,
					ImageMediaType: r.ImageMediaType,
				},
			})
		}
		if abortErr := s.appendToolResults(ctx, calls, results, parts); abortErr != nil {
			return "", abortErr
		}

		// If any tool result contained an image, make a side-channel API call
		// to describe it. GPT models in tool-calling mode never produce text
		// descriptions of images — they immediately write code. This off-loop
		// call with no tools forces the model to use its native vision, then
		// injects the description into the conversation so the agent can use it.
		for i, r := range results {
			if len(r.ImageData) > 0 {
				if desc := s.describeImage(ctx, r); desc != "" {
					// Include the file path so the agent can correlate descriptions
					// to specific files when multiple images/documents are read in one round.
					label := "Image description (from vision)"
					if strings.HasPrefix(r.ImageMediaType, "application/pdf") {
						label = "Document description (from content analysis)"
					}
					if i < len(calls) {
						var args map[string]any
						if json.Unmarshal(calls[i].Arguments, &args) == nil {
							if path, ok := args["file_path"].(string); ok {
								label += " for " + path
							}
						}
					}
					if abortErr := s.withResponseSideEffects(ctx, func() {
						s.Steer(label + ": " + desc + "\n<system-reminder>Visual descriptions are summaries. They may miss or mischaracterize details.</system-reminder>")
					}); abortErr != nil {
						return "", abortErr
					}
				}
			}
		}

		timings.Persistence = time.Since(tPhaseStart)

		// --- Phase: AfterAction ---
		tPhaseStart = time.Now()

		// Notify the context strategy that a tool round completed.
		// AfterAction takes []Turn (not *[]Turn) so it cannot mutate the slice.
		// Pass s.history directly — no copy needed since the loop is single-
		// threaded and nothing else modifies history until AfterAction returns.
		if s.strategy != nil {
			if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
				return "", abortErr
			}
			s.mu.Lock()
			hist := s.history
			s.mu.Unlock()
			if err := s.strategy.AfterAction(ctx, hist, s.client); err != nil {
				if abortErr := s.withResponseSideEffects(ctx, func() {
					s.emit(EventWarning, WarningData{Message: "strategy AfterAction error: " + err.Error()})
				}); abortErr != nil {
					return "", abortErr
				}
			}
			if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
				return "", abortErr
			}
		}

		timings.AfterAction = time.Since(tPhaseStart)

		// Loop detection: track per-call signatures and check for repeating patterns.
		for _, call := range calls {
			toolSigs = append(toolSigs, call.Name+":"+shortHash(call.Arguments))
		}
		if s.cfg.EnableLoopDetection != nil && *s.cfg.EnableLoopDetection {
			if detectLoop(toolSigs, s.cfg.LoopDetectionWindow) {
				s.mu.Lock()
				s.loopDetectionCount++
				count := s.loopDetectionCount
				s.mu.Unlock()

				warning := s.stuckEscalation(count)
				if abortErr := s.withResponseSideEffects(ctx, func() {
					s.emit(EventLoopDetection, LoopDetectionData{Message: warning})
					s.appendTurn(TurnSteering, llm.User(warning))
					s.emit(EventSteeringInjected, SteeringInjectedData{Text: warning})
				}); abortErr != nil {
					return "", abortErr
				}
			}
		}

		// Read-only streak detection: nudge agent if stuck in analysis paralysis.
		allReadOnly := len(calls) > 0
		for _, call := range calls {
			t := s.reg.Get(call.Name)
			if t == nil || !t.ReadOnly {
				allReadOnly = false
				break
			}
		}
		if allReadOnly {
			s.readOnlyStreak++
		} else {
			s.readOnlyStreak = 0
		}
		if s.readOnlyStreak == 5 {
			nudge := "<SYSTEM-REMINDER>You have spent several turns reading without writing or running anything. Review your current task. If you have enough context to make progress, write code or run a command now. A first attempt you can test and fix is more valuable than more reading.</SYSTEM-REMINDER>"
			if abortErr := s.withResponseSideEffects(ctx, func() {
				s.appendTurn(TurnSteering, llm.User(nudge))
				s.emit(EventSteeringInjected, SteeringInjectedData{Text: nudge})
			}); abortErr != nil {
				return "", abortErr
			}
		} else if s.readOnlyStreak == 10 {
			nudge := "<SYSTEM-REMINDER>You have been reading for 10 turns without acting. Stop reading. Write the deliverable file now, even if incomplete. You can iterate after you have something to test.</SYSTEM-REMINDER>"
			if abortErr := s.withResponseSideEffects(ctx, func() {
				s.appendTurn(TurnSteering, llm.User(nudge))
				s.emit(EventSteeringInjected, SteeringInjectedData{Text: nudge})
			}); abortErr != nil {
				return "", abortErr
			}
		}

		// Inject any queued steering messages before the next model call.
		if abortErr := s.withResponseSideEffects(ctx, func() {
			for _, msg := range s.drainSteering() {
				s.appendTurn(TurnSteering, steeringMessageToLLM(msg))
				s.emit(EventSteeringInjected, steeringInjectedDataFromMessage(msg))
			}
		}); abortErr != nil {
			return "", abortErr
		}

		// Task reminder injection.
		if abortErr := s.withResponseSideEffects(ctx, func() {
			if reminder := s.maybeInjectTaskReminder(); reminder != "" {
				s.appendTurn(TurnSteering, llm.User(reminder))
				s.emit(EventSteeringInjected, SteeringInjectedData{Text: reminder})
			}
		}); abortErr != nil {
			return "", abortErr
		}

		// Emit round timings before checking result delivery.
		timings.TotalRound = time.Since(roundStart)
		timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall - timings.ToolExec - timings.Persistence - timings.AfterAction
		s.emit(EventRoundTimings, timings)

		// communicate sets the flag; exit the loop with the communicated message.
		s.mu.Lock()
		delivered := s.communicated
		awaitReply := s.communicateAwaitReply
		text := s.communicateReply
		s.mu.Unlock()
		if delivered {
			// Stop hooks
			if s.hookRunner != nil {
				hi := s.hookInput(HookStop)
				if awaitReply {
					hi.Reason = "communicate.await_reply"
				} else {
					hi.Reason = "communicate.complete"
				}
				stopResult := s.hookRunner.RunStop(ctx, hi)
				for _, msg := range stopResult.SystemMessages {
					s.Steer(msg)
				}
				if stopResult.Blocked {
					// Don't return — continue the loop
					continue
				}
			}
			s.mu.Lock()
			if !s.closingOrClosedLocked() {
				if awaitReply {
					s.state = SessionAwaitingInput
				} else {
					s.state = SessionIdle
				}
			}
			s.mu.Unlock()
			return text, nil
		}
	}

	s.emit(EventTurnLimit, TurnLimitData{MaxToolRoundsPerInput: s.cfg.MaxToolRoundsPerInput})
	s.mu.Lock()
	s.setStateIfOpenLocked(SessionIdle)
	s.mu.Unlock()
	return lastText, nil
}

// buildModelRequest assembles the llm.Request for one round: it lays out the
// system prompt + history into messages (honoring SystemPromptAsUser), then
// applies tools, provider options, reasoning effort, and model metadata.
func (s *Session) buildModelRequest(sys string, history []llm.Message, toolDefs []llm.ToolDefinition) llm.Request {
	var messages []llm.Message
	if s.cfg.SystemPromptAsUser {
		// Combine system prompt with the first user message into one
		// message so instructions are adjacent to the task. GPT-5.4
		// ignores the instructions parameter and follows user messages.
		if len(history) > 0 && history[0].Role == llm.RoleUser {
			messages = make([]llm.Message, len(history))
			copy(messages, history)
			messages[0] = prependSystemPromptToUserMessage(sys, history[0])
		} else {
			messages = append([]llm.Message{llm.User(sys)}, history...)
		}
	} else {
		messages = append([]llm.Message{llm.System(sys)}, history...)
	}

	req := llm.Request{
		Model:      s.profile.Model(),
		Provider:   s.profile.ID(),
		Messages:   messages,
		Tools:      toolDefs,
		ToolChoice: &llm.ToolChoice{Mode: "required"},
		WebSearch:  s.profile.SupportsWebSearch(),
		AdapterTimeout: &llm.AdapterTimeout{
			Connect:    10 * time.Second,
			Request:    10 * time.Minute,
			StreamRead: 30 * time.Second,
		},
	}
	if opts := s.profile.ProviderOptions(); opts != nil {
		req.ProviderOptions = opts
	}
	if s.taskStore != nil {
		if current, ok := s.taskStore.CurrentInProgress(); ok && current.ReasoningEffort != "" {
			s.cfg.ReasoningEffort = current.ReasoningEffort
		}
	}
	if strings.TrimSpace(s.cfg.ReasoningEffort) != "" {
		v := strings.TrimSpace(s.cfg.ReasoningEffort)
		req.ReasoningEffort = &v
	}
	s.applyModelRequestMetadata(&req)
	return req
}

// callModelWithFallback issues the model call for one round and, on a
// fallback-eligible permanent error, retries each configured fallback model in
// order. It returns the (possibly fallback-updated) request actually used so
// downstream logging reflects the model that answered.
func (s *Session) callModelWithFallback(ctx context.Context, req llm.Request, round int) (sessionModelResponse, llm.Request, error) {
	policy := llm.DefaultRetryPolicy()
	if s.cfg.LLMRetryPolicy != nil {
		policy = *s.cfg.LLMRetryPolicy
	}
	callCtx := llm.WithAPILogContext(ctx, s.id, round)
	modelResp, err := s.callModel(callCtx, policy, s.profile, req)
	// Fallback chain: when the primary model returns a Permanent-class
	// provider error (403/404/422/...) or an endpoint-fallback signal,
	// try each configured fallback in literal order. Stops at the first
	// success; if all fallbacks also fail, the LAST attempt's error is
	// returned to the caller. Retryable errors (429/5xx) burn the
	// existing retry budget on the same model and DO NOT trigger the
	// fallback chain — they are handled by the retry loop inside
	// callModel. Kata cxw8.
	if err != nil && len(s.cfg.ModelFallbacks) > 0 && modelFallbackEligible(err) {
		for _, fbModel := range s.cfg.ModelFallbacks {
			// validateModelFallbacks already rejected cross-provider fallbacks,
			// so resolveProfileForRef is guaranteed to return the WithModel path
			// here. We call it anyway so the fallback always uses the same
			// resolution logic as SetModel.
			fbProfile, _, _ := s.resolveProfileForRef(fbModel)
			fbReq := req
			fbReq.Model = fbProfile.Model()
			fbReq.Provider = fbProfile.ID()
			fbReq.WebSearch = fbProfile.SupportsWebSearch()
			fbReq.ProviderOptions = fbProfile.ProviderOptions()
			fbReq.PromptCacheKey = ""
			fbReq.PromptCacheRetention = ""
			s.applyModelRequestMetadata(&fbReq)
			modelResp, err = s.callModel(callCtx, policy, fbProfile, fbReq)
			if err == nil {
				// Reflect the model that actually answered in the
				// request used for downstream logging (transcript,
				// EventAssistantTextStart fallback path, etc).
				req = fbReq
				break
			}
		}
	}
	return modelResp, req, err
}

// logAPICall records one round's request/response (or error) to the transcript.
func (s *Session) logAPICall(round int, roundStart time.Time, llmLatency time.Duration, sys string, historyLen int, req llm.Request, resp llm.Response, err error) {
	if s.transcript != nil {
		apiCall := TranscriptAPICall{
			Round:               round,
			Timestamp:           roundStart.UTC().Format(time.RFC3339),
			LatencyMs:           llmLatency.Milliseconds(),
			SystemPrompt:        sys,
			ContextHistoryTurns: historyLen,
			SystemPromptBytes:   len(sys),
			Request:             llm.BuildAPILogRequest(req),
		}
		if err != nil {
			apiCall.Error = err.Error()
			setAPICallDiagnostic(&apiCall, err)
		} else {
			var endpoint string
			if resp.Raw != nil {
				if v, ok := resp.Raw["endpoint_url"].(string); ok {
					endpoint = v
				}
			}
			apiCall.Response = &llm.APILogResponse{
				ID:            resp.ID,
				Model:         resp.Model,
				FinishReason:  resp.Finish.Reason,
				TextLength:    len(resp.Text()),
				ToolCallCount: len(resp.ToolCalls()),
				Usage:         resp.Usage,
				EndpointURL:   endpoint,
				Raw:           resp.Raw,
			}
		}
		s.transcript.AppendAPICall(apiCall)
	}
}

// expandHistory flattens conversation turns into the per-message slice sent to
// the model: steering/checkpoint/summary turns pass through as-is, and
// aggregated tool-result turns are expanded into individual tool-result messages.
func expandHistory(historyTurns []Turn) []llm.Message {
	history := make([]llm.Message, 0, len(historyTurns))
	for _, t := range historyTurns {
		if t.Kind == TurnSteering {
			history = append(history, t.Message)
			continue
		}
		if t.Kind == TurnToolResults {
			// Expand aggregated tool results into individual messages.
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					history = append(history, llm.ToolResultNamed(
						p.ToolResult.ToolCallID,
						p.ToolResult.Name,
						p.ToolResult.Content,
						p.ToolResult.IsError,
					))
				}
			}
			continue
		}
		if t.Kind == TurnCheckpoint || t.Kind == TurnSummary {
			// Compaction turns carry user-role messages; include as-is.
			history = append(history, t.Message)
			continue
		}
		history = append(history, t.Message)
	}
	return history
}

type sessionModelResponse struct {
	Response          llm.Response
	StreamedAssistant bool
}

func (s *Session) callModel(ctx context.Context, policy llm.RetryPolicy, profile ProviderProfile, req llm.Request) (sessionModelResponse, error) {
	if profile.SupportsStreaming() {
		st, err := llm.Retry(ctx, policy, s.cfg.LLMSleep, nil, func() (llm.Stream, error) {
			st, err := s.client.Stream(ctx, req)
			if err == nil && st == nil {
				return nil, nil
			}
			if streamUnavailable(err) {
				return nil, nil
			}
			return st, err
		})
		if err != nil {
			return sessionModelResponse{}, err
		}
		if st != nil {
			return s.consumeModelStream(ctx, req, st)
		}
	}

	resp, err := llm.Retry(ctx, policy, s.cfg.LLMSleep, nil, func() (llm.Response, error) {
		return s.client.Complete(ctx, req)
	})
	if err != nil {
		return sessionModelResponse{}, err
	}
	return sessionModelResponse{Response: resp}, nil
}

func isTurnCancellation(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return true
	}
	var abort *llm.AbortError
	return errors.As(err, &abort)
}

func streamUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errStreamUnavailable) {
		return true
	}
	return errors.Is(err, llm.ErrStreamUnsupported)
}

func (s *Session) consumeModelStream(ctx context.Context, req llm.Request, st llm.Stream) (sessionModelResponse, error) {
	defer st.Close() //nolint:errcheck

	acc := llm.NewStreamAccumulator()
	toolArgs := map[string]*strings.Builder{}
	toolNames := map[string]string{}
	communicateText := map[string]string{}
	streamedAssistant := false
	assistantStarted := false
	finished := false

	emitAssistantStart := func() {
		if assistantStarted {
			return
		}
		s.emit(EventAssistantTextStart, AssistantTextStartData{Model: req.Model})
		assistantStarted = true
		streamedAssistant = true
	}
	emitAssistantDelta := func(delta string) {
		if delta == "" {
			return
		}
		emitAssistantStart()
		s.emit(EventAssistantTextDelta, AssistantTextDeltaData{Delta: delta})
	}
	emitCommunicatePreview := func(callID string) {
		args := ""
		if b := toolArgs[callID]; b != nil {
			args = b.String()
		}
		message, ok := partialJSONStringField(args, "message")
		if !ok || message == "" {
			return
		}
		prev := communicateText[callID]
		if len(message) <= len(prev) || !strings.HasPrefix(message, prev) {
			return
		}
		communicateText[callID] = message
		emitAssistantDelta(message[len(prev):])
	}

	for ev := range st.Events() {
		acc.Process(ev)
		switch ev.Type {
		case llm.StreamEventTextStart:
			emitAssistantStart()
		case llm.StreamEventTextDelta:
			emitAssistantDelta(ev.Delta)
		case llm.StreamEventToolCallStart:
			if ev.ToolCall == nil || ev.ToolCall.ID == "" {
				break
			}
			toolNames[ev.ToolCall.ID] = s.canonicalToolName(ev.ToolCall.Name)
			if _, ok := toolArgs[ev.ToolCall.ID]; !ok {
				toolArgs[ev.ToolCall.ID] = &strings.Builder{}
			}
		case llm.StreamEventToolCallDelta:
			if ev.ToolCall == nil || ev.ToolCall.ID == "" {
				break
			}
			if ev.ToolCall.Name != "" {
				toolNames[ev.ToolCall.ID] = s.canonicalToolName(ev.ToolCall.Name)
			}
			if _, ok := toolArgs[ev.ToolCall.ID]; !ok {
				toolArgs[ev.ToolCall.ID] = &strings.Builder{}
			}
			if len(ev.ToolCall.Arguments) > 0 {
				toolArgs[ev.ToolCall.ID].Write(ev.ToolCall.Arguments)
			}
			if toolNames[ev.ToolCall.ID] == s.resultToolName() {
				emitCommunicatePreview(ev.ToolCall.ID)
			}
		case llm.StreamEventToolCallEnd:
			if ev.ToolCall == nil || ev.ToolCall.ID == "" {
				break
			}
			if ev.ToolCall.Name != "" {
				toolNames[ev.ToolCall.ID] = s.canonicalToolName(ev.ToolCall.Name)
			}
			if len(ev.ToolCall.Arguments) > 0 {
				b := &strings.Builder{}
				b.Write(ev.ToolCall.Arguments)
				toolArgs[ev.ToolCall.ID] = b
			}
			if toolNames[ev.ToolCall.ID] == s.resultToolName() {
				emitCommunicatePreview(ev.ToolCall.ID)
			}
		case llm.StreamEventFinish:
			finished = true
		case llm.StreamEventError:
			if ev.Err != nil {
				return sessionModelResponse{}, ev.Err
			}
			return sessionModelResponse{}, llm.NewStreamError(req.Provider, "stream error")
		}
	}

	if !finished {
		if err := ctx.Err(); err != nil {
			return sessionModelResponse{}, err
		}
		return sessionModelResponse{}, llm.NewStreamError(req.Provider, "stream ended without finish event")
	}
	resp := acc.Response()
	if resp == nil {
		return sessionModelResponse{}, llm.NewStreamError(req.Provider, "stream ended without response")
	}
	if resp.Provider == "" {
		resp.Provider = req.Provider
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return sessionModelResponse{Response: *resp, StreamedAssistant: streamedAssistant}, nil
}

func partialJSONStringField(raw, field string) (string, bool) {
	key := `"` + field + `"`
	idx := strings.Index(raw, key)
	if idx < 0 {
		return "", false
	}
	rest := raw[idx+len(key):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", false
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t\r\n")
	if !strings.HasPrefix(rest, `"`) {
		return "", false
	}
	rest = rest[1:]

	var b strings.Builder
	for len(rest) > 0 {
		ch := rest[0]
		if ch == '"' {
			return b.String(), true
		}
		if ch == '\\' {
			if len(rest) >= 2 && rest[1] == '/' {
				b.WriteByte('/')
				rest = rest[2:]
				continue
			}
			if strings.HasPrefix(rest, `\u`) {
				r, tail, ok := unquoteJSONUnicodeEscape(rest)
				if !ok {
					return b.String(), true
				}
				b.WriteRune(r)
				rest = tail
				continue
			}
			r, _, tail, err := strconv.UnquoteChar(rest, '"')
			if err != nil {
				return b.String(), true
			}
			b.WriteRune(r)
			rest = tail
			continue
		}
		b.WriteByte(ch)
		rest = rest[1:]
	}
	return b.String(), true
}

func unquoteJSONUnicodeEscape(rest string) (rune, string, bool) {
	if len(rest) < 6 {
		return 0, "", false
	}
	value, err := strconv.ParseUint(rest[2:6], 16, 16)
	if err != nil {
		return 0, "", false
	}
	r := rune(value)
	tail := rest[6:]
	if r >= 0xD800 && r <= 0xDBFF {
		if len(tail) < 6 || !strings.HasPrefix(tail, `\u`) {
			return 0, "", false
		}
		lowValue, err := strconv.ParseUint(tail[2:6], 16, 16)
		if err != nil {
			return 0, "", false
		}
		low := rune(lowValue)
		if low < 0xDC00 || low > 0xDFFF {
			return 0, "", false
		}
		return utf16.DecodeRune(r, low), tail[6:], true
	}
	return r, tail, true
}

// stuckEscalation returns the steering message for the nth loop detection.
// First detection bumps reasoning effort; subsequent detections get increasingly
// direct about abandoning the current approach.
func (s *Session) stuckEscalation(count int) string {
	switch count {
	case 1:
		// Bump reasoning effort to help the agent think harder.
		s.mu.Lock()
		prev := s.cfg.ReasoningEffort
		if prev == "" || prev == "low" || prev == "medium" {
			s.cfg.ReasoningEffort = "high"
		} else if prev == "high" {
			s.cfg.ReasoningEffort = "xhigh"
		}
		s.mu.Unlock()
		return "You are stuck in a loop. Your reasoning effort has been increased. " +
			"Stop and think about why your current approach is not working. " +
			"What assumption are you making that might be wrong?"
	case 2:
		return "You are still stuck. Your current approach is fundamentally not working. " +
			"Abandon it completely and try a different strategy. " +
			"What is the simplest possible way to achieve the goal?"
	default:
		return "You have been stuck for a long time. " +
			"If you cannot make progress, report what you tried and what failed."
	}
}

// looksLikeQuestion returns true when the assistant text appears to be asking
// the user a question or requesting input (ends with "?" or ":").
func looksLikeQuestion(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	return strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, ":")
}

// detectLoop checks the last windowSize tool call signatures for repeating
// patterns of length 1, 2, or 3.
func detectLoop(signatures []string, windowSize int) bool {
	if len(signatures) < windowSize {
		return false
	}
	recent := signatures[len(signatures)-windowSize:]
	for patLen := 1; patLen <= 3; patLen++ {
		if windowSize%patLen != 0 {
			continue
		}
		pattern := recent[:patLen]
		allMatch := true
		for i := patLen; i < windowSize; i++ {
			if recent[i] != pattern[i%patLen] {
				allMatch = false
				break
			}
		}
		if allMatch {
			return true
		}
	}
	return false
}
