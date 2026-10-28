package appwire

import "encoding/json"

type Thread struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	// ProjectID and ProjectPath are hub-resolved identity fields. They are
	// intentionally separate from CWD: a linked worktree may have a different
	// working directory while still belonging to the same canonical project.
	// Empty values mean the source could not resolve a local project (for
	// example, a pathless external thread), which clients must treat as
	// presentation-only.
	ProjectID     string       `json:"projectId,omitempty"`
	ProjectPath   string       `json:"projectPath,omitempty"`
	ForkedFromID  string       `json:"forkedFromId,omitempty"`
	Preview       string       `json:"preview"`
	Ephemeral     bool         `json:"ephemeral"`
	ModelProvider string       `json:"modelProvider"`
	CreatedAt     int64        `json:"createdAt"`
	UpdatedAt     int64        `json:"updatedAt"`
	Status        ThreadStatus `json:"status"`
	Path          string       `json:"path,omitempty"`
	CWD           string       `json:"cwd"`
	CLIVersion    string       `json:"cliVersion"`
	Source        string       `json:"source"`
	ThreadSource  string       `json:"threadSource,omitempty"`
	AgentNickname string       `json:"agentNickname,omitempty"`
	AgentRole     string       `json:"agentRole,omitempty"`
	GitInfo       *GitInfo     `json:"gitInfo,omitempty"`
	Name          string       `json:"name,omitempty"`
	Turns         []Turn       `json:"turns,omitempty"`
	Evener        EvenerThread `json:"evener"`
}

type GitInfo struct {
	SHA       string `json:"sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
	OriginURL string `json:"originUrl,omitempty"`
}

type ThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags,omitempty"`
}

type TaskSummary struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
}

// TaskAggregate carries the authoritative task-list progress for a thread
// snapshot. A nil *TaskAggregate on EvenerThread means the source cannot know
// the session's task state; a present zero is an authoritative empty list.
type TaskAggregate struct {
	Total     int          `json:"total"`
	Done      int          `json:"done"`
	Cancelled int          `json:"cancelled,omitempty"`
	Remaining int          `json:"remaining,omitempty"`
	Current   *TaskSummary `json:"current,omitempty"`
}

