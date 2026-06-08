package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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
	if noContent {
		t.consecutiveEmpty++
		t.totalEmpty++
		if t.consecutiveEmpty <= maxEmptyRetries && t.totalEmpty <= maxTotalEmptyResponses {
			s.emit(events.EventWarning, events.WarningData{
				Message: fmt.Sprintf("empty response from model (retry %d/%d)", t.consecutiveEmpty, maxEmptyRetries),
			})
			var steering string
			switch t.consecutiveEmpty {
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
			s.appendTurn(schema.TurnSteering, llm.User(steering))
			s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: steering})
			return true, nil
		}
		err := &emptyResponseExhaustedError{retries: maxEmptyRetries}
		s.emit(events.EventError, errorDataFromError(err))
		s.mu.Lock()
		s.setStateIfOpenLocked(SessionIdle)
		s.mu.Unlock()
		return false, err
	}

	t.consecutiveEmpty = 0
	t.consecutiveBareText++
	if t.consecutiveBareText <= maxBareTextRetries {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("bare text response without tool call (retry %d/%d)", t.consecutiveBareText, maxBareTextRetries),
		})
		steering := "You responded with bare text instead of a tool call. " +
			"All user-facing messages MUST use " + s.resultToolName() + ". " +
			"If that bare text was meant for the user, call " + s.resultToolName() + " now with that text in message, set await_reply=true only if you need user input, and include the output envelope. " +
			"Otherwise call your next tool and keep working."
		s.appendTurn(schema.TurnSteering, llm.User(steering))
		s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: steering})
		return true, nil
	}
	err := &bareTextWithoutResultToolError{
		toolName: s.resultToolName(),
		retries:  maxBareTextRetries,
	}
	s.emit(events.EventError, errorDataFromError(err))
	s.mu.Lock()
	s.setStateIfOpenLocked(SessionIdle)
	s.mu.Unlock()
	return false, err
}

