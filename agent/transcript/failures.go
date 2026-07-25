package transcript

import (
	"encoding/json"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// ShellToolNames are the tool names whose result carries a process exit code
// that the transcript reads as failure. It mirrors the transcript renderer's
// own shell descriptor (cmd/serf-hub/frontend/src/panes/session/transcript/
// tools/shellTool.tsx's registerToolRenderer match), and the two lists have to
// agree: a name in one and not the other means a row wearing a failure glyph
// that the session's failure count does not include, or the reverse.
var ShellToolNames = []string{"shell", "exec_command", "run_shell_command"}

// IsShellTool reports whether a tool's result carries a process exit code.
func IsShellTool(name string) bool {
	for _, candidate := range ShellToolNames {
		if name == candidate {
			return true
		}
	}
	return false
}

// ExitCodeFromToolState reads the process exit code a shell tool records in its
// opaque tool state. Absent state, unreadable state, or state without the field
// all report nil — "no exit code recorded", which is not a failure.
func ExitCodeFromToolState(raw json.RawMessage) *int64 {
	if len(raw) == 0 {
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
//   - a SHELL call that ran and exited nonzero. That is a clean tool result
//     (IsError false) whose command failed, and the renderer marks it anyway.
//     On the session measured in kata hw2n, counting only IsError would report
//     1 against 6 visible glyphs.
//
// communicate results are excluded: ProjectTurn drops them, so they render no
// row and wear no glyph.
//
// name is the RESOLVED tool name — a result whose own record omits it takes the
// name from the call that announced it, the way ProjectTurn resolves it.
func FailedToolResult(name string, isError bool, toolState json.RawMessage) bool {
	if name == "communicate" {
		return false
	}
	if isError {
		return true
	}
	if !IsShellTool(name) {
		return false
	}
	exitCode := ExitCodeFromToolState(toolState)
	return exitCode != nil && *exitCode != 0
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
// turn announces and counts the failures the turn settles.
func (c *FailureCounter) Observe(turn schema.Turn) {
	if c == nil {
		return
	}
	c.ordinal++
	counting := c.ordinal >= c.from
	for _, part := range turn.Message.Content {
		switch {
		case part.Kind == llm.ContentToolCall && part.ToolCall != nil:
			c.toolNames[part.ToolCall.ID] = part.ToolCall.Name
		case part.Kind == llm.ContentToolResult && part.ToolResult != nil:
			if !counting {
				continue
			}
			name := part.ToolResult.Name
			if name == "" {
				name = c.toolNames[part.ToolResult.ToolCallID]
			}
			if FailedToolResult(name, part.ToolResult.IsError, part.ToolResult.ToolState) {
				c.count++
			}
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