type EvenerThread struct {
	// ResumeRequired means recovery stopped this session and automatic actions
	// must wait for an explicit thread/resume. Saved transcripts remain readable.
	ResumeRequired bool `json:"resumeRequired,omitempty"`
	// ResumeOnlyFoldable means a turn/start sent on this connection right now
	// would be admitted under the resume-only carve-out: the session merely needs
	// its resume, which the send folds into itself. The hub stamps it only when
	// that admission would actually succeed, so a client can offer Send from the
	// hub's own answer instead of inferring the shape from ResumeRequired plus a
	// missing send capability, which this same overlay also sets for a Stop drain,
	// an unconfirmed force-stop exit, the connection-recovery fence, and a daemon
	// on an incompatible protocol - four shapes whose turn/start the hub still
	// refuses.
	ResumeOnlyFoldable bool   `json:"resumeOnlyFoldable,omitempty"`
	Ref                string `json:"ref"`
	InstanceID         string `json:"instanceId,omitempty"`
	ParentRef          string `json:"parentRef,omitempty"`
	Kind               string `json:"kind,omitempty"`
	Profile            string `json:"profile,omitempty"`
	// TurnCount is the daemon's total completed model-response count. It stays
	// independent of Turns so a bounded metadata read never loads the transcript.
	TurnCount        int                `json:"turnCount,omitempty"`
	ActiveTurnID     string             `json:"activeTurnId,omitempty"`
	ContextPressure  float64            `json:"contextPressure,omitempty"`
	ContextUsed      int                `json:"contextUsed,omitempty"`
	ContextWindow    int                `json:"contextWindow,omitempty"`
	ContextRemaining int                `json:"contextRemaining,omitempty"`
	ContextAssumed   bool               `json:"contextAssumed,omitempty"`
	Capabilities     ThreadCapabilities `json:"capabilities"`
	Diagnostics      *EvenerDiagnostics `json:"diagnostics,omitempty"`
	// Queue carries authoritative queue depth + preview for the per-session
	// input queue. Both UIs derive their queue-preview chrome
	// from this field rather than mirroring queue mutations locally, which
	// fixes multi-client incoherence and post-reload state. The empty zero
	// value (Depth==0, Preview==nil) means "no queued messages".
	Queue            QueueState        `json:"queue"`
	PendingMutations []PendingMutation `json:"pendingMutations,omitempty"`
	// MutationStateAuthoritative identifies a daemon snapshot that can settle
	// uncertain sends. Saved transcript data cannot prove a mutation absent.
	MutationStateAuthoritative bool `json:"mutationStateAuthoritative,omitempty"`
	// Tasks carries the task-list progress for a session snapshot. It is nil
	// when the source cannot authoritatively read task state, including an old
	// daemon or a missing persisted task file; a present zero is real zero.
	Tasks *TaskAggregate `json:"tasks,omitempty"`
	// Goal carries the session's /goal state when a goal is set, else nil.
	// It powers `/goal status` and a future status-bar indicator without a
	// bespoke transport — like Queue, it is structured per-session state read
	// from the already-fetched thread snapshot.
	Goal *GoalState `json:"goal,omitempty"`
	// HumanNote carries the human's session whiteboard, line breaks kept, when
	// set, else empty. It powers the shared-notes display without a bespoke
	// transport — like Goal, it is structured per-session state read from the
	// already-fetched thread snapshot.
	HumanNote string `json:"humanNote,omitempty"`
	// AgentNote carries the agent's session whiteboard, line breaks kept, when
	// set, else empty. It is read from the already-fetched thread snapshot like
	// HumanNote.
	AgentNote string `json:"agentNote,omitempty"`
	// SessionURLs carries the session's shared-notes URL list when set, else
	// nil. Empty/nil means no links. It is read from the already-fetched
	// thread snapshot like HumanNote.
	SessionURLs []SessionURL `json:"sessionUrls,omitempty"`
	// ArtifactsDir is the session's retained artifacts directory on the hub
	// that serves it, empty when no local path names one (a remote thread, an
	// unknown ref, or a hub serving no state directory). Agent reports filed
	// there are linked by file:// URL in assistant prose; the transcript turns
	// such a link into a document-pane request, and this is the directory the
	// hub's doc route serves that request out of. Clients compare a link
	// against it rather than guessing whether the hub can open it.
	ArtifactsDir string `json:"artifactsDir,omitempty"`
	// Usage, WorkMillis, and ActiveTurnStartedAt are the daemon's live
	// working-state/token metrics (WS2), served from the daemon's materialized
	// thread envelope, which is refreshed at the turn boundaries that move
	// them. Usage is a pointer
	// (unlike the other two scalars) because EvenerUsage is a value struct whose
	// omitempty would never omit — nil is how a fresh thread, an old daemon, or a
	// source-backed thread that omits the field signals "no token data" rather
	// than rendering ↑0 ↓0.
	// ActiveTurnStartedAt is Unix epoch MILLISECONDS (matching WorkMillis's
	// scale, and the web reducer's epoch-ms read), 0 when no turn is running.
	// Emitting seconds here would mix units with the consumer's ms clock.
	Usage               *EvenerUsage `json:"usage,omitempty"`
	WorkMillis          int64        `json:"workMillis,omitempty"`
	ActiveTurnStartedAt int64        `json:"activeTurnStartedAt,omitempty"`
	// Cost is the session's cumulative estimated dollar total — the "~$X.XX"
	// string EstimateCost derives from Usage at the registry row's cost, the
	// session-scope sibling of the per-turn Turn.Cost (same shape, same "~"
	// estimate marker). Empty (omitted) when Usage is nil or the row carries
	// no cost: an honest "unknown" that renders no chip, never a
	// misleading "~$0.00" — the only "~$0.00" a consumer sees is a genuinely
	// sub-cent priced session. Derived from the authoritative full-session
	// cumulative Usage (the same total the token cluster trusts), never a
	// page of client-loaded turns, so it is pagination-proof by construction.
	// Stamped beside Usage at each EvenerThread producer (the server's live
	// appThread and the hub's past-entry hydrate), so it stays current across
	// snapshots exactly as WorkMillis/Usage do. The whole cumulative Usage is
	// priced at the thread's CURRENT model: after a mid-session model switch,
	// earlier turns are repriced at current rates (the flat CumulativeUsage
	// carries no per-model breakdown; identical to the legacy computation).
	Cost string `json:"cost,omitempty"`
	// FailedToolCalls is how many of this session's tool calls failed — the
	// session-scale count of exactly what the transcript marks with a failure
	// glyph — a tool result carrying an error. A shell command that ran and
	// exited nonzero is NOT counted: it shows as a small inline mark, not a
	// failure (Jesse, 2026-10-16). It answers "did anything go wrong in here" without
	// reading the transcript, which a client cannot answer for itself: a
	// windowed thread/read hands it a fraction of the session, and a count over
	// that fraction would report a comforting "0 failed" for a session full of
	// failures nobody has scrolled to.
	//
	// TWO PRODUCERS, one rule and one scope. A running session's count comes
	// from the daemon, which counts failures as it writes them to the transcript
	// and seeds from the file on resume — complete for a live session, where
	// re-reading the file would return a floor (it is still being appended to)
	// and counting in-memory history would shed whatever compaction summarized
	// away. A cold session's count comes from the hub scanning the finished
	// transcript. Both apply agent/transcript.FailedToolResult over the
	// session's own span, so the figure does not move when a session goes cold.
	//
	// A pointer, because 0 and unknown are different claims and only one of
	// them is good news. Zero means the whole session was counted and nothing
	// failed. Nil means nobody counted: the transcript is unreadable (a legacy
	// format_version 1 file, or a missing one), the session has no transcript,
	// or the producer does not derive the figure at all — an old daemon, a
	// source-backed thread that omits the field, or the hub's per-entry list
	// sweeps, which cannot afford a scan per session. Consumers render nil as
	// nothing, never as a fabricated zero.
	FailedToolCalls *int `json:"failedToolCalls,omitempty"`
	// AskPending is true while an ask_user question is unanswered. Additive:
	// absent on old daemons and source-backed threads that omit the field,
	// decoding as false.
	AskPending bool `json:"askPending,omitempty"`
	// PendingQuestion is the first question of the session's pending ask and
	// how many the ask holds (S1b). The daemon reads both from one sample of
	// the pending set, so it is present exactly when AskPending is true; it is
	// absent from an older daemon. Snapshot-only: thread/status/changed
	// carries AskPending, and nothing carries the question's text.
	PendingQuestion *PendingQuestion `json:"pendingQuestion,omitempty"`
	// Failure summarizes the failed turn the session rests on (S1c). It is
	// present only while the thread's status is systemError, and absent from
	// an older daemon and for a failure that recorded no diagnostic.
	// Snapshot-only: no notification carries it.
	Failure *ThreadFailure `json:"failure,omitempty"`
	// PendingEscalations is the M7 surface-on-entry snapshot: the redacted approval
	// cards for any sandbox-exemption escalations currently blocked on this session,
	// so a client entering / reconnecting to / not-having-seen-live this session
	// surfaces the card(s). It is a HUMAN-CLIENT field only — it is never part of the
	// model's transcript or any model-visible projection. Absent on old daemons
	// and source-backed threads that omit the field.
	PendingEscalations []SandboxEscalationRequested `json:"pendingEscalations,omitempty"`
	// ReasoningEffort, ReasoningEffortLevels, and SupportsReasoning are the
	// live reasoning-effort settings for the session's current profile, so a
	// cold-attached client can render both settings and populate pickers
	// with no prior thread/model/changed or thread/reasoning-effort/changed
	// notification. ModelProvider (on Thread, not here) stays the model field.
	ReasoningEffort       string   `json:"reasoningEffort,omitempty"`
	ReasoningEffortLevels []string `json:"reasoningEffortLevels,omitempty"`
	SupportsReasoning     bool     `json:"supportsReasoning,omitempty"`
	// VisionModel is the session's vision side-channel setting: "" describes
	// with the session model, "off" disables the side-channel, anything else is
	// a model ref. Snapshot-only like the effort fields beside it; live updates
	// arrive as thread/vision-model/changed.
	VisionModel string `json:"visionModel,omitempty"`
	// Activity is the pulse meter of a live root session's whole tree (spec
	// 16.4, S5): the root and every in-process descendant. It rides thread/list
	// rows only, never a thread/read snapshot, because nothing announces its
	// changes to a subscriber; the hub serves it through evener/activity/read,
	// never navigation. Absent on descendant rows and from an older daemon.
	Activity *ThreadActivity `json:"activity,omitempty"`
	// LastTurnEndedAt is when the session's last turn ended, in Unix
	// milliseconds (S4); absent before any turn has ended and from an older
	// daemon. The hub compares it with its seen-through marker to tell a
	// Finished session from an Idle one. Snapshot-only: no notification
	// carries it.
	LastTurnEndedAt int64 `json:"lastTurnEndedAt,omitempty"`
	// LastMessage is the opening of the session's last agent message (S1d):
	// one line of at most MaxMessageExcerptRunes, the agent's own words only,
	// never its reasoning or a tool's output. Absent until the session has
	// written a message, and from an older daemon. Snapshot-only: no
	// notification carries it.
	LastMessage string `json:"lastMessage,omitempty"`
	// Subagents tallies a live root session's whole delegate tree (S3), read
	// from the root's delegate controller when the row is listed. It rides
	// thread/list root rows only, when the tree has at least one subagent, and
	// never a thread/read snapshot: no notification announces its changes.
	Subagents *SubagentTally `json:"subagents,omitempty"`
	// Access is what the session's sandbox lets it reach (S15): the sandbox
	// mode it started under and whether that sandbox allows the network. Every
	// current producer sets it; it is absent from an older daemon or hub, which
	// a client reads as "not known". Snapshot-only: a session's sandbox is
	// fixed when it starts, so no notification carries it.
	Access *ThreadAccess `json:"access,omitempty"`
}

