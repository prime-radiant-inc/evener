package appwire

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Turn struct {
	ID        string        `json:"id"`
	Items     []ThreadItem  `json:"items,omitempty"`
	ItemsView TurnItemsView `json:"itemsView"`
	Status    string        `json:"status"`
	Error     *TurnError    `json:"error,omitempty"`
	// Version is the highest contributing entry ordinal + 1 (0 means an
	// overlay-only turn with no recorded history yet).
	Version uint64 `json:"version,omitempty"`
	// HasEarlierItems and HasLaterItems describe completeness at the item
	// boundaries of a fragment. They are omitted by legacy/full responses.
	HasEarlierItems bool `json:"hasEarlierItems,omitempty"`
	HasLaterItems   bool `json:"hasLaterItems,omitempty"`
	// StartedAt and CompletedAt are Unix epoch MILLISECONDS (nil/0 when unset),
	// the same scale as DurationMS and the web reducer's epoch-ms read. The
	// appprojector/apptranscript producers stamp them via time.Time.UnixMilli.
	StartedAt   *int64 `json:"startedAt,omitempty"`
	CompletedAt *int64 `json:"completedAt,omitempty"`
	DurationMS  *int64 `json:"durationMs,omitempty"`
	// Usage and Cost are the turn's own (not cumulative-session) token totals
	// and estimated dollar cost — nil/empty when not computable (no usage
	// data for this turn, or a registry row with no cost). Populated live by
	// summing EventAssistantTextEnd's per-round usage across the turn
	// (internal/appprojector), and for ended sessions by reading the
	// persisted per-round schema.Turn.Usage (internal/apptranscript).
	Usage *EvenerUsage `json:"usage,omitempty"`
	Cost  string       `json:"cost,omitempty"`
}

// SystemPreludeTurnID is the synthetic turn id for content that belongs
// before the session's first real turn rather than to any turn a user or
// agent produced: apptranscript.PreludeTurn's system-prompt scaffold (the
// persisted-transcript path), and appprojector's own bundling of every
// SESSION_START-time announcement — plugin loads, prompt-loaded notices,
// hook/MCP warnings — that arrives live before turn_1 ever starts (the
// notification path). Both paths reuse the SAME id deliberately: a client
// that sees only this one turn, from either path, is looking at a session
// that has never had a real turn (kata bz2z) — genuinely "dormant" — which
// is the signal the empty-transcript invitation keys on. Real turns use
// "turn_N" (N >= 1) or the reserved "turn_mN" below, so this can never
// collide with one.
const SystemPreludeTurnID = "turn_system"

// ClientMutationTurnID names the turn a client-authored input (turn/start,
// turn/queue) will occupy, from the daemon's durable per-mutation counter.
//
// It is deliberately NOT in the "turn_N" namespace the transcript's
// entry-index numbering owns. A session accumulates transcript entries
// several times faster than it accumulates client mutations, so a reservation
// numbered off the mutation counter always names a LOW number — one that an
// unrelated early legacy entry already owns. The reply would then merge into
// that entry's turn, taking the whole agent response with it (kata rk09).
//
// Raising the counter above the entry count cannot fix this: the entry index
// outgrows the mutation counter, so a fenced reservation falls behind and
// collides again within a few turns. Only a disjoint namespace closes it.
func ClientMutationTurnID(sequence uint64) string {
	return fmt.Sprintf("turn_m%d", sequence)
}

type TurnError struct {
	Message           string           `json:"message"`
	AdditionalDetails string           `json:"additionalDetails,omitempty"`
	CodexErrorInfo    any              `json:"codexErrorInfo,omitempty"`
	Source            string           `json:"source,omitempty"`
	Title             string           `json:"title,omitempty"`
	Hint              string           `json:"hint,omitempty"`
	Cause             *DiagnosticCause `json:"cause,omitempty"`
}

// DiagnosticCause is the wire-level structured cause attached to a
// warning/error notification. Kinds today are "provider" (an HTTP failure
// from an LLM adapter) and "signInRequired" (the user must sign in to a
// provider instance again); consumers can typed-branch on Kind instead of
// substring-matching the message (kata cmfz). The agent's events.ErrorCause
// projects to this shape; absence is signaled by an omitted/nil pointer on
// the carrying envelope.
type DiagnosticCause struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   int    `json:"status,omitempty"`
}

// Stable semantic event kinds for systemMessage transcript items. These values
// identify what happened; display titles and summaries may change independently.
type ThreadItemEventKind string

