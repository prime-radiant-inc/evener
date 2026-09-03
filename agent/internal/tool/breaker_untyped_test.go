package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestSemanticFailureBreaker_UntypedExecutionErrorsDoNotParkAcrossRawVariants(t *testing.T) {
	r := NewRegistry()
	fake := registerSemanticBreakerFake(t, r, func(map[string]any, int) (any, error) {
		return nil, errors.New("generic executor failure")
	})
	env := breakerEnv(t)
	for i, raw := range []string{
		`{"target":"job:one","intent":"first"}`,
		`{"intent":"second","target":"job:one"}`,
		`{"target":"job:one","intent":"third"}`,
	} {
		res := r.ExecuteCall(context.Background(), env, breakerCall(fmt.Sprintf("untyped-%d", i), "semantic_fake", raw))
		if strings.HasPrefix(res.FullOutput, parkPrefix) {
			t.Fatalf("untyped semantic variant %d was parked: %#v", i+1, res)
		}
	}
	if fake.calls != 3 {
		t.Fatalf("untyped semantic variants executed %d times, want 3", fake.calls)
	}
}

func TestSemanticFailureBreaker_UntypedExactFailureStillParks(t *testing.T) {
	r := NewRegistry()
	fake := registerSemanticBreakerFake(t, r, func(map[string]any, int) (any, error) {
		return nil, errors.New("generic executor failure")
	})
	call := breakerCall("untyped-exact", "semantic_fake", `{"target":"job:one"}`)
	for range 2 {
		r.ExecuteCall(context.Background(), breakerEnv(t), call)
	}
	res := r.ExecuteCall(context.Background(), breakerEnv(t), call)
	if !strings.HasPrefix(res.FullOutput, parkPrefix) || fake.calls != 2 {
		t.Fatalf("exact generic failure did not park: calls=%d result=%#v", fake.calls, res)
	}
}

func TestSemanticFailureBreaker_TypedAndKnownBoundaryFailuresStillPark(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "typed", err: breakerFailureError{class: "resource_unavailable", text: "resource unavailable"}},
		{name: "known-boundary", err: errors.New("invalid_request: range is invalid for this target")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			fake := registerSemanticBreakerFake(t, r, func(map[string]any, int) (any, error) { return nil, tc.err })
			for i, raw := range []string{
				`{"target":"job:one","intent":"first"}`,
				`{"intent":"second","target":"job:one"}`,
				`{"target":"job:one","intent":"third"}`,
			} {
				res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("%s-%d", tc.name, i), "semantic_fake", raw))
				if i < 2 && strings.HasPrefix(res.FullOutput, parkPrefix) {
					t.Fatalf("semantic attempt %d parked early: %#v", i+1, res)
				}
				if i == 2 && !strings.HasPrefix(res.FullOutput, parkPrefix) {
					t.Fatalf("semantic attempt 3 did not park: %#v", res)
				}
			}
			if fake.calls != 2 {
				t.Fatalf("semantic failures executed %d times, want 2", fake.calls)
			}
		})
	}
}

func TestSemanticFailureBreaker_SmallExecutorLimitPreservesControlMessage(t *testing.T) {
	r := NewRegistry()
	calls := 0
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "small_semantic", Description: "test", Parameters: map[string]any{"type": "object"}},
		Limit:      schema.ToolOutputLimit{MaxChars: 80, Strategy: schema.TruncTail},
		NormalizeArgs: func(args map[string]any) (map[string]any, error) {
			delete(args, "neutral")
			return args, nil
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			calls++
			return nil, errors.New("invalid_request: small semantic limit")
		},
	}); err != nil {
		t.Fatal(err)
	}
	for i, raw := range []string{
		`{"target":"job:one","neutral":1}`,
		`{"neutral":2,"target":"job:one"}`,
		`{"target":"job:one","neutral":3}`,
	} {
		res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("small-%d", i), "small_semantic", raw))
		if i < 2 && strings.HasPrefix(res.FullOutput, parkPrefix) {
			t.Fatalf("semantic attempt %d parked early: %#v", i+1, res)
		}
		if i == 2 {
			if !strings.HasPrefix(res.Output, parkPrefix) || !strings.Contains(res.Output, "signature") || !strings.Contains(res.Output, "invalid_request") || !strings.Contains(res.Output, "materially different") {
				t.Fatalf("small-limit semantic control message lost its model-facing contract: %#v", res)
			}
		}
	}
	if calls != 2 {
		t.Fatalf("small-limit semantic failures executed %d times, want 2", calls)
	}
}
