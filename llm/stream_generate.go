package llm

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
)

// StreamResult is the high-level streaming generation result. It yields StreamEvent
// values over Events() and exposes the accumulated final response once the stream
// ends.
type StreamResult struct {
	stream *ChanStream

	mu         sync.Mutex
	final      *Response
	partial    *Response
	err        error
	totalUsage Usage
	steps      []StepResult

	done chan struct{}
}

// Events returns the channel of StreamEvent values from the underlying stream.
func (r *StreamResult) Events() <-chan StreamEvent { return r.stream.Events() }

// Close closes the underlying stream.
func (r *StreamResult) Close() error { return r.stream.Close() }

// TextStream returns a channel that yields only the text delta strings from the
// stream. The channel is closed when the underlying event stream ends.
func (r *StreamResult) TextStream() <-chan string {
	ch := make(chan string, 16)
	go func() {
		defer close(ch)
		for ev := range r.stream.Events() {
			if ev.Type == StreamEventTextDelta {
				ch <- ev.Delta
			}
		}
	}()
	return ch
}

// Response blocks until the stream is complete, then returns a copy of the final
// accumulated response and any terminal error. It returns an error if r is nil.
func (r *StreamResult) Response() (*Response, error) {
	if r == nil {
		return nil, fmt.Errorf("stream result is nil")
	}
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.final != nil {
		cp := *r.final
		return &cp, r.err
	}
	return nil, r.err
}

// PartialResponse returns a copy of the most recently accumulated partial response,
// or nil if r is nil or no partial response is available yet.
func (r *StreamResult) PartialResponse() *Response {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.partial == nil {
		return nil
	}
	cp := *r.partial
	return &cp
}

// TotalUsage returns the aggregated token usage across all steps. It is safe to
// call after Response() returns (i.e., after the stream is complete).
func (r *StreamResult) TotalUsage() Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalUsage
}

// Steps returns a copy of the per-step results accumulated during the stream.
// Each tool-execution round is one step, plus the final completion step.
func (r *StreamResult) Steps() []StepResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StepResult{}, r.steps...)
}