const (
	// ThreadItemEventKindSystemPrompt marks the session's system prompt, the
	// long scaffolding block PreludeTurn projects at the head of the
	// transcript. It is the typed discriminator a client renders as a
	// collapsed-by-default disclosure rather than a quiet one-liner, replacing
	// what the web SPA formerly guessed from the item's own char count.
	ThreadItemEventKindSystemPrompt      ThreadItemEventKind = "system_prompt"
	ThreadItemEventKindPluginLoaded      ThreadItemEventKind = "plugin_loaded"
	ThreadItemEventKindSkillActivated    ThreadItemEventKind = "skill_activated"
	ThreadItemEventKindHookCompleted     ThreadItemEventKind = "hook_completed"
	ThreadItemEventKindPromptLoaded      ThreadItemEventKind = "prompt_loaded"
	ThreadItemEventKindContextCompaction ThreadItemEventKind = "context_compaction"
	ThreadItemEventKindCompaction        ThreadItemEventKind = "compaction"
	ThreadItemEventKindTurnLimit         ThreadItemEventKind = "turn_limit"
	ThreadItemEventKindLoopDetection     ThreadItemEventKind = "loop_detection"
	ThreadItemEventKindGoalEnded         ThreadItemEventKind = "goal_ended"
	ThreadItemEventKindForkSummary       ThreadItemEventKind = "fork_summary"
	ThreadItemEventKindRoundTimings      ThreadItemEventKind = "round_timings"
	ThreadItemEventKindToolRepair        ThreadItemEventKind = "tool_repair"
	ThreadItemEventKindModelSwitch       ThreadItemEventKind = "model_switch"
	// ThreadItemEventKindError marks the systemMessage item a reloaded
	// transcript renders for a turn that failed terminally. It lets clients
	// find the failure by type rather than by reading the item's prose.
	ThreadItemEventKindError ThreadItemEventKind = "error"
	// ThreadItemEventKindEnvironment marks the systemMessage item a reloaded
	// transcript renders for a schema.TurnEnvironment turn: the harness's
	// diff-rendered cwd/git-branch/pressure block, not a settings-governed
	// toggle — no visibility preference hides it (transcriptVisibility.ts's
	// "no toggle governs it" default applies).
	ThreadItemEventKindEnvironment ThreadItemEventKind = "environment"
	// ThreadItemEventKindNotesContext marks the systemMessage item a reloaded
	// transcript renders for a schema.TurnNotesContext turn: the harness's
	// shared-notes snapshot block. Web and native hide it at Conversation;
	// elsewhere System events controls visibility and it starts folded.
	ThreadItemEventKindNotesContext ThreadItemEventKind = "notes-context"
	// ThreadItemEventKindWarning marks a live overlay notice for a session
	// warning. Warnings are never recorded, so only the overlay shows them.
	ThreadItemEventKindWarning ThreadItemEventKind = "warning"
	// ThreadItemEventKindInterrupted marks the live overlay notice a model
	// round collapses into when it ended with streamed content or running
	// tools that were never recorded.
	ThreadItemEventKindInterrupted ThreadItemEventKind = "interrupted"
	// ThreadItemEventKindApprovalDecision marks the systemMessage item a
	// human's Allow or Deny on a sandbox escalation leaves in history (S16).
	// apptranscript.ApprovalDecisionAnnouncement documents its Raw.
	ThreadItemEventKindApprovalDecision ThreadItemEventKind = "approval_decision"
	// ThreadItemEventKindMemoryContext marks each systemMessage item a
	// schema.TurnMemoryContext turn projects: one per section of the
	// boundary's memory notification (a scope's index, its index changes, or
	// a notice of read pages another session changed). apptranscript owns the
	// split, each item's exact recorded section text, and the Raw
	// ({"memoryContext": {...}}) that only index sections carry; change and
	// page sections, and a message that does not decode, carry no Raw and
	// clients show their text. The web and native renderers own presenting
	// each as a collapsed steering-style notification.
	ThreadItemEventKindMemoryContext ThreadItemEventKind = "memory-context"
)

// AllThreadItemEventKinds is every ThreadItem.EventKind value emitted for
// systemMessage items, including those used for lifecycle scaffolding.
var AllThreadItemEventKinds = []string{
	string(ThreadItemEventKindSystemPrompt),
	string(ThreadItemEventKindPluginLoaded),
	string(ThreadItemEventKindSkillActivated),
	string(ThreadItemEventKindHookCompleted),
	string(ThreadItemEventKindPromptLoaded),
	string(ThreadItemEventKindContextCompaction),
	string(ThreadItemEventKindCompaction),
	string(ThreadItemEventKindTurnLimit),
	string(ThreadItemEventKindLoopDetection),
	string(ThreadItemEventKindGoalEnded),
	string(ThreadItemEventKindForkSummary),
	string(ThreadItemEventKindRoundTimings),
	string(ThreadItemEventKindToolRepair),
	string(ThreadItemEventKindModelSwitch),
	string(ThreadItemEventKindError),
	string(ThreadItemEventKindEnvironment),
	string(ThreadItemEventKindNotesContext),
	string(ThreadItemEventKindWarning),
	string(ThreadItemEventKindInterrupted),
	string(ThreadItemEventKindApprovalDecision),
	string(ThreadItemEventKindMemoryContext),
}