// ThreadAccess is a session's sandbox mode and network setting (spec 8.6,
// S15). Sandbox is the mode name a session starts with ("off", "read-only",
// "workspace-write" or "restricted"). Network is true when the session may use
// the network: always for "off", and for a sandboxed session unless it started
// with the network turned off.
type ThreadAccess struct {
	Sandbox string `json:"sandbox"`
	Network bool   `json:"network"`
}

// ThreadActivity is one pulse meter sample. Minutes holds seven one-minute
// counts, oldest first, the last ending when the row was listed; each counts
// the transcript items that finished and the tool output events in that minute.
// LastActivityAt is the Unix-millisecond time of the tree's newest transcript
// motion (a turn or item starting, a message or reasoning summary streaming, an
// item finishing, a tool writing output), or the time the daemon began serving
// this session when nothing has moved since.
type ThreadActivity struct {
	Minutes        []int `json:"minutes"`
	LastActivityAt int64 `json:"lastActivityAt"`
	// LatestIntent is the newest intent a tool call of the session's own root
	// turn stated ("Reading the board's row tests."), one line of at most
	// MaxIntentRunes. A Working row shows it in place of the job it is
	// running, because it says what the job is for. Empty until this turn's
	// first tool call that stated one, cleared when a turn begins, and absent
	// from a daemon that predates it.
	LatestIntent string `json:"latestIntent,omitempty"`
	// LastMovedAt is LastActivityAt counting only real motion: absent until
	// the tree has moved since the daemon began serving the session, so a
	// daemon restart is not news to a client comparing it with a seen mark.
	LastMovedAt int64 `json:"lastMovedAt,omitempty"`
}

// SubagentTally counts a live root session's subagents, at every depth, by how
// each one's latest run stands (spec 9, S3). Running: no run has ended yet, or
// a run is open again after the last one ended. Failed: the latest run ended
// failed or exhausted. Done: it ended any other way (completed, cancelled or
// stopped), including a subagent idle between runs.
type SubagentTally struct {
	Running int `json:"running"`
	Failed  int `json:"failed"`
	Done    int `json:"done"`
}

// ThreadFailure summarizes the failed turn a session rests on (S1c): the
// failure's headline (the diagnostic classifier's title, such as "Provider
// error" or "Usage limit reached", cut to one line of MaxFailureTitleRunes)
// and its structured cause. It never carries the failure's message, which can
// quote a provider's error body; the transcript's error row has that.
type ThreadFailure struct {
	Title string           `json:"title,omitempty"`
	Cause *DiagnosticCause `json:"cause,omitempty"`
}

