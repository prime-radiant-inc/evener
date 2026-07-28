package events

import (
	"encoding/json"

	"primeradiant.com/serf/llm"
)

// Typed event payload structs. JSON tags match the map keys used previously.

// SessionStartData is the payload for an EventSessionStart event.
type SessionStartData struct {
	Profile           string `json:"profile"`
	Model             string `json:"model"`
	Restored          bool   `json:"restored,omitempty"`
	Turns             int    `json:"turns,omitempty"`
	LastInputTokens   int    `json:"last_input_tokens,omitempty"`
	ContextWindowSize int    `json:"context_window_size,omitempty"`
	// TranscriptEntries is the number of entries already persisted in the
	// transcript at the moment this event fires (0 for a fresh session, whose
	// transcript is empty at SessionStart). A live-event consumer that also
	// projects turn ids from a cold read of the same transcript (kata eptj:
	// internal/apptranscript numbers turn_N by entry index) must seed its own
	// turn counter at or above this value on Restored, or its first live turn
	// mints an id the reload path already assigned to a persisted entry.
	TranscriptEntries int `json:"transcript_entries,omitempty"`
	// State is the session's state at the moment SessionStart fires, carried
	// so a restored session's re-derived state (a completed, unanswered
	// ask_user call at the transcript tail rests "awaiting"; see
	// agent.SessionAwaiting) reaches the wire instead of an assumed idle. A
	// fresh session always starts idle and leaves this empty; downstream
	// consumers already default an empty/unrecognized value to idle.
	State string `json:"state,omitempty"`
}

// SessionEndData is the payload for an EventSessionEnd event.
type SessionEndData struct {
	Reason string `json:"reason"`
	State  string `json:"state,omitempty"`
	Turns  int    `json:"turns,omitempty"`
	// Interrupted is true when the input was aborted via the interrupt
	// signal (POST /interrupt or TurnInterrupt RPC). When set, the
	// session remains alive (State="idle") but the active turn was cut
	// short; the appwire projection maps this to a "canceled" turn
	// status while the thread status stays idle.
	Interrupted bool `json:"interrupted,omitempty"`
}

// UserInputData is the payload for an EventUserInput event.
type UserInputData struct {
	Text             string           `json:"text"`
	Images           []UserInputImage `json:"images,omitempty"`
	ClientMutationID string           `json:"client_mutation_id,omitempty"`
	StableTurnID     string           `json:"stable_turn_id,omitempty"`
	// Turn is the 1-based transcript entry index for this USER_INPUT turn.
	// The hub renderer uses it for turn-targeted operations such as fork.
	Turn int `json:"turn,omitempty"`
}

// UserInputImage carries enough metadata for the UI to render a thumbnail.
// MediaType is e.g. "image/png"; Data is the raw bytes (JSON un/marshals as
// base64); Name is the original filename when known.
type UserInputImage struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"data,omitempty"`
	Name      string `json:"name,omitempty"`
}

// AssistantTextStartData is the payload for an EventAssistantTextStart event.
type AssistantTextStartData struct {
	Model string `json:"model"`
}

// AssistantTextDeltaData is the payload for an EventAssistantTextDelta event.
type AssistantTextDeltaData struct {
	Delta string `json:"delta"`
}

// AssistantTextResetData is the payload for an EventAssistantTextReset event.
// It carries no fields: the reset always targets the active turn's in-progress
// assistant message, which consumers already track.
type AssistantTextResetData struct{}

// ReasoningSummaryDeltaData is the payload for an EventReasoningSummaryDelta
// event: an incremental chunk of the model's reasoning summary. SummaryIndex
// increments when the model opens a new reasoning section.
type ReasoningSummaryDeltaData struct {
	Delta        string `json:"delta"`
	SummaryIndex int    `json:"summary_index"`
}

// AssistantTextEndData is the payload for an EventAssistantTextEnd event.
type AssistantTextEndData struct {
	Text         string    `json:"text"`
	Usage        llm.Usage `json:"usage"`
	FinishReason string    `json:"finish_reason"`
	Model        string    `json:"model"`
	Reasoning    string    `json:"reasoning,omitempty"`
}

// ToolCallStartData is the payload for an EventToolCallStart event.
type ToolCallStartData struct {
	ToolName      string `json:"tool_name"`
	CallID        string `json:"call_id"`
	ArgumentsJSON string `json:"arguments_json,omitempty"`
	Description   string `json:"description,omitempty"`
}

// ToolCallOutputDeltaData is the payload for an EventToolCallOutputDelta event.
type ToolCallOutputDeltaData struct {
	ToolName string `json:"tool_name"`
	CallID   string `json:"call_id"`
	Delta    string `json:"delta"`
}

// OutputImage is a lightweight descriptor for an image produced by a tool. It
// carries placement and fetch metadata for dashboards; it never carries image
// bytes.
type OutputImage struct {
	Source    string `json:"source"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Size      int64  `json:"size,omitempty"`
	URL       string `json:"url,omitempty"`
	SHA       string `json:"sha,omitempty"`
	Path      string `json:"path,omitempty"`
}

