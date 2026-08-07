package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// The decided intervention texts, repeated verbatim here so a reworded
// constant in the implementation fails the test rather than silently
// changing what the model reads.
const (
	wantFailureNudge    = "You just ran the same tool twice with the same arguments and got the same failure. Consider an alternate approach"
	wantRepetitionNudge = "You have now made this same call twice and received the identical result. Repeating it will not change the answer — use the result you already have, or change your approach."
)

func wantFailurePark(toolName string) string {
	return "serf did not execute this call: " + toolName + " with these exact arguments has now failed 3 times with the same error; it will not be executed again until you change the arguments or the approach."
}

// breakerFake is a registered tool whose executor is supplied by the test and
// which counts how many times it actually ran, so a parked dispatch is proven
// by the counter standing still rather than by inspecting the ledger.
type breakerFake struct {
	calls int
	fn    func(calls int) (any, error)
}

func registerBreakerFake(t *testing.T, r *Registry, name string, fn func(calls int) (any, error)) *breakerFake {
	t.Helper()
	fake := &breakerFake{fn: fn}
	err := r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        name,
			Description: "breaker test fake",
			Parameters:  map[string]any{"type": "object"},
		}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			fake.calls++
			return fake.fn(fake.calls)
		},
	})
	if err != nil {
		t.Fatalf("Register(%s): %v", name, err)
	}
	return fake
}

func breakerCall(id, name, args string) llm.ToolCallData {
	return llm.ToolCallData{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

func breakerEnv(t *testing.T) execenv.ExecutionEnvironment {
	t.Helper()
	return execenv.NewLocalExecutionEnvironment(t.TempDir())
}

func TestBreakerDispatch_IdenticalFailureNudgesThenParks(t *testing.T) {
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "flaky", func(int) (any, error) {
		return nil, errors.New("boom: connection refused")
	})
	env := breakerEnv(t)
	ctx := context.Background()
	call := breakerCall("c1", "flaky", `{"target":"a"}`)

	first := r.ExecuteCall(ctx, env, call)
	if !first.IsError {
		t.Fatalf("first call should be an error result: %#v", first)
	}
	if strings.Contains(first.Output, wantFailureNudge) {
		t.Errorf("first call must not be nudged: %q", first.Output)
	}

	second := r.ExecuteCall(ctx, env, call)
	if !strings.HasSuffix(second.Output, wantFailureNudge) {
		t.Errorf("second call output must end with the failure nudge, got %q", second.Output)
	}
	if !strings.HasSuffix(second.FullOutput, wantFailureNudge) {
		t.Errorf("second call FullOutput must end with the failure nudge, got %q", second.FullOutput)
	}
	if fake.calls != 2 {
		t.Fatalf("executor invocations after two calls = %d, want 2", fake.calls)
	}

	third := r.ExecuteCall(ctx, env, call)
	if fake.calls != 2 {
		t.Errorf("third call executed the tool: invocations = %d, want 2", fake.calls)
	}
	if !third.IsError {
		t.Errorf("parked result must be an error result: %#v", third)
	}
	if third.PrevalOnly {
		t.Errorf("parked result must not claim pre-validation refused it: %#v", third)
	}
	if !strings.HasPrefix(third.Output, wantFailurePark("flaky")) {
		t.Errorf("parked output missing the decided failure park sentence, got %q", third.Output)
	}
	if !strings.Contains(third.Output, "The failures so far:\n1. boom: connection refused\n2. boom: connection refused") {
		t.Errorf("parked output missing both failure snippets, got %q", third.Output)
	}

	fourth := r.ExecuteCall(ctx, env, call)
	if fake.calls != 2 {
		t.Errorf("fourth identical call executed the tool: invocations = %d, want 2", fake.calls)
	}
	if !strings.HasPrefix(fourth.Output, wantFailurePark("flaky")) {
		t.Errorf("fourth call should stay parked, got %q", fourth.Output)
	}

	fifth := r.ExecuteCall(ctx, env, breakerCall("c5", "flaky", `{"target":"b"}`))
	if fake.calls != 3 {
		t.Errorf("different arguments must dispatch: invocations = %d, want 3", fake.calls)
	}
	if strings.Contains(fifth.Output, "serf did not execute this call:") {
		t.Errorf("different arguments must not be parked, got %q", fifth.Output)
	}
}

func TestBreakerDispatch_IdenticalSuccessBodyNudgesEveryRepeat(t *testing.T) {
	// The set_viewport shape: a failing call the tool reports as a success,
	// with a byte-identical body every time. Repetition nudges but never
	// parks, so every call still reaches the tool.
	const body = "Error: set_viewport requires payload with width and height"
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "viewport", func(int) (any, error) { return body, nil })
	env := breakerEnv(t)
	ctx := context.Background()
	call := breakerCall("v1", "viewport", `{"width":800}`)

	first := r.ExecuteCall(ctx, env, call)
	if first.IsError {
		t.Fatalf("fake reports success: %#v", first)
	}
	if first.Output != body {
		t.Errorf("first call output = %q, want %q", first.Output, body)
	}

	// The nudge repeats from the second identical result onward: with parking
	// off the table, nothing else applies pressure to break the loop.
	for i := range 2 {
		res := r.ExecuteCall(ctx, env, call)
		if fake.calls != i+2 {
			t.Fatalf("call %d must dispatch: invocations = %d, want %d", i+2, fake.calls, i+2)
		}
		if res.IsError {
			t.Errorf("call %d must not be an error result: %#v", i+2, res)
		}
		if !strings.HasSuffix(res.Output, wantRepetitionNudge) {
			t.Errorf("call %d output must end with the repetition nudge, got %q", i+2, res.Output)
		}
		if !strings.HasSuffix(res.FullOutput, wantRepetitionNudge) {
			t.Errorf("call %d FullOutput must end with the repetition nudge, got %q", i+2, res.FullOutput)
		}
	}
}

