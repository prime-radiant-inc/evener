package agent

import (
	"time"

	"primeradiant.com/serf/llm"
)

// TurnKind identifies the category of a Turn in the Session history.
type TurnKind string

const (
	// TurnUserInput is a turn carrying input from the user.
	TurnUserInput TurnKind = "USER_INPUT"
	// TurnSteering is a turn carrying steering input from the user.
	TurnSteering TurnKind = "STEERING"
	// TurnAssistant is a turn carrying an assistant message.
	TurnAssistant TurnKind = "ASSISTANT"
	// TurnTool is a turn carrying tool output.
	TurnTool TurnKind = "TOOL" // Deprecated: use TurnToolResults for new code.
	// TurnToolResults is a turn carrying aggregated tool results from one round.
	TurnToolResults TurnKind = "TOOL_RESULTS" // Aggregated tool results from one round.
	// TurnSystem is a turn carrying a system message.
	TurnSystem TurnKind = "SYSTEM"
	// TurnCheckpoint is a turn carrying a deterministic checkpoint from compaction Layer 3.
	TurnCheckpoint TurnKind = "CHECKPOINT" // Deterministic checkpoint from compaction Layer 3.
	// TurnSummary is a turn carrying an LLM-generated summary from compaction Layer 4.
	TurnSummary TurnKind = "SUMMARY" // LLM-generated summary from compaction Layer 4.
)

// Turn is the Session's typed history item. Steering turns are kept distinct for observability,
// but are converted to user-role messages when building the LLM request.
type Turn struct {
	Kind       TurnKind    `json:"kind"`
	Message    llm.Message `json:"message"`
	Timestamp  time.Time   `json:"timestamp"`
	Usage      llm.Usage   `json:"usage,omitempty"`
	ResponseID string      `json:"response_id,omitempty"`
}

// NewTurn creates a Turn with the current UTC time.
func NewTurn(kind TurnKind, msg llm.Message) Turn {
	return Turn{Kind: kind, Message: msg, Timestamp: time.Now().UTC()}
}
