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

	mu      sync.Mutex
	final   *Response
	partial *Response
	err     error

	done chan struct{}
}

func (r *StreamResult) Events() <-chan StreamEvent { return r.stream.Events() }
func (r *StreamResult) Close() error               { return r.stream.Close() }

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
	if opts.Prompt != nil {
		history = append(history, User(*opts.Prompt))
	} else if len(opts.Messages) > 0 {
		history = append(history, opts.Messages...)
	} else {
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

		for {
			if sctx.Err() != nil {
				err := wrapContextError(opts.Provider, sctx.Err())
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
			}

			callCtx, cancelStep := WithTimeout(sctx, opts.TimeoutPerStep)
			st, err := Retry(callCtx, policy, opts.Sleep, nil, func() (Stream, error) {
				return client.Stream(callCtx, req)
			})
			if err != nil {
				cancelStep()
				err = wrapContextError(req.Provider, err)
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
				err := wrapContextError(req.Provider, callCtx.Err())
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
			if len(calls) == 0 || !hasActiveTool || maxToolRounds == 0 || toolRoundsUsed >= maxToolRounds {
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
				res.mu.Unlock()
				return
			}

			// Continue the tool loop.
			history = append(history, stepResp.Message)

			results := executeToolCalls(sctx, toolIndex, calls)
			for _, r := range results {
				history = append(history, ToolResultNamed(r.ToolCallID, r.Name, r.Content, r.IsError))
			}
			toolRoundsUsed++

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
		}
	}()

	return res, nil
}
