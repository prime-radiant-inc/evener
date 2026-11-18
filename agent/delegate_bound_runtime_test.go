package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// TestBoundRuntimeIsTheOneAttachedToTheMatchingBinding pins that availability is
// sampled from the runtime attached to a generation's own binding.
//
// A retained child keeps a resident runtime while a newly committed generation
// holds a binding with no runtime attached yet. Sampling live.runtime in that
// window reports the resident registry — one generation's tools — against the new
// generation, which has registered nothing so far. Until a runtime is attached,
// the generation has no observation and must fall back rather than borrow one.
func TestBoundRuntimeIsTheOneAttachedToTheMatchingBinding(t *testing.T) {
	t.Parallel()

	resident := &Session{reg: tool.NewRegistry()}
	if err := resident.reg.Register(regTool(llm.ToolDefinition{Name: "read_file", Description: "fixture"})); err != nil {
		t.Fatalf("register: %v", err)
	}
	attached := &Session{reg: tool.NewRegistry()}

	controller := &delegateTreeController{}

	t.Run("no binding yields no runtime", func(t *testing.T) {
		live := &delegateLiveState{runtime: resident}
		if got := controller.boundRuntimeLocked(live, 2); got != nil {
			t.Errorf("a live state with no binding yielded %p, want nil", got)
		}
	})

	t.Run("a binding for another generation yields no runtime even though the resident is present", func(t *testing.T) {
		live := &delegateLiveState{
			runtime: resident,
			binding: &delegateRuntimeBinding{lease: delegateLease{generation: 1}},
		}
		if got := controller.boundRuntimeLocked(live, 2); got != nil {
			t.Errorf("generation 2 borrowed generation 1's binding runtime: %p", got)
		}
	})

	t.Run("a matching binding with no attached runtime borrows nothing", func(t *testing.T) {
		live := &delegateLiveState{
			runtime: resident,
			binding: &delegateRuntimeBinding{lease: delegateLease{generation: 2}},
		}
		if got := controller.boundRuntimeLocked(live, 2); got != nil {
			t.Errorf("a generation whose runtime is not attached yet sampled the resident %p, attributing one generation's registry to another", got)
		}
	})

	t.Run("a matching binding with an attached runtime yields exactly that runtime", func(t *testing.T) {
		live := &delegateLiveState{
			runtime: resident,
			binding: &delegateRuntimeBinding{lease: delegateLease{generation: 2}, runtime: attached},
		}
		if got := controller.boundRuntimeLocked(live, 2); got != attached {
			t.Errorf("boundRuntimeLocked = %p, want the attached runtime %p", got, attached)
		}
	})
}
