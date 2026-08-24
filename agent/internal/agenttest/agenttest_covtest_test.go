package agenttest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestCovFakeAdapter covers FakeAdapter.Name, Complete, Stream,
// PlanResponsesContinuation, and Requests (agenttest.go lines 40-86).
func TestCovFakeAdapter(t *testing.T) {
	a := &FakeAdapter{Provider: "fake"}

	// Name.
	if got := a.Name(); got != "fake" {
		t.Fatalf("Name = %q", got)
	}

	// Complete with no steps → default "done" response.
	resp, err := a.Complete(context.Background(), llm.Request{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if resp.Provider != "fake" || resp.Model != "gpt-5" {
		t.Fatalf("resp = %+v", resp)
	}
	if msg := resp.Text(); !strings.Contains(msg, "done") {
		t.Fatalf("message = %q", msg)
	}

	// Complete with a step.
	called := false
	a.Steps = []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			called = true
			return llm.Response{Message: llm.Assistant("step response")}
		},
	}
	resp, err = a.Complete(context.Background(), llm.Request{Model: "gpt-5"})
	if err != nil || !called {
		t.Fatalf("step not called: resp=%+v err=%v called=%v", resp, err, called)
	}

	// Stream → ErrStreamUnsupported.
	_, err = a.Stream(context.Background(), llm.Request{})
	if !errors.Is(err, llm.ErrStreamUnsupported) {
		t.Fatalf("Stream error = %v, want ErrStreamUnsupported", err)
	}

	// Requests.
	if reqs := a.Requests(); len(reqs) != 2 {
		t.Fatalf("Requests = %d, want 2", len(reqs))
	}
}

// TestCovFakeAdapter_PlanResponsesContinuation covers
// PlanResponsesContinuation (agenttest.go lines 69-79).
func TestCovFakeAdapter_PlanResponsesContinuation(t *testing.T) {
	a := &FakeAdapter{Provider: "fake"}

	// Without a planner → error.
	_, err := a.PlanResponsesContinuation(llm.Request{})
	if err == nil {
		t.Fatal("expected error without PlanResponsesContinuationFunc")
	}

	// With a planner.
	a.PlanResponsesContinuationFunc = func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
		return llm.ResponsesContinuationPlan{EndpointFamily: llm.ResponsesEndpointFamilyOpenAIPublic}, nil
	}
	a.CanFallbackToChat = true
	plan, err := a.PlanResponsesContinuation(llm.Request{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if plan.EndpointFamily != llm.ResponsesEndpointFamilyOpenAIPublic || !plan.CanFallbackToChat {
		t.Fatalf("plan = %+v", plan)
	}

	// With a planner that errors.
	a.PlanResponsesContinuationFunc = func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
		return llm.ResponsesContinuationPlan{}, errors.New("plan failed")
	}
	_, err = a.PlanResponsesContinuation(llm.Request{})
	if err == nil {
		t.Fatal("expected plan error")
	}
}

// TestCovModelTrackingAdapter covers ModelTrackingAdapter.Name, Complete,
// Stream, Models, and Requests (agenttest.go lines 101-140).
func TestCovModelTrackingAdapter(t *testing.T) {
	a := &ModelTrackingAdapter{
		Provider: "tracking",
		Respond: func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("ok")}, nil
		},
	}

	// Name.
	if got := a.Name(); got != "tracking" {
		t.Fatalf("Name = %q", got)
	}

	// Complete.
	resp, err := a.Complete(context.Background(), llm.Request{Model: "gpt-5"})
	if err != nil || resp.Provider != "tracking" || resp.Model != "gpt-5" {
		t.Fatalf("Complete: resp=%+v err=%v", resp, err)
	}

	// Models.
	if models := a.Models(); len(models) != 1 || models[0] != "gpt-5" {
		t.Fatalf("Models = %v", models)
	}

	// Requests.
	if reqs := a.Requests(); len(reqs) != 1 {
		t.Fatalf("Requests = %d", len(reqs))
	}

	// Stream → ErrStreamUnsupported.
	_, err = a.Stream(context.Background(), llm.Request{})
	if !errors.Is(err, llm.ErrStreamUnsupported) {
		t.Fatalf("Stream error = %v", err)
	}

	// Complete with error from Respond.
	a.Respond = func(req llm.Request) (llm.Response, error) {
		return llm.Response{}, errors.New("boom")
	}
	_, err = a.Complete(context.Background(), llm.Request{Model: "gpt-5"})
	if err == nil {
		t.Fatal("expected error from Respond")
	}
}