// ToolCallEndData is the payload for an EventToolCallEnd event.
type ToolCallEndData struct {
	ToolName      string        `json:"tool_name"`
	CallID        string        `json:"call_id"`
	ArgumentsJSON string        `json:"arguments_json,omitempty"`
	Output        string        `json:"output,omitempty"`
	Error         string        `json:"error,omitempty"`
	OutputImages  []OutputImage `json:"output_images,omitempty"`
	// PrevalOnly is true when Error came from prepareToolCall's pre-dispatch
	// repair step (an unknown tool name, or arguments that still fail schema
	// validation after repair) and the tool's own ExecuteCall never ran (kata
	// hgm1). It is the wire-level fact a client needs to tell "the model's
	// call itself was malformed, and nothing downstream of the model - a
	// human answering ask_user, a command touching the filesystem - ever
	// ran" apart from "the tool ran and its own work failed or was denied":
	// same Error-is-non-empty shape, very different meaning to a reader.
	PrevalOnly bool `json:"preval_only,omitempty"`

	// ToolState is an optional JSON snapshot produced by the tool
	// executor. Dashboards and other consumers render from this directly
	// rather than reconstructing state by replaying the event stream.
	// The LLM never sees this field.
	ToolState json.RawMessage `json:"tool_state,omitempty"`
}

// ToolCallRepairedData reports the repairs applied to a tool call's arguments.
// Each entry in Changes is encoded "kind:field:detail".
type ToolCallRepairedData struct {
	ToolName string   `json:"tool_name"`
	CallID   string   `json:"call_id"`
	Changes  []string `json:"changes"`
}

// SandboxEscalationRequestedData is the payload for an
// EventSandboxEscalationRequested event (M7): a harness-raised, human-gated
// sandbox-exemption approval request. Its fields mirror sandbox.EscalationRequest
// but stay in the events package (plain values) so the projector can map it to the
// AppWire notification without importing agent/sandbox. DeniedPath carries the FULL
// literal path for informed consent — only non-sensitive (containment) denials ever
// escalate, so the full path is safe and necessary; a sensitive path (which never
// escalates) degrades to "<denied>" as a defensive floor. File contents never appear.
type SandboxEscalationRequestedData struct {
	EscalationID string `json:"escalation_id"`
	Mode         string `json:"mode"`
	Tool         string `json:"tool"`
	Kind         string `json:"kind"`
	DeniedPath   string `json:"denied_path"`
	Command      string `json:"command,omitempty"`
	OutputSoFar  string `json:"output_so_far,omitempty"`
	PartiallyRan bool   `json:"partially_ran,omitempty"`
}

// SandboxEscalationResolvedData is the payload for an
// EventSandboxEscalationResolved event (M7, wire-honesty spec Part B): the
// escalation named by EscalationID left the pending set, by explicit resolve,
// turn-interrupt, or session close. It carries no decision (reason/approved) —
// the sole consumer clears its card by id identically regardless of outcome,
// and the producer (the convergence point in escalateOnSandboxDenial) cannot
// reliably distinguish close-cancel from interrupt anyway. Additive later if a
// "resolved elsewhere" toast ever wants more.
type SandboxEscalationResolvedData struct {
	EscalationID string `json:"escalation_id"`
}