// An identical-body success loop is the shape of a session repeating
// communicate, or of read_file on a path whose contents change underneath.
// Parking it would strand the session or refuse the read that would finally
// differ, so the breaker only ever nudges here.
func TestBreakerDispatch_IdenticalSuccessLoopIsNeverParked(t *testing.T) {
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "communicate", func(int) (any, error) {
		return `{"accepted":true,"end_turn":true}`, nil
	})
	env := breakerEnv(t)
	ctx := context.Background()
	call := breakerCall("m1", "communicate", `{"message":"done","end_turn":true}`)

	for i := range 6 {
		res := r.ExecuteCall(ctx, env, call)
		if fake.calls != i+1 {
			t.Fatalf("call %d was refused: invocations = %d", i+1, fake.calls)
		}
		if strings.Contains(res.Output, "serf did not execute this call:") {
			t.Fatalf("call %d parked: %q", i+1, res.Output)
		}
		if !strings.HasPrefix(res.Output, `{"accepted":true,"end_turn":true}`) {
			t.Fatalf("call %d lost its result body: %q", i+1, res.Output)
		}
	}
}

func TestBreakerDispatch_ParkedResultDoesNotUnparkTheNextCall(t *testing.T) {
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "stuck", func(int) (any, error) {
		return nil, errors.New("same failure every time")
	})
	env := breakerEnv(t)
	ctx := context.Background()
	call := breakerCall("s1", "stuck", `{}`)

	for range 2 {
		r.ExecuteCall(ctx, env, call)
	}
	if fake.calls != 2 {
		t.Fatalf("setup invocations = %d, want 2", fake.calls)
	}
	// The park's own output must never be recorded: recording it would
	// replace the stored body hash and release the very next identical call.
	for i := range 3 {
		res := r.ExecuteCall(ctx, env, call)
		if fake.calls != 2 {
			t.Fatalf("call %d after the park executed the tool: invocations = %d, want 2", i+3, fake.calls)
		}
		if !strings.HasPrefix(res.Output, wantFailurePark("stuck")) {
			t.Fatalf("call %d after the park was released: %q", i+3, res.Output)
		}
	}
}