// PendingQuestion is the first question of a session's pending ask (S1b): what
// its Needs you row says ("Question · keep or drop the implied options?") and
// the option labels a long-press preview lists. The daemon cuts Question and
// each label to one line (Excerpt, at MaxQuestionTextRunes and
// MaxQuestionOptionRunes) and sends at most MaxQuestionOptions labels. Count
// is how many questions the pending ask holds, so a client can say "Question
// 1 of 2".
type PendingQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Count    int      `json:"count"`
}

// GoalState is the wire representation of a session's /goal. Status is the
// lifecycle status ("active", "complete", "blocked"); Iterations is the number
// of continuation turns taken. A nil *GoalState on EvenerThread means no goal is
// set.
type GoalState struct {
	Objective  string `json:"objective,omitempty"`
	Status     string `json:"status"`
	Iterations int    `json:"iterations"`
}

// SessionURL is one entry in a session's shared-notes URL list.
type SessionURL struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Label   string `json:"label,omitempty"`
	AddedBy string `json:"addedBy,omitempty"`
	AddedAt int64  `json:"addedAt,omitempty"`
}

// EvenerUsage carries a evener session's cumulative self-only token totals for
// the status row. A nil *EvenerUsage on EvenerThread means no token data (an old
// daemon, a source-backed thread that omits the field, or a session with zero
// usage) — the clusters hide rather than render ↑0 ↓0.
type EvenerUsage struct {
	InputTokens     int64 `json:"inputTokens,omitempty"`
	OutputTokens    int64 `json:"outputTokens,omitempty"`
	CacheReadTokens int64 `json:"cacheReadTokens,omitempty"`
	TotalTokens     int64 `json:"totalTokens,omitempty"`
}

// QueueState is the wire representation of a session's per-input queue
// (kata r80p). Depth is len(Preview) at projection time; Preview entries
// are FIFO with the head at index 0 and have been truncated to a single
// line so the UI can render them without further processing. IDs is
// FIFO-aligned with Preview: each entry's stable queue-entry id, minted by
// the daemon at enqueue time. turn/promoteQueuedAsSteer echoes an id back
// as expectedEntryId so a queue that shifted under the client's snapshot is
// rejected instead of promoting the wrong message (review F1, issue #22).
// Texts is FIFO-aligned with Preview and carries each entry's FULL
// untruncated text, so the edit affordance (issue #23) can restore the
// complete message into the composer before turn/cancelQueued removes the
// entry — the preview line alone would silently truncate multi-line
// messages. Absent on old daemons; clients must treat a missing Texts as
// "edit unavailable" rather than falling back to the truncated preview.
type QueueState struct {
	Depth             int      `json:"depth,omitempty"`
	Revision          uint64   `json:"revision"`
	Preview           []string `json:"preview,omitempty"`
	IDs               []string `json:"ids,omitempty"`
	ClientMutationIDs []string `json:"clientMutationIds,omitempty"`
	Texts             []string `json:"texts,omitempty"`
	// SkillNames is FIFO-aligned with Preview and carries each entry's
	// canonical skill selections (empty slice for an entry with none), so
	// editing or returning a queued entry restores its chips instead of
	// silently dropping them. Absent on old daemons; clients must treat a
	// missing SkillNames as "selections unavailable", never as "no
	// selections".
	SkillNames [][]string `json:"skillNames,omitempty"`
	// CommandNames is FIFO-aligned with Preview and retains exact command
	// identities independently from skill selections when editing or returning.
	CommandNames [][]string `json:"commandNames,omitempty"`
	// Mentions is FIFO-aligned with Texts, retaining each atom's exact kind
	// and UTF-16 location. It is editing metadata, not activation authority.
	Mentions [][]InputMention `json:"mentions,omitempty"`
}

// ThreadQueueChangedParams is the params shape for thread/queueChanged
// (kata r80p). Queue mirrors the queue field on EvenerThread so consumers can
// store it verbatim on the cached thread state. ConsumedClientMutationIDs
// does NOT belong on Queue: it is a one-shot fact about THIS push's own
// transition (currently: a drain folding the queue into steering), never a
// property of the durable queue a client caches — a client settles those
// optimistic records by positive evidence instead of inferring consumption
// from sequence order. Absent on old daemons and on every push
// that is not the consuming transition.
type ThreadQueueChangedParams struct {
	ThreadID                  string     `json:"threadId"`
	Ref                       string     `json:"ref"`
	Queue                     QueueState `json:"queue"`
	ConsumedClientMutationIDs []string   `json:"consumedClientMutationIds,omitempty"`
}

// TaskUpdatedParams is the params shape for evener/task/updated: the session's
// task-list progress after a change, so a client refreshes the status row
// event-driven instead of polling evener/tasks/list.
type TaskUpdatedParams struct {
	ThreadID  string       `json:"threadId"`
	Ref       string       `json:"ref"`
	Total     int          `json:"total"`
	Done      int          `json:"done"`
	Cancelled int          `json:"cancelled,omitempty"`
	Remaining int          `json:"remaining,omitempty"`
	Current   *TaskSummary `json:"current,omitempty"`
}

// GoalUpdatedParams is the complete session goal state after a mutation. Goal
// is deliberately not omitempty: nil explicitly clears a previously known goal.
type GoalUpdatedParams struct {
	ThreadID string     `json:"threadId"`
	Ref      string     `json:"ref"`
	Goal     *GoalState `json:"goal"`
}

// NotesHumanSetParams sets the human's session whiteboard. It follows the
// retry-safe-mutation shape (clientMutationId, expected-instance fencing) so a
// hub retry of the outer RPC converges instead of double-applying.
type NotesHumanSetParams struct {
	Ref                string `json:"ref"`
	ClientMutationID   string `json:"clientMutationId"`
	ExpectedInstanceID string `json:"expectedInstanceId"`
	Note               string `json:"note,omitempty"`
}