// SteeringSourceUser marks steering that originated from the human user —
// the UI steer action, or queued user input drained as steering — as opposed
// to daemon/system-generated nudges (task reminders, loop detection, hook
// context, job notifications, …). UIs render user-sourced steering as a user
// message rather than a system steering divider (issue #24).
const SteeringSourceUser = "user"

// Steering kinds name what the daemon injected, set at the injection site and
// carried to the UI so a label is ground truth rather than a guess at the
// message's prose. Absent kind means "unknown" and the UI claims nothing.
const (
	SteeringKindInterrupted       = "interrupted"
	SteeringKindAgentMessage      = "agent-message"
	SteeringKindHookContext       = "hook-context"
	SteeringKindPrecompactHook    = "precompact-hook"
	SteeringKindCompactNudge      = "compact-nudge"
	SteeringKindImageDescription  = "image-description"
	SteeringKindNoToolCalls       = "no-tool-calls"
	SteeringKindLoopDetected      = "loop-detected"
	SteeringKindTasksDone         = "tasks-done"
	SteeringKindTaskNudge         = "task-nudge"
	SteeringKindTaskInactive      = "task-inactive"
	SteeringKindNoteHandoff       = "note-handoff"
	SteeringKindGoalObjective     = "goal-objective"
	SteeringKindTranscriptPointer = "transcript-pointer"
	SteeringKindCurrentTask       = "current-task"
	SteeringKindTaskList          = "task-list"
	SteeringKindNotification      = "notification"
)

// AllSteeringKinds is every kind a call site may emit. Task 3's coverage test
// asserts each one is produced somewhere; a kind that stops being emitted
// fails that test rather than going stale unnoticed (the failure mode the
// deleted read-only classifier rule demonstrated).
var AllSteeringKinds = []string{
	SteeringKindInterrupted,
	SteeringKindAgentMessage,
	SteeringKindHookContext,
	SteeringKindPrecompactHook,
	SteeringKindCompactNudge,
	SteeringKindImageDescription,
	SteeringKindNoToolCalls,
	SteeringKindLoopDetected,
	SteeringKindTasksDone,
	SteeringKindTaskNudge,
	SteeringKindTaskInactive,
	SteeringKindNoteHandoff,
	SteeringKindGoalObjective,
	SteeringKindTranscriptPointer,
	SteeringKindCurrentTask,
	SteeringKindTaskList,
	SteeringKindNotification,
}

// SteeringInjectedData is the payload for an EventSteeringInjected event.
type SteeringInjectedData struct {
	Text             string           `json:"text"`
	Images           []UserInputImage `json:"images,omitempty"`
	ClientMutationID string           `json:"client_mutation_id,omitempty"`
	StableTurnID     string           `json:"stable_turn_id,omitempty"`
	// Source carries the steering provenance: SteeringSourceUser for
	// human-sent steering, empty for daemon/system steering. Optional and
	// additive; absent means system.
	Source string `json:"source,omitempty"`
	// Kind names what was injected (events.SteeringKind*). Optional and
	// additive; absent means the daemon did not say, and the UI shows no kind.
	Kind string `json:"kind,omitempty"`
}

// QueueChangedData carries an authoritative snapshot of the per-session
// input queue after a mutation (kata r80p). Preview entries are FIFO with
// the head at index 0 and have been collapsed to a single line. IDs is
// FIFO-aligned with Preview and carries each entry's stable queue-entry id
// (minted at enqueue time) so a client can promote a specific entry by
// identity rather than by bare index (review F1, issue #22). Texts is
// FIFO-aligned with Preview and carries each entry's FULL untruncated text
// so the edit affordance (issue #23) can restore the complete message into
// the composer — the preview line alone would silently truncate multi-line
// messages.
type QueueChangedData struct {
	Depth             int      `json:"depth"`
	Revision          uint64   `json:"revision"`
	Preview           []string `json:"preview,omitempty"`
	IDs               []string `json:"ids,omitempty"`
	ClientMutationIDs []string `json:"client_mutation_ids,omitempty"`
	Texts             []string `json:"texts,omitempty"`
}

