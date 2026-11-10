package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/toolname"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// handleNoToolCalls reacts to a model response that produced no tool calls.
// noContent distinguishes a truly empty response — a model glitch (e.g.
// gpt-5.3-codex null-content); retried with escalating "you went silent" steering,
// phase-only responses included — from bare text that bypassed the result tool,
// which violates the all-user-facing-messages-go-through-the-result-tool contract
// and is retried with "use the result tool" steering. It updates the retry counters
// in t and returns retry=true to inject the steering and retry the round, or
// retry=false with the terminal error once the relevant retry budget is spent.
func (s *Session) handleNoToolCalls(noContent bool, t *retryTracker) (retry bool, ferr error) {
	dec := decideNoToolCalls(noContent, *t, s.resultToolName())
	*t = dec.Tracker
	return s.applyNoToolCallsDecision(dec)
}

func (s *Session) applyNoToolCallsDecision(dec noToolCallsDecision) (retry bool, ferr error) {
	if dec.Retry {
		s.emit(events.WarningData{Message: dec.WarningMsg})
		s.appendSteeringTurn(dec.SteeringText, events.SteeringKindNoToolCalls)
		return true, nil
	}
	switch dec.TerminalKind {
	case noToolTerminalEmptyExhausted:
		err := &emptyResponseExhaustedError{retries: maxEmptyRetries}
		s.emitTurnFailure(errorDataFromError(err))
		s.finishProcessingAtFailureBoundary(context.Background())
		return false, err
	case noToolTerminalBareTextExhausted:
		err := &bareTextWithoutResultToolError{
			toolName: s.resultToolName(),
			retries:  maxBareTextRetries,
		}
		s.emitTurnFailure(errorDataFromError(err))
		s.finishProcessingAtFailureBoundary(context.Background())
		return false, err
	}
	return false, nil
}

// noToolTerminal names the terminal outcome when the no-tool-calls retry budget is
// exhausted (none when the round should retry instead).
type noToolTerminal int

const (
	noToolTerminalNone noToolTerminal = iota
	noToolTerminalEmptyExhausted
	noToolTerminalBareTextExhausted
)

// noToolCallsDecision is the outcome of decideNoToolCalls.
type noToolCallsDecision struct {
	Tracker      retryTracker
	Retry        bool
	WarningMsg   string
	SteeringText string
	TerminalKind noToolTerminal
}

// decideNoToolCalls is the pure decision core lifted out of handleNoToolCalls. Given
// whether the response was truly empty (noContent) versus bare text that bypassed the
// result tool, the current retry counters, and the result tool's name, it advances the
// counters and decides whether to retry (with the warning + steering text to inject)
// or to terminate (empty- or bare-text-budget exhausted). The caller performs all
// side effects. The returned Tracker is the updated counter snapshot.
func decideNoToolCalls(noContent bool, t retryTracker, resultToolName string) noToolCallsDecision {
	if noContent {
		t.consecutiveEmpty++
		t.totalEmpty++
		if t.consecutiveEmpty <= maxEmptyRetries && t.totalEmpty <= maxTotalEmptyResponses {
			var steering string
			switch t.consecutiveEmpty {
			case 1:
				steering = "Your previous response was empty. Please continue working on the task."
			case 2:
				steering = "Your response was empty again. If you're stuck, write what you've tried so far " +
					"to notes.txt, then try a different approach. You have plenty of rounds left."
			default:
				steering = "You've produced multiple empty responses. You MUST either call a tool to continue " +
					"working, or call " + resultToolName + " with your best effort so far. Take a breath — you've " +
					"got this. Try a completely different approach."
			}
			return noToolCallsDecision{
				Tracker:      t,
				Retry:        true,
				WarningMsg:   fmt.Sprintf("empty response from model (retry %d/%d)", t.consecutiveEmpty, maxEmptyRetries),
				SteeringText: steering,
			}
		}
		return noToolCallsDecision{Tracker: t, TerminalKind: noToolTerminalEmptyExhausted}
	}

	t.consecutiveEmpty = 0
	t.consecutiveBareText++
	if t.consecutiveBareText <= maxBareTextRetries {
		steering := "You responded with bare text instead of a tool call. " +
			"All user-facing messages MUST use " + resultToolName + ". " +
			"If that bare text was meant for the user, call " + resultToolName + " now with that text in message, set end_turn=true, and include the output envelope. " +
			"Otherwise call your next tool and keep working."
		return noToolCallsDecision{
			Tracker:      t,
			Retry:        true,
			WarningMsg:   fmt.Sprintf("bare text response without tool call (retry %d/%d)", t.consecutiveBareText, maxBareTextRetries),
			SteeringText: steering,
		}
	}
	return noToolCallsDecision{Tracker: t, TerminalKind: noToolTerminalBareTextExhausted}
}

