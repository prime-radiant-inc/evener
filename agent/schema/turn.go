package schema

import (
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/llm"
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
	// TurnTool is a turn carrying tool output. Old transcripts may use this
	// kind (value "TOOL"); current code writes TurnToolResults instead.
	TurnTool TurnKind = "TOOL"
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
	// TurnHookCompleted is a turn recording that one plugin hook finished —
	// the detail rides Turn.Hook. Presentational only, exactly like
	// TurnModelSwitch and TurnFailure: rendered as a systemMessage item by
	// both projection paths and excluded from expandHistory, because a hook's
	// own bookkeeping is not conversation. Without it a hook exit existed
	// only as a live event, so the two Settings → Transcript hook-exit
	// toggles had nothing to act on for any session a reader came back to
	// (kata qm9y).
	TurnHookCompleted TurnKind = "HOOK_COMPLETED"
	// TurnEnvironment is a harness-injected environment-context update (cwd,
	// date, sandbox, git branch, resource pressure) rendered as a diff by
	// agent/envctx. Unlike the presentational kinds above it IS
	// model-bound: expandHistory passes its user-role message through, and
	// because it is only ever appended (never edited) it preserves
	// provider prompt caches. UIs render it as harness chrome, not user speech.
	TurnEnvironment TurnKind = "ENVIRONMENT"
	// TurnNotesContext is a harness-injected shared-notes snapshot (human and
	// agent whiteboards plus the session URL list). Like TurnEnvironment it IS
	// model-bound: expandHistory passes its user-role message through, and it
	// is only ever appended (never edited) so provider prompt caches survive.
	// UIs render it as harness chrome, not user speech. The persisted note is
	// the source of truth; each turn carries a fresh projection of it.
	TurnNotesContext TurnKind = "NOTES_CONTEXT"
	// TurnAttentionResolution records the terminal disposition of one durable
	// attention item. Provider projection excludes it; generic presentation may
	// retain the marker while hiding its private metadata.
	TurnAttentionResolution TurnKind = "ATTENTION_RESOLUTION"

	// The kinds below are transcript only (see TranscriptOnly): they are
	// written to the transcript for the history projection and never enter a
	// session's in-memory history.

	// TurnCompletion records how an execution turn ended — the detail rides
	// Turn.Completion. It is the last entry of the turn's execution span.
	TurnCompletion TurnKind = "TURN_COMPLETION"
	// TurnReopen marks a turn that recovery reclaimed and runs again under
	// the same TurnID: the turn is open again until its next completion.
	TurnReopen TurnKind = "TURN_REOPEN"
	// TurnCommunicate records a communicate message at the moment it is
	// delivered — the detail rides Turn.Communicate.
	TurnCommunicate TurnKind = "COMMUNICATE"
	// TurnNotice records a presentational notice that is history rather than
	// an ephemeral live notice — the detail rides Turn.Notice.
	TurnNotice TurnKind = "NOTICE"
)

// TurnFormatIdentity is the entry format that carries persisted turn
// identity (TurnID, TurnKind and the other identity fields). An entry without
// it is a legacy entry, whose turn identity readers infer as they always have.
const TurnFormatIdentity = 1

// TranscriptOnly reports whether entries of this kind are written to the
// transcript only. They never enter a session's in-memory history, resume
// skips them, and every other consumer of transcript entries skips them too:
// they exist for the history projection alone.
func (k TurnKind) TranscriptOnly() bool {
	switch k {
	case TurnCompletion, TurnReopen, TurnCommunicate, TurnNotice:
		return true
	default:
		return false
	}
}

// TurnSpanKind is the kind of turn an entry opens. It is persisted on the
// first entry of each turn only.
type TurnSpanKind string

const (
	// TurnSpanExecution is a real run of the model.
	TurnSpanExecution TurnSpanKind = "execution"
	// TurnSpanGap holds standalone entries recorded between executions.
	TurnSpanGap TurnSpanKind = "gap"
	// TurnSpanDelivery holds one entry delivered while no execution ran, or
	// written by a writer with no session.
	TurnSpanDelivery TurnSpanKind = "delivery"
	// TurnSpanPrelude holds a fresh session's startup entries, before its
	// first execution.
	TurnSpanPrelude TurnSpanKind = "prelude"
)

// TurnCompletionStatus is how an execution turn ended.
type TurnCompletionStatus string

const (
	TurnCompleted   TurnCompletionStatus = "completed"
	TurnFailed      TurnCompletionStatus = "failed"
	TurnInterrupted TurnCompletionStatus = "interrupted"
)

