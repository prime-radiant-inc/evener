package agent

import (
	"context"
	"strings"
	"testing"
)

// Delegate dispatch validates the effort on its normalized form, so disable
// aliases are accepted (and frozen into the child as the canonical off) while
// out-of-vocabulary values still fail loudly.
func TestDelegateCreate_NormalizesReasoningEffortAlias(t *testing.T) {
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