// batchToolCalls renders one PostToolBatch tool_calls entry per call in the
// round. It maps the four fields the event documents directly instead of going
// through toolHookInput, which would run the whole hookInput builder (two mu
// sections, currentEnv, TranscriptPath) per call for three of them:
//
// A call whose raw bytes the repair step rejected omits tool_input, exactly as
// PostToolUse does for the same call; the raw-argument bound is checked before
// any decode, so oversized or invalid-UTF-8 bytes never reach a lossy
// json.Unmarshal. A call with no positional result (an aborted round) carries an
// empty tool_response.
// beginToolRound starts the per-round record of dispatched arguments. The map
// is replaced, not cleared, so a round's entries cannot outlive it: without
// this a long-lived session would retain every tool payload it ever ran.
func (s *Session) beginToolRound() {
	s.mu.Lock()
	s.dispatchedToolArgs = map[string][]byte{}
	s.mu.Unlock()
}

// recordDispatchedToolArguments remembers the arguments a call actually ran
// with, after the repair step and any PreToolUse updatedInput.
func (s *Session) recordDispatchedToolArguments(call llm.ToolCallData) {
	if s == nil || call.ID == "" {
		return
	}
	s.mu.Lock()
	if s.dispatchedToolArgs == nil {
		s.dispatchedToolArgs = map[string][]byte{}
	}
	s.dispatchedToolArgs[call.ID] = append([]byte(nil), call.Arguments...)
	s.mu.Unlock()
}

// dispatchedToolArguments returns the arguments call was dispatched with, or
// its original bytes when nothing was recorded (a call that never dispatched).
func (s *Session) dispatchedToolArguments(call llm.ToolCallData) []byte {
	s.mu.Lock()
	raw, ok := s.dispatchedToolArgs[call.ID]
	s.mu.Unlock()
	if ok {
		return raw
	}
	return call.Arguments
}

// batchToolInput decodes one dispatched call's arguments for PostToolBatch,
// through the registered tool's own decoder so an exact integer survives as it
// does for the tool and for the per-tool hooks.
func (s *Session) batchToolInput(call llm.ToolCallData) map[string]any {
	raw := s.dispatchedToolArguments(call)
	if len(raw) == 0 || tool.ValidateRawArguments(raw) != nil {
		return nil
	}
	var input map[string]any
	registered := s.reg.Get(call.Name)
	if registered == nil {
		registered = &tool.RegisteredTool{}
	}
	if err := registered.UnmarshalArguments(raw, &input); err != nil {
		return nil
	}
	return input
}

func (s *Session) batchToolCalls(calls []llm.ToolCallData, results []tool.ExecResult) []hooks.BatchToolCall {
	out := make([]hooks.BatchToolCall, len(calls))
	for i, call := range calls {
		// batchToolInput applies the same raw-argument bound and the same
		// per-tool decoder as dispatch, so a rejected byte sequence yields no
		// input and an exact integer keeps its value.
		input := s.batchToolInput(call)
		response := ""
		callID := call.ID
		if i < len(results) {
			response = results[i].Output
			// prepareToolCall synthesizes an ID for a provider call that had
			// none, and dispatch records the arguments under it. The result
			// carries the ID the call actually ran under, which is what a hook
			// matches on.
			if callID == "" {
				callID = results[i].CallID
			}
		}
		out[i] = hooks.BatchToolCall{
			ToolName:     toolname.EvenerToClaude(call.Name),
			ToolInput:    input,
			ToolUseID:    callID,
			ToolResponse: response,
		}
	}
	return out
}