type ThreadItem struct {
	Actor                *SessionActor               `json:"actor,omitempty"`
	Observation          *SessionObservationMetadata `json:"observation,omitempty"`
	Authorship           *TranscriptAuthorship       `json:"authorship,omitempty"`
	Type                 string                      `json:"type"`
	ID                   string                      `json:"id"`
	TranscriptKey        string                      `json:"transcriptKey,omitempty"`
	Position             *ThreadItemPosition         `json:"position,omitempty"`
	TurnID               string                      `json:"turnId,omitempty"`
	TranscriptEntryIndex int                         `json:"transcriptEntryIndex,omitempty"`
	Text                 string                      `json:"text,omitempty"`
	Delta                string                      `json:"delta,omitempty"`
	Images               []InputItem                 `json:"images,omitempty"`
	ToolName             string                      `json:"toolName,omitempty"`
	CallID               string                      `json:"callId,omitempty"`
	ArgumentsJSON        string                      `json:"argumentsJson,omitempty"`
	Description          string                      `json:"description,omitempty"`
	Output               string                      `json:"output,omitempty"`
	Error                string                      `json:"error,omitempty"`
	// OutputImages is omitzero; see the nil-vs-empty rule in output_images.go.
	// Images stays omitempty: nothing removes an item's input images.
	OutputImages []OutputImage `json:"outputImages,omitzero"`
	Status       string        `json:"status,omitempty"`
	// PrevalOnly is true when Error came from a pre-dispatch rejection (an
	// unknown tool name, or arguments that failed schema validation even
	// after repair) rather than the tool's own execution - the call never
	// reached ExecuteCall. A client uses this to tell a
	// self-corrected malformed-call bounce apart from a real execution
	// failure or denial: same non-empty Error, different meaning. False (the
	// default) for a real execution failure, and meaningless when Error is
	// empty.
	PrevalOnly bool `json:"prevalOnly,omitempty"`
	// StartedAt and CompletedAt are Unix epoch MILLISECONDS (nil when unset),
	// matching DurationMS's scale and the web reducer's epoch-ms read; stamped
	// by the appprojector/apptranscript producers via time.Time.UnixMilli.
	StartedAt   *int64 `json:"startedAt,omitempty"`
	CompletedAt *int64 `json:"completedAt,omitempty"`
	// DurationMS is the item's real server-measured runtime in milliseconds
	// (tool-call items only). Stamped live from the event stream's own
	// timestamps; nil when no honest span was recorded (the web hover meta
	// shows real times or nothing).
	DurationMS *int64 `json:"durationMs,omitempty"`
	// ExitCode is the exit status of the process behind this item, on the two
	// item kinds that have one:
	//
	//   - a shell tool call's process, promoted onto the settled
	//     commandExecution item from the ToolState JSON snapshot the
	//     projector/transcript already hold (the "exit_code" field of
	//     shellToolResult, agent/session_tools_shell.go:483; wire-honesty
	//     spec Part A);
	//   - a hook's process, on a systemMessage item with EventKind
	//     ThreadItemEventKindHookCompleted, from events.HookEndData.ExitCode.
	//     Clients split "show every hook exit" from "show clean exits only"
	//     on this number instead of re-parsing the "... exit N" announcement
	//     prose, so rewording that text cannot change what a reader sees.
	//
	// Nil on every other item, and on any of the above whose source carries
	// no exit code — never fabricated as zero.
	ExitCode  *int64              `json:"exitCode,omitempty"`
	Raw       json.RawMessage     `json:"raw,omitempty"`
	EventKind ThreadItemEventKind `json:"eventKind,omitempty"`
	// Source carries item provenance for steering items: "user" for
	// human-sent steering (rendered as a user message), empty for
	// daemon/system steering.
	Source string `json:"source,omitempty"`
	// SteeringKind names what a daemon-originated steering item was
	// (events.SteeringKind*), set at the injection site so a client labels it
	// from ground truth instead of guessing from Text's prose. Empty on
	// non-steering items and on steering items the daemon didn't classify.
	SteeringKind     string `json:"steeringKind,omitempty"`
	ClientMutationID string `json:"clientMutationId,omitempty"`
	// Version is the highest entry ordinal (below) among the entries that
	// contributed to this item, stored and sent as ordinal + 1 so 0 means "no
	// history version" (an overlay item). The higher version wins.
	Version uint64 `json:"version,omitempty"`
	// RoundID names the round an ASSISTANT-projected item belongs to. Empty
	// for items that are not projected from an ASSISTANT entry.
	RoundID string `json:"roundId,omitempty"`
	// CompletedAtEntry is, on a tool item, the entry ordinal + 1 of the
	// TOOL_RESULTS entry that completed it (the spec's `completedAt`
	// metadata; CompletedAt is the completion timestamp). 0 until results
	// are recorded. Key and position stay the opener's, so the item never
	// moves when it completes; a client drops the overlay's execution state
	// for the item's key once it holds a non-zero value.
	CompletedAtEntry uint64 `json:"completedAtEntry,omitempty"`
}

type OutputImage struct {
	Source    string `json:"source"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Size      int64  `json:"size,omitempty"`
	URL       string `json:"url,omitempty"`
	SHA       string `json:"sha,omitempty"`
	Path      string `json:"path,omitempty"`
}

// InputMention records one visible selection's UTF-16 offset in a text item.
// Canonical skill/command items remain the sole activation authority.
type InputMention struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Offset int    `json:"offset"`
}

type InputItem struct {
	Type      string            `json:"type"`
	Text      string            `json:"text,omitempty"`
	Mentions  []InputMention    `json:"mentions,omitempty"`
	URL       string            `json:"url,omitempty"`
	MediaType string            `json:"mediaType,omitempty"`
	Data      []byte            `json:"data,omitempty"`
	Name      string            `json:"name,omitempty"`
	Path      string            `json:"path,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type ThreadListParams struct {
	Cursor           string   `json:"cursor,omitempty"`
	Limit            int      `json:"limit,omitempty"`
	SortKey          string   `json:"sortKey,omitempty"`
	SortDirection    string   `json:"sortDirection,omitempty"`
	SearchTerm       string   `json:"searchTerm,omitempty"`
	Statuses         []string `json:"statuses,omitempty"`
	SourceIDs        []string `json:"sourceIds,omitempty"`
	IncludeSubagents bool     `json:"includeSubagents,omitempty"`
	// StatusOnly asks a daemon for the diagnostics a liveness probe reads and
	// nothing else: each row's Diagnostics carries only Jobs, Watches, and
	// Delegates reduced to delegateId, childSessionId and lifecycle. A daemon
	// that predates the field, and a hub, ignore it and return the full answer.
	StatusOnly bool `json:"statusOnly,omitempty"`
}

type ThreadListResponse struct {
	Data            []Thread `json:"data"`
	NextCursor      string   `json:"nextCursor,omitempty"`
	BackwardsCursor string   `json:"backwardsCursor,omitempty"`
}