// TurnCompletionInfo is the persisted end of an execution turn.
type TurnCompletionInfo struct {
	Status      TurnCompletionStatus `json:"status"`
	CompletedAt time.Time            `json:"completed_at"`
	DurationMS  int64                `json:"duration_ms"`
}

// CommunicateInfo is the persisted form of one delivered communicate message.
// It mirrors events.CommunicateData deliberately rather than reusing it: this
// shape is persisted transcript data and must stay stable on its own.
type CommunicateInfo struct {
	CallID  string `json:"call_id,omitempty"`
	EndTurn bool   `json:"end_turn"`
	Message string `json:"message"`
}

// NoticeKind names a presentational notice.
type NoticeKind string

const (
	NoticeToolRepair     NoticeKind = "tool_repair"
	NoticeGoalEnded      NoticeKind = "goal_ended"
	NoticeTurnLimit      NoticeKind = "turn_limit"
	NoticeSkillActivated NoticeKind = "skill_activated"
)

// NoticeInfo is a persisted presentational notice: its kind and exactly one
// payload. Each payload mirrors the live event's data field for field, for
// the same reason HookInfo mirrors its event.
type NoticeInfo struct {
	Kind           NoticeKind            `json:"kind"`
	ToolRepair     *ToolRepairNotice     `json:"tool_repair,omitempty"`
	GoalEnded      *GoalEndedNotice      `json:"goal_ended,omitempty"`
	TurnLimit      *TurnLimitNotice      `json:"turn_limit,omitempty"`
	SkillActivated *SkillActivatedNotice `json:"skill_activated,omitempty"`
}

// ToolRepairNotice mirrors events.ToolCallRepairedData.
type ToolRepairNotice struct {
	ToolName string   `json:"tool_name"`
	CallID   string   `json:"call_id"`
	Changes  []string `json:"changes"`
}

// GoalEndedNotice mirrors events.GoalEndedData.
type GoalEndedNotice struct {
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Iterations int    `json:"iterations"`
}

// TurnLimitNotice mirrors events.TurnLimitData.
type TurnLimitNotice struct {
	MaxTurns              int `json:"max_turns,omitempty"`
	MaxToolRoundsPerInput int `json:"max_tool_rounds_per_input,omitempty"`
}

// SkillActivatedNotice mirrors events.SkillActivatedData.
type SkillActivatedNotice struct {
	Name string `json:"name"`
}

// AttentionResolutionInfo identifies one durable attention item and its
// terminal disposition. The resolution is append-only so cold reconciliation
// can repeat safely after a crash.
type AttentionResolutionInfo struct {
	AttentionID      string `json:"attention_id"`
	Disposition      string `json:"disposition"`
	ResumeGeneration uint64 `json:"resume_generation,omitempty"`
}

// DelegateDeliveryCommit records which exact tool call durably received one
// delegate delivery on this tool-result turn. It is private persistence
// metadata, not model content.
type DelegateDeliveryCommit struct {
	ToolCallID string `json:"tool_call_id"`
	DeliveryID string `json:"delivery_id"`
}

// HookInfo is the persisted detail of one completed hook: the same fields the
// live events.HookEndData carries, so a reloaded transcript describes the hook
// exactly as the live session did rather than in a poorer summary of it.
//
// It mirrors the event payload deliberately rather than reusing it: this shape
// is persisted transcript data and must stay stable independently of the
// event's evolution.
type HookInfo struct {
	Event      string `json:"event,omitempty"`
	HookType   string `json:"hook_type,omitempty"`
	Matcher    string `json:"matcher,omitempty"`
	PluginName string `json:"plugin_name,omitempty"`
	// ExitCode is the hook process's own exit status. It is deliberately NOT
	// omitempty: exit 0 is the single most common value and the meaningful
	// one for "Hook exits (normal only)", so a clean hook must persist as a
	// present zero rather than as an absent field a reader cannot distinguish
	// from "no code was recorded".
	ExitCode   int   `json:"exit_code"`
	DurationMS int64 `json:"duration_ms,omitempty"`
}

