// Package agenttest provides shared test scaffolding — scripted fake adapters,
// request/response builders, and a fake execution environment — for tests of
// the agent package and its subpackages.
//
// Everything here depends only on the public llm and execenv APIs and the
// standard library, never on package agent itself. That constraint is
// load-bearing: a package agent test file that imports a helper package which
// in turn imported agent would form a test import cycle. Keeping agenttest
// agent-free means black-box (agent_test), white-box (agent), and subpackage
// (e.g. mcpconfig, internal/mcp) test files can all use it.
package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"primeradiant.com/serf/llm"
)

// FakeAdapter is a scripted llm.ProviderAdapter. Each call to Complete runs the
// next function in Steps (recording every request for later assertions); once
// Steps is exhausted it returns a generic "done" assistant message.
type FakeAdapter struct {
	Provider                      string
	Steps                         []func(req llm.Request) llm.Response
	PlanResponsesContinuationFunc func(req llm.Request) (llm.ResponsesContinuationPlan, error)
	CanFallbackToChat             bool

	mu       sync.Mutex
	requests []llm.Request
	i        int
}

// Name reports the provider name the adapter answers to.
func (a *FakeAdapter) Name() string { return a.Provider }

// Complete runs the next scripted step and stamps the provider/model fields.
func (a *FakeAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	if a.i >= len(a.Steps) {
		return llm.Response{Provider: a.Provider, Model: req.Model, Message: llm.Assistant("done")}, nil
	}
	resp := a.Steps[a.i](req)
	a.i++
	// Fill required response fields best-effort.
	resp.Provider = a.Provider
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

// Stream is unsupported: FakeAdapter exercises the non-streaming path.
func (a *FakeAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

// PlanResponsesContinuation delegates to the configured planner hook.
func (a *FakeAdapter) PlanResponsesContinuation(req llm.Request) (llm.ResponsesContinuationPlan, error) {
	if a.PlanResponsesContinuationFunc == nil {
		return llm.ResponsesContinuationPlan{}, errors.New("fake adapter missing PlanResponsesContinuationFunc")
	}
	plan, err := a.PlanResponsesContinuationFunc(req)
	if err != nil {
		return plan, err
	}
	plan.CanFallbackToChat = a.CanFallbackToChat
	return plan, nil
}

// Requests returns a copy of every request the adapter has seen.
func (a *FakeAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

// ModelTrackingAdapter is a non-streaming adapter that records the model and
// full request of every Complete call (for fallback-chain assertions) and
// produces responses via Respond.
type ModelTrackingAdapter struct {
	Provider string
	Respond  func(req llm.Request) (llm.Response, error)

	mu       sync.Mutex
	models   []string
	requests []llm.Request
}

// Name reports the provider name.
func (a *ModelTrackingAdapter) Name() string { return a.Provider }

// Complete records the model/request and stamps the response provider/model.
func (a *ModelTrackingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	a.models = append(a.models, req.Model)
	a.requests = append(a.requests, req)
	a.mu.Unlock()
	resp, err := a.Respond(req)
	if err != nil {
		return resp, err
	}
	resp.Provider = a.Provider
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

// Stream is unsupported.
func (a *ModelTrackingAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

// Models returns a copy of the models seen, in call order.
func (a *ModelTrackingAdapter) Models() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.models...)
}

// Requests returns a copy of the requests seen, in call order.
func (a *ModelTrackingAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}

// EmptyResponse returns a response with no text and no tool calls, simulating a
// model that emits null content (e.g. gpt-5.3-codex's empty-final behavior).
func EmptyResponse() llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant}}
}

// CommunicateCall builds a tool call to the communicate tool carrying message.
func CommunicateCall(id, message string) llm.ToolCallData {
	return CommunicateCallArgs(id, map[string]any{"message": message})
}

// CommunicateCallArgs builds a communicate tool call from raw argument values,
// normalizing them the way the real communicate tool expects.
func CommunicateCallArgs(id string, args map[string]any) llm.ToolCallData {
	normalized := map[string]any{}

	var message string
	if v, ok := args["message"]; ok && v != nil {
		message = strings.TrimSpace(fmt.Sprint(v))
	}

	endTurn := true
	if v, ok := args["end_turn"].(bool); ok {
		endTurn = v
	}

	output := map[string]any{
		"message":   "",
		"data":      map[string]any{},
		"artifacts": []string{},
	}
	if rawOutput, ok := args["output"].(map[string]any); ok {
		for k, v := range rawOutput {
			output[k] = v
		}
	}
	if message == "" {
		if outMsg, ok := output["message"].(string); ok {
			message = outMsg
		}
	}

	normalized["message"] = message
	normalized["end_turn"] = endTurn
	normalized["output"] = output

	raw, _ := json.Marshal(normalized)
	return llm.ToolCallData{
		ID:        id,
		Name:      "communicate",
		Arguments: raw,
		Type:      "function",
	}
}

// ToolCallResponse builds an assistant llm.Response carrying the given tool calls.
func ToolCallResponse(calls ...llm.ToolCallData) llm.Response {
	parts := make([]llm.ContentPart, len(calls))
	for i, c := range calls {
		parts[i] = llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &c}
	}
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: parts},
	}
}

// CommunicateResponse builds an assistant response that calls the communicate
// tool with the given message and end_turn flag.
func CommunicateResponse(endTurn bool, message string) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":  message,
		"end_turn": endTurn,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	return llm.Response{
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        "communicate_test_call",
						Name:      "communicate",
						Arguments: args,
						Type:      "function",
					},
				},
			},
		},
	}
}

// FinalResponse is a communicate response that does not await a reply.
func FinalResponse(message string) llm.Response { return CommunicateResponse(true, message) }