// NotesHumanSetResponse reports the canonical whiteboard and durable acceptance
// receipt. Acceptance does not imply the model has consumed its notification.
type NotesHumanSetResponse struct {
	Note    string          `json:"note"`
	Receipt MutationReceipt `json:"receipt"`
}

// UrlsRemoveParams removes one URL list entry by id. It follows the
// retry-safe-mutation shape like NotesHumanSetParams.
type UrlsRemoveParams struct {
	Ref                string `json:"ref"`
	ClientMutationID   string `json:"clientMutationId"`
	ExpectedInstanceID string `json:"expectedInstanceId"`
	ID                 string `json:"id"`
}

// UrlsRemoveResponse acknowledges a URL list removal. It carries no state: the
// authoritative list arrives via the evener/urls/updated push.
type UrlsRemoveResponse struct{}

// NotesUpdatedParams is the session whiteboard state after a mutation.
type NotesUpdatedParams struct {
	ThreadID  string `json:"threadId"`
	Ref       string `json:"ref"`
	HumanNote string `json:"humanNote,omitempty"`
	AgentNote string `json:"agentNote,omitempty"`
}

// UrlsUpdatedParams is the session URL list after a mutation.
type UrlsUpdatedParams struct {
	ThreadID string       `json:"threadId"`
	Ref      string       `json:"ref"`
	URLs     []SessionURL `json:"urls,omitempty"`
}

// SandboxEscalationRequested is the payload of a
// evener/sandbox/escalation/requested notification (M7): a harness-raised approval
// card for a single sandbox denial. It carries only what the human needs to decide
// — never file contents. DeniedPath is the FULL literal path for informed consent
// (only non-sensitive containment denials escalate, so the full path is safe; a
// sensitive path, which never escalates, degrades to "<denied>" as a defensive
// floor). Kind selects the card shape; the shell fields (Command/OutputSoFar/
// PartiallyRan) are reserved and empty in v1 (file-tool escalation only — see the
// M7 spec on why bwrap masking makes shell escalation unbuildable). It is never
// appended to the model's transcript.
type SandboxEscalationRequested struct {
	// ThreadID/Ref identify the SESSION this escalation belongs to, so a client can
	// route it by session (enqueue for a non-viewed session, answer the right one)
	// rather than assuming the currently-viewed session — like every other
	// thread-scoped notification.
	ThreadID     string `json:"threadId"`
	Ref          string `json:"ref"`
	EscalationID string `json:"escalationId"`
	Mode         string `json:"mode"`
	Tool         string `json:"tool"`
	Kind         string `json:"kind"`
	DeniedPath   string `json:"deniedPath"`
	Command      string `json:"command,omitempty"`
	OutputSoFar  string `json:"outputSoFar,omitempty"`
	PartiallyRan bool   `json:"partiallyRan,omitempty"`
}

// SandboxEscalationResolved is the payload of a evener/sandbox/escalation/resolved
// notification (M7, wire-honesty spec Part B): a previously-raised escalation
// left the pending set. It intentionally carries no reason or approved decision
// — the sole consumer clears its card by id identically regardless of outcome,
// and the producer cannot reliably distinguish close-cancel from interrupt
// anyway (see the spec's round-two finding on the close-path race). Additive
// later if a "resolved elsewhere" toast ever wants more.
type SandboxEscalationResolved struct {
	// ThreadID/Ref identify the SESSION this escalation belongs to, exactly like
	// SandboxEscalationRequested above.
	ThreadID     string `json:"threadId"`
	Ref          string `json:"ref"`
	EscalationID string `json:"escalationId"`
}

// DelegateStopParams is the request shape for evener/delegate/stop: the root
// session that owns the delegate tree (ThreadID/Ref, as every turn mutation
// names it) and the delegate to stop (EvenerDelegateInfo.DelegateID).
type DelegateStopParams struct {
	ThreadID   string `json:"threadId,omitempty"`
	Ref        string `json:"ref,omitempty"`
	DelegateID string `json:"delegateId"`
}

// DelegateStopOutcome is what evener/delegate/stop did.
type DelegateStopOutcome string

const (
	// DelegateStopStopping: the subagent's run was cancelled. It ends as
	// cancelled, which evener/delegate/updated reports.
	DelegateStopStopping DelegateStopOutcome = "stopping"
	// DelegateStopNotRunning: the subagent had no run to stop, because it was
	// idle, finished, or already finishing. A repeated stop answers this.
	DelegateStopNotRunning DelegateStopOutcome = "notRunning"
)

// DelegateStopResponse is the result of evener/delegate/stop.
type DelegateStopResponse struct {
	Outcome DelegateStopOutcome `json:"outcome"`
}

// SandboxEscalationResolveParams is the request shape for
// evener/sandbox/escalation/resolve (M7): the human's approve/deny decision for a
// pending escalation. Approve re-runs the single denied invocation with the one
// path granted; deny returns the typed error to the model.
type SandboxEscalationResolveParams struct {
	ThreadID     string `json:"threadId,omitempty"`
	Ref          string `json:"ref,omitempty"`
	EscalationID string `json:"escalationId"`
	Approve      bool   `json:"approve"`
}