// execToolBatch executes the round's tool calls and returns their results. When the
// profile supports parallel tool calls and there is more than one, it batches
// consecutive read-only calls to run concurrently (ordered-group algorithm) and
// serializes everything else; otherwise it runs them in order. On cancellation it
// records canceled results for the outstanding calls — keeping history well-formed —
// and returns the abort error alongside the partial results.
func (s *Session) execToolBatch(ctx context.Context, calls []llm.ToolCallData, profile *provider.Profile) ([]tool.ExecResult, error) {
	results := make([]tool.ExecResult, len(calls))
	if profile.SupportsParallelToolCalls() && len(calls) > 1 {
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
					return results, err
				}
				readBatch = readBatch[:0]
				if abortErr := s.abortResponseProcessing(ctx); abortErr != nil {
					if ctx.Err() != nil && !s.isClosingOrClosed() {
						s.appendCanceledToolResults(calls, results, abortErr)
					}
					return results, abortErr
				}
				results[i] = s.execTool(ctx, call)
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
			results[i] = s.execTool(ctx, calls[i])
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

// persistToolResults aggregates the round's tool results into a single tool-result
// turn and appends it to history. For any result carrying image/document data it
// makes a side-channel vision call (GPT models in tool-calling mode never describe
// images themselves — they immediately write code) and injects the description as
// steering so the agent can use it. It returns the abort error if the turn is
// canceled mid-persist.
func (s *Session) persistToolResults(ctx context.Context, calls []llm.ToolCallData, results []tool.ExecResult) error {
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
		return abortErr
	}

	for i, r := range results {
		if len(r.ImageData) > 0 {
			if desc := s.describeImage(ctx, r); desc != "" {
				// Include the file path so the agent can correlate descriptions to
				// specific files when multiple images/documents are read in one round.
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
					return abortErr
				}
			}
		}
	}
	return nil
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
	// AfterAction takes []Turn (not *[]Turn) so it cannot mutate the slice. Pass
	// s.history directly — no copy needed since the loop is single-threaded and
	// nothing else modifies history until AfterAction returns.
	s.mu.Lock()
	hist := s.history
	s.mu.Unlock()
	if err := s.strategy.AfterAction(ctx, hist, s.client); err != nil {
		if abortErr := s.withResponseSideEffects(ctx, func() {
			s.emit(events.EventWarning, events.WarningData{Message: "strategy AfterAction error: " + err.Error()})
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
// to toolSigs and runs loop detection, tracks the read-only streak (nudging at 5 and
// 10 consecutive read-only rounds), drains queued steering messages, and injects any
// task reminder. It returns the abort error if the turn is canceled mid-injection.
func (s *Session) injectPostToolSteering(ctx context.Context, calls []llm.ToolCallData, toolSigs *[]string) error {
	// Loop detection: track per-call signatures and check for repeating patterns.
	for _, call := range calls {
		*toolSigs = append(*toolSigs, call.Name+":"+shortHash(call.Arguments))
	}
	if s.cfg.EnableLoopDetection != nil && *s.cfg.EnableLoopDetection {
		if detectLoop(*toolSigs, s.cfg.LoopDetectionWindow) {
			s.mu.Lock()
			s.loopDetectionCount++
			count := s.loopDetectionCount
			s.mu.Unlock()

			warning := s.stuckEscalation(count)
			if abortErr := s.withResponseSideEffects(ctx, func() {
				s.emit(events.EventLoopDetection, events.LoopDetectionData{Message: warning})
				s.appendTurn(schema.TurnSteering, llm.User(warning))
				s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: warning})
			}); abortErr != nil {
				return abortErr
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
	switch s.readOnlyStreak {
	case 5:
		nudge := "<SYSTEM-REMINDER>You have spent several turns reading without writing or running anything. Review your current task. If you have enough context to make progress, write code or run a command now. A first attempt you can test and fix is more valuable than more reading.</SYSTEM-REMINDER>"
		if abortErr := s.withResponseSideEffects(ctx, func() {
			s.appendTurn(schema.TurnSteering, llm.User(nudge))
			s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: nudge})
		}); abortErr != nil {
			return abortErr
		}
	case 10:
		nudge := "<SYSTEM-REMINDER>You have been reading for 10 turns without acting. Stop reading. Write the deliverable file now, even if incomplete. You can iterate after you have something to test.</SYSTEM-REMINDER>"
		if abortErr := s.withResponseSideEffects(ctx, func() {
			s.appendTurn(schema.TurnSteering, llm.User(nudge))
			s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: nudge})
		}); abortErr != nil {
			return abortErr
		}
	}

	// Inject any queued steering messages before the next model call.
	if abortErr := s.withResponseSideEffects(ctx, func() {
		for _, msg := range s.drainSteering() {
			s.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
			s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
		}
	}); abortErr != nil {
		return abortErr
	}

	// Task reminder injection.
	if abortErr := s.withResponseSideEffects(ctx, func() {
		if reminder := s.maybeInjectTaskReminder(); reminder != "" {
			s.appendTurn(schema.TurnSteering, llm.User(reminder))
			s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: reminder})
		}
	}); abortErr != nil {
		return abortErr
	}
	return nil
}

// deliverIfCommunicated checks whether the agent delivered a result this round via
// the communicate/result tool. If so — and unless a Stop hook blocks completion, in
// which case the turn keeps looping — it runs the Stop hooks, transitions the session
// to awaiting-input or idle, and returns done=true with the reply to hand back to the
// caller. It returns done=false when nothing was delivered or a Stop hook blocked
// completion, meaning the turn should continue to the next round.
func (s *Session) deliverIfCommunicated(ctx context.Context) (done bool, text string) {
	s.mu.Lock()
	delivered := s.comm.called
	awaitReply := s.comm.awaitReply
	text = s.comm.reply
	s.mu.Unlock()
	if !delivered {
		return false, ""
	}
	// Stop hooks
	if s.hookRunner != nil {
		hi := s.hookInput(plugin.HookStop)
		if awaitReply {
			hi.Reason = "communicate.await_reply"
		} else {
			hi.Reason = "communicate.complete"
		}
		stopResult := s.hookRunner.RunStop(ctx, hi)
		for _, msg := range stopResult.SystemMessages {
			s.Steer(msg)
		}
		// TODO(phase-B): additionalContext is model-context; route distinctly from
		// user-visible systemMessage once a context channel exists.
		for _, msg := range stopResult.AdditionalContext {
			s.Steer(msg)
		}
		if stopResult.Blocked {
			// Don't finish — keep looping.
			return false, ""
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
	return true, text
}
