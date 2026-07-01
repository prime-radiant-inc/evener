package hooks

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

// NewRunner with a real client wires a prompt-hook adapter; with nil it disables
// prompt hooks but still constructs a usable Runner.
func TestCov_NewRunner(t *testing.T) {
	r := NewRunner(llm.NewClient(), "gpt-test")
	if r == nil || r.client == nil {
		t.Fatal("NewRunner with a client should wire a prompt-hook client")
	}
	if r.model != "gpt-test" {
		t.Errorf("model = %q, want gpt-test", r.model)
	}
	rn := NewRunner(nil, "")
	if rn == nil || rn.client != nil {
		t.Fatal("NewRunner(nil) should leave the prompt-hook client unset")
	}
}

// executeCommandHook rejects the reserved powershell shell and any non-bash
// shell selection.
func TestCov_ExecuteCommandHook_ShellSelection(t *testing.T) {
	_, err := executeCommandHook(context.Background(),
		plugin.RegisteredHook{Type: "command", Command: "echo hi", Shell: "powershell", Timeout: 5}, Input{})
	if err == nil || !strings.Contains(err.Error(), "powershell") {
		t.Errorf("powershell shell should be rejected, got %v", err)
	}
	_, err = executeCommandHook(context.Background(),
		plugin.RegisteredHook{Type: "command", Command: "echo hi", Shell: "tcsh", Timeout: 5}, Input{})
	if err == nil || !strings.Contains(err.Error(), "unsupported shell") {
		t.Errorf("non-bash shell should be rejected, got %v", err)
	}
}

// runHook: a prompt hook with no client is skipped; an unknown type is a no-op
// that lets the action continue.
func TestCov_RunHook_PromptNoClientAndUnknownType(t *testing.T) {
	r := newRunner(nil, "")
	skip := r.runHook(context.Background(),
		plugin.RegisteredHook{Type: "prompt"}, plugin.HookPreToolUse, Input{})
	if !skip.Continue || !strings.Contains(skip.SystemMessage, "no LLM client") {
		t.Errorf("prompt hook without a client should be skipped, got %+v", skip)
	}
	unknown := r.runHook(context.Background(),
		plugin.RegisteredHook{Type: "mystery"}, plugin.HookPreToolUse, Input{})
	if !unknown.Continue {
		t.Errorf("unknown hook type should continue, got %+v", unknown)
	}
}

// runHook surfaces an infrastructure error (bad shell) as a non-blocking
// IsError output.
func TestCov_RunHook_CommandError(t *testing.T) {
	r := newRunner(nil, "")
	out := r.runHook(context.Background(),
		plugin.RegisteredHook{Type: "command", Command: "echo hi", Shell: "tcsh", Timeout: 5},
		plugin.HookPreToolUse, Input{})
	if !out.Continue || !out.IsError {
		t.Errorf("a shell error should yield a non-blocking IsError output, got %+v", out)
	}
}

func TestCov_MergeHookInputMaps(t *testing.T) {
	// Empty src returns dst unchanged.
	if got := mergeHookInputMaps(map[string]any{"a": 1}, nil); got["a"] != 1 || len(got) != 1 {
		t.Errorf("empty src should return dst unchanged, got %v", got)
	}
	// Nil dst is allocated; src keys are copied and override.
	got := mergeHookInputMaps(nil, map[string]any{"x": 1, "y": 2})
	if got["x"] != 1 || got["y"] != 2 {
		t.Errorf("merge into nil dst failed: %v", got)
	}
	got = mergeHookInputMaps(map[string]any{"x": 1}, map[string]any{"x": 9, "z": 3})
	if got["x"] != 9 || got["z"] != 3 {
		t.Errorf("src should override dst: %v", got)
	}
}

// RunSessionEnd dispatches SessionEnd hooks (fire-and-forget, no result).
func TestCov_RunSessionEnd(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Command: "true", Timeout: 5, PluginName: "p"})
	// Must not panic and must complete synchronously.
	r.RunSessionEnd(context.Background(), Input{})
}
