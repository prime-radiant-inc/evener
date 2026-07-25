package transcript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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
	if !FailedToolResult("read_file", true, nil) {
		t.Fatal("FailedToolResult(read_file, isError=true) = false, want true")
	}
}

func TestFailedToolResultCountsAShellCallThatExitedNonzero(t *testing.T) {
	// The glyph marks this row even though the tool result itself is clean, so
	// the count has to as well: on kata hw2n's session, counting only IsError
	// reported 1 against 6 visible marks.
	if !FailedToolResult("shell", false, exitState(2)) {
		t.Fatal("FailedToolResult(shell, exit=2) = false, want true")
	}
}

func TestFailedToolResultIgnoresAShellCallThatSucceeded(t *testing.T) {
	if FailedToolResult("shell", false, exitState(0)) {
		t.Fatal("FailedToolResult(shell, exit=0) = true, want false")
	}
}

func TestFailedToolResultIgnoresAnExitCodeOnANonShellTool(t *testing.T) {
	if FailedToolResult("read_file", false, exitState(2)) {
		t.Fatal("FailedToolResult(read_file, exit=2) = true, want false: only shell results carry a process exit")
	}
}

func TestFailedToolResultIgnoresCommunicate(t *testing.T) {
	// ProjectTurn drops communicate results, so they render no row and wear no
	// glyph. Counting one would put the figure a mark ahead of the transcript.
	if FailedToolResult("communicate", true, nil) {
		t.Fatal("FailedToolResult(communicate, isError=true) = true, want false")
	}
}

func TestFailureCounterCountsEveryFailureItObserves(t *testing.T) {
	c := NewFailureCounter(0)
	c.Observe(toolCallTurn("call_1", "shell"))
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "shell", ToolState: exitState(1)}))
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_2", Name: "read_file", IsError: true}))
	if got := c.Count(); got != 2 {
		t.Fatalf("Count() = %d, want 2", got)
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
	// shell result reads as a non-shell tool and its nonzero exit goes uncounted.
	c := NewFailureCounter(0)
	c.Observe(toolCallTurn("call_1", "shell"))
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", ToolState: exitState(3)}))
	if got := c.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1", got)
	}
}

func TestFailureCounterSkipsAnInheritedForkPrefix(t *testing.T) {
	// A fork child's transcript opens with a verbatim copy of the parent's
	// prefix, whose failures the PARENT made. Counting from the divergence
	// ordinal is the same attribution rule the token sum applies.
	c := NewFailureCounter(3)
	c.Observe(toolCallTurn("call_1", "shell"))                                                                  // ordinal 1, parent's
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", ToolState: exitState(1)}))                // ordinal 2, parent's
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "shell", ToolState: exitState(1)})) // ordinal 3, the child's own
	if got := c.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1 (the parent's failure is not the child's)", got)
	}
}

func TestFailureCounterResolvesNamesFromBeforeTheDivergenceCut(t *testing.T) {
	// The name map is filled from EVERY entry, including ones before the cut: a
	// child's own result can answer a call the inherited prefix announced.
	c := NewFailureCounter(2)
	c.Observe(toolCallTurn("call_1", "shell"))
	c.Observe(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", ToolState: exitState(1)}))
	if got := c.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1", got)
	}
}