type ThreadCapabilities struct {
	Send         bool `json:"send"`
	Steer        bool `json:"steer"`
	Interrupt    bool `json:"interrupt"`
	Compact      bool `json:"compact"`
	Clear        bool `json:"clear"`
	ForkFromTurn bool `json:"forkFromTurn"`
	Shutdown     bool `json:"shutdown"`
	ChangeModel  bool `json:"changeModel"`
	// ChangeVisionModel advertises support for thread/vision-model/set. True for
	// a live evener session whose daemon wires a vision-model hook.
	ChangeVisionModel bool `json:"changeVisionModel"`
	// Queue advertises harness support for turn/queue: true
	// when the daemon wires a queue seam and the thread is not closed, not when
	// a turn happens to be in flight. The client applies the status, so
	// turn/queue is still meaningful only mid-turn.
	Queue bool `json:"queue"`
	// Goal advertises support for goal/set (the /goal objective engine). True
	// for a evener session that can accept a goal; false for sources that do not
	// advertise the capability, so goal/set is gated like every other thread action.
	Goal bool `json:"goal"`
	// SharedNotes advertises support for the shared-notes surface. Only two of
	// its verbs are hub RPCs — notes/human/set and urls/remove. The other two,
	// notes/agent/set and urls/add, are agent tools the daemon handles in
	// session and are deliberately absent from the RPC method catalog. True for
	// a live evener session whose daemon wires them; false for sources that do
	// not advertise the capability, so the notes verbs are gated like every
	// other thread action.
	SharedNotes bool `json:"sharedNotes"`
	// Rename advertises support for evener/thread/name/set. True for a live evener
	// session (the daemon method) and for ended local sessions (the hub edits
	// meta); false for non-local/source-backed threads that do not advertise it.
	Rename bool `json:"rename"`
	// SkillInput advertises support for canonical {type:"skill", name} input
	// items on the input-bearing turn mutations. A live daemon advertises it
	// when all of those endpoints are wired; the hub's cold projections
	// (past reads, close and gave-up frames, and list rows) advertise the same
	// current-daemon floor, since each input-bearing mutation re-verifies
	// against the live daemon. ValidateSkillInputSupport keeps skill items
	// rejected wherever this capability is false.
	SkillInput bool `json:"skillInput,omitempty"`
	// CommandInput advertises canonical {type:"command", name} consumption
	// on input-bearing mutations, with empty args and exact catalog identity.
	CommandInput bool `json:"commandInput,omitempty"`
	// StopSubagent advertises evener/delegate/stop on a root session (S6):
	// true while its daemon wires the stop and the session is open. Absent
	// from an older daemon, from a session with no daemon running (it runs no
	// subagents), and from a subagent's own thread: the stop targets the root
	// that owns the tree.
	StopSubagent bool `json:"stopSubagent,omitempty"`
	// PageBefore advertises that thread/turns/list pages this thread from a
	// before position, with or without a cursor: a client that trimmed rows
	// from the top of its window can page them back. The hub answers for its
	// own local and saved threads. A thread on another host stays masked until
	// the hub can join such a page to its remote paging window; an
	// older hub never sends it.
	PageBefore bool `json:"pageBefore,omitempty"`
}

// EvenerHookEventStatus describes a single hook event's registration state.
type EvenerHookEventStatus struct {
	Event     string `json:"event"`
	Count     int    `json:"count"`
	Tier      string `json:"tier,omitempty"`
	Supported bool   `json:"supported"`
}

type EvenerDiagnostics struct {
	// Commands is this owning session's loaded, path-free command inventory.
	// Nil is unreported, an explicit empty slice is an authoritative empty inventory.
	Commands   []CommandDescriptor     `json:"commands,omitzero"`
	Tools      []EvenerToolInfo        `json:"tools,omitempty"`
	MCP        []EvenerMCPServerInfo   `json:"mcp,omitempty"`
	Skills     []EvenerSkillInfo       `json:"skills,omitempty"`
	Plugins    []EvenerPluginInfo      `json:"plugins,omitzero"` // nil (a source that cannot report plugins) is absent; empty is sent as []
	HookEvents []EvenerHookEventStatus `json:"hookEvents,omitempty"`
	Jobs       []EvenerJobInfo         `json:"jobs,omitempty"`
	Delegates  []EvenerDelegateInfo    `json:"delegates,omitempty"`
	Watches    []EvenerWatchInfo       `json:"watches,omitempty"`
	TurnSlots  *EvenerTurnSlots        `json:"turnSlots,omitempty"`
	Agents     []string                `json:"agents,omitempty"`
	// DelegateDiagnostics carries delegate-SUBSYSTEM diagnostics that are
	// not about any one delegate -- e.g. the shared delegates.jsonl itself
	// being unreadable (delegatestore.ErrLineTooLong), which yields zero
	// delegates to attach a per-delegate diagnostic to. Delegates[].Diagnostics
	// (EvenerDelegateInfo) is per-delegate and can only ever be populated
	// when at least one delegate exists, so a diagnostic about the shared
	// journal itself needs a vessel that does not depend on any delegate
	// surviving to carry it.
	DelegateDiagnostics []string `json:"delegateDiagnostics,omitempty"`
	// SkillDiagnostics carries skill-discovery diagnostics copied from the
	// Stage 1 catalog (collisions, unreadable sources, invalid metadata or
	// controls). Skills stays the path-free completion catalog;
	// SkillDiagnostics is the explicit source-detail view, so the two never
	// borrow each other's identity.
	SkillDiagnostics []EvenerSkillDiagnostic `json:"skillDiagnostics,omitempty"`
}

type EvenerToolInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type EvenerMCPServerInfo struct {
	Name   string   `json:"name"`
	Tools  []string `json:"tools"`
	Status string   `json:"status,omitempty"`
	Error  string   `json:"error,omitempty"`
}

type EvenerSkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// DisableModelInvocation, UserInvocable, and Available copy the Stage 1
	// invocation controls and availability verdict verbatim; AppWire adds no
	// policy of its own. The two control booleans serialize without omission
	// so an unwired source reads as a concrete false rather than an implied
	// one. Composer completion uses Available && UserInvocable, and still
	// requires the target's SkillInput capability before submission.
	DisableModelInvocation bool     `json:"disableModelInvocation"`
	UserInvocable          bool     `json:"userInvocable"`
	Available              bool     `json:"available"`
	AllowedTools           []string `json:"allowedTools,omitempty"`
}

// EvenerSkillDiagnostic is the wire form of one Stage 1 skill-discovery
// diagnostic. Source and OtherSource carry explicit source detail (a
// collision's winner and displaced source); the path-free EvenerSkillInfo
// catalog never carries it.
type EvenerSkillDiagnostic struct {
	Category    string `json:"category"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	OtherSource string `json:"otherSource,omitempty"`
	Field       string `json:"field,omitempty"`
	Message     string `json:"message"`
}

type EvenerPluginInfo struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	SkillCount int    `json:"skillCount"`
	AgentCount int    `json:"agentCount"`
	HookCount  int    `json:"hookCount"`
	MCPCount   int    `json:"mcpCount"`
}

type EvenerJobInfo struct {
	JobID            string `json:"jobId"`
	JobType          string `json:"jobType"`
	Status           string `json:"status"`
	Reason           string `json:"reason,omitempty"`
	ExhaustionBudget string `json:"exhaustionBudget,omitempty"`
	ExhaustionLimit  int    `json:"exhaustionLimit,omitempty"`
	Resumable        *bool  `json:"resumable,omitempty"`
	ExitCode         *int   `json:"exitCode,omitempty"`
	OutputBytes      int64  `json:"outputBytes"`
	TranscriptRef    string `json:"transcriptRef,omitempty"`
	FromWatch        bool   `json:"fromWatch,omitempty"`
	Background       bool   `json:"background,omitempty"`
	Command          string `json:"command,omitempty"`
	// Intent is the tool call's `intent` argument (see WithIntentParameter):
	// the model's own one-line statement of why the command is being run.
	// Captured on the job record at launch so job surfaces (the sidebar rail,
	// the activity panel) can show it alongside the command.
	Intent           string `json:"intent,omitempty"`
	ParentDelegateID string `json:"parentDelegateId,omitempty"`
	DelegateID       string `json:"delegateId,omitempty"`
	Task             string `json:"task,omitempty"`
	OriginTurnID     string `json:"originTurnId,omitempty"`
	OriginToolCallID string `json:"originToolCallId,omitempty"`
	OriginItemID     string `json:"originItemId,omitempty"`
}

// EvenerWatchCadence is one trigger cadence on a live watch: Kind is one of
// "after" (one-shot timer), "every" (repeating timer), "progress" (progress
// interval), "output" (output match), or "events" (session/job event watch).
// Seconds carries the interval for the timer and progress kinds and is absent
// for output and event watches. Trigger sources are orthogonal, so one watch
// can carry more than one cadence.
type EvenerWatchCadence struct {
	Kind    string  `json:"kind"`
	Seconds float64 `json:"seconds,omitempty"`
	// DerivedNextFireAt is the next instant this clock-driven cadence is
	// expected to fire, derived at the daemon from the install instant and the
	// newest CLOCK fire (a repeating cadence advances from whichever of the two
	// is later; a one-shot from the install instant alone). The delivery ring is
	// display-only and is deliberately not consulted, because it mixes every
	// delivery kind and an output match would move a progress cadence's date. It
	// is approximate (the runtime keeps a ticker the scheduler can delay) and
	// can slide later, so consumers word it with a "~". Absent for output and
	// event cadences, which have no schedule.
	DerivedNextFireAt string `json:"derivedNextFireAt,omitempty"`
	// Every is the fire-every-Nth-matching-event throttle on an "events"
	// cadence; absent (zero) means fire on every matching event. Only the
	// events kind carries it.
	Every int `json:"every,omitempty"`
	// Filter is the events-kind watch's event filter in the model-facing
	// condition summary's own vocabulary (e.g. "tool_name=Bash, status=error");
	// absent when the watch filters nothing. Only the events kind carries it.
	Filter string `json:"filter,omitempty"`
}

// EvenerWatchInfo is the structured projection of one live watch, so the web
// UI can render a watch's note and cadence without parsing the condition prose
// job_list hands the model. It is purely additive: an older daemon omits
// Watches entirely, and consumers treat absence as an empty list.
type EvenerWatchInfo struct {
	ID             string               `json:"id"`
	Source         string               `json:"source"`
	Target         string               `json:"target,omitempty"`
	SendTo         string               `json:"sendTo,omitempty"`
	Note           string               `json:"note,omitempty"`
	Cadence        []EvenerWatchCadence `json:"cadence,omitempty"`
	OutputMatch    string               `json:"outputMatch,omitempty"`
	Events         []string             `json:"events,omitempty"`
	WildcardEvents bool                 `json:"wildcardEvents,omitempty"`
	Deliveries     int                  `json:"deliveries"`
	// DeliveryTimes is the bounded, oldest-first ring of this watch's most
	// recent delivery instants, formatted like CreatedAt. Absent when the
	// watch has not delivered.
	DeliveryTimes []string `json:"deliveryTimes,omitempty"`
	CreatedAt     string   `json:"createdAt"`
	Active        bool     `json:"active"`
	EndReason     string   `json:"endReason,omitempty"`
}

// EvenerDelegateInfo is the turn-free stable delegate projection shared by live
// notifications and thread diagnostics. It contains no activation job fields
// and no call-scoped wait result.
type EvenerDelegateInfo struct {
	// RunGeneration identifies the current activation; zero means no run has started.
	RunGeneration  uint64 `json:"runGeneration"`
	DelegateID     string `json:"delegateId"`
	OwnerSessionID string `json:"ownerSessionId"`
	// LogicalOwnerSessionID is the session whose activity rows scope this
	// delegate: the nearest ancestor session (AncestorSessionIDs[0]) when there
	// is one, else OwnerSessionID. OwnerSessionID is always the physical root, so
	// a session-scoped client buckets its own children from a descendant's by
	// this field.
	LogicalOwnerSessionID string `json:"logicalOwnerSessionId,omitempty"`
	RootSessionID         string `json:"rootSessionId"`
	ChildSessionID        string `json:"childSessionId"`
	TranscriptRef         string `json:"transcriptRef"`
	ParentDelegateID      string `json:"parentDelegateId,omitempty"`
	Type                  string `json:"type"`
	Lifecycle             string `json:"lifecycle"`
	Phase                 string `json:"phase"`
	Status                string `json:"status"`
	Outcome               string `json:"outcome,omitempty"`
	Reason                string `json:"reason,omitempty"`
	Error                 string `json:"error,omitempty"`
	Terminal              bool   `json:"terminal,omitempty"`
	Resumable             bool   `json:"resumable"`
	NeedsAttention        bool   `json:"needsAttention"`
	NotResumableReason    string `json:"notResumableReason,omitempty"`
	ProjectionRevision    uint64 `json:"projectionRevision"`
	// ReportPreview is the settled current run's reported text, capped at 4096
	// Unicode code points including an ellipsis when truncated. Absent while the
	// run is open, for an error-only or unreported outcome, and for a prior
	// generation once a new run has started.
	ReportPreview          string `json:"reportPreview,omitempty"`
	ReportPreviewTruncated bool   `json:"reportPreviewTruncated,omitempty"`
	Task                   string `json:"task,omitempty"`
	Description            string `json:"description,omitempty"`
	AgentType              string `json:"agentType,omitempty"`
	RequestedModel         string `json:"requestedModel,omitempty"`
	ResolvedProfileID      string `json:"resolvedProfileId,omitempty"`
	ResolvedModel          string `json:"resolvedModel,omitempty"`
	Model                  string `json:"model,omitempty"`
	ReasoningEffort        string `json:"reasoningEffort,omitempty"`
	OriginTurnID           string `json:"originTurnId,omitempty"`
	OriginToolCallID       string `json:"originToolCallId,omitempty"`
	OriginItemID           string `json:"originItemId,omitempty"`
	RunStartedAt           string `json:"runStartedAt,omitempty"`
	RunEndedAt             string `json:"runEndedAt,omitempty"`
	LatestActivityAt       string `json:"latestActivityAt,omitempty"`
	// LatestIntent is the open generation's newest stated tool-call intent
	// ("Reading the board's row tests."), one line of at most MaxIntentRunes:
	// the row's display line, the same Excerpt-bounded value the read
	// projects. Empty whenever no run is open, and absent from older daemons.
	LatestIntent        string               `json:"latestIntent,omitempty"`
	RunningForMS        *int64               `json:"runningForMs,omitempty"`
	QuietForMS          *int64               `json:"quietForMs,omitempty"`
	DurationMS          *int64               `json:"durationMs,omitempty"`
	PacketKind          string               `json:"packetKind,omitempty"`
	Message             json.RawMessage      `json:"message,omitempty"`
	StructuredResult    json.RawMessage      `json:"structuredResult,omitempty"`
	StructuredValid     *bool                `json:"structuredResultValid,omitempty"`
	StructuredReason    string               `json:"structuredResultReason,omitempty"`
	Warnings            []string             `json:"warnings,omitempty"`
	Diagnostics         []string             `json:"diagnostics,omitempty"`
	ExhaustionBudget    string               `json:"exhaustionBudget,omitempty"`
	ExhaustionLimit     int                  `json:"exhaustionLimit,omitempty"`
	ExhaustionResumable *bool                `json:"exhaustionResumable,omitempty"`
	DelegationAllowance int                  `json:"delegationAllowance,omitempty"`
	ParentWatchGranted  bool                 `json:"parentWatchGranted,omitempty"`
	Usage               *EvenerUsage         `json:"usage,omitempty"`
	Worktree            *JobActivityWorktree `json:"worktree,omitempty"`
}

type EvenerDelegateParams struct {
	ThreadID string             `json:"threadId"`
	Ref      string             `json:"ref"`
	Delegate EvenerDelegateInfo `json:"delegate"`
}

type EvenerTurnSlots struct {
	InUse  int64 `json:"inUse"`
	Cap    int64 `json:"cap"`
	Jobs   int64 `json:"jobs"`
	Drives int64 `json:"driveTurns"`
}

// TurnItemsView identifies whether a turn carries its complete item list or a
// fragment selected by item-mode paging.
type TurnItemsView string

const (
	TurnItemsViewFull     TurnItemsView = "full"
	TurnItemsViewFragment TurnItemsView = "fragment"
)

// ThreadItemPosition is an absolute position in the decoded transcript and
// the final visible projected item slice for that entry. Sub orders notices
// anchored at the same (Entry, Item) position; it is omitted at zero.
type ThreadItemPosition struct {
	Entry uint64 `json:"entry"`
	Item  uint32 `json:"item"`
	Sub   uint32 `json:"sub,omitempty"`
}

// NoticeAnchorItem is the Item value of every notice anchor: past every real
// content part index, so a notice's position always sorts after the entry's
// real items.
const NoticeAnchorItem uint32 = 1 << 30
