package tool

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func TestBreakerLifetime_ReplacementStartsFreshExactFailureStreak(t *testing.T) {
	r := NewRegistry()
	env := breakerEnv(t)
	call := breakerCall("lifetime", "lifetime_tool", `{"raw":"same"}`)
	old := registerBreakerFake(t, r, "lifetime_tool", func(int) (any, error) { return nil, errors.New("old failure") })
	for range 2 {
		r.ExecuteCall(context.Background(), env, call)
	}
	if old.calls != 2 {
		t.Fatalf("old calls = %d, want 2", old.calls)
	}
	newTool := registerBreakerFake(t, r, "lifetime_tool", func(int) (any, error) { return "new executor", nil })
	res := r.ExecuteCall(context.Background(), env, call)
	if newTool.calls != 1 || res.IsError {
		t.Fatalf("replacement must execute instead of parking: calls=%d result=%#v", newTool.calls, res)
	}
}

func TestBreakerLifetime_RemovalPathsAndUnknownCallsStartFresh(t *testing.T) {
	for _, remove := range []struct {
		name string
		fn   func(*Registry)
	}{
		{"remove", func(r *Registry) { r.Remove("lifetime_tool") }},
		{"restrict", func(r *Registry) { r.RestrictKeepingResultTool(map[string]bool{}, "communicate") }},
		{"unregister", func(r *Registry) { r.Unregister("lifetime_tool") }},
	} {
		t.Run(remove.name, func(t *testing.T) {
			r := NewRegistry()
			env := breakerEnv(t)
			call := breakerCall("lifetime", "lifetime_tool", `{}`)
			old := registerBreakerFake(t, r, "lifetime_tool", func(int) (any, error) { return nil, errors.New("old failure") })
			for range 2 {
				r.ExecuteCall(context.Background(), env, call)
			}
			remove.fn(r)
			newTool := registerBreakerFake(t, r, "lifetime_tool", func(int) (any, error) { return "fresh", nil })
			res := r.ExecuteCall(context.Background(), env, call)
			if old.calls != 2 || newTool.calls != 1 || res.IsError {
				t.Fatalf("removal/re-registration must start fresh: old=%d new=%d result=%#v", old.calls, newTool.calls, res)
			}
		})
	}

	// Unknown calls have no registered lifetime, so they must not leave history
	// that parks a tool registered with the same name later.
	r := NewRegistry()
	env := breakerEnv(t)
	call := breakerCall("unknown", "later_tool", `{}`)
	for range 2 {
		r.ExecuteCall(context.Background(), env, call)
	}
	registered := registerBreakerFake(t, r, "later_tool", func(int) (any, error) { return "registered", nil })
	if res := r.ExecuteCall(context.Background(), env, call); registered.calls != 1 || res.IsError {
		t.Fatalf("first registered call inherited unknown-tool history: calls=%d result=%#v", registered.calls, res)
	}
}

func TestBreakerLifetime_InFlightOldFailuresDoNotContaminateReplacement(t *testing.T) {
	r := NewRegistry()
	env := breakerEnv(t)
	call := breakerCall("race", "race_tool", `{}`)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var oldCalls atomic.Int32
	if err := r.Register(RegisteredTool{Definition: llm.ToolDefinition{Name: "race_tool", Description: "test", Parameters: map[string]any{"type": "object"}}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
		oldCalls.Add(1)
		started <- struct{}{}
		<-release
		return nil, errors.New("old failure")
	}}); err != nil {
		t.Fatal(err)
	}
	var oldResults [2]ExecResult
	var wg sync.WaitGroup
	for i := range oldResults {
		wg.Add(1)
		go func(i int) { defer wg.Done(); oldResults[i] = r.ExecuteCall(context.Background(), env, call) }(i)
	}
	for range oldResults {
		<-started
	}
	var newCalls atomic.Int32
	if err := r.Register(RegisteredTool{Definition: llm.ToolDefinition{Name: "race_tool", Description: "test", Parameters: map[string]any{"type": "object"}}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
		newCalls.Add(1)
		return nil, errors.New("new failure")
	}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	for _, res := range oldResults {
		if !res.IsError || strings.HasPrefix(res.Output, wantFailurePark("race_tool")) {
			t.Fatalf("old in-flight result must return but not finalize a replacement lifetime: %#v", res)
		}
	}
	for range 2 {
		if res := r.ExecuteCall(context.Background(), env, call); !res.IsError || res.Output == wantFailurePark("race_tool") {
			t.Fatalf("fresh lifetime failure was parked early: %#v", res)
		}
	}
	if newCalls.Load() != 2 {
		t.Fatalf("new executor calls = %d, want 2", newCalls.Load())
	}
	if res := r.ExecuteCall(context.Background(), env, call); !strings.HasPrefix(res.Output, wantFailurePark("race_tool")) {
		t.Fatalf("third new-lifetime failure was not parked: %#v", res)
	}
	if oldCalls.Load() != 2 || newCalls.Load() != 2 {
		t.Fatalf("old/new executor calls = %d/%d, want 2/2", oldCalls.Load(), newCalls.Load())
	}
}