// TaskUpdatedData is the payload for an EventTaskUpdated event: the current
// task-list progress after an append or status change, so subscribers refresh
// the task-status row without re-polling.
type TaskUpdatedData struct {
	Total int `json:"total"`
	Done  int `json:"done"`
}

// SessionNameChangedData is the payload for an EventSessionNameChanged event.
type SessionNameChangedData struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
}

// ModelChangedData is the payload for an EventModelChanged event: SetModel
// committed a provider/model switch. OldProvider/OldModel and
// NewProvider/NewModel let subscribers diff the change; ReasoningEffortLevels
// and SupportsReasoning describe the NEW profile so pickers re-key without a
// separate model/list round trip.
type ModelChangedData struct {
	OldProvider           string   `json:"old_provider"`
	OldModel              string   `json:"old_model"`
	NewProvider           string   `json:"new_provider"`
	NewModel              string   `json:"new_model"`
	ReasoningEffortLevels []string `json:"reasoning_effort_levels,omitempty"`
	SupportsReasoning     bool     `json:"supports_reasoning,omitempty"`
	// MarkerText is the persisted switch-marker text ("Switched model: <old>
	// → <new>", plus any warning lines) — the exact text SetModel wrote to
	// the transcript's TurnModelSwitch turn. The live projector renders it
	// verbatim as a systemMessage item so live and replay stay byte-identical.
	MarkerText string `json:"marker_text,omitempty"`
}

// ReasoningEffortChangedData is the payload for an EventReasoningEffortChanged
// event: SetReasoningEffort committed a new effort value (already normalized;
// empty means unset/default).
type ReasoningEffortChangedData struct {
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// TurnLimitData is the payload for an EventTurnLimit event.
type TurnLimitData struct {
	MaxTurns              int `json:"max_turns,omitempty"`
	MaxToolRoundsPerInput int `json:"max_tool_rounds_per_input,omitempty"`
}

// LoopDetectionData is the payload for an EventLoopDetection event.
type LoopDetectionData struct {
	Message string `json:"message"`
}

// CommunicateData is the payload for an EventCommunicate event.
type CommunicateData struct {
	EndTurn bool   `json:"end_turn"`
	Message string `json:"message"`
}

// SkillActivatedData is the payload for an EventSkillActivated event.
type SkillActivatedData struct {
	Name string `json:"name"`
}

// ContextCompactionData is the payload for an EventContextCompaction event.
type ContextCompactionData struct {
	Layer           string `json:"layer,omitempty"`
	TurnsBefore     int    `json:"turns_before,omitempty"`
	TurnsAfter      int    `json:"turns_after,omitempty"`
	EstTokensBefore int    `json:"est_tokens_before,omitempty"`
	EstTokensAfter  int    `json:"est_tokens_after,omitempty"`
}

// CompactionTurnData is the payload for an EventCompactionTurn event.
type CompactionTurnData struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// WarningData is the payload for an EventWarning event.
type WarningData struct {
	Message           string `json:"message"`
	Source            string `json:"source,omitempty"`
	Title             string `json:"title,omitempty"`
	Hint              string `json:"hint,omitempty"`
	ApproxTokens      int    `json:"approx_tokens,omitempty"`
	ContextWindowSize int    `json:"context_window_size,omitempty"`
	Percent           int    `json:"percent,omitempty"`
	// PluginName names the plugin a hook-configuration warning is about (empty
	// for warnings unrelated to plugin hooks). Carries only the plugin name, no
	// hook payload or secrets.
	PluginName string `json:"plugin_name,omitempty"`
	// EventName is the offending hook event name (or, for an invalid matcher,
	// the event the matcher was declared under) for a hook-configuration
	// warning. Carries only the name, no hook payload or secrets.
	EventName string `json:"event_name,omitempty"`
}

// ErrorData is the payload for an EventError event.
type ErrorData struct {
	Error  string `json:"error"`
	Source string `json:"source,omitempty"`
	Title  string `json:"title,omitempty"`
	Hint   string `json:"hint,omitempty"`
	// Cause is an optional structured classifier. Today only provider
	// failures populate it; consumers can typed-branch on Cause.Kind
	// instead of substring-matching the Error message (kata ts0x). A nil
	// Cause means the failure source is unknown (back-compat default).
	Cause *ErrorCause `json:"cause,omitempty"`
}

// ErrorCause describes a structured root cause for an EventError. Today the
// only Kind is "provider" (HTTP failure from an LLM adapter); additional
// kinds can be added as further consumers need typed dispatch (kata ts0x).
type ErrorCause struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   int    `json:"status,omitempty"`
}

