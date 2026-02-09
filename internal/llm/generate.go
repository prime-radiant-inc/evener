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

	// Retry policy for each individual LLM call within generate().
	RetryPolicy *RetryPolicy
	Sleep       SleepFunc

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
		}

		callCtx, cancelStep := WithTimeout(ctx, opts.TimeoutPerStep)
		resp, err := Retry(callCtx, policy, opts.Sleep, nil, func() (Response, error) {
			return client.Complete(callCtx, req)
		})
		cancelStep()
		if err != nil {
			return nil, wrapContextError(req.Provider, err)
		}

		step := StepResult{
			Text:         resp.Text(),
			Reasoning:    resp.ReasoningText(),
			ToolCalls:    resp.ToolCalls(),
			ToolResults:  nil,
			FinishReason: resp.Finish,
			Usage:        resp.Usage,
			Response:     resp,
			Warnings:     append([]Warning{}, resp.Warnings...),
		}
		totalUsage = totalUsage.Add(resp.Usage)

		calls := resp.ToolCalls()
		if len(calls) == 0 || !hasActiveTool || maxToolRounds == 0 || toolRoundsUsed >= maxToolRounds {
			// No tool loop (natural completion, tool loops disabled, or budget exhausted).
			steps = append(steps, step)
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
			}, nil
		}

		// Add assistant message (including tool calls/thinking) before tool results.
		history = append(history, resp.Message)

		// Passive tool call: return tool calls to the caller when we lack an execute handler.
		for _, call := range calls {
			if t, ok := toolIndex[call.Name]; ok && t.Execute == nil {
				steps = append(steps, step)
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
				}, nil
			}
		}

		// Execute tools (in parallel) and continue.
		results := executeToolCalls(ctx, toolIndex, calls)
		step.ToolResults = results
		steps = append(steps, step)

		// Append tool results to conversation and iterate.
		for _, r := range results {
			history = append(history, ToolResultNamed(r.ToolCallID, r.Name, r.Content, r.IsError))
		}
		toolRoundsUsed++
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

func executeToolCalls(ctx context.Context, toolIndex map[string]Tool, calls []ToolCallData) []ToolResultData {
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
				r.IsError = true
				r.Content = fmt.Sprintf("invalid tool arguments: %v", err)
				results[i] = r
				return
			}

			out, err := t.Execute(ctx, args)
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
