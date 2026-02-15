package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

type Tool struct {
	Definition ToolDefinition
	Execute    func(ctx context.Context, args any) (any, error)
	schema     *jsonschema.Schema
}

// ToolCallContext provides additional context to tool handlers via context.Value.
type ToolCallContext struct {
	Messages   []Message
	ToolCallID string
}

type toolCallContextKey struct{}

// ToolCallContextFromCtx extracts the ToolCallContext from a context, if present.
func ToolCallContextFromCtx(ctx context.Context) (ToolCallContext, bool) {
	v, ok := ctx.Value(toolCallContextKey{}).(ToolCallContext)
	return v, ok
}

type GenerateOptions struct {
	Client *Client

	Model    string
	Provider string

	Prompt   *string
	Messages []Message
	System   *string

	Tools      []Tool
	ToolChoice *ToolChoice

	// MaxToolRounds is the maximum number of tool-execution rounds.
	// Nil means use the spec default (1).
	// A value of 0 disables automatic tool execution (passive tool mode).
	MaxToolRounds *int

	ResponseFormat  *ResponseFormat
	Temperature     *float64
	TopP            *float64
	MaxTokens       *int
	StopSequences   []string
	ReasoningEffort *string
	Metadata        map[string]string
	ProviderOptions map[string]any

	// RepairToolCall is called when tool argument validation fails. If it returns
	// repaired args (nil error), they are re-validated and used. If nil, validation
	// errors are sent directly to the model as error results.
	RepairToolCall func(ctx context.Context, call ToolCallData, validationError error) (json.RawMessage, error)

	// Retry policy for each individual LLM call within generate().
	RetryPolicy *RetryPolicy
	Sleep       SleepFunc

	// StopWhen is an optional predicate called after each tool execution round.
	// If it returns true, the tool loop terminates early.
	StopWhen func(steps []StepResult) bool

	// WebSearch enables provider-native web search for the request.
	WebSearch bool

	// AdapterTimeout configures adapter-level HTTP timeouts.
	// If nil, adapters use their own defaults.
	AdapterTimeout *AdapterTimeout

	// Optional timeouts for the multi-step operation.
	TimeoutTotal   time.Duration
	TimeoutPerStep time.Duration
}

type StepResult struct {
	Text         string
	Reasoning    string
	ToolCalls    []ToolCallData
	ToolResults  []ToolResultData
	FinishReason FinishReason
	Usage        Usage
	Response     Response
	Warnings     []Warning
}

type GenerateResult struct {
	Text         string
	Reasoning    string
	ToolCalls    []ToolCallData
	ToolResults  []ToolResultData
	FinishReason FinishReason
	Usage        Usage
	TotalUsage   Usage
	Steps        []StepResult
	Response     Response
	Output       any
}

func newResult(step StepResult, totalUsage Usage, steps []StepResult, resp Response) *GenerateResult {
	return &GenerateResult{
		Text:         step.Text,
		Reasoning:    step.Reasoning,
		ToolCalls:    step.ToolCalls,
		ToolResults:  step.ToolResults,
		FinishReason: step.FinishReason,
		Usage:        step.Usage,
		TotalUsage:   totalUsage,
		Steps:        steps,
		Response:     resp,
	}
}