// Announcement renders the one-line summary a reader sees for this hook. It
// lives here, on the persisted shape, so the live projector and the reloaded
// transcript build the identical sentence: a returning reader who was shown a
// differently-worded line than the watching one saw would be a smaller
// version of the divergence kata qm9y is about.
func (h HookInfo) Announcement() string {
	label := strings.TrimSpace(h.Event)
	if label == "" {
		label = "hook"
	}
	parts := []string{label + " hook"}
	for _, field := range []string{h.PluginName, h.Matcher, h.HookType} {
		if v := strings.TrimSpace(field); v != "" {
			parts = append(parts, v)
		}
	}
	parts = append(parts, fmt.Sprintf("exit %d", h.ExitCode))
	return strings.Join(parts, " ")
}

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
	// SteeringCarrier marks the TurnFailure shapes that are ALSO a resolution
	// boundary: a steering carrier turn whose acceptance already cleared
	// askPending before it recorded nothing else useful. Two shapes set it:
	//   - the carrier's own steer failed to append, so it recorded nothing
	//     else at all — no TurnSteering, no TurnUserInput, nothing
	//     (agent/session_lifecycle.go's acceptSteeringCarrierInput,
	//     carrierSteerUndelivered).
	//   - the carrier's claimed steer failed its skill-selection prepare
	//     (agent/session_queue.go's recordFailedSteeringSelection, when the
	//     failing steer is the one the carrier claim is draining).
	// Both read the turn's mere ACCEPTANCE as having already cleared
	// askPending unconditionally on entry, before its steer ever tried to
	// land (processOneInput's "Pending asks resolve with this accepted turn")
	// — but ONLY when the claimed steer itself answers the ask
	// (steeringCarrierClaimAnswersAsk, agent/session_tools_ask.go): a
	// human-note carrier's entry clear is skipped, so its own failure
	// (either shape) leaves this false and askPending stays live-pending.
	//
	// turnResolvesAskBoundary (agent/session_tools_ask.go) reads this flag as
	// a resolution boundary for deriveRestoredAskPending/deriveRestoredState,
	// so a restore agrees with the live session on whether this failure left
	// askPending live-pending.
	SteeringCarrier bool `json:"steering_carrier,omitempty"`
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

// ModelSwitchInfo records the resolved provider instances and model IDs on
// either side of a successful model switch. These are configuration identities,
// not model aliases reported by a provider response.
type ModelSwitchInfo struct {
	OldProvider string `json:"old_provider"`
	OldModel    string `json:"old_model"`
	NewProvider string `json:"new_provider"`
	NewModel    string `json:"new_model"`
}

// GoalContinuationInfo keeps the human-facing notice separate from the full
// continuation input in Message, which remains available to resumed models.
type GoalContinuationInfo struct {
	Text string `json:"text"`
}

