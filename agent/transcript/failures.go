package transcript

import (
	"bytes"
	"encoding/json"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// ShellToolNames are the tool names whose result carries a process exit code.
// It exists for the failure-rule fuzz oracle, which draws tool names from it.
// It is NOT the list of tools whose failures count: a nonzero exit is not a
// failure (Jesse, 2026-10-16); only a result carrying an error counts (see
// FailedToolResult). The clients' own shell-family list lives in
// @evener/appwire-client's toolSummaries (toolFamily + toolExitCode).
var ShellToolNames = []string{"shell", "exec_command", "run_shell_command"}

// ExitCodeFromToolState reads the process exit code a shell tool records in its
// opaque tool state. Absent state, unreadable state, or state without the field
// all report nil — "no exit code recorded", which is not a failure.
func ExitCodeFromToolState(raw json.RawMessage) *int64 {
	// Only an object can hold the field. Other state (task_list's is a large
	// array) would cost a whole decode just to fail.
	if trimmed := bytes.TrimLeft(raw, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var v struct {
		ExitCode *int64 `json:"exit_code"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v.ExitCode
}

// FailedToolResult is the whole failure rule, in one place so every count and
// the glyphs the transcript draws cannot drift apart. It lives here, in the
// package that owns the on-disk record, because two modules ask the question:
// the daemon counts failures as it writes them (Writer.TrackFailures) and the
// hub counts them by reading a finished transcript back
// (internal/apptranscript's FailedToolCallsFromFile). A rule in only one of
// them is a rule that will disagree with itself.
//
// WHAT COUNTS is exactly what the transcript marks with a --danger glyph:
//
//   - a tool result carrying an error (llm.ToolResultData.IsError — the wire's
//     ThreadItem.Error, and the "failed" status SettledToolStatus stamps);
//
// A SHELL call that ran and exited nonzero is NOT one of them (Jesse,
// 2026-10-16). A nonzero exit is common and expected, so the transcript shows
// it as a small inline mark on the call, never as a failure — and a count of
// failures must match the glyphs, not outrun them. Only a result carrying an
// error reads as failed, on any tool.
//
// communicate results are excluded: ProjectTurn drops them, so they render no
// row and wear no glyph.
//
// name is the RESOLVED tool name — a result whose own record omits it takes the
// name from the call that announced it, the way ProjectTurn resolves it.
func FailedToolResult(name string, isError bool) bool {
	if name == "communicate" {
		return false
	}
	return isError
}

// FailureCounter accumulates a session's failure count over the turns it is
// shown, in the order the transcript records them.
//
// It is stateful because the rule is: a tool call announces a name, and the
// result that answers it may omit its own. Resolving that needs the calls seen
// so far, which no single turn carries.
//
// It counts forward only, so a live session's figure is never a window: seed it
// with the entries already on disk, then show it every turn as it is appended,
// and the total covers the whole session for as long as the session lasts. That
// is why the count is not re-derived from the session's in-memory history —
// compaction rewrites that history, and a count over it would silently shed the
// failures it summarized away.
type FailureCounter struct {
	// toolNames resolves a result whose own record omits its name, mirroring
	// ProjectTurn's map of the same name. It is filled from EVERY turn,
	// including ones before the divergence cut: a fork child's own result can
	// answer a call the inherited prefix announced.
	toolNames map[string]string
	count     int
	ordinal   int
	from      int
}

// NewFailureCounter returns a counter that begins counting at fromEntryOrdinal,
// the 1-based entry ordinal at which the session's OWN history begins — a fork
// child's schema.SessionMeta.DivergenceTurn. A child transcript opens with a
// verbatim copy of the parent's prefix, and those failures were the parent's;
// charging them to the child is the attribution bug the token sum's identical
// bound exists to prevent. Pass 0 (or 1) for a session that inherited nothing.
func NewFailureCounter(fromEntryOrdinal int) *FailureCounter {
	return &FailureCounter{toolNames: map[string]string{}, from: fromEntryOrdinal}
}

// Observe records one transcript entry's turn: it learns the tool names the
// turn announces and counts the failures the turn settles. A compaction fold
// copy is re-appended after its original, so it takes its ordinal but settles
// nothing — its original already counted.
func (c *FailureCounter) Observe(turn schema.Turn) {
	if c == nil {
		return
	}
	c.observe(turn, nil)
}

// ObserveEntry is Observe for a caller that must learn whether a nameless tool
// result's name was resolvable from the calls seen so far. It reports true when
// one was not: a counter restored from persisted state (the transcript-index
// builder's extension path) then cannot count such a result the same way a full
// scan does, and must not present a figure it could not compute.
func (c *FailureCounter) ObserveEntry(turn schema.Turn) (unresolved bool) {
	if c == nil {
		return false
	}
	c.observe(turn, &unresolved)
	return unresolved
}

func (c *FailureCounter) observe(turn schema.Turn, unresolved *bool) {
	c.ordinal++
	if turn.OriginalOrdinal != nil {
		return // a compaction fold copy: its original already counted
	}
	counting := c.ordinal >= c.from
	for _, part := range turn.Message.Content {
		switch {
		case part.Kind == llm.ContentToolCall && part.ToolCall != nil:
			c.toolNames[part.ToolCall.ID] = part.ToolCall.Name
		case part.Kind == llm.ContentToolResult && part.ToolResult != nil:
			c.settle(counting, part.ToolResult.ToolCallID, part.ToolResult.Name, part.ToolResult.IsError, unresolved)
		}
	}
}

// settle counts one tool result when it is inside the session's own span and
// fails the rule, resolving a nameless result from the calls seen so far.
func (c *FailureCounter) settle(counting bool, toolCallID, name string, isError bool, unresolved *bool) {
	if !counting {
		return
	}
	if name == "" {
		resolved, ok := c.toolNames[toolCallID]
		if !ok && unresolved != nil {
			*unresolved = true
		}
		name = resolved
	}
	if FailedToolResult(name, isError) {
		c.count++
	}
}

// ToolPart is the narrow decode of a message content part that the failure
// rule needs: only the tool calls and tool results. A reader that scans a
// whole transcript decodes this instead of llm.ContentPart so it never pays
// for inline image bytes.
type ToolPart struct {
	Kind     llm.ContentKind `json:"kind"`
	ToolCall *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"tool_call"`
	ToolResult *struct {
		ToolCallID string `json:"tool_call_id"`
		Name       string `json:"name"`
		IsError    bool   `json:"is_error"`
	} `json:"tool_result"`
}

// ObserveToolParts is Observe for a reader that decoded only the tool parts of
// an entry. foldCopy is whether the turn carries an original ordinal. Every
// entry must be shown, folded or not, so the counter's ordinal stays in step
// with the transcript's.
func (c *FailureCounter) ObserveToolParts(foldCopy bool, parts []ToolPart) {
	if c == nil {
		return
	}
	c.ordinal++
	if foldCopy {
		return
	}
	counting := c.ordinal >= c.from
	for _, part := range parts {
		switch {
		case part.ToolCall != nil && part.Kind == llm.ContentToolCall:
			c.toolNames[part.ToolCall.ID] = part.ToolCall.Name
		case part.ToolResult != nil && part.Kind == llm.ContentToolResult:
			c.settle(counting, part.ToolResult.ToolCallID, part.ToolResult.Name, part.ToolResult.IsError, nil)
		}
	}
}

// Count is how many failures the counter has seen.
func (c *FailureCounter) Count() int {
	if c == nil {
		return 0
	}
	return c.count
}

// Reset drops the names and count the counter has learned. A caller that has
// finished consuming it — the transcript-index builder at the end of a build —
// releases the per-call state so a long-lived index does not retain one entry
// per tool call of the whole session. A later extension then resolves only the
// calls it sees itself, and reports a nameless result it cannot resolve.
func (c *FailureCounter) Reset() {
	if c == nil {
		return
	}
	c.toolNames = map[string]string{}
	c.count = 0
}

// Len is how many tool calls the counter currently retains names for. A caller
// that must bound memory — the transcript-index builder — uses it to show the
// state is released once a build no longer needs it.
func (c *FailureCounter) Len() int {
	if c == nil {
		return 0
	}
	return len(c.toolNames)
}