// TestCovEmptyResponse covers EmptyResponse (agenttest.go lines 144-146).
func TestCovEmptyResponse(t *testing.T) {
	resp := EmptyResponse()
	if resp.Message.Role != llm.RoleAssistant {
		t.Fatalf("Role = %v", resp.Message.Role)
	}
	if text := resp.Text(); text != "" {
		t.Fatalf("expected empty text, got %q", text)
	}
}

// TestCovCommunicateCall covers CommunicateCall (agenttest.go lines 149-151).
func TestCovCommunicateCall(t *testing.T) {
	call := CommunicateCall("call_1", "hello world")
	if call.ID != "call_1" || call.Name != "communicate" {
		t.Fatalf("call = %+v", call)
	}
	if !strings.Contains(string(call.Arguments), "hello world") {
		t.Fatalf("arguments missing message: %s", call.Arguments)
	}
}

// TestCovCommunicateCallArgs covers CommunicateCallArgs (agenttest.go lines 155-193).
func TestCovCommunicateCallArgs(t *testing.T) {
	// With message and end_turn.
	call := CommunicateCallArgs("call_1", map[string]any{
		"message":  "done",
		"end_turn": false,
	})
	if call.ID != "call_1" || call.Name != "communicate" {
		t.Fatalf("call = %+v", call)
	}
	args := string(call.Arguments)
	if !strings.Contains(args, `"message":"done"`) {
		t.Fatalf("args missing message: %s", args)
	}
	if !strings.Contains(args, `"end_turn":false`) {
		t.Fatalf("args missing end_turn=false: %s", args)
	}

	// With output.message instead of message.
	call = CommunicateCallArgs("call_2", map[string]any{
		"output": map[string]any{"message": "from output"},
	})
	if !strings.Contains(string(call.Arguments), "from output") {
		t.Fatalf("args missing output message: %s", call.Arguments)
	}

	// With nil args.
	call = CommunicateCallArgs("call_3", nil)
	if !strings.Contains(string(call.Arguments), `"message":""`) {
		t.Fatalf("args should have empty message: %s", call.Arguments)
	}
	if !strings.Contains(string(call.Arguments), `"end_turn":true`) {
		t.Fatalf("args should default end_turn=true: %s", call.Arguments)
	}
}

// TestCovToolCallResponse covers ToolCallResponse (agenttest.go lines 196-204).
func TestCovToolCallResponse(t *testing.T) {
	calls := []llm.ToolCallData{
		{ID: "c1", Name: "exec_command"},
		{ID: "c2", Name: "read_file"},
	}
	resp := ToolCallResponse(calls...)
	if resp.Message.Role != llm.RoleAssistant {
		t.Fatalf("Role = %v", resp.Message.Role)
	}
	if len(resp.Message.Content) != 2 {
		t.Fatalf("Content parts = %d", len(resp.Message.Content))
	}
}

// TestCovCommunicateResponse covers CommunicateResponse (agenttest.go lines 208-234).
func TestCovCommunicateResponse(t *testing.T) {
	resp := CommunicateResponse(true, "final answer")
	if resp.Message.Role != llm.RoleAssistant {
		t.Fatalf("Role = %v", resp.Message.Role)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("Content = %d", len(resp.Message.Content))
	}
	tc := resp.Message.Content[0].ToolCall
	if tc == nil || tc.Name != "communicate" {
		t.Fatalf("ToolCall = %+v", tc)
	}
	args := string(tc.Arguments)
	if !strings.Contains(args, "final answer") || !strings.Contains(args, `"end_turn":true`) {
		t.Fatalf("args = %s", args)
	}
}

// TestCovFinalResponse covers FinalResponse (agenttest.go line 237).
func TestCovFinalResponse(t *testing.T) {
	resp := FinalResponse("all done")
	if len(resp.Message.Content) != 1 {
		t.Fatalf("Content = %d", len(resp.Message.Content))
	}
	args := string(resp.Message.Content[0].ToolCall.Arguments)
	if !strings.Contains(args, "all done") || !strings.Contains(args, `"end_turn":true`) {
		t.Fatalf("args = %s", args)
	}
}
