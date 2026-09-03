package tool

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func TestPrevalidationLifetime_StaleSnapshotsCannotContaminateSemanticTransitions(t *testing.T) {
	tests := []struct {
		name       string
		transition func(*Registry)
		absent     bool
	}{
		{
			name: "replacement",
			transition: func(r *Registry) {
				registerPrevalidationLifetimeTool(t, r, "transition_tool")
			},
		},
		{
			name: "remove",
			transition: func(r *Registry) {
				r.Remove("transition_tool")
			},
			absent: true,
		},
		{
			name: "restrict removal",
			transition: func(r *Registry) {
				r.RestrictKeepingResultTool(map[string]bool{}, "communicate")
			},
			absent: true,
		},
		{
			name: "unregister",
			transition: func(r *Registry) {
				r.Unregister("transition_tool")
			},
			absent: true,
		},
		{
			name: "core semantic policy",
			transition: func(r *Registry) {
				r.MarkRegisteredToolsCoreSemanticMetadata()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			call := breakerCall("transition", "transition_tool", `{"value":"same"}`)
			registerPrevalidationLifetimeTool(t, r, call.Name)
			_, stale := r.SnapshotPrevalidation(call.Name)
			tt.transition(r)

			for range 2 {
				res := r.FinalizePrevalidationFailure(context.Background(), stale, call, "old validation failure", "schema_validation", errors.New("old validation failure"))
				if !res.IsError || res.BreakerExactSignature != "" || res.BreakerSemanticSignature != "" || res.BreakerBypassed {
					t.Fatalf("stale completion must retain only its validation error: %#v", res)
				}
			}

			for i := range 2 {
				_, current := r.SnapshotPrevalidation(call.Name)
				boundary := "schema_validation"
				if tt.absent {
					boundary = "unknown_tool"
				}
				res := r.FinalizePrevalidationFailure(context.Background(), current, call, "current validation failure", boundary, errors.New("current validation failure"))
				if !res.IsError || strings.HasPrefix(res.Output, wantFailurePark(call.Name)) {
					t.Fatalf("current lifetime failure %d was parked early: %#v", i+1, res)
				}
			}
			_, current := r.SnapshotPrevalidation(call.Name)
			res := r.FinalizePrevalidationFailure(context.Background(), current, call, "current validation failure", "schema_validation", errors.New("current validation failure"))
			if !strings.HasPrefix(res.Output, wantFailurePark(call.Name)) {
				t.Fatalf("third current-lifetime failure was not parked: %#v", res)
			}
		})
	}
}

func TestPrevalidationLifetime_CloneTransitionsNeverReuseLiveOrTombstonedLifetime(t *testing.T) {
	parent := NewRegistry()
	registerPrevalidationLifetimeTool(t, parent, "clone_live")
	registerPrevalidationLifetimeTool(t, parent, "clone_tombstone")
	parent.Remove("clone_tombstone")
	clone := parent.Clone()
	// Tokens cannot be passed between independent registries, so the tombstone
	// copy itself has no external operation to observe. Verify the one required
	// internal invariant directly; the transition behavior below proves that the
	// copied allocator does not reuse either retained lifetime.
	if clone.lifetimes["clone_tombstone"] != parent.lifetimes["clone_tombstone"] || clone.nextGeneration != parent.nextGeneration {
		t.Fatalf("clone did not preserve lifetime tombstones/allocator: clone=%v/%d parent=%v/%d", clone.lifetimes, clone.nextGeneration, parent.lifetimes, parent.nextGeneration)
	}

	for _, name := range []string{"clone_live", "clone_tombstone"} {
		t.Run(name, func(t *testing.T) {
			call := breakerCall("clone-"+name, name, `{"value":"same"}`)
			_, stale := clone.SnapshotPrevalidation(name)
			registerPrevalidationLifetimeTool(t, clone, name)
			clone.Remove(name)
			res := clone.FinalizePrevalidationFailure(context.Background(), stale, call, "old clone failure", "unknown_tool", errors.New("old clone failure"))
			if !res.IsError || res.BreakerExactSignature != "" || res.BreakerSemanticSignature != "" {
				t.Fatalf("clone reused a prior %s lifetime: %#v", name, res)
			}

			_, current := clone.SnapshotPrevalidation(name)
			res = clone.FinalizePrevalidationFailure(context.Background(), current, call, "current clone failure", "unknown_tool", errors.New("current clone failure"))
			if !res.IsError || strings.HasPrefix(res.Output, wantFailurePark(name)) {
				t.Fatalf("fresh clone absent lifetime did not start empty: %#v", res)
			}
		})
	}
}

func TestExecutionLifetime_InFlightFailuresCannotContaminateSemanticTransitions(t *testing.T) {
	tests := []struct {
		name       string
		transition func(*Registry)
	}{
		{"replacement", func(r *Registry) { registerPrevalidationLifetimeTool(t, r, "execution_transition_tool") }},
		{"remove", func(r *Registry) {
			r.Remove("execution_transition_tool")
			registerPrevalidationLifetimeTool(t, r, "execution_transition_tool")
		}},
		{"restrict removal", func(r *Registry) {
			r.RestrictKeepingResultTool(map[string]bool{}, "communicate")
			registerPrevalidationLifetimeTool(t, r, "execution_transition_tool")
		}},
		{"unregister", func(r *Registry) {
			r.Unregister("execution_transition_tool")
			registerPrevalidationLifetimeTool(t, r, "execution_transition_tool")
		}},
		{"core semantic policy", func(r *Registry) { r.MarkRegisteredToolsCoreSemanticMetadata() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			call := breakerCall("execution-transition", "execution_transition_tool", `{"value":"same"}`)
			started := make(chan struct{})
			release := make(chan struct{})
			var calls atomic.Int32
			if err := r.Register(RegisteredTool{
				Definition: llm.ToolDefinition{Name: call.Name, Description: "in-flight lifetime test", Parameters: map[string]any{"type": "object"}},
				Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
					if calls.Add(1) == 1 {
						close(started)
						<-release
					}
					return nil, errors.New("execution failure")
				},
			}); err != nil {
				t.Fatal(err)
			}

			done := make(chan ExecResult, 1)
			go func() { done <- r.ExecuteCall(context.Background(), breakerEnv(t), call) }()
			<-started
			tt.transition(r)
			close(release)
			old := <-done
			if !old.IsError || old.BreakerExactSignature != "" || old.BreakerSemanticSignature != "" || old.BreakerBypassed {
				t.Fatalf("stale execution completion must not finalize successor telemetry: %#v", old)
			}

			for i := range 2 {
				res := r.ExecuteCall(context.Background(), breakerEnv(t), call)
				if !res.IsError || strings.HasPrefix(res.Output, wantFailurePark(call.Name)) {
					t.Fatalf("successor failure %d was parked early: %#v", i+1, res)
				}
			}
			if res := r.ExecuteCall(context.Background(), breakerEnv(t), call); !strings.HasPrefix(res.Output, wantFailurePark(call.Name)) {
				t.Fatalf("third successor failure was not parked: %#v", res)
			}
		})
	}
}

func registerPrevalidationLifetimeTool(t *testing.T, r *Registry, name string) {
	t.Helper()
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: name, Description: "lifetime test", Parameters: map[string]any{"type": "object"}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return nil, errors.New("current execution failure")
		},
	}); err != nil {
		t.Fatalf("Register(%q): %v", name, err)
	}
}