func TestBreakerDispatch_ChangingBodyIsNeverNudgedOrParked(t *testing.T) {
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "ticker", func(calls int) (any, error) {
		return fmt.Sprintf("running for %d ms", calls), nil
	})
	env := breakerEnv(t)
	ctx := context.Background()
	call := breakerCall("t1", "ticker", `{"job":"j1"}`)

	for i := range 10 {
		res := r.ExecuteCall(ctx, env, call)
		if fake.calls != i+1 {
			t.Fatalf("call %d was not dispatched: invocations = %d", i+1, fake.calls)
		}
		if strings.Contains(res.Output, "serf did not execute this call:") ||
			strings.Contains(res.Output, wantRepetitionNudge) ||
			strings.Contains(res.Output, wantFailureNudge) {
			t.Fatalf("call %d with a changing body was judged: %q", i+1, res.Output)
		}
	}
}

func TestBreakerDispatch_DifferentFailuresThenSuccessDoesNotPark(t *testing.T) {
	// The two failures carry different error classes, so neither streak
	// reaches the park threshold and the third call still reaches the tool.
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "recovering", func(calls int) (any, error) {
		switch calls {
		case 1:
			return nil, errors.New("alpha: no such host")
		case 2:
			return nil, errors.New("beta: permission denied")
		default:
			return "finally worked", nil
		}
	})
	env := breakerEnv(t)
	ctx := context.Background()
	call := breakerCall("r1", "recovering", `{}`)

	for i := range 3 {
		res := r.ExecuteCall(ctx, env, call)
		if fake.calls != i+1 {
			t.Fatalf("call %d was not dispatched: invocations = %d", i+1, fake.calls)
		}
		if strings.Contains(res.Output, "serf did not execute this call:") {
			t.Fatalf("call %d parked: %q", i+1, res.Output)
		}
		if strings.Contains(res.Output, wantFailureNudge) || strings.Contains(res.Output, wantRepetitionNudge) {
			t.Fatalf("call %d nudged: %q", i+1, res.Output)
		}
	}
	if fake.calls != 3 {
		t.Fatalf("invocations = %d, want 3", fake.calls)
	}
}

func TestBreakerDispatch_CloneStartsWithAFreshLedger(t *testing.T) {
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "flaky", func(int) (any, error) {
		return nil, errors.New("boom: connection refused")
	})
	env := breakerEnv(t)
	ctx := context.Background()
	call := breakerCall("c1", "flaky", `{}`)

	for range 3 {
		r.ExecuteCall(ctx, env, call)
	}
	if fake.calls != 2 {
		t.Fatalf("prototype invocations = %d, want 2 (third parked)", fake.calls)
	}

	clone := r.Clone()
	res := clone.ExecuteCall(ctx, env, call)
	if fake.calls != 3 {
		t.Errorf("a clone is a new dispatch scope and must execute: invocations = %d, want 3", fake.calls)
	}
	if strings.Contains(res.Output, "serf did not execute this call:") {
		t.Errorf("clone inherited a tripped signature: %q", res.Output)
	}
	if clone.breaker == r.breaker {
		t.Errorf("clone shares the prototype's ledger")
	}
}

func TestBreakerDispatch_BypassExecutesAParkedCall(t *testing.T) {
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "flaky", func(int) (any, error) {
		return nil, errors.New("boom: connection refused")
	})
	env := breakerEnv(t)
	ctx := context.Background()
	call := breakerCall("c1", "flaky", `{}`)

	for range 3 {
		r.ExecuteCall(ctx, env, call)
	}
	if fake.calls != 2 {
		t.Fatalf("setup invocations = %d, want 2 (third parked)", fake.calls)
	}

	res := r.ExecuteCall(WithBreakerBypass(ctx), env, call)
	if fake.calls != 3 {
		t.Errorf("a bypassed call must execute: invocations = %d, want 3", fake.calls)
	}
	if strings.Contains(res.Output, "serf did not execute this call:") {
		t.Errorf("bypassed call was parked: %q", res.Output)
	}
}