// execToolBatch executes the round's tool calls and returns their results. When the
// profile supports parallel tool calls and there is more than one, it batches
// consecutive read-only calls to run concurrently (ordered-group algorithm) and
// serializes everything else; otherwise it runs them in order. On cancellation it
// records canceled results for the outstanding calls — keeping history well-formed —
// and returns the abort error alongside the partial results.
func (s *Session) execToolBatch(ctx context.Context, calls []llm.ToolCallData, profile *provider.Profile, finishReason string) ([]tool.ExecResult, error) {
	results := make([]tool.ExecResult, len(calls))
	if len(calls) > 1 {
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
				results[batch[0]] = s.execTool(ctx, calls[batch[0]], finishReason)
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
						results[i] = s.execTool(ctx, calls[i], finishReason)
					}()
				}
				wg.Wait()
				if panicValue != nil {
					return toolBatchPanicError(panicValue)
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
					return results, err
				}
				readBatch = readBatch[:0]
				if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
					if ctx.Err() != nil && !s.isClosingOrClosed() {
						s.appendCanceledToolResults(calls, results, abortErr)
					}
					return results, abortErr
				}
				results[i] = s.execTool(ctx, call, finishReason)
			}
		}
		if err := flushReadBatch(readBatch); err != nil {
			if ctx.Err() != nil && !s.isClosingOrClosed() {
				s.appendCanceledToolResults(calls, results, err)
			}
			return results, err
		}
	} else {
		for i := range calls {
			if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
				if ctx.Err() != nil && !s.isClosingOrClosed() {
					s.appendCanceledToolResults(calls, results, abortErr)
				}
				return results, abortErr
			}
			results[i] = s.execTool(ctx, calls[i], finishReason)
		}
	}

	if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
		if ctx.Err() != nil && !s.isClosingOrClosed() {
			s.appendCanceledToolResults(calls, results, abortErr)
		}
		return results, abortErr
	}
	return results, nil
}

func toolBatchPanicError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	panic(value)
}

// persistToolResults aggregates the round's tool results into a single tool-result
// turn and appends it to history. For any result carrying image/document data it
// makes a side-channel vision call (GPT models in tool-calling mode never describe
// images themselves — they immediately write code) and injects the description as
// steering so the agent can use it. It returns the abort error if the turn is
// canceled mid-persist.
func (s *Session) persistToolResults(ctx context.Context, calls []llm.ToolCallData, results []tool.ExecResult) (retErr error) {
	defer func() {
		if retErr != nil {
			s.removeAllTurnOwnedSteering()
		}
	}()
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
				PrevalOnly:     r.PrevalOnly,
				DurationMS:     r.DurationMS,
				ToolState:      r.ToolState,
				ImageData:      r.ImageData,
				ImageMediaType: r.ImageMediaType,
			},
		})
	}
	if abortErr := s.appendToolResults(ctx, calls, results, parts); abortErr != nil {
		return abortErr
	}
	s.consumePersistedTerminalJobNotifications(calls, results)

	for i, r := range results {
		if len(r.ImageData) > 0 {
			vision := s.describeImageCall(ctx, r)
			path := ""
			if i < len(calls) {
				var args map[string]any
				if json.Unmarshal(calls[i].Arguments, &args) == nil {
					path, _ = args["file_path"].(string)
				}
			}
			if vision.outcome == visionSideChannelOwnedTimeout || vision.outcome == visionSideChannelProviderFailure ||
				vision.outcome == visionSideChannelUndeliverableDocument {
				owner := &struct{ _ byte }{}
				if abortErr := s.withResponseSideEffects(ctx, func() {
					s.trySteerTurnOwnedMessage(steeringMessage{Text: visionFailureSteering(path, vision), Kind: events.SteeringKindImageDescription}, owner)
				}); abortErr != nil {
					return abortErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
			} else if vision.outcome == visionSideChannelParentCanceled {
				return ctx.Err()
			} else if vision.description != "" {
				desc := vision.description + "\n" + formatVisionSideChannelStats(vision)
				// Include the file path so the agent can correlate descriptions to
				// specific files when multiple images/documents are read in one round.
				label := "Image description (from vision)"
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.ImageMediaType)), "application/pdf") {
					label = "Document description (from content analysis)"
				}
				if path != "" {
					label += " for " + path
				}
				var steerErr error
				if abortErr := s.withResponseSideEffects(ctx, func() {
					steerErr = s.steerKind(label+": "+desc+"\n"+systemReminder(visionConsumerReminder),
						events.SteeringKindImageDescription)
				}); abortErr != nil {
					return abortErr
				}
				if steerErr != nil {
					return steerErr
				}
			}
		}
	}
	return nil
}

