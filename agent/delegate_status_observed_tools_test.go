package agent

import (
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// TestDelegateStatusReportsObservedRegistrationsNotTheCeiling is medium 3 of the
// review, and it separates the two questions one field was answering.
//
// The frozen descriptor is committed before the child finishes initializing, so
// it cannot know which names initialization registered: a fallback server that
// came up in the child's own workspace, or a name rolled back by a collision, is
// invisible to it. It stays the AUTHORIZATION ceiling — what the child may call,
// and what recovery replays — and reporting reads what the child registered.
//
// A child that registered nothing reports nothing. That is the case a
// ceiling-copying report gets wrong: the descriptor still names svc__echo, so
// the reader is told the delegate has a tool it does not.
func TestDelegateStatusReportsObservedRegistrationsNotTheCeiling(t *testing.T) {
	t.Parallel()
	descriptor := delegatestore.Descriptor{
		Task:            "report from observation",
		ToolNameCeiling: []string{"read_file", "svc__echo", "svc__lookup"},
		Resumable:       true,
	}

	t.Run("a child that registered nothing reports nothing", func(t *testing.T) {
		t.Parallel()
		// An empty, non-nil observation is the honest answer; nil would mean "no
		// child was observed" and falls back to the ceiling by design.
		snapshot := delegateSnapshot{descriptor: descriptor, observedTools: []string{}}
		got := projectDelegateStatus(time.Now(), snapshot)
		if len(got.Tools) != 0 {
			t.Errorf("status reported %v for a child that registered nothing", got.Tools)
		}
	})

	t.Run("a child that registered one of two reports that one", func(t *testing.T) {
		t.Parallel()
		snapshot := delegateSnapshot{descriptor: descriptor, observedTools: []string{"read_file", "svc__lookup"}}
		got := projectDelegateStatus(time.Now(), snapshot)
		if slices.Contains(got.Tools, "svc__echo") {
			t.Errorf("status reported %q, which the child did not register: %v", "svc__echo", got.Tools)
		}
		if !slices.Contains(got.Tools, "svc__lookup") {
			t.Errorf("status dropped %q, which the child did register: %v", "svc__lookup", got.Tools)
		}
	})

	t.Run("no observed runtime still falls back to the ceiling", func(t *testing.T) {
		t.Parallel()
		snapshot := delegateSnapshot{descriptor: descriptor}
		got := projectDelegateStatus(time.Now(), snapshot)
		if !slices.Contains(got.Tools, "svc__echo") {
			t.Errorf("with no observed child the status dropped a ceiling entry: %v", got.Tools)
		}
	})
}

// TestObservedDelegateToolsReadsTheChildRegistry asserts the capture side reads
// the child's own registry — including a name the ceiling never listed, which is
// what a fallback server coming up in the child's workspace produces.
func TestObservedDelegateToolsReadsTheChildRegistry(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	for _, name := range []string{"read_file", "svc__lookup"} {
		if err := reg.Register(regTool(llm.ToolDefinition{Name: name, Description: "fixture"})); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
	}
	got := observedDelegateTools(&Session{reg: reg})
	for _, want := range []string{"read_file", "svc__lookup"} {
		if !slices.Contains(got, want) {
			t.Errorf("observed tools omit %q, which the child registered: %v", want, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("observed tools = %v, want exactly the two registered names", got)
	}
	if observedDelegateTools(nil) != nil {
		t.Error("no runtime must observe nil, which the reporter reads as \"no observation\"")
	}
}

// TestTerminalPacketReportsOneToolList is the packet half of the same rule: the
// owner reads this metadata as text, so listing the observation AND the ceiling
// tells the parent model that the child has tools it never registered.
//
// The observation therefore replaces the ceiling in Tools whenever one exists,
// and an empty observation is still an observation. Only a packet with no
// observation at all falls back to the ceiling, because then the ceiling is the
// only answer the parent has.
func TestTerminalPacketReportsOneToolList(t *testing.T) {
	t.Parallel()
	descriptor := delegatestore.Descriptor{
		Task:            "one tool list",
		ToolNameCeiling: []string{"read_file", "svc__echo"},
	}
	reg := tool.NewRegistry()
	for _, name := range []string{"svc__lookup"} {
		if err := reg.Register(regTool(llm.ToolDefinition{Name: name, Description: "fixture"})); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
	}

	t.Run("the observation replaces the ceiling", func(t *testing.T) {
		t.Parallel()
		metadata := delegateTerminalMetadataFromRun(delegateTerminalRunInputs{
			descriptor: descriptor,
			session:    &Session{reg: reg},
		})
		if metadata.ObservedTools == nil {
			t.Fatal("a session with a registry produced no observation")
		}
		if len(metadata.Tools) != 0 {
			t.Errorf("metadata.Tools = %v alongside observed %v; the report would list tools the child never registered", metadata.Tools, *metadata.ObservedTools)
		}
		if !slices.Contains(*metadata.ObservedTools, "svc__lookup") {
			t.Errorf("observation omits %q, which the child registered: %v", "svc__lookup", *metadata.ObservedTools)
		}
	})

	t.Run("an empty observation still replaces the ceiling", func(t *testing.T) {
		t.Parallel()
		metadata := delegateTerminalMetadataFromRun(delegateTerminalRunInputs{
			descriptor: descriptor,
			session:    &Session{reg: tool.NewRegistry()},
		})
		if metadata.ObservedTools == nil || len(*metadata.ObservedTools) != 0 {
			t.Fatalf("observation = %v, want a present empty list", metadata.ObservedTools)
		}
		if len(metadata.Tools) != 0 {
			t.Errorf("metadata.Tools = %v for a child that registered nothing", metadata.Tools)
		}
	})

	t.Run("no observation falls back to the ceiling", func(t *testing.T) {
		t.Parallel()
		metadata := delegateTerminalMetadataFromRun(delegateTerminalRunInputs{descriptor: descriptor})
		if metadata.ObservedTools != nil {
			t.Fatalf("observation = %v with no session, want absent", *metadata.ObservedTools)
		}
		if !slices.Equal(metadata.Tools, descriptor.ToolNameCeiling) {
			t.Errorf("metadata.Tools = %v, want the ceiling %v when nothing was observed", metadata.Tools, descriptor.ToolNameCeiling)
		}
	})
}
