package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// The delegate normalizer must leave arguments it does not rewrite untouched
// (dispatch runs it several times per call) and drop an effort that means
// "nothing configured" so the enum never sees an empty string.
func TestNormalizeDelegateArgs(t *testing.T) {
	t.Parallel()
	canonical := map[string]any{
		"prompt":           "p",
		"reasoning_effort": "max",
		"task_list": []any{
			map[string]any{"title": "t", "prompt": "read", "reasoning_effort": "high"},
			map[string]any{"title": "u", "prompt": "read"},
		},
	}
	got, err := normalizeDelegateArgs(canonical)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(canonical).Pointer() {
		t.Fatalf("canonical args were rebuilt instead of returned untouched: %#v", got)
	}

	aliased := map[string]any{
		"prompt":           "p",
		"reasoning_effort": " OFF ",
		"task_list": []any{
			map[string]any{"title": "t", "prompt": "read", "reasoning_effort": "MAX"},
			map[string]any{"title": "u", "prompt": "read", "reasoning_effort": ""},
			map[string]any{"title": "v", "prompt": "read", "reasoning_effort": "inherit"},
		},
	}
	got, err = normalizeDelegateArgs(aliased)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got["reasoning_effort"] != llm.ReasoningEffortNone {
		t.Errorf("top-level effort = %v, want the canonical none", got["reasoning_effort"])
	}
	tasks := got["task_list"].([]any)
	for i, want := range []any{"max", nil, nil} {
		fields := tasks[i].(map[string]any)
		effort, present := fields["reasoning_effort"]
		if want == nil {
			if present {
				t.Errorf("task_list[%d].reasoning_effort = %v, want it dropped", i, effort)
			}
			continue
		}
		if effort != want {
			t.Errorf("task_list[%d].reasoning_effort = %v, want %v", i, effort, want)
		}
	}
	if effort := aliased["reasoning_effort"]; effort != " OFF " {
		t.Errorf("normalizer mutated its input: %v", effort)
	}

	empty, err := normalizeDelegateArgs(map[string]any{"prompt": "p", "reasoning_effort": ""})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if _, present := empty["reasoning_effort"]; present {
		t.Errorf("empty reasoning_effort = %v, want it dropped", empty["reasoning_effort"])
	}
}

// Delegate dispatch validates the effort on its normalized form, so disable
// aliases are accepted (and frozen into the child as the canonical off) while
// out-of-vocabulary values still fail loudly.
func TestDelegateCreate_NormalizesReasoningEffortAlias(t *testing.T) {
	t.Parallel()
	root, client, _ := newDelegateResourceBootstrapSession(t)
	adapter := newTask6FrozenDescriptorAdapter()
	client.Register(adapter)
	t.Cleanup(adapter.releaseRun)

	bad := root.createDelegate(context.Background(), delegateArgs{
		Task:            "reject the garbage effort",
		Model:           "gpt-5.2",
		ReasoningEffort: "ultra",
	})
	if bad.Err == nil || !strings.Contains(bad.Err.Error(), "invalid reasoning_effort") {
		t.Fatalf("createDelegate(ultra) err = %v, want the vocabulary rejection", bad.Err)
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:            "accept the disable alias",
		Model:           "gpt-5.2",
		ReasoningEffort: "OFF",
	})
	if result.Err != nil {
		t.Fatalf("createDelegate(OFF): %v (disable aliases must be accepted and normalized)", result.Err)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID)
	if got := aggregate.Descriptor.Config.ReasoningEffort; got != "none" {
		t.Fatalf("descriptor ReasoningEffort = %q, want the canonical none", got)
	}
}

// A delegate spawned through the registered tool accepts every level the
// session vocabulary defines. "max" was rejected at argument validation
// because the schema enum listed only low/medium/high, while the handler
// already validated against the full six-level vocabulary.
func TestDelegateCreate_RegisteredAcceptsEveryVocabularyLevel(t *testing.T) {
	t.Parallel()
	cases := []struct{ requested, want string }{
		{"minimal", "minimal"},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "xhigh"},
		{"max", "max"},
		{"none", "none"},
		// The handler accepts these forms too; dispatch must canonicalize them
		// rather than refuse them at the schema enum.
		{"MAX", "max"},
		{" Max ", "max"},
		{"off", "none"},
		{"0", "none"},
		{"false", "none"},
		{"null", "none"},
	}
	for _, tc := range cases {
		t.Run(tc.requested, func(t *testing.T) {
			t.Parallel()
			root, client, _ := newDelegateResourceBootstrapSession(t)
			adapter := newTask6FrozenDescriptorAdapter()
			client.Register(adapter)
			t.Cleanup(adapter.releaseRun)

			call := executeDelegateTool(context.Background(), root, "registered-effort-"+tc.requested, "delegate", map[string]any{
				"prompt":           "spawn at " + tc.requested,
				"model":            "gpt-5.2",
				"reasoning_effort": tc.requested,
			})
			if call.IsError {
				t.Fatalf("registered delegate rejected reasoning_effort %q: %s", tc.requested, call.Output)
			}
			var result map[string]any
			if err := json.Unmarshal(toolResultJSON(call), &result); err != nil {
				t.Fatalf("decode registered delegate output %q: %v", call.Output, err)
			}
			delegateID, _ := result["delegate_id"].(string)
			if delegateID == "" {
				t.Fatalf("registered delegate result has no delegate_id: %s", call.Output)
			}
			aggregate := delegateAggregateSnapshot(t, root.delegateController, delegateID)
			if got := aggregate.Descriptor.Config.ReasoningEffort; got != tc.want {
				t.Fatalf("descriptor ReasoningEffort = %q, want %q", got, tc.want)
			}
		})
	}
}
