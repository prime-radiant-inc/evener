package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

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
	levels := append(llm.ReasoningEffortVocabulary(), llm.ReasoningEffortNone)
	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			t.Parallel()
			root, client, _ := newDelegateResourceBootstrapSession(t)
			adapter := newTask6FrozenDescriptorAdapter()
			client.Register(adapter)
			t.Cleanup(adapter.releaseRun)

			raw, err := json.Marshal(map[string]any{
				"prompt":           "spawn at " + level,
				"model":            "gpt-5.2",
				"reasoning_effort": level,
			})
			if err != nil {
				t.Fatal(err)
			}
			call := root.reg.ExecuteCall(context.Background(), root.currentEnv(), llm.ToolCallData{
				ID:        "registered-effort-" + level,
				Name:      "delegate",
				Arguments: raw,
			})
			if call.IsError {
				t.Fatalf("registered delegate rejected reasoning_effort %q: %s", level, call.Output)
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
			if got := aggregate.Descriptor.Config.ReasoningEffort; got != level {
				t.Fatalf("descriptor ReasoningEffort = %q, want %q", got, level)
			}
		})
	}
}
