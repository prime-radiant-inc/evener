package hooks

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/sandbox"
)

type recordingSandboxWrapperRuntime struct {
	invocations []commandHookInvocation
}

func (r *recordingSandboxWrapperRuntime) Environ() []string { return nil }

func (r *recordingSandboxWrapperRuntime) Run(_ context.Context, invocation commandHookInvocation) (hookResult, error) {
	r.invocations = append(r.invocations, invocation)
	return hookResult{}, nil
}

func TestCovSetSandboxWrapperControlsCommandHookInvocation(t *testing.T) {
	runner := NewRunner(nil, "gpt-5")
	runtime := &recordingSandboxWrapperRuntime{}
	runner.commandHookRuntime = runtime
	hook := plugin.RegisteredHook{Type: "command", Command: "echo hook", Timeout: 1}
	wrapper := &sandbox.Wrapper{}

	runner.SetSandboxWrapper(wrapper)
	runner.runHook(context.Background(), hook, plugin.HookNotification, Input{})
	if len(runtime.invocations) != 1 {
		t.Fatalf("sandboxed invocation count = %d, want 1", len(runtime.invocations))
	}
	if runtime.invocations[0].SandboxWrapper != wrapper {
		t.Fatalf("sandboxed invocation wrapper = %p, want %p", runtime.invocations[0].SandboxWrapper, wrapper)
	}

	runner.SetSandboxWrapper(nil)
	runner.runHook(context.Background(), hook, plugin.HookNotification, Input{})
	if len(runtime.invocations) != 2 {
		t.Fatalf("unconfined invocation count = %d, want 2", len(runtime.invocations))
	}
	if runtime.invocations[1].SandboxWrapper != nil {
		t.Fatalf("unconfined invocation wrapper = %p, want nil", runtime.invocations[1].SandboxWrapper)
	}
}