// hasTerminalJobResult reports whether any reply in the round tells the caller
// a job ended (terminalJobResult). Such a round is written durably, because
// consumePersistedTerminalJobNotifications settles those jobs' notifications
// only on the strength of the reply having reached the transcript.
func hasTerminalJobResult(calls []llm.ToolCallData, results []tool.ExecResult) bool {
	if len(calls) != len(results) {
		return false
	}
	for i, call := range calls {
		if _, ok := terminalJobResult(call, results[i]); ok {
			return true
		}
	}
	return false
}

// terminalJobResult returns the job a tool reply told the caller had ended: a
// terminal job_status read, or a foreground shell kept as a job because its
// output was too big to ride inline (its reply carries the job_id and the
// terminal status). Both replies spell those two fields the same way. A shell
// reply in background mode never counts, whatever its status: it tells the
// caller the job carries on and will notify (a foreground runtime timeout
// whose finalization failed replies failed/finalize_failed that way).
func terminalJobResult(call llm.ToolCallData, result tool.ExecResult) (string, bool) {
	if result.IsError || (call.Name != "job_status" && call.Name != "shell") {
		return "", false
	}
	var reply struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(result.ToolState, &reply); err != nil || reply.JobID == "" || !jobstore.Status(reply.Status).IsTerminal() {
		return "", false
	}
	if call.Name == "shell" && reply.Mode != string(shellModeForeground) {
		return "", false
	}
	return reply.JobID, true
}

// consumePersistedTerminalJobNotifications settles the notification of every
// job a just-persisted tool reply told the caller had ended. Settling only
// after the reply is durable keeps the crash case right: a reply that never
// reached the transcript leaves the job for restore to re-arm. The durable
// append reports success for a line it wrote but could not sync (fsync and
// rollback both failed), so durability is established here before anything
// settles; if it cannot be, nothing does, and a restore re-arms the jobs. (A
// session with no transcript attached has no durability to establish.) A
// crash after the reply is durable but before a settlement is written re-arms
// too: a repeated notification, never a lost one. So does a kept shell whose
// job_finished write failed and is still being retried when its reply
// persists: the record is not yet terminal, nothing settles, and the later
// finalization leaves it not_armed. Both are rare fault paths, and both err
// toward telling the caller twice rather than not at all.
func (s *Session) consumePersistedTerminalJobNotifications(calls []llm.ToolCallData, results []tool.ExecResult) {
	if !hasTerminalJobResult(calls, results) {
		return
	}
	if tw := s.attachedTranscript(); tw != nil && tw.EstablishDurability() != nil {
		return
	}
	for i, call := range calls {
		if i >= len(results) {
			continue
		}
		jobID, ok := terminalJobResult(call, results[i])
		if !ok {
			continue
		}
		jm, rec, err := s.nestedOrLocalJobManager(jobID)
		if err != nil {
			continue
		}
		consumeTerminalJobNotification(s, jm, rec)
	}
}

