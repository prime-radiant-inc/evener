package transcript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func toolCallTurn(id, name string) schema.Turn {
	return schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{
		Kind:     llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{ID: id, Name: name},
	}}}}
}

func toolResultTurn(results ...llm.ToolResultData) schema.Turn {
	parts := make([]llm.ContentPart, 0, len(results))
	for i := range results {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &results[i]})
	}
	return schema.Turn{Message: llm.Message{Content: parts}}
}

func exitState(code int64) json.RawMessage {
	raw, err := json.Marshal(struct {
		ExitCode int64 `json:"exit_code"`
	}{code})
	if err != nil {
		panic(err)
	}
	return raw
}

func TestFailedToolResultCountsAnErroredResult(t *testing.T) {
	if !FailedToolResult("read_file", true) {
		t.Fatal("FailedToolResult(read_file, isError=true) = false, want true")
	}
}

func TestFailedToolResultIgnoresACleanResult(t *testing.T) {
	// A clean result is not a failure whatever tool produced it. In particular
	// a nonzero exit — which the rule no longer even reads (Jesse, 2026-10-16) —
	// never makes one.
	if FailedToolResult("shell", false) {
		t.Fatal("FailedToolResult(shell, isError=false) = true, want false")
	}
}

func TestFailedToolResultIgnoresCommunicate(t *testing.T) {
	// ProjectTurn drops communicate results, so they render no row and wear no
	// glyph. Counting one would put the figure a mark ahead of the transcript.
	if FailedToolResult("communicate", true) {
		t.Fatal("FailedToolResult(communicate, isError=true) = true, want false")
	}
}

func TestFailureCounterCountsEveryFailureItObserves(t *testing.T) {
	c := NewFailureCounter(0)
	c.Observe(toolCallTurn("call_1", "shell"))
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "shell", ToolState: exitState(1)})) // exit alone: not a failure
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_2", Name: "read_file", IsError: true}))
	if got := c.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1 (the errored result, not the shell exit)", got)
	}
}

func TestFailureCounterDoesNotCountAShellCallThatOnlyExitedNonzero(t *testing.T) {
	c := NewFailureCounter(0)
	c.Observe(toolCallTurn("call_1", "shell"))
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "shell", ToolState: exitState(1)}))
	if got := c.Count(); got != 0 {
		t.Fatalf("Count() = %d, want 0: a nonzero exit alone is not a failure", got)
	}
}

func TestFailureCounterReportsZeroForACleanRun(t *testing.T) {
	c := NewFailureCounter(0)
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file"}))
	if got := c.Count(); got != 0 {
		t.Fatalf("Count() = %d, want 0", got)
	}
}

func TestFailureCounterResolvesANamelessResultFromItsCall(t *testing.T) {
	// A result whose own record omits its name is resolved from the call that
	// announced it, the same way ProjectTurn resolves it — otherwise a nameless
	// errored result loses the name its count depends on.
	c := NewFailureCounter(0)
	c.Observe(toolCallTurn("call_1", "read_file"))
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", IsError: true}))
	if got := c.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1", got)
	}
}

func TestFailureCounterSkipsAnInheritedForkPrefix(t *testing.T) {
	// A fork child's transcript opens with a verbatim copy of the parent's
	// prefix, whose failures the PARENT made. Counting from the divergence
	// ordinal is the same attribution rule the token sum applies.
	c := NewFailureCounter(3)
	c.Observe(toolCallTurn("call_1", "read_file"))                                                        // ordinal 1, parent's
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", IsError: true}))                    // ordinal 2, parent's
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})) // ordinal 3, the child's own
	if got := c.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1 (the parent's failure is not the child's)", got)
	}
}

func TestFailureCounterResolvesNamesFromBeforeTheDivergenceCut(t *testing.T) {
	// The name map is filled from EVERY entry, including ones before the cut: a
	// child's own result can answer a call the inherited prefix announced.
	c := NewFailureCounter(2)
	c.Observe(toolCallTurn("call_1", "read_file"))
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", IsError: true}))
	if got := c.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1", got)
	}
}

// TestExitCodeFromToolStateSkipsNonObjectState: state that is not a JSON object
// cannot carry an exit code, so it reports nil without decoding it. Large
// array states (task_list) otherwise cost a full failed decode on every
// projection of their result.
func TestExitCodeFromToolStateSkipsNonObjectState(t *testing.T) {
	array := json.RawMessage(`  [{"id":1,"exit_code":3},{"id":2}]`)
	if got := ExitCodeFromToolState(array); got != nil {
		t.Fatalf("array state exit code = %d, want nil", *got)
	}
	if allocs := testing.AllocsPerRun(10, func() { ExitCodeFromToolState(array) }); allocs != 0 {
		t.Fatalf("array state decoded (%v allocations), want it skipped", allocs)
	}
	if got := ExitCodeFromToolState(json.RawMessage(` {"exit_code":3}`)); got == nil || *got != 3 {
		t.Fatalf("object state exit code = %v, want 3", got)
	}
}

// A reader that decodes only the tool parts must count exactly what the full
// turn decode counts: nameless results resolved from their call, communicate
// excluded, a fold copy taking its ordinal but counting nothing, and a fork
// cut hiding the inherited prefix while its calls still teach names.
func TestObserveToolPartsAgreesWithObserve(t *testing.T) {
	fold := toolResultTurn(llm.ToolResultData{ToolCallID: "c1", IsError: true})
	ordinal := uint64(2)
	fold.OriginalOrdinal = &ordinal
	turns := []schema.Turn{
		toolCallTurn("c1", "read_file"),
		toolResultTurn(llm.ToolResultData{ToolCallID: "c1", IsError: true}),
		fold,
		toolCallTurn("c2", "communicate"),
		toolResultTurn(llm.ToolResultData{ToolCallID: "c2", IsError: true}),
		toolResultTurn(llm.ToolResultData{ToolCallID: "c3", Name: "shell", IsError: true}, llm.ToolResultData{ToolCallID: "c4", Name: "shell"}),
	}
	for _, from := range []int{0, 1, 3, 6} {
		full := NewFailureCounter(from)
		narrow := NewFailureCounter(from)
		for _, turn := range turns {
			full.Observe(turn)
			raw, err := json.Marshal(turn.Message.Content)
			if err != nil {
				t.Fatal(err)
			}
			var parts []ToolPart
			if err := json.Unmarshal(raw, &parts); err != nil {
				t.Fatal(err)
			}
			narrow.ObserveToolParts(turn.OriginalOrdinal != nil, parts)
		}
		if full.Count() != narrow.Count() {
			t.Fatalf("from %d: narrow count %d, full count %d", from, narrow.Count(), full.Count())
		}
	}
}
