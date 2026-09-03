package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestSemanticFailureBreaker_StableUntypedExecutionErrorsParkAcrossRawVariants(t *testing.T) {
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
		if i < 2 && strings.HasPrefix(res.FullOutput, parkPrefix) {
			t.Fatalf("untyped semantic variant %d parked early: %#v", i+1, res)
		}
		if i == 2 && !strings.HasPrefix(res.FullOutput, parkPrefix) {
			t.Fatalf("stable untyped semantic variant did not park: %#v", res)
		}
	}
	if fake.calls != 2 {
		t.Fatalf("stable untyped semantic variants executed %d times, want 2", fake.calls)
	}
}

func TestSemanticFailureBreaker_DistinctUntypedExecutionErrorsDoNotParkAcrossRawVariants(t *testing.T) {
	r := NewRegistry()
	fake := registerSemanticBreakerFake(t, r, func(_ map[string]any, calls int) (any, error) {
		return nil, errors.New([]string{"backend alpha unavailable", "backend beta unavailable", "backend gamma unavailable"}[calls-1])
	})
	for i, raw := range []string{
		`{"target":"job:one","intent":"first"}`,
		`{"intent":"second","target":"job:one"}`,
		`{"target":"job:one","intent":"third"}`,
	} {
		res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("untyped-distinct-%d", i), "semantic_fake", raw))
		if strings.HasPrefix(res.FullOutput, parkPrefix) {
			t.Fatalf("distinct untyped semantic variant %d was parked: %#v", i+1, res)
		}
	}
	if fake.calls != 3 {
		t.Fatalf("distinct untyped semantic variants executed %d times, want 3", fake.calls)
	}
}

func TestStripPresentationTraceSuffix(t *testing.T) {
	longToken := strings.Repeat("a", 65)
	widthChangingPrefix := strings.Repeat("Ⱥ", 20)
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "leading and trailing whitespace", input: "  backend unavailable [trace a]  ", want: "backend unavailable"},
		{name: "case insensitive marker", input: "backend unavailable [TrAcE A]", want: "backend unavailable"},
		{name: "valid bounded token", input: "backend unavailable [trace a_1.foo:bar-]", want: "backend unavailable"},
		{name: "invalid bracket details remain meaningful", input: "backend unavailable [trace has spaces]", want: "backend unavailable [trace has spaces]"},
		{name: "token length bound", input: "backend unavailable [trace " + longToken + "]", want: "backend unavailable [trace " + longToken + "]"},
		{name: "utf8 prefix", input: "échec backend [trace a]", want: "échec backend"},
		{name: "width changing unicode prefix", input: widthChangingPrefix + " [trace a]", want: widthChangingPrefix},
		{name: "trace-like text is not terminal", input: "backend unavailable [trace a] while retrying", want: "backend unavailable [trace a] while retrying"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stripPresentationTraceSuffix(tc.input)
			if got != tc.want || !utf8.ValidString(got) {
				t.Fatalf("stripPresentationTraceSuffix(%q) = %q (valid=%t), want %q", tc.input, got, utf8.ValidString(got), tc.want)
			}
		})
	}
}

func TestGenericExecutionClass_StripsTerminalTraceBeforeTruncation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
		token  string
	}{
		{name: "200 rune boundary", prefix: strings.Repeat("x", 200), token: "a"},
		{name: "256 byte boundary", prefix: strings.Repeat("x", 246), token: "a"},
		{name: "past 256 byte boundary", prefix: strings.Repeat("x", 247), token: "a"},
		{name: "long utf8 prefix and 259 rune input", prefix: strings.Repeat("é", 190), token: strings.Repeat("a", 60)},
		{name: "case variants", prefix: strings.Repeat("x", 190), token: "A_B.C:Z-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.prefix + " [TrAcE " + tc.token + "]"
			want := "generic:" + TruncateRunes(strings.ToLower(tc.prefix), 200)
			if got := genericExecutionClass(input); got != want {
				t.Fatalf("genericExecutionClass(%q) = %q, want %q", input, got, want)
			}
		})
	}

	prefix := strings.Repeat("x", 190)
	for _, input := range []string{
		prefix + " [trace invalid token]",
		prefix + " [trace a] while retrying",
	} {
		if got := genericExecutionClass(input); got == genericExecutionClass(prefix) {
			t.Fatalf("genericExecutionClass(%q) discarded semantic trace-like detail", input)
		}
	}
}

func TestSemanticFailureBreaker_LongTerminalTraceVariantsPark(t *testing.T) {
	r := NewRegistry()
	calls := 0
	prefix := strings.Repeat("x", 190)
	registerSemanticBreakerFake(t, r, func(map[string]any, int) (any, error) {
		calls++
		return nil, errors.New(prefix + " [trace " + []string{"a", "b", "c"}[calls-1] + "]")
	})
	for i, raw := range []string{
		`{"target":"job:one","intent":"first"}`,
		`{"intent":"second","target":"job:one"}`,
		`{"target":"job:one","intent":"third"}`,
	} {
		res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("long-trace-%d", i), "semantic_fake", raw))
		if i < 2 && strings.HasPrefix(res.FullOutput, parkPrefix) {
			t.Fatalf("long terminal trace variant %d parked early: %#v", i+1, res)
		}
		if i == 2 && !strings.HasPrefix(res.FullOutput, parkPrefix) {
			t.Fatalf("long terminal trace variants evaded semantic breaker: %#v", res)
		}
	}
	if calls != 2 {
		t.Fatalf("long terminal trace variants executed %d times, want 2", calls)
	}
}

func FuzzStripPresentationTraceSuffix_ValidUTF8(f *testing.F) {
	for _, seed := range []string{
		"",
		strings.Repeat("Ⱥ", 20) + " [trace a]",
		strings.Repeat("é", 190) + " [trace " + strings.Repeat("a", 60) + "]",
		"backend unavailable [trace has spaces]",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if !utf8.ValidString(input) {
			return
		}
		got := stripPresentationTraceSuffix(input)
		if !utf8.ValidString(got) {
			t.Fatalf("stripPresentationTraceSuffix(%q) returned invalid UTF-8 %q", input, got)
		}
		_ = genericExecutionClass(input)
	})
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