type ThreadReadParams struct {
	ThreadContentParams
	ThreadID            string `json:"threadId,omitempty"`
	Ref                 string `json:"ref,omitempty"`
	IncludeTurns        bool   `json:"includeTurns"`
	ItemsView           string `json:"itemsView,omitempty"`
	Subscribe           bool   `json:"subscribe,omitempty"`
	ReplaceSubscription bool   `json:"replaceSubscription,omitempty"`
	ItemLimit           int    `json:"itemLimit,omitempty"`
	// RequestGeneration is the client's own read-request counter, echoed back
	// on ThreadReadResponse so a client can discard a stale reply that
	// resolves after a newer request it already issued.
	RequestGeneration uint64 `json:"requestGeneration,omitempty"`
	// HeldSnapshot names what the client's currently held read was projected
	// from, so the server can answer with HistoryChanges instead of a full
	// re-read when nothing outside the client's window changed.
	HeldSnapshot *SnapshotIdentity `json:"heldSnapshot,omitempty"`
	// AnswerContinuation requests only a recorded ancestry verdict and its
	// snapshot identity. It cannot subscribe or include a transcript window.
	AnswerContinuation *AnswerContinuationQuery `json:"answerContinuation,omitempty"`
}

type AnswerContinuationQuery struct {
	AncestorTurnID  string `json:"ancestorTurnId"`
	CandidateTurnID string `json:"candidateTurnId"`
}

type AnswerContinuationVerdict struct {
	AncestorTurnID  string `json:"ancestorTurnId"`
	CandidateTurnID string `json:"candidateTurnId"`
	Matches         bool   `json:"matches"`
}

func (p ThreadReadParams) ValidateAnswerContinuation() error {
	if q := p.AnswerContinuation; q != nil {
		if err := ValidateThreadReadParams(p); err != nil {
			return err
		}
		if q.AncestorTurnID == "" || q.CandidateTurnID == "" || p.Subscribe || p.ReplaceSubscription || p.IncludeTurns {
			return InvalidParams("answerContinuation requires two execution IDs and a non-subscribing, verdict-only read")
		}
	}
	return nil
}

type ThreadReadResponse struct {
	Thread             Thread                     `json:"thread"`
	AnswerContinuation *AnswerContinuationVerdict `json:"answerContinuation,omitempty"`
	// OlderCursor is set when itemLimit truncated the returned items; pass it
	// to thread/turns/list to fetch the page just before the window. Empty means
	// the response already includes the oldest item.
	OlderCursor string `json:"olderCursor,omitempty"`
	// RequestGeneration echoes ThreadReadParams.RequestGeneration.
	RequestGeneration uint64 `json:"requestGeneration,omitempty"`
	// BootGeneration is the serving daemon's boot generation for this thread:
	// its counter for its root thread, "<n>@<rootSessionID>" for a descendant
	// (DescendantBootGeneration), or DaemonlessBootGeneration for the hub's
	// own read. See CompareBootGeneration.
	BootGeneration string `json:"bootGeneration,omitempty"`
	// Epoch is the history epoch this response was projected under; it
	// advances on a replacement (a new incarnation, a resync epoch, or an
	// authoritative daemonless read).
	Epoch uint64 `json:"epoch,omitempty"`
	// Snapshot names what this read was projected from.
	Snapshot *SnapshotIdentity `json:"snapshot,omitempty"`
	// Overlay carries the live, not-yet-recorded state (streams, previews,
	// running tools, notices) alongside the recorded thread.
	Overlay []OverlayItem `json:"overlay,omitempty"`
	// Authoritative is true when this response can settle uncertain state
	// (a live daemon read), false for a saved-transcript read that cannot.
	Authoritative bool `json:"authoritative,omitempty"`
	// Changes carries items and turns outside the client's held window whose
	// version grew since HeldSnapshot, in place of a full re-read.
	Changes *HistoryChanges `json:"changes,omitempty"`
}

// SnapshotIdentity names what a history read or update was projected from:
// the incarnation (identity of the recorded history line, changed by a
// replacement) and its length at projection time.
type SnapshotIdentity struct {
	Incarnation string `json:"incarnation"`
	Length      int64  `json:"length"`
}

const NotifyHistoryUpdated = "history/updated"

// HistoryUpdatedParams carries the full current form of every item and turn
// whose recorded entries changed. Turns carry no Items — the reducer merges
// them against items it already holds by key.
type HistoryUpdatedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	// BootGeneration is the publishing daemon's boot generation for this
	// thread: its counter for its root thread, "<n>@<rootSessionID>" for a
	// descendant (DescendantBootGeneration). See CompareBootGeneration.
	BootGeneration string           `json:"bootGeneration"`
	Epoch          uint64           `json:"epoch"`
	Snapshot       SnapshotIdentity `json:"snapshot"`
	Turns          []Turn           `json:"turns,omitempty"`
	Items          []ThreadItem     `json:"items,omitempty"`
}

// OverlayKind discriminates the four shapes of live, not-yet-recorded state
// an OverlayItem can carry.
type OverlayKind string

const (
	OverlayStream  OverlayKind = "stream"
	OverlayPreview OverlayKind = "preview"
	OverlayTool    OverlayKind = "tool"
	OverlayNotice  OverlayKind = "notice"
)

// OverlayItem is one piece of live state that is not recorded history: a
// streaming assistant/reasoning run, a tool-call preview, a running tool, or
// an ephemeral notice.
type OverlayItem struct {
	// Key identifies this overlay slot: "stream:<streamId>:<agentMessage|reasoning>",
	// "preview:<callId>", "tool:<historyKey>", or a notice key. Notice keys
	// are opaque: a daemon's are "notice:<n>", and the hub mints its own
	// (such as "notice:hub:relay-gave-up").
	Key        string              `json:"key"`
	Kind       OverlayKind         `json:"kind"`
	TurnID     string              `json:"turnId,omitempty"`
	RoundID    string              `json:"roundId,omitempty"`
	StreamID   string              `json:"streamId,omitempty"`
	CallID     string              `json:"callId,omitempty"`
	HistoryKey string              `json:"historyKey,omitempty"` // tool: the call item's transcriptKey
	Anchor     *ThreadItemPosition `json:"anchor,omitempty"`     // notice
	Item       ThreadItem          `json:"item"`                 // the display form
}

