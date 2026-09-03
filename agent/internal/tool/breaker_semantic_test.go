package tool

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func registerSemanticBreakerFake(t *testing.T, r *Registry, fn func(args map[string]any, calls int) (any, error)) *breakerFake {
	t.Helper()
	fake := &breakerFake{}
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "semantic_fake",
			Description: "semantic breaker test fake",
			Parameters:  map[string]any{"type": "object"},
		},
		NormalizeArgs: func(args map[string]any) (map[string]any, error) {
			delete(args, "neutral")
			return args, nil
		},
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			fake.calls++
			return fn(args, fake.calls)
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return fake
}

func TestSemanticFailureBreaker_IntentAndNormalizedDefaultsCannotEvade(t *testing.T) {
	r := NewRegistry()
	fake := registerSemanticBreakerFake(t, r, func(map[string]any, int) (any, error) {
		return nil, errors.New("invalid_request: range applies only to session transcript refs")
	})
	env, ctx := breakerEnv(t), context.Background()

	for i, args := range []string{
		`{"target":"job:one","intent":"Reading first","neutral":null}`,
		`{"neutral":null,"intent":"Trying a different wording","target":"job:one"}`,
		`{"target":"job:one","intent":"One more time"}`,
	} {
		res := r.ExecuteCall(ctx, env, breakerCall(fmt.Sprintf("c%d", i), "semantic_fake", args))
		if res.BreakerExactSignature == "" || res.BreakerSemanticSignature == "" || len(res.BreakerSemanticSignature) > 96 {
			t.Fatalf("attempt %d missing bounded breaker telemetry: %#v", i+1, res)
		}
		if strings.Contains(res.BreakerSemanticSignature, "Reading") || strings.Contains(res.BreakerSemanticSignature, "Trying") {
			t.Fatalf("attempt %d leaked presentation text into telemetry: %q", i+1, res.BreakerSemanticSignature)
		}
		if i < 2 && strings.Contains(res.Output, parkPrefix) {
			t.Fatalf("attempt %d parked early: %q", i+1, res.Output)
		}
		if i == 2 {
			if !strings.Contains(res.Output, "semantic failure loop") || !strings.Contains(res.Output, "invalid_request") || !strings.Contains(res.Output, "materially different") {
				t.Fatalf("semantic park lacks loop, boundary, or alternate action: %q", res.Output)
			}
		}
	}
	if fake.calls != 2 {
		t.Fatalf("intent/default variants executed %d times, want 2 before semantic park", fake.calls)
	}
}

func TestSemanticFailureBreaker_MeaningfulArgumentsAndInterleavedRunsRemainDistinct(t *testing.T) {
	r := NewRegistry()
	fake := registerSemanticBreakerFake(t, r, func(map[string]any, int) (any, error) {
		return nil, errors.New("invalid_request: output_match is not valid RE2")
	})
	env, ctx := breakerEnv(t), context.Background()
	call := func(id, target string) ExecResult {
		return r.ExecuteCall(ctx, env, breakerCall(id, "semantic_fake", fmt.Sprintf(`{"target":%q,"intent":%q}`, target, id)))
	}

	for _, tc := range []struct{ id, target string }{{"a1", "job:a"}, {"b1", "job:b"}, {"a2", "job:a"}, {"b2", "job:b"}} {
		if res := call(tc.id, tc.target); strings.Contains(res.Output, parkPrefix) {
			t.Fatalf("%s parked early: %q", tc.id, res.Output)
		}
	}
	if fake.calls != 4 {
		t.Fatalf("interleaved failures executed %d times, want 4", fake.calls)
	}
	for _, tc := range []struct{ id, target string }{{"a3", "job:a"}, {"b3", "job:b"}} {
		if res := call(tc.id, tc.target); !strings.Contains(res.Output, "semantic failure loop") {
			t.Fatalf("%s did not park its independent semantic run: %q", tc.id, res.Output)
		}
	}
	if fake.calls != 4 {
		t.Fatalf("parked semantic runs executed the fake: %d", fake.calls)
	}

	if res := call("c1", "job:corrected"); strings.Contains(res.Output, parkPrefix) || fake.calls != 5 {
		t.Fatalf("meaningfully corrected target must get fresh execution: calls=%d result=%q", fake.calls, res.Output)
	}
}