// JobStartedData is the payload for an EventJobStarted event.
type JobStartedData struct {
	JobID            string `json:"job_id"`
	JobType          string `json:"job_type"`
	Status           string `json:"status"`
	FromWatch        bool   `json:"from_watch,omitempty"`
	Background       bool   `json:"background,omitempty"`
	Command          string `json:"command,omitempty"`
	DelegateID       string `json:"delegate_id,omitempty"`
	Task             string `json:"task,omitempty"`
	TranscriptRef    string `json:"transcript_ref,omitempty"`
	OriginTurnID     string `json:"origin_turn_id,omitempty"`
	OriginToolCallID string `json:"origin_tool_call_id,omitempty"`
	OriginItemID     string `json:"origin_item_id,omitempty"`
}

// JobFinishedData is the payload for an EventJobFinished event.
type JobFinishedData struct {
	JobID            string `json:"job_id"`
	JobType          string `json:"job_type"`
	Status           string `json:"status"`
	Reason           string `json:"reason"`
	ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
	Resumable        *bool  `json:"resumable,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	OutputBytes      int64  `json:"output_bytes"`
	TranscriptRef    string `json:"transcript_ref,omitempty"`
	FromWatch        bool   `json:"from_watch,omitempty"`
	Background       bool   `json:"background,omitempty"`
	Command          string `json:"command,omitempty"`
	DelegateID       string `json:"delegate_id,omitempty"`
	Task             string `json:"task,omitempty"`
	OriginTurnID     string `json:"origin_turn_id,omitempty"`
	OriginToolCallID string `json:"origin_tool_call_id,omitempty"`
	OriginItemID     string `json:"origin_item_id,omitempty"`
}

// PluginLoadedData is the payload for an EventPluginLoaded event.
type PluginLoadedData struct {
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	SkillCount int    `json:"skill_count"`
	AgentCount int    `json:"agent_count"`
	MCPCount   int    `json:"mcp_count"`
	// ManifestFlavor is which manifest directory the plugin loaded from:
	// "claude" (.claude-plugin) or "codex" (.codex-plugin).
	ManifestFlavor string `json:"manifest_flavor,omitempty"`
	// ManifestPath is the absolute path of the loaded plugin.json.
	ManifestPath string `json:"manifest_path,omitempty"`
}

// HookStartData is the payload for an EventHookStart event.
type HookStartData struct {
	Event      string `json:"event"`
	HookType   string `json:"hook_type"`
	Matcher    string `json:"matcher"`
	PluginName string `json:"plugin_name"`
}

// HookEndData is the payload for an EventHookEnd event.
type HookEndData struct {
	Event      string `json:"event"`
	HookType   string `json:"hook_type"`
	Matcher    string `json:"matcher"`
	PluginName string `json:"plugin_name"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
}

// ForkSummaryData is the payload for an EventForkSummary event.
type ForkSummaryData struct {
	Turn int `json:"turn"`
}

// PromptLoadedData is the payload for an EventPromptLoaded event.
type PromptLoadedData struct {
	Label string `json:"label"`
	Size  int    `json:"size"`
}

// GoalContinuationData is the payload for an EventGoalContinuation event. Text is
// a compact human-facing marker for the continuation (e.g. "Continuing toward:
// <objective>"), NOT the full rendered continuation prompt — that scaffolding goes
// to the model via the steering turn and is kept out of the UI projection.
type GoalContinuationData struct {
	Text string `json:"text"`
}

// GoalEndedData is the payload for an EventGoalEnded event. It reports the
// terminal goal status, the reason the loop stopped, and how many continuation
// turns were taken.
type GoalEndedData struct {
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Iterations int    `json:"iterations"`
}

// TurnEndedData is the payload for an EventTurnEnded event.
type TurnEndedData struct {
	TurnDurationMS int64 `json:"turn_duration_ms"`
}