// notifyStrategyAfterAction tells the context strategy that a tool round completed
// so it can run any post-action bookkeeping. A strategy error is surfaced as a
// warning, not a turn failure; only cancellation aborts the turn.
func (s *Session) notifyStrategyAfterAction(ctx context.Context) error {
	if s.strategy == nil {
		return nil
	}
	if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
		return abortErr
	}
	// AfterAction takes []Turn (not *[]Turn) so it cannot mutate the slice, but
	// a read-only parameter is not ownership: asynchronous attention delivery
	// replaces resident entries in place and removes them with a shifting
	// delete, both under mu, at any time. Hand the strategy a private snapshot
	// so its reads never race those writes — the same snapshot the
	// request-preparation path takes. The turns themselves are immutable:
	// attention's writes replace whole elements and never mutate a turn's
	// message, so a shallow copy of the slice is sufficient.
	s.mu.Lock()
	hist := append([]schema.Turn(nil), s.history...)
	s.mu.Unlock()
	afterErr := s.strategy.AfterAction(ctx, hist, s.client)
	// Warn once per failing streak: a repeated failure is suppressed while a
	// successful call clears the state so a later failure warns again.
	degraded := afterErr != nil
	s.mu.Lock()
	warn := degraded && !s.strategyDegraded
	s.strategyDegraded = degraded
	s.mu.Unlock()
	if warn {
		if abortErr := s.withResponseSideEffects(ctx, func() {
			s.emit(bareWarningDataFromError("strategy AfterAction error", afterErr))
		}); abortErr != nil {
			return abortErr
		}
	}
	if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
		return abortErr
	}
	return nil
}

// injectPostToolSteering runs the post-tool-round bookkeeping that may inject
// steering before the next model call: it appends the round's tool-call signatures
// to toolSigs and whether each call failed to toolSigFailed, runs loop detection,
// drains queued steering messages, and injects any task reminder. The bool return
// asks the turn loop to yield after a watch frame is handed to an observer and no
// callback steering is ready yet.
func (s *Session) injectPostToolSteering(ctx context.Context, calls []llm.ToolCallData, results []tool.ExecResult, toolSigs *[]string, toolSigFailed *[]bool) (bool, error) {
	// Loop detection: track per-call signatures and check for repeating patterns.
	// Results match calls positionally, as persistToolResults does. A length
	// mismatch means the round was aborted before every call produced a result,
	// so nothing in it is judged a failure rather than guessing which call is which.
	haveResults := len(results) == len(calls)
	lastFailure := ""
	for i, call := range calls {
		// RawArguments is always empty here: calls come from resp.ToolCalls()
		// (value copies), before assistantHistoryMessage sets RawArguments.
		*toolSigs = append(*toolSigs, call.Name+":"+shortHash(call.Arguments))
		failed := haveResults && results[i].IsError
		*toolSigFailed = append(*toolSigFailed, failed)
		if failed {
			lastFailure = results[i].Output
		}
	}
	if s.cfg.EnableLoopDetection != nil && *s.cfg.EnableLoopDetection {
		if detectLoop(*toolSigs, s.cfg.LoopDetectionWindow) {
			s.mu.Lock()
			s.loopDetectionCount++
			count := s.loopDetectionCount
			s.mu.Unlock()

			// A window with no successes in it needs a structural intervention, not
			// more reasoning effort. The count still advances so a later mixed loop
			// escalates from the right tier.
			var warning string
			if allFailed(*toolSigFailed, s.cfg.LoopDetectionWindow) {
				warning = failureLoopIntervention(*toolSigs, s.cfg.LoopDetectionWindow, lastFailure)
			} else {
				warning = s.stuckEscalation(count)
			}
			if abortErr := s.withResponseSideEffects(ctx, func() {
				s.emit(events.LoopDetectionData{Message: warning})
				s.appendSteeringTurn(warning, events.SteeringKindLoopDetected)
			}); abortErr != nil {
				s.removeAllTurnOwnedSteering()
				return false, abortErr
			}
		}
	}

	drained, err := s.drainPostToolWatchSends(ctx)
	if err != nil {
		s.removeAllTurnOwnedSteering()
		return false, err
	}
	yieldToObserverCallback := drained.observerHandoff
	if hooks, ok := ctx.Value(sessionToolRoundHooksKey{}).(sessionToolRoundHooks); ok && hooks.beforeSteering != nil {
		hooks.beforeSteering()
	}

	// Inject any queued steering messages before the next model call. A terminal
	// communicate result owns this input's boundary, so leave client-authored
	// steering durable pending work for the steering carrier turn the drain
	// ladder runs next (or, failing that, wakeForPendingSteering). Consuming it
	// here would mark it incorporated in a result that ends the turn without a
	// model request that can act on it.
	if abortErr := s.withResponseSideEffects(ctx, func() {
		if !s.communicated() {
			if s.hasPendingSteering() {
				yieldToObserverCallback = false
			}
			s.injectDrainedSteering()
		}
	}); abortErr != nil {
		s.removeAllTurnOwnedSteering()
		return false, abortErr
	}
	s.finishTurnOwnedSteering()
	if hooks, ok := ctx.Value(sessionToolRoundHooksKey{}).(sessionToolRoundHooks); ok && hooks.beforeTaskReminder != nil {
		hooks.beforeTaskReminder()
	}

	// Task reminder injection.
	if abortErr := s.withResponseSideEffects(ctx, func() {
		if reminder, kind := s.maybeInjectTaskReminder(); reminder != "" {
			s.appendSteeringTurn(reminder, kind)
		}
	}); abortErr != nil {
		return false, abortErr
	}
	// Shared-notes refresh: a note event that landed mid-turn (a human save,
	// an agent-note set, a URL add/remove) projects the new current state
	// before the next round, so the following model call reads what the
	// events already announced. maybeAppendNotesContext is append-only and
	// renders nothing when the store is empty.
	if abortErr := s.withResponseSideEffects(ctx, func() {
		s.maybeAppendNotesContext()
	}); abortErr != nil {
		return false, abortErr
	}
	return yieldToObserverCallback, nil
}

