package llm

import (
	"context"
	"fmt"
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

func (r *StreamResult) Events() <-chan StreamEvent { return r.stream.Events() }
func (r *StreamResult) Close() error               { return r.stream.Close() }

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
	client := opts.Client
	if client == nil {
		var err error
		client, err = DefaultClient()
		if err != nil {
			return nil, err
		}
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}

	// Spec default.
	maxToolRounds := 1
	if opts.MaxToolRounds != nil {
		maxToolRounds = *opts.MaxToolRounds
	}
	if maxToolRounds < 0 {
		maxToolRounds = 0
	}

	// Prompt standardization.
	if opts.Prompt != nil && len(opts.Messages) > 0 {
		return nil, &ConfigurationError{Message: "provide either prompt or messages, not both"}
	}
	var history []Message // includes system prefix (if any)
	if opts.System != nil && *opts.System != "" {
		history = append(history, System(*opts.System))
	}
	switch {
	case opts.Prompt != nil:
		history = append(history, User(*opts.Prompt))
	case len(opts.Messages) > 0:
		history = append(history, opts.Messages...)
	default:
		return nil, &ConfigurationError{Message: "prompt/messages required"}
	}

	toolIndex, toolDefs, err := prepareTools(opts.Tools)
	if err != nil {
		return nil, err
	}
	hasActiveTool := false
	for _, t := range opts.Tools {
		if t.Execute != nil {
			hasActiveTool = true
			break
		}
	}

	policy := DefaultRetryPolicy()
	if opts.RetryPolicy != nil {
		policy = *opts.RetryPolicy
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
				Model:           opts.Model,
				Provider:        opts.Provider,
				Messages:        append([]Message{}, history...),
				Tools:           toolDefs,
				ToolChoice:      opts.ToolChoice,
				ResponseFormat:  opts.ResponseFormat,
				Temperature:     opts.Temperature,
				TopP:            opts.TopP,
				MaxTokens:       opts.MaxTokens,
				StopSequences:   opts.StopSequences,
				ReasoningEffort: opts.ReasoningEffort,
				Metadata:        opts.Metadata,
				ProviderOptions: opts.ProviderOptions,
				WebSearch:       opts.WebSearch,
				AdapterTimeout:  opts.AdapterTimeout,
			}

			callCtx, cancelStep := WithTimeout(sctx, opts.TimeoutPerStep)
			st, err := Retry(callCtx, policy, opts.Sleep, nil, func() (Stream, error) {
				return client.Stream(callCtx, req)
			})
			if err != nil {
				cancelStep()
				err = WrapContextError(req.Provider, err)
				outStream.Send(StreamEvent{Type: StreamEventError, Err: err})
				res.mu.Lock()
				res.err = err
				res.mu.Unlock()
				return
			}

			acc := NewStreamAccumulator()
			var finishEv *StreamEvent

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
				default:
					outStream.Send(ev)
					if ev.Type == StreamEventError && ev.Err != nil {
						// Spec: do not retry after partial data delivered.
						_ = st.Close()
						cancelStep()
						res.mu.Lock()
						res.err = ev.Err
						res.mu.Unlock()
						return
					}
				}
			}
			_ = st.Close()
			cancelStep()

			if finishEv == nil {
				err := WrapContextError(req.Provider, callCtx.Err())
				if err == nil {
					err = NewStreamError(strings.TrimSpace(req.Provider), "stream ended without finish event")
				}
				outStream.Send(StreamEvent{Type: StreamEventError, Err: err})
				res.mu.Lock()
				res.err = err
				res.mu.Unlock()
				return
			}

			stepResp := finishEv.Response
			if stepResp == nil {
				stepResp = acc.Response()
			}
			if stepResp == nil {
				err := NewStreamError(strings.TrimSpace(req.Provider), "missing response in finish event")
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
			if len(calls) == 0 || (stepResp.Finish.Reason != FinishReasonToolCalls && stepResp.Finish.Reason != FinishReasonPauseTurn) || !hasActiveTool || maxToolRounds == 0 || toolRoundsUsed >= maxToolRounds {
				stopNow = true
			}
			// Passive tool call: if a tool is defined but has no execute handler, return to caller.
			if !stopNow {
				for _, call := range calls {
					if t, ok := toolIndex[call.Name]; ok && t.Execute == nil {
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

			results := executeToolCalls(sctx, toolIndex, calls, history, opts.RepairToolCall)
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