const (
	NotifyOverlayUpserted = "overlay/upserted" // OverlayUpsertedParams
	NotifyOverlayDelta    = "overlay/delta"    // OverlayDeltaParams
	NotifyOverlayReset    = "overlay/reset"    // OverlayResetParams
	NotifyOverlayEnd      = "overlay/end"      // OverlayEndParams
)

// OverlayUpsertedParams is the payload of overlay/upserted: one overlay item
// was created or replaced.
type OverlayUpsertedParams struct {
	ThreadID string      `json:"threadId"`
	Ref      string      `json:"ref"`
	Item     OverlayItem `json:"item"`
}

// OverlayDeltaField selects which field of a targeted overlay item an
// OverlayDeltaParams appends to.
type OverlayDeltaField string

const (
	OverlayDeltaText   OverlayDeltaField = "text"
	OverlayDeltaOutput OverlayDeltaField = "output"
)

// OverlayDeltaParams is the payload of overlay/delta: an incremental chunk
// appended to one overlay item's text or output.
type OverlayDeltaParams struct {
	ThreadID string            `json:"threadId"`
	Ref      string            `json:"ref"`
	Key      string            `json:"key"`
	Field    OverlayDeltaField `json:"field"`
	Delta    string            `json:"delta"`
}

// OverlayResetParams is the payload of overlay/reset: discard the named
// stream's in-progress overlay item (a retry replaces it).
type OverlayResetParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	StreamID string `json:"streamId"`
}

// OverlayEndParams is the payload of overlay/end: the named round's overlay
// state (previews, running tools) is final and about to be replaced by
// recorded history.
type OverlayEndParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	RoundID  string `json:"roundId"`
}

// HistoryChanges are items and turns outside a daemonless latest window whose
// version grew since the snapshot the client held.
type HistoryChanges struct {
	Turns []Turn       `json:"turns,omitempty"`
	Items []ThreadItem `json:"items,omitempty"`
}

