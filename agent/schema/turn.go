package schema

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
	// TurnModelSwitch is a turn carrying a persisted marker for a successful
	// mid-session model switch. Presentational only: rendered as a
	// systemMessage item by both projection paths, and excluded from
	// expandHistory (never sent to the model).
	TurnModelSwitch TurnKind = "MODEL_SWITCH"
	// TurnFailure is a turn recording that the turn in progress failed
	// terminally — the diagnostic rides Turn.Error. Presentational only: like
	// TurnModelSwitch it is rendered as a systemMessage item by both
	// projection paths and excluded from expandHistory (never sent to the
	// model). Without it a failure existed only as a live event, so a reload
	// showed the prompt and no answer and read as a hang rather than a break.
	TurnFailure TurnKind = "TURN_FAILURE"
)

// TurnFailureInfo is the persisted diagnostic of a failed turn: the same
// message/source/title/hint/cause a live client receives on the error event,
// so a reloaded transcript describes the failure exactly as the live session
// did rather than in a poorer summary of it.
type TurnFailureInfo struct {
	Message string `json:"message"`
	Source  string `json:"source,omitempty"`
	Title   string `json:"title,omitempty"`
	Hint    string `json:"hint,omitempty"`
	// Cause is an optional structured classifier so readers can typed-branch
	// instead of substring-matching Message. Nil means the failure source is
	// unknown.
	Cause *TurnFailureCause `json:"cause,omitempty"`
}

// TurnFailureCause is the structured root cause of a failed turn. It mirrors
// the event-side cause deliberately rather than reusing it: this shape is
// persisted transcript data, and must stay stable independently of the event
// payload's evolution.
type TurnFailureCause struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   int    `json:"status,omitempty"`
}

// Turn is the Session's typed history item. Steering turns are kept distinct for observability,
// but are converted to user-role messages when building the LLM request.
type Turn struct {
	Kind      TurnKind    `json:"kind"`      // category of this history item
	Message   llm.Message `json:"message"`   // the underlying LLM message
	Timestamp time.Time   `json:"timestamp"` // when the turn was recorded (UTC)
	// Usage carries the token-usage stats reported by the provider; set only on
	// assistant turns.
	Usage llm.Usage `json:"usage,omitempty"`
	// SteeringSource records the provenance of a TurnSteering entry:
	// "user" for human-sent steering (the UI steer action or queued user
	// input drained as steering), empty for daemon/system nudges. Persisted
	// so replay/hydration can render user steering as user speech
	// (issue #24). Empty on non-steering turns.
	SteeringSource string `json:"steering_source,omitempty"`
	// Error carries the diagnostic of a terminally failed turn. Set only on
	// TurnFailure turns; nil everywhere else.
	Error *TurnFailureInfo `json:"error,omitempty"`
	// ResponseID is the provider's response identifier (from llm.Response.ID),
	// recorded on assistant turns and surfaced in ATIF trajectory export.
	ResponseID                      string `json:"response_id,omitempty"`
	ResponseIDHash                  string `json:"response_id_hash,omitempty"`
	ResponseProvider                string `json:"response_provider,omitempty"`
	ResponseModel                   string `json:"response_model,omitempty"`
	ResponseRequestModel            string `json:"response_request_model,omitempty"`
	AttemptGroupID                  string `json:"attempt_group_id,omitempty"`
	ResponseEndpointFamily          string `json:"response_endpoint_family,omitempty"`
	ResponseEndpoint                string `json:"response_endpoint,omitempty"`
	ResponseStorageScopeFingerprint string `json:"response_storage_scope_fingerprint,omitempty"`
	ResponseRequestFingerprint      string `json:"response_request_fingerprint,omitempty"`
	ResponseContextMarker           string `json:"response_context_marker,omitempty"`
}

// NewTurn creates a Turn with the current UTC time.
func NewTurn(kind TurnKind, msg llm.Message) Turn {
	return Turn{Kind: kind, Message: msg, Timestamp: time.Now().UTC()}
}
