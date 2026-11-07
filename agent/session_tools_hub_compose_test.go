package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// A Manager session composes its job_watch wrapper once per tool-set rebuild.
// The wrapper's ordinary-branch availability must be decided at that compose
// time, like the read and input tools beside it, so a concurrent registry or
// profile rebuild cannot flip a tool call's branch after the model has
// resolved it.
func TestManagerJobWatchOrdinaryBranchDecidedAtCompose(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter()
	adapter.script("root", func(llm.Request) llm.Response { return agenttest.FinalResponse("idle") })
	root := newManagerPromptRoot(t, adapter, func(cfg *SessionConfig) { cfg.Manager = true })

	stub := tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "job_watch",
			Description: "stub ordinary job_watch",
			Parameters:  map[string]any{"properties": map[string]any{}},
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return "stub ordinary job_watch ran", nil
		},
	}
	root.mu.Lock()
	root.reg.Remove("job_watch")
	if err := root.reg.Register(stub); err != nil {
		root.mu.Unlock()
		t.Fatal(err)
	}
	stubCopy := stub
	root.hubOrdinary = map[string]*tool.RegisteredTool{"job_watch": &stubCopy}
	root.rebuildToolDefsCache()
	held := *root.reg.Get("job_watch")
	// The runtime mutation the compose/execution gap exposes: the ordinary
	// tool disappears while the previously composed wrapper is still callable.
	root.reg.Remove("job_watch")
	root.hubOrdinary = nil
	root.rebuildToolDefsCache()
	root.mu.Unlock()

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := held.Exec(context.Background(), env, map[string]any{})
	if err != nil {
		t.Fatalf("held wrapper should dispatch to its composed ordinary tool, got error: %v", err)
	}
	if got != "stub ordinary job_watch ran" {
		t.Fatalf("held wrapper result = %v, want the composed ordinary tool's result", got)
	}
}