// Turn is the Session's typed history item. Steering turns are kept distinct for observability,
// but are converted to user-role messages when building the LLM request.
// Fields MUST stay declared in alphabetical JSON-key order: the public
// line projection (agent's publicTranscriptLine) re-marshals the turn
// through string-keyed maps, which sort keys alphabetically, and relies
// on this struct's field order matching that sort so a projected line
// stays byte-identical to the persisted one.
// TestReadSessionTranscriptExpansionLosslesslyReturnsEverySemanticTurn
// pins the round trip. attention_id, attention_resolution, and
// delegate_delivery_commits are deleted by the projection rather than
// projected, so turns carrying them are not byte-identical by design;
// they stay sorted here too so a future key lands in one obvious place.
type Turn struct {
	AttemptGroupID string `json:"attempt_group_id,omitempty"`
	// AttentionID identifies steering that requires durable terminal cleanup.
	// It is empty for ordinary steering and all non-steering turns.
	AttentionID string `json:"attention_id,omitempty"`
	// AttentionResolution is set only on TurnAttentionResolution turns.
	AttentionResolution *AttentionResolutionInfo `json:"attention_resolution,omitempty"`
	// ClientMutationID identifies retry-safe client-authored input. StableTurnID
	// preserves the logical turn identity across live events and transcript
	// recovery for both client input and daemon goal continuations.
	ClientMutationID string `json:"client_mutation_id,omitempty"`
	// Communicate carries a delivered message on TurnCommunicate entries.
	Communicate *CommunicateInfo `json:"communicate,omitempty"`
	// Completion carries how an execution ended on TurnCompletion entries.
	Completion              *TurnCompletionInfo      `json:"completion,omitempty"`
	DelegateDeliveryCommits []DelegateDeliveryCommit `json:"delegate_delivery_commits,omitempty"`
	// Error carries the diagnostic of a terminally failed turn. Set only on
	// TurnFailure turns; nil everywhere else.
	Error *TurnFailureInfo `json:"error,omitempty"`
	// Format is the entry's format: TurnFormatIdentity on every entry that
	// carries persisted turn identity, zero on legacy entries. Readers branch
	// on it rather than on which identity fields happen to be present.
	Format int `json:"format,omitempty"`
	// GoalContinuation marks goal-engine steering that opens a fresh logical
	// turn and displays a compact notice rather than its model instructions.
	GoalContinuation *GoalContinuationInfo `json:"goal_continuation,omitempty"`
	// Hook carries the detail of one completed plugin hook. Set only on
	// TurnHookCompleted turns; nil everywhere else.
	Hook    *HookInfo   `json:"hook,omitempty"`
	Kind    TurnKind    `json:"kind"`    // category of this history item
	Message llm.Message `json:"message"` // the underlying LLM message
	// Model is the configured model the session was using when the entry was
	// recorded, so cost can be computed from the entry's usage alone.
	Model string `json:"model,omitempty"`
	// ModelSwitch carries resolved identities on TurnModelSwitch turns.
	ModelSwitch *ModelSwitchInfo `json:"model_switch,omitempty"`
	// Notice carries a presentational notice on TurnNotice entries.
	Notice *NoticeInfo `json:"notice,omitempty"`
	// OriginalOrdinal is set on a copy a compaction fold re-appends after its
	// markers: the entry ordinal of the entry it copies. Copies are model
	// history for resume, and are neither projected nor announced.
	OriginalOrdinal *uint64 `json:"original_ordinal,omitempty"`
	// OwningTurnID identifies the logical turn that owns an ordinary steering
	// entry. It differs from StableTurnID, which identifies the client mutation.
	OwningTurnID           string `json:"owning_turn_id,omitempty"`
	ResponseContextMarker  string `json:"response_context_marker,omitempty"`
	ResponseEndpoint       string `json:"response_endpoint,omitempty"`
	ResponseEndpointFamily string `json:"response_endpoint_family,omitempty"`
	// ResponseID is the provider's response identifier (from llm.Response.ID),
	// recorded on assistant turns and surfaced in ATIF trajectory export.
	ResponseID                      string `json:"response_id,omitempty"`
	ResponseIDHash                  string `json:"response_id_hash,omitempty"`
	ResponseModel                   string `json:"response_model,omitempty"`
	ResponseProtocol                string `json:"response_protocol,omitempty"`
	ResponseProvider                string `json:"response_provider,omitempty"`
	ResponseRequestFingerprint      string `json:"response_request_fingerprint,omitempty"`
	ResponseRequestModel            string `json:"response_request_model,omitempty"`
	ResponseStorageScopeFingerprint string `json:"response_storage_scope_fingerprint,omitempty"`
	// RoundID names the model round an ASSISTANT entry (including a salvage
	// entry) records: every request from the round's first attempt up to the
	// first recorded ASSISTANT entry shares it.
	RoundID string `json:"round_id,omitempty"`
	// SkillState carries explicit typed operation records, not inferred history.
	SkillState   *SkillTurnState `json:"skill_state,omitempty"`
	StableTurnID string          `json:"stable_turn_id,omitempty"`
	// SteeringKind records what a TurnSteering entry was (events.SteeringKind*),
	// so a reloaded transcript labels a steer the same way the live path did.
	SteeringKind string `json:"steering_kind,omitempty"`
	// SteeringSource records the provenance of a TurnSteering entry:
	// "user" for human-sent steering (the UI steer action or queued user
	// input drained as steering), empty for daemon/system nudges. Persisted
	// so replay/hydration can render user steering as user speech
	// (issue #24). Empty on non-steering turns.
	SteeringSource string    `json:"steering_source,omitempty"`
	Timestamp      time.Time `json:"timestamp"` // when the turn was recorded (UTC)
	// TurnID is the turn this entry belongs to: turn_m<N> for a
	// client-mutation turn, turn_system for the prelude, t_<id> otherwise.
	// Unlike StableTurnID it is set on every entry that carries identity.
	TurnID string `json:"turn_id,omitempty"`
	// TurnKind is set on the first entry of each turn only.
	TurnKind TurnSpanKind `json:"turn_kind,omitempty"`
	// Usage carries the token-usage stats reported by the provider; set only on
	// assistant turns.
	Usage llm.Usage `json:"usage"`
}

// NewTurn creates a Turn with the current UTC time.
func NewTurn(kind TurnKind, msg llm.Message) Turn {
	return Turn{Kind: kind, Message: msg, Timestamp: time.Now().UTC()}
}
