// Package transcript owns the shared transcript currency for the TUI: the
// chat-message and tool-call value types, the live reducer that folds appwire
// thread events into a message list, and the helpers that build messages from
// thread items. It depends only on appwire + toolsummary + hubdiagnostics.
package transcript

import "time"

type MessageKind int

const (
	MsgUser        MessageKind = iota
	MsgAssistant               // agent's streamed preamble/narration text
	MsgCommunicate             // agent's communicate output (the actual response)
	MsgTool
	MsgSystem
	MsgSteering  // user-initiated steering placeholder + authoritative steering chip
	MsgReasoning // the model's reasoning summary ("thinking"), streamed live
)

type SubagentRunInfo struct {
	DelegateID       string
	JobID            string
	JobType          string
	Status           string
	Reason           string
	Background       bool
	Command          string
	Task             string
	TranscriptRef    string
	OriginTurnID     string
	OriginToolCallID string
	OriginItemID     string
	OutputBytes      int64
}

type ToolCallInfo struct {
	Name        string
	Description string // compact one-liner header
	Detail      string // rich multi-line body shown when expanded
	RawArgs     string // raw JSON arguments string; preferred over Description for arg parsing
	Raw         string // raw JSON metadata from appwire ThreadItem.Raw
	Output      string
	Error       string
	Duration    time.Duration
	Expanded    bool
	Done        bool
	Hidden      bool // suppress from display (e.g. communicate)
	Subagent    *SubagentRunInfo
}

const ToolCollapseThreshold = 5

type ChatMessage struct {
	Kind       MessageKind
	Text       string
	TurnID     string
	TurnIndex  int
	ItemID     string
	ToolCallID string
	Tool       *ToolCallInfo

	// PendingID is non-zero when this message is an optimistic placeholder
	// created in response to a user click before the authoritative event
	// arrives. It matches the PendingEntry.ID from the pending coordinator (pendingpkg).
	PendingID int64
	// Done marks a MsgReasoning thought whose turn has moved on (the agent began
	// answering or the turn completed), so the renderer collapses it to a
	// one-line gist instead of the whole streaming block.
	Done bool
	// Expanded re-opens a Done MsgReasoning thought to its whole block (the
	// ctrl+t browse toggle), overriding the default collapsed gist.
	Expanded bool
	// Pending is true while the optimistic call is in flight. The renderer
	// prefixes the row with a spinner glyph and dims the color while true.
	Pending bool
	// Failed is true if the optimistic call rejected or timed out without
	// reconciling. Mutually exclusive with Pending. Renderer shows a red
	// ✗ prefix and the Reason.
	Failed bool
	// Reason is the failure message when Failed is true.
	Reason string
}