// StreamGenerate is the high-level streaming API (spec stream()). It is equivalent
// to Generate(), but yields StreamEvent values incrementally and continues across
// tool-execution steps. Between tool steps it emits a STEP_FINISH event.
func StreamGenerate(ctx context.Context, opts GenerateOptions) (*StreamResult, error) {
	gs, err := prepareGeneration(opts)
	if err != nil {
		return nil, err
	}

	ctxTotal, cancelTotal := WithTimeout(ctx, opts.TimeoutTotal)
	sctx, cancel := context.WithCancel(ctxTotal)
	cancelAll := func() {
		cancel()
		cancelTotal()
	}

	outStream := NewChanStream(cancelAll)
	res := &StreamResult{
		stream: outStream,
		done:   make(chan struct{}),
	}

	go func() {
		defer close(res.done)
		defer cancelTotal()
		defer outStream.CloseSend()

		history := gs.history
		stepIndex := 0
		toolRoundsUsed := 0
		var steps []StepResult
		totalUsage := Usage{Raw: map[string]any{}}

		for {
			if sctx.Err() != nil {
				err := WrapContextError(opts.Provider, sctx.Err())
				outStream.Send(StreamEvent{Type: StreamEventError, Err: err})
				res.mu.Lock()
				res.err = err
				res.mu.Unlock()
				return
			}

			req := Request{
				Model:                opts.Model,
				Provider:             opts.Provider,
				Messages:             append([]Message{}, history...),
				Tools:                gs.toolDefs,
				ToolChoice:           opts.ToolChoice,
				ResponseFormat:       opts.ResponseFormat,
				Temperature:          opts.Temperature,
				TopP:                 opts.TopP,
				MaxTokens:            opts.MaxTokens,
				StopSequences:        opts.StopSequences,
				ReasoningEffort:      opts.ReasoningEffort,
				Metadata:             opts.Metadata,
				ClientMetadata:       opts.ClientMetadata,
				Include:              opts.Include,
				PromptCacheKey:       opts.PromptCacheKey,
				PreviousResponseID:   opts.PreviousResponseID,
				ConversationID:       opts.ConversationID,
				ServiceTier:          opts.ServiceTier,
				SafetyIdentifier:     opts.SafetyIdentifier,
				PromptCacheRetention: opts.PromptCacheRetention,
				Truncation:           opts.Truncation,
				MaxToolCalls:         opts.MaxToolCalls,
				Background:           opts.Background,
				Store:                opts.Store,
				SessionID:            opts.SessionID,
				ThreadID:             opts.ThreadID,
				ProviderOptions:      opts.ProviderOptions,
				WebSearch:            opts.WebSearch,
				AdapterTimeout:       opts.AdapterTimeout,
			}

			callCtx, cancelStep := WithTimeout(sctx, opts.TimeoutPerStep)

			// openAndConsumeStream opens one stream attempt and drains it.
			// Returns (finishEv, acc, hasPartialOutput, streamErr).
			// hasPartialOutput is true once any text delta has been forwarded
			// to outStream; that flag gates whether the caller may retry.
			openAndConsumeStream := func() (finishEv *StreamEvent, acc *StreamAccumulator, hasPartialOutput bool, streamErr error) {
				var st Stream
				st, streamErr = gs.client.Stream(callCtx, req)
				if streamErr != nil {
					return
				}

				acc = NewStreamAccumulator()

				for ev := range st.Events() {
					acc.Process(ev)

					// Best-effort: expose partial response for consumers.
					if ev.Type == StreamEventTextDelta {
						if pr := acc.PartialResponse(); pr != nil {
							res.mu.Lock()
							res.partial = pr
							res.mu.Unlock()
						}
					}

					switch ev.Type {
					case StreamEventStreamStart:
						// Emit only once for the high-level stream.
						if stepIndex == 0 {
							outStream.Send(ev)
						}
					case StreamEventFinish:
						// Buffer finish until we know whether to continue tool looping.
						cp := ev
						finishEv = &cp
					case StreamEventError:
						if ev.Err != nil {
							// Do not forward attempt-local errors before the
							// retry loop commits this attempt. If no partial
							// output was delivered, a retry may still succeed;
							// if partial output was delivered, the final error
							// is emitted once below after retries are ruled out.
							_ = st.Close()
							streamErr = ev.Err
							return
						}
						outStream.Send(ev)
					default:
						outStream.Send(ev)
						if ev.Type == StreamEventTextDelta {
							hasPartialOutput = true
						}
					}
				}
				_ = st.Close()

				if finishEv == nil {
					streamErr = WrapContextError(req.Provider, callCtx.Err())
					if streamErr == nil {
						streamErr = NewStreamError(strings.TrimSpace(req.Provider), "stream ended without finish event", nil)
					}
				}
				return
			}

			// Retry the open+consume cycle using the policy's backoff.
			//
			// This mirrors the Codex run_with_retry pattern
			// (codex-rs/codex-client/src/retry.rs): retry 429 / 5xx / transport
			// errors from the initial connection AND stream-level truncations
			// (stream ended without finish event) — but only when no partial
			// output has been forwarded to the caller yet.
			//
			// Permanent provider errors (403 auth, 404 model-not-found, 400
			// bad-request) short-circuit the chain after a single attempt
			// instead of burning the full budget (kata xgzz).
			maxRetries := gs.policy.MaxRetries
			if maxRetries < 0 {
				maxRetries = 0
			}
			sleep := opts.Sleep
			if sleep == nil {
				sleep = DefaultSleep
			}

			var finishEv *StreamEvent
			var acc *StreamAccumulator
			var stepErr error
			for attempt := 0; attempt <= maxRetries; attempt++ {
				if callCtx.Err() != nil {
					stepErr = callCtx.Err()
					break
				}
				var hasPartial bool
				finishEv, acc, hasPartial, stepErr = openAndConsumeStream()
				if stepErr == nil {
					break
				}
				if hasPartial {
					// Partial data already delivered to the caller — surface
					// immediately, no retry (Spec: do not retry after partial
					// data delivered).
					break
				}
				// Permanent (and Fallback — handled inside the adapter) errors
				// don't get the rest of the retry budget. Only ErrorClassRetryable
				// proceeds; the budget cap is checked next.
				if Classify(stepErr) != ErrorClassRetryable || attempt == maxRetries {
					break
				}
				delay, ok := retryDelay(gs.policy, rand.Float64, stepErr, attempt)
				if !ok {
					break
				}
				if gs.policy.OnRetry != nil {
					gs.policy.OnRetry(stepErr, attempt+1, delay)
				}
				if sleepErr := sleep(callCtx, delay); sleepErr != nil {
					stepErr = sleepErr
					break
				}
			}

			cancelStep()
			if stepErr != nil {
				stepErr = WrapContextError(req.Provider, stepErr)
				outStream.Send(StreamEvent{Type: StreamEventError, Err: stepErr})
				res.mu.Lock()
				res.err = stepErr
				res.mu.Unlock()
				return
			}

			stepResp := finishEv.Response
			if stepResp == nil {
				stepResp = acc.Response()
			}
			if stepResp == nil {
				err := NewStreamError(strings.TrimSpace(req.Provider), "missing response in finish event", nil)
				outStream.Send(StreamEvent{Type: StreamEventError, Err: err})
				res.mu.Lock()
				res.err = err
				res.mu.Unlock()
				return
			}

			calls := stepResp.ToolCalls()

			// Stop if there are no tool calls, tool looping is disabled, we lack active tools,
			// or we've exhausted the tool-round budget.
			stopNow := false
			if len(calls) == 0 || (stepResp.Finish.Reason != FinishReasonToolCalls && stepResp.Finish.Reason != FinishReasonPauseTurn) || !gs.hasActiveTool || gs.maxToolRounds == 0 || toolRoundsUsed >= gs.maxToolRounds {
				stopNow = true
			}
			// Passive tool call: if a tool is defined but has no execute handler, return to caller.
			if !stopNow {
				for _, call := range calls {
					if t, ok := gs.toolIndex[call.Name]; ok && t.Execute == nil {
						stopNow = true
						break
					}
				}
			}

			if stopNow {
				// Build final step and aggregate usage.
				finalStep := StepResult{
					Text:         stepResp.Text(),
					Reasoning:    stepResp.ReasoningText(),
					ToolCalls:    calls,
					FinishReason: stepResp.Finish,
					Usage:        stepResp.Usage,
					Response:     *stepResp,
					Warnings:     append([]Warning{}, stepResp.Warnings...),
				}
				steps = append(steps, finalStep)
				totalUsage = totalUsage.Add(stepResp.Usage)

				// Final completion: forward FINISH and close.
				if finishEv.Response == nil {
					cp := *stepResp
					finishEv.Response = &cp
				}
				outStream.Send(*finishEv)
				res.mu.Lock()
				cp := *stepResp
				res.final = &cp
				res.partial = &cp
				res.totalUsage = totalUsage
				res.steps = steps
				res.mu.Unlock()
				return
			}

			// Continue the tool loop.
			history = append(history, stepResp.Message)

			results := executeToolCalls(sctx, gs.toolIndex, calls, history, opts.RepairToolCall)
			for _, r := range results {
				history = append(history, ToolResultNamed(r.ToolCallID, r.Name, r.Content, r.IsError))
			}
			toolRoundsUsed++

			// Build step result and aggregate usage.
			step := StepResult{
				Text:         stepResp.Text(),
				Reasoning:    stepResp.ReasoningText(),
				ToolCalls:    calls,
				ToolResults:  results,
				FinishReason: stepResp.Finish,
				Usage:        stepResp.Usage,
				Response:     *stepResp,
				Warnings:     append([]Warning{}, stepResp.Warnings...),
			}
			steps = append(steps, step)
			totalUsage = totalUsage.Add(stepResp.Usage)

			// Step boundary (spec): emit STEP_FINISH after tool execution, before next model call.
			stepCopy := *stepResp
			outStream.Send(StreamEvent{
				Type:         StreamEventStepFinish,
				FinishReason: finishEv.FinishReason,
				Usage:        finishEv.Usage,
				Response:     &stepCopy,
				Raw: map[string]any{
					"step_index":    stepIndex,
					"tool_results":  results,
					"tool_round":    toolRoundsUsed,
					"tool_call_cnt": len(calls),
				},
			})
			stepIndex++

			// Check custom stop condition (spec 4.3).
			if opts.StopWhen != nil && opts.StopWhen(steps) {
				outStream.Send(StreamEvent{
					Type:         StreamEventFinish,
					FinishReason: finishEv.FinishReason,
					Usage:        finishEv.Usage,
					Response:     &stepCopy,
				})
				res.mu.Lock()
				res.final = &stepCopy
				res.partial = &stepCopy
				res.totalUsage = totalUsage
				res.steps = steps
				res.mu.Unlock()
				return
			}
		}
	}()

	return res, nil
}
