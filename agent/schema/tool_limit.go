package schema

// TruncationStrategy selects how tool output exceeding a limit is shortened.
type TruncationStrategy string

const (
	// TruncHeadTail keeps the head and tail of the output, removing the middle.
	TruncHeadTail TruncationStrategy = "head_tail"
	// TruncTail keeps the last portion of the output, removing the start.
	TruncTail TruncationStrategy = "tail"
	// TruncHeadCount keeps the first MaxLines entries (one per line, as
	// produced by glob/grep) and appends a structural summary stating the
	// total match count, the shown count, and a hint to narrow the search —
	// unlike TruncTail, it never silently drops the head of the result.
	TruncHeadCount TruncationStrategy = "head_count"
)

// ToolOutputLimit specifies the character and line bounds, and the truncation
// strategy, applied to a tool's output before it is sent to the model.
type ToolOutputLimit struct {
	MaxChars int                `json:"max_chars,omitempty"` // max output characters (0 = no char limit)
	MaxLines int                `json:"max_lines,omitempty"` // max output lines (0 = no line limit)
	Strategy TruncationStrategy `json:"strategy,omitempty"`  // how to trim when a limit is exceeded
}
