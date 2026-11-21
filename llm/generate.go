package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/llm/registry"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Tool is a callable tool made available to the model. It pairs a ToolDefinition
// with an optional Execute handler; tools without an Execute handler are passive
// (their calls are returned to the caller rather than run by the tool loop).
type Tool struct {
	Definition ToolDefinition
	Execute    func(ctx context.Context, args any) (any, error)
	// ReadOnly marks tools that never modify filesystem, network, or external state
	// (e.g. read_file, grep, glob, list_dir). Used by executeToolCalls to batch
	// consecutive read-only calls in parallel while serializing everything else.
	ReadOnly bool
	schema   *jsonschema.Schema
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

// GenerateOptions configures a Generate call: the client, model/provider,
// prompt or messages, tools and tool-loop behavior, sampling and request
// parameters, retry policy, and operation timeouts.
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

	ResponseFormat       *ResponseFormat
	Temperature          *float64
	TopP                 *float64
	MaxTokens            *int
	StopSequences        []string
	ReasoningEffort      *string
	Metadata             map[string]string
	ClientMetadata       map[string]string
	Include              []string
	PromptCacheKey       string
	PreviousResponseID   string
	ConversationID       string
	ServiceTier          string
	SafetyIdentifier     string
	PromptCacheRetention string
	Truncation           string
	MaxToolCalls         *int
	Background           *bool
	Store                *bool
	SessionID            string
	ThreadID             string
	ProviderOptions      map[string]any

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

// StepResult holds the outcome of a single generation step: the model's text,
// reasoning, tool calls and their results, finish reason, token usage, the raw
// Response, and any warnings.
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

// GenerateResult is the result of a Generate call. Its Text, Reasoning,
// ToolCalls, ToolResults, FinishReason, Usage, and Response mirror the final
// step, while TotalUsage aggregates usage across all steps and Steps holds the
// per-step results.
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
	// Output holds the parsed, schema-validated object and is populated only by
	// GenerateObject; a plain Generate call leaves it nil.
	Output any
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

// generationState holds the resolved/validated state shared by Generate and StreamGenerate.
type generationState struct {
	client        *Client
	history       []Message
	toolIndex     map[string]Tool
	toolDefs      []ToolDefinition
	hasActiveTool bool
	maxToolRounds int
	policy        RetryPolicy
	// toolDefsJSONBytes is toolDefs' serialized length, measured once when the
	// list is built. It is stamped on every request the generation builds, so
	// the per-dispatch token estimate prices the catalog without re-serializing
	// it (#6437). The list is never reassigned after prepareGeneration, so the
	// length describes exactly the slice every request carries.
	//
	// Like every stamp, it describes the bytes at stamping time, so the list and
	// every element's Parameters must be treated as immutable for the
	// generation's life (see Request.ToolsJSONBytes). prepareTools already
	// copies the slice; the parameter maps are the caller's, and a caller that
	// rewrites one mid-generation would drift the estimate. Build a new
	// GenerateOptions instead.
	toolDefsJSONBytes int
}

func prepareGeneration(opts GenerateOptions) (*generationState, error) {
	client := opts.Client
	if client == nil {
		// A caller that names no client gets one with no registry of its
		// own: it resolves against EmbeddedRegistry (spec §8.1).
		client = NewClient()
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}

	maxToolRounds := 1
	if opts.MaxToolRounds != nil {
		maxToolRounds = *opts.MaxToolRounds
	}
	if maxToolRounds < 0 {
		maxToolRounds = 0
	}

	if opts.Prompt != nil && len(opts.Messages) > 0 {
		return nil, &ConfigurationError{Message: "provide either prompt or messages, not both"}
	}
	var history []Message
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
	// Measured once here beside tool preparation as the declared catalog length;
	// dispatch applies any adapter-specific parameterless-schema delta once on a
	// shallow copy without touching credentials during preparation.
	toolDefsJSONBytes := ToolsJSONBytes(toolDefs)
	hasActiveTool := false
	for _, t := range opts.Tools {
		if t.Execute != nil {
			hasActiveTool = true
			break
		}
	}

	if maxToolRounds > 0 && hasActiveTool {
		if err := preflightGoogleAutoToolLoop(client, opts, toolDefs); err != nil {
			return nil, err
		}
	}

	policy := DefaultRetryPolicy()
	if opts.RetryPolicy != nil {
		policy = *opts.RetryPolicy
	}

	return &generationState{
		client:            client,
		history:           history,
		toolIndex:         toolIndex,
		toolDefs:          toolDefs,
		toolDefsJSONBytes: toolDefsJSONBytes,
		hasActiveTool:     hasActiveTool,
		maxToolRounds:     maxToolRounds,
		policy:            policy,
	}, nil
}

func preflightGoogleAutoToolLoop(client *Client, opts GenerateOptions, toolDefs []ToolDefinition) error {
	providerName := normalizeProviderName(opts.Provider)
	if providerName == "" {
		providerName = client.DefaultProvider()
	}
	if providerName == "" {
		return nil
	}
	res, err := client.Registry().ResolveInstanceModelFacts(providerName, opts.Model)
	if err != nil {
		return nil //nolint:nilerr // preflight only inspects resolvable Google rows; resolution errors surface at dispatch
	}
	if res.Protocol != registry.ProtocolGoogle {
		return nil
	}
	anchor := strings.TrimSpace(opts.PreviousResponseID)
	if gOpts, ok := opts.ProviderOptions[registry.ProtocolGoogle].(map[string]any); ok {
		if rawAnchor, exists := gOpts["previous_interaction_id"]; exists && rawAnchor != nil {
			s, ok := rawAnchor.(string)
			if !ok {
				return &ConfigurationError{ProviderInstance: res.Instance, Message: "google provider_options.previous_interaction_id must be a string"}
			}
			if anchor == "" {
				anchor = strings.TrimSpace(s)
			}
		}
	}
	if anchor != "" {
		return &ConfigurationError{ProviderInstance: res.Instance, Message: "google interactions with previous_interaction_id cannot use automatic Generate/StreamGenerate tool loops; use Client.Complete/Client.Stream or set MaxToolRounds=0"}
	}
	var rawReplacement any
	hasReplacement := false
	if pinned, ok := res.Transport.Body["tools"]; ok {
		rawReplacement = pinned
		hasReplacement = true
	} else if gOpts, ok := opts.ProviderOptions[registry.ProtocolGoogle].(map[string]any); ok {
		if optTools, ok := gOpts["tools"]; ok {
			rawReplacement = optTools
			hasReplacement = true
		}
	}
	if hasReplacement {
		if hasFunc, hasSearch := GoogleEffectiveToolsKinds(rawReplacement); hasFunc && hasSearch {
			return &ConfigurationError{ProviderInstance: res.Instance, Message: "google interactions combining web_search with active function tools cannot use automatic Generate/StreamGenerate tool loops; use Client.Complete/Client.Stream or set MaxToolRounds=0"}
		}
		return nil
	}
	toolsSupported := res.Caps.Tools == nil || *res.Caps.Tools
	webSearch := opts.WebSearch && (res.Caps.WebSearch == nil || *res.Caps.WebSearch) && !registry.IsVertexGeminiOmni(res.Transport.Preset, res.WireID)
	if webSearch && len(toolDefs) > 0 && toolsSupported && registry.BoolValue(res.Caps.CombinedSearchAndFunctions) {
		return &ConfigurationError{ProviderInstance: res.Instance, Message: "google interactions combining web_search with active function tools cannot use automatic Generate/StreamGenerate tool loops; use Client.Complete/Client.Stream or set MaxToolRounds=0"}
	}
	return nil
}

// request builds the model request for one step from opts and the conversation so far.
func (gs *generationState) request(opts GenerateOptions, history []Message) Request {
	// Each definition carries its own declaration, so the adapters can recover
	// "no arguments" from the same list the budget prices and the override
	// adapter marshals. Nothing request-scoped has to be marked.
	return Request{
		Model:                opts.Model,
		Provider:             opts.Provider,
		Messages:             append([]Message{}, history...),
		Tools:                gs.toolDefs,
		ToolsJSONBytes:       gs.toolDefsJSONBytes,
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
}

// endsToolLoop reports whether a step's response is the last one: it carries no
// executable tool calls, tool looping is disabled or its round budget is spent,
// or a tool call has no Execute handler and so goes back to the caller.
func (gs *generationState) endsToolLoop(resp *Response, calls []ToolCallData, toolRoundsUsed int) bool {
	if len(calls) == 0 || (resp.Finish.Reason != FinishReasonToolCalls && resp.Finish.Reason != FinishReasonPauseTurn) || !gs.hasActiveTool || gs.maxToolRounds == 0 || toolRoundsUsed >= gs.maxToolRounds {
		return true
	}
	for _, call := range calls {
		if t, ok := gs.toolIndex[call.Name]; ok && t.Execute == nil {
			return true
		}
	}
	return false
}

// newStepResult summarizes one model response and the tool results it produced.
func newStepResult(resp *Response, calls []ToolCallData, results []ToolResultData) StepResult {
	return StepResult{
		Text:         resp.Text(),
		Reasoning:    resp.ReasoningText(),
		ToolCalls:    calls,
		ToolResults:  results,
		FinishReason: resp.Finish,
		Usage:        resp.Usage,
		Response:     *resp,
		Warnings:     append([]Warning{}, resp.Warnings...),
	}
}

// Generate runs a generation request described by opts. It calls the model
// (with retries per the configured policy), and when the model requests tool
// calls it executes any tools that have an Execute handler and feeds the
// results back, looping up to the configured tool-round budget or until a
// StopWhen predicate halts it. Passive tool calls (no Execute handler) cause it
// to return the calls to the caller. It returns a GenerateResult aggregating the
// steps and total usage.
func Generate(ctx context.Context, opts GenerateOptions) (*GenerateResult, error) {
	gs, err := prepareGeneration(opts)
	if err != nil {
		return nil, err
	}

	ctx, cancelTotal := WithTimeout(ctx, opts.TimeoutTotal)
	defer cancelTotal()

	history := gs.history
	steps := []StepResult{}
	totalUsage := Usage{Raw: map[string]any{}}

	toolRoundsUsed := 0
	for {
		req := gs.request(opts, history)

		callCtx, cancelStep := WithTimeout(ctx, opts.TimeoutPerStep)
		callCtx, attemptGroupScope := BeginAPIAttemptGroupScope(callCtx)
		resp, err := Retry(callCtx, gs.policy, opts.Sleep, nil, func() (Response, error) {
			return gs.client.Complete(callCtx, req)
		})
		attemptGroupScope.SettleResult(err)
		cancelStep()
		if err != nil {
			return nil, WrapContextError(req.Provider, err)
		}

		calls := resp.ToolCalls()
		for i := range calls {
			_ = calls[i].Parse() // best-effort; invalid JSON handled later in validation
		}

		step := newStepResult(&resp, calls, nil)
		totalUsage = totalUsage.Add(resp.Usage)
		if gs.endsToolLoop(&resp, calls, toolRoundsUsed) {
			// Natural completion, tool loops disabled, budget exhausted, or a passive tool call for the caller.
			steps = append(steps, step)
			return newResult(step, totalUsage, steps, resp), nil
		}

		// Add assistant message (including tool calls/thinking) before tool results.
		history = append(history, resp.Message)

		// Execute tools (in parallel) and continue.
		results := executeToolCalls(ctx, gs.toolIndex, calls, history, opts.RepairToolCall)
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
		// A definition that declares no parameters carries the open empty schema
		// in this catalog, because that is the schema every adapter puts on the
		// wire for it and the budget has to price what goes on the wire. What the
		// declaration was is kept separately: "no arguments" and "the open empty
		// object" are different claims, and only the first is strict-expressible.
		//
		// Keeping the schema here rather than restoring it at dispatch is what
		// stops the stamp from describing a list no adapter sends: the stamp is
		// measured from this catalog, so a nil here would have the budget admit a
		// request the wire is larger than.
		if t.Definition.Parameters == nil {
			t.Definition = t.Definition.WithNoParametersDeclared()
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
	compileSchemaHookMu.RLock()
	defer compileSchemaHookMu.RUnlock()
	return compileSchemaUnlocked(params)
}

var (
	compileSchemaHookMu      sync.RWMutex
	compileSchemaAddResource = func(c *jsonschema.Compiler, uri string, r io.Reader) error {
		return c.AddResource(uri, r)
	}
)

func compileSchemaUnlocked(params map[string]any) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	// Use an absolute URI so the library never calls filepath.Abs → os.Getwd().
	const schemaURI = "urn:evener:tool-schema"
	if err := compileSchemaAddResource(c, schemaURI, bytes.NewReader(b)); err != nil {
		return nil, err
	}
	return c.Compile(schemaURI)
}

// executeSingleToolCall runs one tool call and writes the result into results[idx].
func executeSingleToolCall(ctx context.Context, toolIndex map[string]Tool, call ToolCallData, messages []Message, repairFn func(context.Context, ToolCallData, error) (json.RawMessage, error), results []ToolResultData, idx int) {
	r := ToolResultData{
		ToolCallID: call.ID,
		Name:       call.Name,
	}

	t, ok := toolIndex[call.Name]
	if !ok || t.Execute == nil {
		r.IsError = true
		r.Content = "unknown tool: " + call.Name
		results[idx] = r
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
			results[idx] = r
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
		results[idx] = r
		return
	}
	r.Content = out
	results[idx] = r
}

// indexedToolCall pairs an original slice index with a tool call, used when
// building parallel read-only batches in executeToolCalls.
type indexedToolCall struct {
	idx  int
	call ToolCallData
}

func executeToolCalls(ctx context.Context, toolIndex map[string]Tool, calls []ToolCallData, messages []Message, repairFn func(context.Context, ToolCallData, error) (json.RawMessage, error)) []ToolResultData {
	results := make([]ToolResultData, len(calls))

	// Single-call fast path: no goroutine overhead.
	if len(calls) == 1 {
		executeSingleToolCall(ctx, toolIndex, calls[0], messages, repairFn, results, 0)
		return results
	}

	// Ordered-group algorithm: batch consecutive read-only calls for parallel
	// execution; serialize everything else.
	flushReadBatch := func(batch []indexedToolCall) {
		switch len(batch) {
		case 0:
			// nothing to do
		case 1:
			executeSingleToolCall(ctx, toolIndex, batch[0].call, messages, repairFn, results, batch[0].idx)
		default:
			var wg sync.WaitGroup
			wg.Add(len(batch))
			for _, item := range batch {
				go func() {
					defer wg.Done()
					executeSingleToolCall(ctx, toolIndex, item.call, messages, repairFn, results, item.idx)
				}()
			}
			wg.Wait()
		}
	}

	var readBatch []indexedToolCall
	for i, call := range calls {
		t, ok := toolIndex[call.Name]
		if ok && t.ReadOnly {
			readBatch = append(readBatch, indexedToolCall{i, call})
		} else {
			flushReadBatch(readBatch)
			readBatch = readBatch[:0]
			executeSingleToolCall(ctx, toolIndex, call, messages, repairFn, results, i)
		}
	}
	flushReadBatch(readBatch)

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
