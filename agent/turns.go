package agent

import (
	"time"

	"primeradiant.com/serf/llm"
)

type TurnKind string

const (
	TurnUserInput   TurnKind = "USER_INPUT"
	TurnSteering    TurnKind = "STEERING"
	TurnAssistant   TurnKind = "ASSISTANT"
	TurnTool        TurnKind = "TOOL"         // Deprecated: use TurnToolResults for new code.
	TurnToolResults TurnKind = "TOOL_RESULTS" // Aggregated tool results from one round.
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