func TestBreakerLifetime_CloneReplacementDoesNotReuseGeneration(t *testing.T) {
	r := NewRegistry()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	if err := r.Register(RegisteredTool{Definition: llm.ToolDefinition{Name: "clone_tool", Description: "test", Parameters: map[string]any{"type": "object"}}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
		started <- struct{}{}
		<-release
		return nil, errors.New("old clone failure")
	}}); err != nil {
		t.Fatal(err)
	}
	clone := r.Clone()
	before := clone.Get("clone_tool").generation
	call := breakerCall("clone", "clone_tool", `{}`)
	done := make(chan ExecResult, 1)
	go func() { done <- clone.ExecuteCall(context.Background(), breakerEnv(t), call) }()
	<-started
	newTool := registerBreakerFake(t, clone, "clone_tool", func(int) (any, error) { return "clone replacement", nil })
	if got := clone.Get("clone_tool").generation; got == before {
		t.Fatalf("clone replacement reused live generation %d", got)
	}
	close(release)
	<-done
	if res := clone.ExecuteCall(context.Background(), breakerEnv(t), call); newTool.calls != 1 || res.IsError {
		t.Fatalf("stale clone result contaminated replacement: calls=%d result=%#v", newTool.calls, res)
	}
}

func TestBreakerLifetime_MarkingSemanticMetadataRotatesLifetime(t *testing.T) {
	r := NewRegistry()
	env := breakerEnv(t)
	call := breakerCall("metadata", "metadata_tool", `{}`)
	old := registerBreakerFake(t, r, "metadata_tool", func(int) (any, error) { return nil, errors.New("failure") })
	for range 2 {
		r.ExecuteCall(context.Background(), env, call)
	}
	r.MarkRegisteredToolsCoreSemanticMetadata()
	res := r.ExecuteCall(context.Background(), env, call)
	if old.calls != 3 || !res.IsError || res.Output == wantFailurePark("metadata_tool") {
		t.Fatalf("metadata identity change retained breaker history: calls=%d result=%#v", old.calls, res)
	}
}

func TestBreakerLifetime_PrevalidationAndBypassDoNotCrossReplacement(t *testing.T) {
	r := NewRegistry()
	env := breakerEnv(t)
	call := breakerCall("prevalidation", "prevalidation_tool", `{"bad":true}`)
	old := registerBreakerFake(t, r, "prevalidation_tool", func(int) (any, error) { return "old", nil })
	for range 2 {
		r.FinalizePrevalidationFailure(context.Background(), call, "prevalidation failure", "schema_validation", errors.New("prevalidation failure"))
	}
	newTool := registerBreakerFake(t, r, "prevalidation_tool", func(int) (any, error) { return "new", nil })
	if res := r.ExecuteCall(context.Background(), env, call); old.calls != 0 || newTool.calls != 1 || res.IsError {
		t.Fatalf("replacement inherited prevalidation history: old=%d new=%d result=%#v", old.calls, newTool.calls, res)
	}

	// A bypassed old dispatch must not clear failures accumulated by the
	// replacement while that old executor was in flight.
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	if err := r.Register(RegisteredTool{Definition: llm.ToolDefinition{Name: "bypass_tool", Description: "test", Parameters: map[string]any{"type": "object"}}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
		started <- struct{}{}
		<-release
		return nil, errors.New("old bypass failure")
	}}); err != nil {
		t.Fatal(err)
	}
	bypassCall := breakerCall("bypass", "bypass_tool", `{}`)
	oldDone := make(chan ExecResult, 1)
	go func() { oldDone <- r.ExecuteCall(WithBreakerBypass(context.Background()), env, bypassCall) }()
	<-started
	newFailures := registerBreakerFake(t, r, "bypass_tool", func(int) (any, error) { return nil, errors.New("new failure") })
	for range 2 {
		r.ExecuteCall(context.Background(), env, bypassCall)
	}
	close(release)
	<-oldDone
	if res := r.ExecuteCall(context.Background(), env, bypassCall); !strings.HasPrefix(res.Output, wantFailurePark("bypass_tool")) || newFailures.calls != 2 {
		t.Fatalf("stale bypass cleared replacement failures: calls=%d result=%#v", newFailures.calls, res)
	}
}