type sessionToolRoundHooksKey struct{}

type sessionToolRoundHooks struct {
	drainErr           error
	beforeSteering     func()
	beforeTaskReminder func()
}

func (s *Session) drainPostToolWatchSends(ctx context.Context) (watchSendDrainResult, error) {
	if hooks, ok := ctx.Value(sessionToolRoundHooksKey{}).(sessionToolRoundHooks); ok && hooks.drainErr != nil {
		return watchSendDrainResult{}, hooks.drainErr
	}
	return s.drainPendingWatchSendsReport(ctx)
}

// deliverIfCommunicated checks whether the round just completed ended the
// turn — via the communicate/result tool, via a posted ask_user question, or
// both (spec §5.1: communicate composes with an ask, never collides) — and if
// so transitions the session to the right boundary state and returns
// done=true with the reply to hand back to the caller. askedThisRound is the
// caller's per-round delta (askPendingCount grew during this round); it takes
// priority in choosing the boundary state (SessionAwaiting over SessionIdle)
// and — critically — bypasses Stop hooks entirely: pending questions are a
// stronger stop than a hook's veto, since a Blocked result would force the
// model to answer its own questions and a persistent one would exhaust the
// round cap and strand the session idle instead of awaiting. Stop hooks are
// still consulted for a communicate-only boundary, exactly as before. It
// returns done=false when nothing was delivered and no ask was posted, or a
// Stop hook blocked a communicate-only completion, meaning the turn should
// continue to the next round.
func (s *Session) deliverIfCommunicated(ctx context.Context, askedThisRound bool) (done bool, text string, err error) {
	s.mu.Lock()
	delivered := s.comm.called
	text = s.comm.reply
	s.mu.Unlock()
	stopHookActive := s.stopHookActive.Load()
	if !delivered && !askedThisRound {
		return false, "", nil
	}
	boundaryState := SessionIdle
	if askedThisRound {
		boundaryState = SessionAwaiting
	} else if s.hookRunner != nil {
		// Stop hooks (communicate-only boundary; not consulted at an ask
		// boundary — spec §5.1/§5.5).
		hi := s.hookInput(plugin.HookStop)
		hi.Reason = "communicate.end_turn"
		// stop_hook_active tells a Stop hook whether this dispatch follows a
		// block in the same turn, so a hook that blocks to ask for more work can
		// tell the continuation apart and avoid blocking forever. The reply text
		// is last_assistant_message: it is what the turn is about to deliver.
		hi.StopHookActive = &stopHookActive
		hi.LastAssistantMessage = text
		stopResult := s.hookRunner.RunStop(s.apiLogContext(ctx), hi)
		for _, m := range stopResult.ModelContext {
			if err := s.deliverHookContext(m); err != nil {
				return false, "", err
			}
		}
		for _, m := range stopResult.UserMessages {
			s.deliverHookUserMessage(m)
		}
		// continue:false on Stop ends the turn: the hook's stopReason becomes the
		// text this boundary delivers, replacing a reply the hook is vetoing. A
		// stop takes precedence over a block -- the hook asked for the action to
		// end, not to continue -- so it is checked first.
		if stopResult.Stopped {
			text = s.hookStopText(stopResult.StopReason, "Stop hook stopped this turn")
			s.deliverHookUserMessage(text)
			// The communicate call accepted and recorded its reply before this
			// dispatch, so replacing only the returned text would leave clients
			// serving the reply the hook vetoed, including after a reconnect.
			// The hook's text becomes the turn's reply on every surface.
			s.replaceAcceptedReply(text)
			// The hook replaced the reply this turn would have delivered. A
			// delegate's terminal packet must report this text, not the
			// communicated reply it vetoed.
			s.mu.Lock()
			s.stopHookReplacedReply = true
			s.mu.Unlock()
			// A stop ends the turn exactly as the ordinary boundary does, so it
			// must perform the same terminal-communicate bookkeeping: in a
			// TurnEndsProcess session the latch is what lets the drain discard
			// watch frames the model declined to wait for and keeps the process
			// from waiting on undeliverable watch-send residue (#329, #865).
			if delivered && s.cfg.TurnEndsProcess {
				s.acceptTerminalCommunicate()
			}
			s.finishProcessingAtBoundary(ctx, boundaryState)
			if hook := s.cfg.testOnly.afterCommunicateBoundary; hook != nil {
				hook(s)
			}
			return true, text, nil
		}
		if stopResult.Blocked {
			// Don't finish — keep looping. The next Stop dispatch in this turn
			// is a continuation, which is what stop_hook_active reports.
			s.stopHookActive.Store(true)
			return false, "", nil
		}
	}
	if delivered && s.cfg.TurnEndsProcess {
		// The model has explicitly ended the turn that ends the process. Record
		// it before the boundary transition: the one-shot drain reads it to
		// abandon residue with no live work behind it (issue #329). A completion
		// that landed during this answer is not residue; it is still delivered
		// on a later notification turn (#865).
		s.acceptTerminalCommunicate()
	}
	s.finishProcessingAtBoundary(ctx, boundaryState)
	if hook := s.cfg.testOnly.afterCommunicateBoundary; hook != nil {
		hook(s)
	}
	return true, text, nil
}

// replaceAcceptedReply makes the hook's text this turn's reply on every
// surface, after a Stop continue:false vetoed the one the communicate call had
// already accepted and recorded.
func (s *Session) replaceAcceptedReply(text string) {
	s.mu.Lock()
	if s.comm.called {
		s.comm.text = text
		s.comm.reply = text
	}
	if s.latestReport.called {
		s.latestReport.text = text
		s.latestReport.reply = text
	}
	s.mu.Unlock()
}