func decodeStrictJSON(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

func (p *ThreadReadParams) UnmarshalJSON(data []byte) error {
	type wire ThreadReadParams
	var decoded wire
	if err := decodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*p = ThreadReadParams(decoded)
	return nil
}

type ThreadUnsubscribeParams struct {
	ThreadID string `json:"threadId,omitempty"`
	Ref      string `json:"ref,omitempty"`
}

type ThreadTurnsListParams struct {
	ThreadContentParams
	ThreadID  string `json:"threadId,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	ItemsView string `json:"itemsView,omitempty"`
	ItemLimit int    `json:"itemLimit,omitempty"`
	// Before, when set, ends the page just before this position. With a
	// Cursor it moves the cursor's boundary, keeping the cursor's identity
	// fence; with none the source mints a cursor there under the thread's
	// current identity. A client that dropped rows from the top of its window
	// names the oldest row it kept and pages the dropped rows back.
	Before *ThreadItemPosition `json:"before,omitempty"`
}

// ThreadTurnsListResponse is one backfill page. It carries no request
// generation: backfill pages accumulate within their snapshot in any arrival
// order, and only latest-window reads are ordered by generation.
type ThreadTurnsListResponse struct {
	Data       []Turn `json:"data"`
	NextCursor string `json:"nextCursor,omitempty"`
	// BootGeneration is the serving daemon's boot generation for this thread,
	// as on ThreadReadResponse. See CompareBootGeneration.
	BootGeneration string `json:"bootGeneration,omitempty"`
	// Epoch is the history epoch this page was projected under; see
	// ThreadReadResponse.Epoch.
	Epoch uint64 `json:"epoch,omitempty"`
	// Snapshot names what this page was projected from.
	Snapshot *SnapshotIdentity `json:"snapshot,omitempty"`
	// Authoritative is true when this page can settle uncertain state; see
	// ThreadReadResponse.Authoritative.
	Authoritative bool `json:"authoritative,omitempty"`
}

func (p *ThreadTurnsListParams) UnmarshalJSON(data []byte) error {
	type wire ThreadTurnsListParams
	var decoded wire
	if err := decodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*p = ThreadTurnsListParams(decoded)
	return nil
}

type ThreadTurnItemsListParams struct {
	ThreadID string `json:"threadId,omitempty"`
	Ref      string `json:"ref,omitempty"`
	TurnID   string `json:"turnId"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type ThreadTurnItemsListResponse struct {
	Data       []ThreadItem `json:"data"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

// SessionImageParams selects one image from the recipient hub's own local
// session state. Exactly one selector must be set, matching the two URL forms a
// hub stamps into its own thread snapshots:
//
//   - SHA addresses the replayed-input form /s/<session>/images/<sha>, whose
//     bytes are re-scanned out of the session transcript;
//   - Path is a file path for the file-backed form
//     /doc/image?session=<session>&path=<path>: relative to the session's
//     current working directory, or absolute inside that directory or the
//     session's retained artifacts directory. Any other absolute path is
//     refused.
//
// Both set, or neither, is InvalidParams. SessionID names the session in the
// recipient hub's own namespace; it is never a routing field for another source.
type SessionImageParams struct {
	SessionID string `json:"sessionId"`
	SHA       string `json:"sha,omitempty"`
	Path      string `json:"path,omitempty"`
}

// SessionImageResponse is one session image: the raw bytes (base64 inside the
// JSON frame), their re-derived media type, the byte length, and the lowercase
// hex sha256 of Data. The media type is never the value stored in a transcript.
type SessionImageResponse struct {
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	SHA       string `json:"sha,omitempty"`
	Data      []byte `json:"data"`
}

// SessionDocumentParams names one file in a session's working directory on the
// recipient hub. SessionID names the session in the recipient's own namespace
// and is never a routing field for another source. Path is resolved exactly as
// the local /doc/file route resolves it: relative to the session's working
// directory, or absolute inside it, and refused when it or a symlink along it
// leads outside.
type SessionDocumentParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

// SessionDocumentResponse is one read of a session document: at most the first
// 512 KiB of the file (base64 inside the JSON frame), the file's true size, the
// lowercase hex sha256 of the whole file (absent for a file too large to hash),
// and its modification time in Unix milliseconds.
type SessionDocumentResponse struct {
	Data       []byte `json:"data"`
	TotalSize  int64  `json:"totalSize"`
	Revision   string `json:"revision,omitempty"`
	ModifiedAt int64  `json:"modifiedAt,omitempty"`
}

type ThreadTranscriptListParams struct {
	Ref string `json:"ref"`
}

type ThreadTranscriptTarget struct {
	Ref       string `json:"ref"`
	ThreadID  string `json:"threadId,omitempty"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Status    string `json:"status,omitempty"`
	Source    string `json:"source,omitempty"`
	TurnsUsed int    `json:"turnsUsed,omitempty"`
}

type ThreadTranscriptListResponse struct {
	Data []ThreadTranscriptTarget `json:"data"`
}

type EvenerSubagentPreviewParams struct {
	Ref   string `json:"ref"`
	Limit int    `json:"limit,omitempty"`
}

type EvenerSubagentPreviewResponse struct {
	Ref       string       `json:"ref"`
	Items     []ThreadItem `json:"items"`
	Truncated bool         `json:"truncated"`
}

type ThreadStartParams struct {
	Harness string `json:"harness,omitempty"`
	// Source names the registered source that should spawn this thread, e.g. a
	// remote host's host name. It is a bare source ID, not a ref. Empty falls
	// back to the harness/default routing (local).
	Source          string             `json:"source,omitempty"`
	CWD             string             `json:"cwd"`
	Input           []InputItem        `json:"input,omitempty"`
	ModelProvider   string             `json:"modelProvider,omitempty"`
	Model           string             `json:"model,omitempty"`
	Profile         string             `json:"profile,omitempty"`
	ReasoningEffort string             `json:"reasoningEffort,omitempty"`
	NonInteractive  *bool              `json:"nonInteractive,omitempty"`
	LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
	// Manager starts this session as a Manager of the sessions its hub can
	// reach. It is a start-time property, not a setting: the daemon persists it
	// with the session, so a resume keeps it and nothing can turn it off.
	Manager bool `json:"manager,omitempty"`
}

type ThreadStartResponse struct {
	Thread Thread `json:"thread"`
	Turn   Turn   `json:"turn"`
}

type ThreadResumeParams struct {
	Ref     string `json:"ref,omitempty"`
	Session string `json:"sessionId,omitempty"`
}

type ThreadResumeResponse struct {
	Thread Thread `json:"thread"`
}

type ThreadForkParams struct {
	Ref           string `json:"ref"`
	EditedInput   string `json:"editedInput,omitempty"`
	Label         string `json:"label,omitempty"`
	ModelProvider string `json:"modelProvider,omitempty"`
	Model         string `json:"model,omitempty"`
	// DeferInput forks at the source turn WITHOUT appending a replacement
	// message: the child thread holds only the entries before the turn, and
	// the turn's original text comes back in ThreadForkResponse.OriginalInput
	// so the client can stage it for editing and explicit submission (the
	// fork never auto-runs the message). Mutually exclusive with EditedInput
	// and Aside.
	DeferInput bool `json:"deferInput,omitempty"`
	// Aside forks a local evener thread at its tip instead of at a source turn:
	// the child is a complete copy of the parent session (same permissions and
	// config via the inherited session meta) and opens as a side thread. Aside
	// is mutually exclusive with SourceItemKey, EditedInput, DeferInput, and
	// Label, and is only supported for local evener threads.
	Aside bool `json:"aside,omitempty"`
	// SourceItemKey names the divergence position as an item key
	// (transcriptindex.ItemKey): a 0-based entry ordinal into the parent
	// transcript's ENTRY list — every entry, not just the ones that opened a
	// turn — plus the content part that opened the item. The hub parses it
	// with parseSourceItemKey, which turns the ordinal into the matching
	// 1-based entry index and hands that straight to
	// agent.ForkSessionAtUserTurn. Despite embedding a turn id, the key is
	// NOT read as one for this: send ThreadItem.TranscriptKey, never Turn.ID;
	// the entry ordinal a live turn's own id implies coincides with its
	// transcript entry index only on a transcript replayed from disk,
	// because every live turn minter numbers turns off its own counter.
	// Required unless Aside.
	SourceItemKey string `json:"sourceItemKey,omitempty"`
}

type ThreadForkResponse struct {
	Thread Thread `json:"thread"`
	// OriginalInput is the source turn's original user text, set only when
	// the fork was requested with DeferInput.
	OriginalInput string `json:"originalInput,omitempty"`
}

type TurnStartParams struct {
	ManagerProof       *SessionActorProof `json:"managerProof,omitempty"`
	Ref                string             `json:"ref,omitempty"`
	ThreadID           string             `json:"threadId,omitempty"`
	ClientMutationID   string             `json:"clientMutationId"`
	ExpectedInstanceID string             `json:"expectedInstanceId"`
	Input              []InputItem        `json:"input,omitempty"`
}

type TurnStartResponse struct {
	Turn    Turn            `json:"turn"`
	Receipt MutationReceipt `json:"receipt"`
}

// Control mutations are session-scoped: they apply to whatever the session is
// running rather than to a turn the client names. By the time a user's intent
// reaches the daemon the session may already be on a later turn, and that is
// fine — the intent should apply as soon as possible instead of bouncing. Each
// retry-safe mutation still carries ExpectedInstanceID: a stable workspace ref
// can survive thread/clear while its live session instance changes, and a
// delayed old-generation intent must not run against the replacement.
type TurnSteerParams struct {
	ManagerProof       *SessionActorProof `json:"managerProof,omitempty"`
	Ref                string             `json:"ref,omitempty"`
	ThreadID           string             `json:"threadId,omitempty"`
	ClientMutationID   string             `json:"clientMutationId"`
	ExpectedInstanceID string             `json:"expectedInstanceId"`
	Input              []InputItem        `json:"input,omitempty"`
}

// TurnInterruptParams cancels whatever turn the session is running. The receipt
// names the turn actually cancelled, which is how a client learns what it
// stopped without having had to name it first.
type TurnInterruptParams struct {
	Ref                string `json:"ref,omitempty"`
	ThreadID           string `json:"threadId,omitempty"`
	ClientMutationID   string `json:"clientMutationId"`
	ExpectedInstanceID string `json:"expectedInstanceId"`
}

// TurnQueueParams queues a user message during a running turn for processing
// after the active turn completes. The daemon enqueues immediately and returns;
// no turn id is reserved or returned.
type TurnQueueParams struct {
	ManagerProof       *SessionActorProof `json:"managerProof,omitempty"`
	Ref                string             `json:"ref"`
	ClientMutationID   string             `json:"clientMutationId"`
	ExpectedInstanceID string             `json:"expectedInstanceId"`
	Input              []InputItem        `json:"input,omitempty"`
}

type MutationProjectionState string

const (
	MutationProjectionPending   MutationProjectionState = "pending"
	MutationProjectionReflected MutationProjectionState = "reflected"
	MutationProjectionRemoved   MutationProjectionState = "removed"
)

type MutationDisposition string

const (
	MutationDispositionApplied  MutationDisposition = "applied"
	MutationDispositionReplayed MutationDisposition = "replayed"
)

type MutationReceipt struct {
	Actor            SessionActor            `json:"actor,omitzero"`
	ClientMutationID string                  `json:"clientMutationId"`
	Disposition      MutationDisposition     `json:"disposition"`
	ThreadID         string                  `json:"threadId"`
	InstanceID       string                  `json:"instanceId,omitempty"`
	TurnID           string                  `json:"turnId,omitempty"`
	QueueEntryIDs    []string                `json:"queueEntryIds,omitempty"`
	ProjectionState  MutationProjectionState `json:"projectionState"`
	// ConsumedClientMutationIDs names the client mutation ids a drain's OWN
	// transition consumed (currently the only mutation that folds other
	// queued entries into itself). It rides the durable mutation result, so
	// a replayed disposition carries the same ids the first execution did —
	// unlike queueChanged's own copy of this fact, a replay needs no live
	// push to settle those records. Absent on every receipt
	// that is not a drain's.
	ConsumedClientMutationIDs []string `json:"consumedClientMutationIds,omitempty"`
}

type PendingMutation struct {
	Actor            SessionActor            `json:"actor,omitzero"`
	ClientMutationID string                  `json:"clientMutationId"`
	Method           string                  `json:"method"`
	Input            []InputItem             `json:"input,omitempty"`
	ExecutionState   string                  `json:"executionState"`
	TurnID           string                  `json:"turnId,omitempty"`
	QueueEntryIDs    []string                `json:"queueEntryIds,omitempty"`
	ProjectionState  MutationProjectionState `json:"projectionState"`
}

type TurnSteerResponse struct {
	Receipt MutationReceipt `json:"receipt"`
}

type TurnInterruptResponse struct {
	Receipt MutationReceipt `json:"receipt"`
}

type TurnQueueResponse struct {
	Receipt MutationReceipt `json:"receipt"`
}

// GoalSetParams sets (or clears) the session's /goal objective. An empty
// Objective clears the goal. The daemon forwards it to Session.SetGoal /
// ClearGoal and returns immediately; the goal loop runs asynchronously.
type GoalSetParams struct {
	Ref       string `json:"ref"`
	Objective string `json:"objective,omitempty"`
}

// GoalSetResponse reports whether the goal loop started immediately. Started is
// false when the objective was cleared, when a turn is already running (its gate
// picks the goal up after the current turn), or when no immediate start was
// possible — in those cases the goal is still set; it just begins after the
// current turn rather than right away.
type GoalSetResponse struct {
	Started bool `json:"started"`
}

// TurnDrainAsSteerParams is the wire shape for turn/drainAsSteer (kata
// 0bq1 force-steer combined action). Pops every queued message and sends
// them to the in-flight turn as a single STEERING message. Input lets clients
// atomically append the current composer payload before the drain.
type TurnDrainAsSteerParams struct {
	ManagerProof          *SessionActorProof `json:"managerProof,omitempty"`
	Ref                   string             `json:"ref"`
	ClientMutationID      string             `json:"clientMutationId"`
	ExpectedInstanceID    string             `json:"expectedInstanceId"`
	ExpectedQueueRevision uint64             `json:"expectedQueueRevision"`
	Input                 []InputItem        `json:"input,omitempty"`
}

type TurnDrainAsSteerResponse struct {
	Receipt MutationReceipt `json:"receipt"`
}

// TurnPromoteQueuedAsSteerParams is the wire shape for
// turn/promoteQueuedAsSteer (issue #22 per-message promote). Index selects
// one entry of the session's FIFO input queue (matching the position shown
// in the queue preview); the daemon removes just that entry and injects it
// as a user-sourced steering message into the in-flight turn — or, when no
// turn is running, as pending steering the next turn delivers — leaving the
// other queued messages in place. ExpectedEntryID, when non-empty, must
// match the id the daemon minted for that entry (surfaced via
// QueueState.IDs): the queue head can be consumed mid-turn, so a bare index
// from an older snapshot may point at a different message — a mismatch is a
// Conflict, not a wrong-message promote (review F1). The daemon returns
// Conflict when the index is out of range or the expected id no longer
// matches. Unlike turn/steer, no turn needs to be in flight: a queue a Stop
// parked is promoteable as the queue strip's "run this now".
type TurnPromoteQueuedAsSteerParams struct {
	ManagerProof       *SessionActorProof `json:"managerProof,omitempty"`
	Ref                string             `json:"ref"`
	Index              int                `json:"index"`
	ClientMutationID   string             `json:"clientMutationId"`
	ExpectedInstanceID string             `json:"expectedInstanceId"`
	ExpectedEntryID    string             `json:"expectedEntryId"`
}

type TurnPromoteQueuedAsSteerResponse struct {
	Receipt MutationReceipt `json:"receipt"`
}

// TurnCancelQueuedParams is the wire shape for turn/cancelQueued (issue
// #23): removes the queued follow-up at Index so it is never consumed. It
// is also the removal half of the web UI's edit action (edit = restore the
// full text from QueueState.Texts into the composer, then cancel the queued
// copy). ExpectedEntryID plays the same review-F1 role as on
// turn/promoteQueuedAsSteer: when non-empty it must match the id the daemon
// minted for the entry at Index, so a queue that shifted under the client's
// snapshot is a Conflict rather than removing the wrong message. Unlike
// promote, cancel does NOT require an in-flight turn — a queued entry is
// cancellable whenever it is still queued. The daemon returns Conflict when
// the index is out of range (e.g. the entry was already consumed) or the
// expected id no longer matches.
type TurnCancelQueuedParams struct {
	Ref                string `json:"ref"`
	Index              int    `json:"index"`
	ClientMutationID   string `json:"clientMutationId"`
	ExpectedInstanceID string `json:"expectedInstanceId"`
	ExpectedEntryID    string `json:"expectedEntryId"`
}

// TurnCancelQueuedResponse echoes what turn/cancelQueued removed.
// RemovedText is the entry's full untruncated text (the client normally
// already holds it via QueueState.Texts; the echo lets it verify the
// removal matched its snapshot). RemovedImages counts image attachments
// that were on the entry and are NOT restored by an edit — the client warns
// the user to re-attach before resending rather than silently dropping
// them.
type TurnCancelQueuedResponse struct {
	RemovedText   string          `json:"removedText"`
	RemovedImages int             `json:"removedImages,omitempty"`
	Receipt       MutationReceipt `json:"receipt"`
}

type ThreadCompactStartParams struct {
	Ref string `json:"ref"`
}

type ThreadForceStopParams struct {
	Ref string `json:"ref"`
	// ExpectedDaemon carries the exact ownership evidence the resident UI
	// resolved for this daemon; nil preserves existing ref-only callers.
	ExpectedDaemon *DaemonIdentity `json:"expectedDaemon,omitempty"`
}

type ThreadShutdownParams struct {
	Ref string `json:"ref"`
}

type ThreadClearParams struct {
	Ref                string `json:"ref"`
	ClientMutationID   string `json:"clientMutationId"`
	ExpectedInstanceID string `json:"expectedInstanceId"`
}

type ThreadClearResponse struct {
	Thread  Thread          `json:"thread"`
	Ref     string          `json:"ref"`
	Receipt MutationReceipt `json:"receipt"`
}

type ThreadModelSetParams struct {
	Ref           string `json:"ref"`
	ModelProvider string `json:"modelProvider"`
	Model         string `json:"model"`
}

// ThreadNameSetParams renames a thread (user-chosen title).
type ThreadNameSetParams struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

// ThreadNameChangedParams reports a thread title update. Source records the
// title provenance when known ("prompt", "compaction", or "user").
type ThreadNameChangedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	Name     string `json:"name"`
	Source   string `json:"source,omitempty"`
}

// ThreadModelChangedParams reports a mid-session model/provider switch.
// ReasoningEffortLevels and SupportsReasoning describe the NEW profile so a
// client's effort picker re-keys without a separate model/list round trip.
type ThreadModelChangedParams struct {
	ThreadID              string   `json:"threadId"`
	Ref                   string   `json:"ref"`
	ModelProvider         string   `json:"modelProvider"`
	Model                 string   `json:"model"`
	ReasoningEffortLevels []string `json:"reasoningEffortLevels,omitempty"`
	SupportsReasoning     bool     `json:"supportsReasoning,omitempty"`
}

// ThreadReasoningEffortChangedParams reports a mid-session reasoning-effort
// change.
type ThreadReasoningEffortChangedParams struct {
	ThreadID        string `json:"threadId"`
	Ref             string `json:"ref"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// ThreadReasoningEffortSetParams sets the reasoning effort on a running session.
// An empty ReasoningEffort resets to the session/model default; "none" disables
// reasoning. The daemon clamps the value to what the active model supports.
type ThreadReasoningEffortSetParams struct {
	Ref             string `json:"ref"`
	ReasoningEffort string `json:"reasoningEffort"`
}

// ThreadVisionModelSetParams sets the vision side-channel routing on a running
// session. VisionModel carries the whole setting: "" describes with the
// session's active model, "off" disables the side-channel, and any other value
// is a "model" or "provider/model" ref — a single string because a
// provider/model split cannot express the first two states.
type ThreadVisionModelSetParams struct {
	Ref         string `json:"ref"`
	VisionModel string `json:"visionModel"`
}

// ThreadVisionModelChangedParams reports a mid-session vision-model change.
type ThreadVisionModelChangedParams struct {
	ThreadID    string `json:"threadId"`
	Ref         string `json:"ref"`
	VisionModel string `json:"visionModel"`
}