func TestSemanticFailureBreaker_SuccessAndBypassClearOnlyMatchingRun(t *testing.T) {
	r := NewRegistry()
	seenA := 0
	fake := registerSemanticBreakerFake(t, r, func(args map[string]any, _ int) (any, error) {
		if args["target"] == "job:a" {
			seenA++
			if seenA == 3 {
				return "recovered", nil
			}
		}
		return nil, errors.New("invalid_request: mode is not valid for target")
	})
	env, ctx := breakerEnv(t), context.Background()
	call := func(ctx context.Context, id, target string) ExecResult {
		return r.ExecuteCall(ctx, env, breakerCall(id, "semantic_fake", fmt.Sprintf(`{"target":%q,"intent":%q}`, target, id)))
	}

	call(ctx, "a1", "job:a")
	call(ctx, "a2", "job:a")
	call(ctx, "b1", "job:b")
	call(ctx, "b2", "job:b")
	if res := call(WithBreakerBypass(ctx), "a-approved", "job:a"); res.IsError || !res.BreakerBypassed || fake.calls != 5 {
		t.Fatalf("approved semantic retry = %#v, calls=%d", res, fake.calls)
	}
	if res := call(ctx, "a-fresh", "job:a"); strings.Contains(res.Output, parkPrefix) || fake.calls != 6 {
		t.Fatalf("matching success did not reset semantic run: calls=%d result=%q", fake.calls, res.Output)
	}
	if res := call(ctx, "b-parked", "job:b"); !strings.Contains(res.Output, "semantic failure loop") || fake.calls != 6 {
		t.Fatalf("success for a altered b's semantic run: calls=%d result=%q", fake.calls, res.Output)
	}
}

func TestSemanticFailureBreaker_SignaturesAreBoundedAndRedacted(t *testing.T) {
	base := semanticCallSignature("read_transcript", map[string]any{
		"transcript_ref": "job:one",
		"intent":         "secret present only here",
		"description":    "presentation only",
		"token":          "super-secret-token",
		"body":           strings.Repeat("x", 4096),
		"output_match":   "needle",
		"offset_bytes":   float64(0),
	})
	changed := semanticCallSignature("read_transcript", map[string]any{
		"transcript_ref": "job:one", "output_match": "other", "offset_bytes": float64(0),
	})
	if len(base) > 96 || strings.Contains(base, "secret") || strings.Contains(base, strings.Repeat("x", 20)) {
		t.Fatalf("signature is not bounded/redacted: %q", base)
	}
	if base == changed {
		t.Fatalf("meaningful regex change must have a distinct semantic signature: %q", base)
	}
	longPatternA := semanticCallSignature("read_transcript", map[string]any{"output_match": strings.Repeat("a", 300)})
	longPatternB := semanticCallSignature("read_transcript", map[string]any{"output_match": strings.Repeat("b", 300)})
	if longPatternA == longPatternB {
		t.Fatalf("long meaningful regexes must remain distinct: %q", longPatternA)
	}
	bodyA := semanticCallSignature("write_file", map[string]any{"file_path": "a", "content": strings.Repeat("a", 4096)})
	bodyB := semanticCallSignature("write_file", map[string]any{"file_path": "a", "content": strings.Repeat("b", 4096)})
	if bodyA == bodyB {
		t.Fatalf("behavior-driving bodies must remain distinct in the private semantic identity: %q", bodyA)
	}
	meaningful := map[string]any{"transcript_ref": "job:one", "offset_bytes": float64(0), "watch_id": "watch-a", "task_id": float64(1), "operation": "create"}
	baseline := semanticCallSignature("job_watch", meaningful)
	for field, value := range map[string]any{"transcript_ref": "job:two", "offset_bytes": float64(1), "watch_id": "watch-b", "task_id": float64(2), "operation": "clear"} {
		variant := make(map[string]any, len(meaningful))
		maps.Copy(variant, meaningful)
		variant[field] = value
		if got := semanticCallSignature("job_watch", variant); got == baseline {
			t.Fatalf("meaningful %s change did not alter signature", field)
		}
	}
	ledger := newSemanticFailureLedger()
	for i := 0; i <= maxFailureLedgerEntries; i++ {
		base := fmt.Sprintf("semantic_fake:%d", i)
		ledger.record(base, "invalid_request", "invalid_request")
	}
	if got := ledger.len(); got != maxFailureLedgerEntries {
		t.Fatalf("bounded semantic history = %d, want %d", got, maxFailureLedgerEntries)
	}
}