func Generate(ctx context.Context, opts GenerateOptions) (*GenerateResult, error) {
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

	ctx, cancelTotal := WithTimeout(ctx, opts.TimeoutTotal)
	defer cancelTotal()

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

	steps := []StepResult{}
	totalUsage := Usage{Raw: map[string]any{}}

	toolRoundsUsed := 0
	for {
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

		callCtx, cancelStep := WithTimeout(ctx, opts.TimeoutPerStep)
		resp, err := Retry(callCtx, policy, opts.Sleep, nil, func() (Response, error) {
			return client.Complete(callCtx, req)
		})
		cancelStep()
		if err != nil {
			return nil, WrapContextError(req.Provider, err)
		}

		calls := resp.ToolCalls()
		for i := range calls {
			_ = calls[i].Parse() // best-effort; invalid JSON handled later in validation
		}

		step := StepResult{
			Text:         resp.Text(),
			Reasoning:    resp.ReasoningText(),
			ToolCalls:    calls,
			ToolResults:  nil,
			FinishReason: resp.Finish,
			Usage:        resp.Usage,
			Response:     resp,
			Warnings:     append([]Warning{}, resp.Warnings...),
		}
		totalUsage = totalUsage.Add(resp.Usage)
		if len(calls) == 0 || (resp.Finish.Reason != FinishReasonToolCalls && resp.Finish.Reason != FinishReasonPauseTurn) || !hasActiveTool || maxToolRounds == 0 || toolRoundsUsed >= maxToolRounds {
			// No tool loop (natural completion, tool loops disabled, or budget exhausted).
			steps = append(steps, step)
			return newResult(step, totalUsage, steps, resp), nil
		}

		// Add assistant message (including tool calls/thinking) before tool results.
		history = append(history, resp.Message)

		// Passive tool call: return tool calls to the caller when we lack an execute handler.
		for _, call := range calls {
			if t, ok := toolIndex[call.Name]; ok && t.Execute == nil {
				steps = append(steps, step)
				return newResult(step, totalUsage, steps, resp), nil
			}
		}

		// Execute tools (in parallel) and continue.
		results := executeToolCalls(ctx, toolIndex, calls, history, opts.RepairToolCall)
		step.ToolResults = results
		steps = append(steps, step)

		// Append tool results to conversation and iterate.
		for _, r := range results {
			history = append(history, ToolResultNamed(r.ToolCallID, r.Name, r.Content, r.IsError))
		}
		toolRoundsUsed++

		// Check custom stop condition (spec 4.3).
		if opts.StopWhen != nil && opts.StopWhen(steps) {
			return newResult(step, totalUsage, steps, resp), nil
		}
	}
}

func prepareTools(tools []Tool) (map[string]Tool, []ToolDefinition, error) {
	idx := map[string]Tool{}
	defs := make([]ToolDefinition, 0, len(tools))
	for i := range tools {
		t := tools[i]
		name := t.Definition.Name
		if err := ValidateToolName(name); err != nil {
			return nil, nil, err
		}
		if t.Definition.Parameters == nil {
			t.Definition.Parameters = defaultToolParameters()
		}
		if err := validateToolParameters(t.Definition.Parameters); err != nil {
			return nil, nil, err
		}
		s, err := compileSchema(t.Definition.Parameters)
		if err != nil {
			return nil, nil, err
		}
		t.schema = s
		idx[name] = t
		defs = append(defs, t.Definition)
	}
	return idx, defs, nil
}

func compileSchema(params map[string]any) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if err := c.AddResource("schema.json", bytes.NewReader(b)); err != nil {
		return nil, err
	}
	return c.Compile("schema.json")
}

func executeToolCalls(ctx context.Context, toolIndex map[string]Tool, calls []ToolCallData, messages []Message, repairFn func(context.Context, ToolCallData, error) (json.RawMessage, error)) []ToolResultData {
	results := make([]ToolResultData, len(calls))
	var wg sync.WaitGroup
	wg.Add(len(calls))
	for i := range calls {
		i := i
		go func() {
			defer wg.Done()
			call := calls[i]
			r := ToolResultData{
				ToolCallID: call.ID,
				Name:       call.Name,
			}

			t, ok := toolIndex[call.Name]
			if !ok || t.Execute == nil {
				r.IsError = true
				r.Content = fmt.Sprintf("unknown tool: %s", call.Name)
				results[i] = r
				return
			}

			args, err := parseAndValidateArgs(t.schema, call.Arguments)
			if err != nil {
				if repairFn != nil {
					repaired, repairErr := repairFn(ctx, call, err)
					if repairErr == nil {
						args, err = parseAndValidateArgs(t.schema, repaired)
					}
				}
				if err != nil {
					r.IsError = true
					r.Content = fmt.Sprintf("invalid tool arguments: %v", err)
					results[i] = r
					return
				}
			}

			tcCtx := context.WithValue(ctx, toolCallContextKey{}, ToolCallContext{
				Messages:   messages,
				ToolCallID: call.ID,
			})
			out, err := t.Execute(tcCtx, args)
			if err != nil {
				r.IsError = true
				r.Content = err.Error()
				results[i] = r
				return
			}
			r.Content = out
			results[i] = r
		}()
	}
	wg.Wait()
	return results
}

func parseAndValidateArgs(schema *jsonschema.Schema, raw json.RawMessage) (any, error) {
	var v any = map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
	}
	if schema == nil {
		return v, nil
	}
	if err := schema.Validate(v); err != nil {
		return nil, err
	}
	return v, nil
}

func (opts GenerateOptions) validate() error {
	if strings.TrimSpace(opts.Model) == "" {
		return &ConfigurationError{Message: "model is required"}
	}
	return nil
}

// WithTimeout wraps ctx in a context with the given timeout. A zero or negative
// timeout returns the input context unchanged.
func WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
