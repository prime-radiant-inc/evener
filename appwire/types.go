package appwire

import (
	"encoding/json"
	"fmt"
)

const ProtocolVersion = "serf-appwire-v2"

const (
	MethodInitialize                = "initialize"
	MethodInitialized               = "initialized"
	MethodPing                      = "ping"
	MethodThreadList                = "thread/list"
	MethodThreadRead                = "thread/read"
	MethodThreadTurnsList           = "thread/turns/list"
	MethodThreadTurnItemsList       = "thread/turns/items/list"
	MethodThreadStart               = "thread/start"
	MethodThreadResume              = "thread/resume"
	MethodThreadFork                = "thread/fork"
	MethodThreadClear               = "thread/clear"
	MethodThreadModelSet            = "thread/model/set"
	MethodThreadReasoningEffortSet  = "thread/reasoning-effort/set"
	MethodThreadCompactStart        = "thread/compact/start"
	MethodThreadShutdown            = "thread/shutdown"
	MethodTurnStart                 = "turn/start"
	MethodTurnSteer                 = "turn/steer"
	MethodTurnInterrupt             = "turn/interrupt"
	MethodTurnQueue                 = "turn/queue"
	MethodTurnDrainAsSteer          = "turn/drainAsSteer"
	MethodTurnPromoteQueuedAsSteer  = "turn/promoteQueuedAsSteer"
	MethodTurnCancelQueued          = "turn/cancelQueued"
	MethodGoalSet                   = "goal/set"
	MethodSerfTasksList             = "serf/tasks/list"
	MethodSerfJobsList              = "serf/jobs/list"
	MethodSerfJobsOutput            = "serf/jobs/output"
	MethodSerfThreadNameSet         = "serf/thread/name/set"
	MethodSerfThreadTranscriptsList = "serf/thread/transcripts/list"
	MethodSerfSubagentPreview       = "serf/subagentPreview"
	MethodSerfPathsComplete         = "serf/paths/complete"
	MethodSerfProjectsRecent        = "serf/projects/recent"
	MethodSerfPathValidate          = "serf/path/validate"
	MethodSerfHarnessesList         = "serf/harnesses/list"
	MethodSerfUpgrade               = "serf/upgrade"
	MethodSerfAuthStatus            = "serf/auth/status"
	MethodSerfAuthTest              = "serf/auth/test"
	MethodSerfAuthLoginStart        = "serf/auth/login/start"
	MethodSerfAuthLoginComplete     = "serf/auth/login/complete"
	MethodSerfAuthLogout            = "serf/auth/logout"
	MethodSerfAuthList              = "serf/auth/list"
	MethodSerfAuthApiKeySet         = "serf/auth/apiKey/set"
	MethodSerfAuthDeviceStart       = "serf/auth/device/start"
	MethodSerfAuthDevicePoll        = "serf/auth/device/poll"
	MethodSerfLaunchResolve         = "serf/launch/resolve"
	MethodSerfLaunchSchema          = "serf/launch/schema"
	MethodSerfLaunchGetLayer        = "serf/launch/getLayer"
	MethodSerfLaunchSetLayer        = "serf/launch/setLayer"
	MethodSerfLaunchTrustRepo       = "serf/launch/trustRepo"
	MethodModelList                 = "model/list"
	MethodSerfInstanceList          = "serf/instance/list"
	MethodSerfInstanceCreate        = "serf/instance/create"
	MethodSerfInstanceEdit          = "serf/instance/edit"
	MethodSerfInstanceRemove        = "serf/instance/remove"
	MethodSerfInstanceSetDefault    = "serf/instance/setDefault"
	MethodSerfPluginCheckNow        = "serf/plugin/checkNow"
	MethodSerfMarketplaceList       = "serf/marketplace/list"
	MethodSerfMarketplaceAdd        = "serf/marketplace/add"
	MethodSerfMarketplaceRemove     = "serf/marketplace/remove"
	MethodSerfMarketplaceRefresh    = "serf/marketplace/refresh"
	MethodSerfMarketplaceBrowse     = "serf/marketplace/browse"
	MethodSerfPluginList            = "serf/plugin/list"
	MethodSerfPluginInstall         = "serf/plugin/install"
	MethodSerfPluginUpgrade         = "serf/plugin/upgrade"
	MethodSerfPluginRemove          = "serf/plugin/remove"
	MethodSerfPluginEnable          = "serf/plugin/enable"
	MethodSerfPluginDisable         = "serf/plugin/disable"
	MethodSerfPluginSetAutoUpgrade  = "serf/plugin/setAutoUpgrade"
	MethodSerfCommandList           = "serf/command/list"
	// MethodSerfSettingsOverview returns the field bag behind six settings
	// sections whose only data path today is Go-template variables:
	// hub/runtime, storage, agent roster, codex launch configs, and probed MCP
	// servers. See SettingsOverviewResponse's doc comment.
	MethodSerfSettingsOverview = "serf/settings/overview"
	// MethodSerfSandboxEscalationResolve delivers a human's approve/deny decision
	// for a pending sandbox-exemption escalation (M7). Client→server; ScopeBoth
	// (daemon serves it; hub relays). It is a UI-only request, never advertised to
	// the model.
	MethodSerfSandboxEscalationResolve = "serf/sandbox/escalation/resolve"
)

const (
	NotifyThreadStarted       = "thread/started"
	NotifyThreadClosed        = "thread/closed"
	NotifyThreadStatusChanged = "thread/status/changed"
	NotifyThreadQueueChanged  = "thread/queueChanged"
	NotifyThreadNameChanged   = "serf/thread/name/changed"
	// NotifyThreadModelChanged pushes a mid-session model/provider switch so
	// clients converge without re-reading the thread. See ThreadModelChangedParams.
	NotifyThreadModelChanged = "thread/model/changed"
	// NotifyThreadReasoningEffortChanged pushes a mid-session reasoning-effort
	// change. See ThreadReasoningEffortChangedParams.
	NotifyThreadReasoningEffortChanged = "thread/reasoning-effort/changed"
	NotifyTurnStarted                  = "turn/started"
	NotifyTurnCompleted                = "turn/completed"
	NotifyItemStarted                  = "item/started"
	NotifyItemCompleted                = "item/completed"
	NotifyAgentMessageDelta            = "item/agentMessage/delta"
	NotifyAgentMessageReset            = "item/agentMessage/reset"
	NotifyReasoningSummaryDelta        = "item/reasoning/summaryTextDelta"
	NotifyToolOutputDelta              = "item/toolOutput/delta"
	NotifyWarning                      = "warning"
	NotifySerfContextPressure          = "serf/thread/contextPressure/updated"
	NotifySerfThreadModelRetry         = "serf/thread/modelRetry"
	NotifySerfThreadResync             = "serf/thread/resync"
	NotifySerfTaskUpdated              = "serf/task/updated"
	NotifySerfSteeringInjected         = "serf/steering/injected"
	NotifySerfJobStarted               = "serf/job/started"
	NotifySerfJobFinished              = "serf/job/finished"
	NotifySerfJobsTreeUpdated          = "serf/jobs/treeUpdated"
	NotifySerfAuthUpdated              = "serf/auth/updated"
	NotifySerfLaunchUpdated            = "serf/launch/updated"
	NotifySerfAttentionChanged         = "serf/attention/changed"
	NotifySerfMarketplaceUpdated       = "serf/marketplace/updated"
	NotifySerfPluginUpdated            = "serf/plugin/updated"
	// NotifySerfSandboxEscalationRequested pushes a harness-raised, human-gated
	// sandbox-exemption approval card to the client (M7). The tool-exec goroutine
	// blocks until the client answers with MethodSerfSandboxEscalationResolve.
	NotifySerfSandboxEscalationRequested = "serf/sandbox/escalation/requested"
	// NotifySerfSandboxEscalationResolved pushes notice that a previously-raised
	// escalation left the pending set (M7, wire-honesty spec Part B): resolved
	// explicitly, cleared by turn-interrupt, or cleared by session close. Every
	// OTHER subscribed client uses it to clear its own now-stale copy of the
	// card. Emitted exactly once per escalation, from the convergence point in
	// agent/session_escalation.go's escalateOnSandboxDenial.
	NotifySerfSandboxEscalationResolved = "serf/sandbox/escalation/resolved"
	// NotifySerfTreeChanged pushes a hint that tree-relevant state changed
	// (roster delta, past-index change, or an archive/favorite/rename/
	// project-delete mutation) so the web sidebar can refetch /api/tree
	// instead of polling. Hub-originated; never sent by daemons.
	NotifySerfTreeChanged = "serf/tree/changed"
)

const (
	ThreadStatusIdle        = "idle"
	ThreadStatusActive      = "active"
	ThreadStatusAwaiting    = "awaiting"
	ThreadStatusWarning     = "warning"
	ThreadStatusClosed      = "closed"
	ThreadStatusNotLoaded   = "notLoaded"
	ThreadStatusSystemError = "systemError"
)

const (
	TurnStatusInProgress  = "inProgress"
	TurnStatusCompleted   = "completed"
	TurnStatusFailed      = "failed"
	TurnStatusInterrupted = "interrupted"
)

type InitializeParams struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ClientInfo      ClientInfo   `json:"clientInfo"`
	Capabilities    Capabilities `json:"capabilities"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Capabilities struct {
	ExperimentalAPI         bool     `json:"experimentalApi"`
	OptOutNotificationNames []string `json:"optOutNotificationMethods,omitempty"`
}

type InitializeResponse struct {
	ServerInfo      ServerInfo `json:"serverInfo"`
	ProtocolVersion string     `json:"protocolVersion"`
	SourceID        string     `json:"sourceId"`
	Features        FeatureSet `json:"features"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type FeatureSet struct {
	ThreadList        bool `json:"threadList"`
	ThreadTurnsList   bool `json:"threadTurnsList"`
	TurnStart         bool `json:"turnStart"`
	TurnSteer         bool `json:"turnSteer"`
	ThreadClear       bool `json:"threadClear"`
	ThreadShutdown    bool `json:"threadShutdown"`
	ForkFromTurn      bool `json:"forkFromTurn"`
	Tasks             bool `json:"tasks"`
	TranscriptList    bool `json:"transcriptList"`
	ModelList         bool `json:"modelList"`
	DirectoryComplete bool `json:"directoryComplete"`
	Auth              bool `json:"auth"`
}

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
	Serf          SerfThread   `json:"serf"`
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

// TaskAggregate carries the authoritative task-list progress for a thread
// snapshot. A nil *TaskAggregate on SerfThread means the source cannot know
// the session's task state; a present zero is an authoritative empty list.
type TaskAggregate struct {
	Total int `json:"total"`
	Done  int `json:"done"`
}

type SerfThread struct {
	Ref              string             `json:"ref"`
	ParentRef        string             `json:"parentRef,omitempty"`
	Kind             string             `json:"kind,omitempty"`
	Profile          string             `json:"profile,omitempty"`
	ActiveTurnID     string             `json:"activeTurnId,omitempty"`
	ContextPressure  float64            `json:"contextPressure,omitempty"`
	ContextUsed      int                `json:"contextUsed,omitempty"`
	ContextWindow    int                `json:"contextWindow,omitempty"`
	ContextRemaining int                `json:"contextRemaining,omitempty"`
	Capabilities     ThreadCapabilities `json:"capabilities"`
	Diagnostics      *SerfDiagnostics   `json:"diagnostics,omitempty"`
	// Queue carries authoritative queue depth + preview for the per-session
	// input queue (kata r80p). Both UIs derive their queue-preview chrome
	// from this field rather than mirroring queue mutations locally, which
	// fixes multi-client incoherence and post-reload state. The empty zero
	// value (Depth==0, Preview==nil) means "no queued messages".
	Queue            QueueState        `json:"queue"`
	PendingMutations []PendingMutation `json:"pendingMutations,omitempty"`
	// Tasks carries the task-list progress for a session snapshot. It is nil
	// when the source cannot authoritatively read task state, including an old
	// daemon or a missing persisted task file; a present zero is real zero.
	Tasks *TaskAggregate `json:"tasks,omitempty"`
	// Goal carries the session's /goal state when a goal is set, else nil.
	// It powers `/goal status` and a future status-bar indicator without a
	// bespoke transport — like Queue, it is structured per-session state read
	// from the already-fetched thread snapshot.
	Goal *GoalState `json:"goal,omitempty"`
	// Usage, WorkMillis, and ActiveTurnStartedAt are the daemon's live
	// working-state/token metrics (WS2), served from the daemon's materialized
	// thread envelope, which is refreshed at the turn boundaries that move
	// them. Usage is a pointer
	// (unlike the other two scalars) because SerfUsage is a value struct whose
	// omitempty would never omit — nil is how a fresh/old-daemon/codex thread
	// signals "no token data" rather than rendering ↑0 ↓0.
	// ActiveTurnStartedAt is Unix epoch MILLISECONDS (matching WorkMillis's
	// scale, and the web reducer's epoch-ms read), 0 when no turn is running.
	// Emitting seconds here would mix units with the consumer's ms clock.
	Usage               *SerfUsage `json:"usage,omitempty"`
	WorkMillis          int64      `json:"workMillis,omitempty"`
	ActiveTurnStartedAt int64      `json:"activeTurnStartedAt,omitempty"`
	// Cost is the session's cumulative estimated dollar total — the "~$X.XX"
	// string EstimateCost derives from Usage at the thread's model price, the
	// session-scope sibling of the per-turn Turn.Cost (same shape, same "~"
	// estimate marker). Empty (omitted) when Usage is nil or the model is
	// uncataloged: an honest "unknown" that renders no chip, never a
	// misleading "~$0.00" — the only "~$0.00" a consumer sees is a genuinely
	// sub-cent priced session. Derived from the authoritative full-session
	// cumulative Usage (the same total the token cluster trusts), never a
	// page of client-loaded turns, so it is pagination-proof by construction.
	// Stamped beside Usage at each SerfThread producer (the server's live
	// appThread and the hub's past-entry hydrate), so it stays current across
	// snapshots exactly as WorkMillis/Usage do. The whole cumulative Usage is
	// priced at the thread's CURRENT model: after a mid-session model switch,
	// earlier turns are repriced at current rates (the flat CumulativeUsage
	// carries no per-model breakdown; identical to the legacy computation).
	Cost string `json:"cost,omitempty"`
	// FailedToolCalls is how many of this session's tool calls failed — the
	// session-scale count of exactly what the transcript marks with a failure
	// glyph (a tool result carrying an error, or a shell command that ran and
	// exited nonzero). It answers "did anything go wrong in here" without
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
	// Codex-sourced thread, or the hub's per-entry list sweeps, which cannot
	// afford a scan per session. Consumers render nil as nothing, never as a
	// fabricated zero.
	FailedToolCalls *int `json:"failedToolCalls,omitempty"`
	// AskPending mirrors StatusInfo.PendingAsk (Track A §2 ask-tiering) —
	// true while an ask_user question is unanswered. Additive: absent on old
	// daemons and Codex threads, decoding as false.
	AskPending bool `json:"askPending,omitempty"`
	// PendingEscalations is the M7 surface-on-entry snapshot: the redacted approval
	// cards for any sandbox-exemption escalations currently blocked on this session,
	// so a client entering / reconnecting to / not-having-seen-live this session
	// surfaces the card(s). It is a HUMAN-CLIENT field only — it is never part of the
	// model's transcript or any model-visible projection. Absent on old daemons /
	// Codex threads.
	PendingEscalations []SandboxEscalationRequested `json:"pendingEscalations,omitempty"`
	// ReasoningEffort, ReasoningEffortLevels, and SupportsReasoning are the
	// live reasoning-effort settings for the session's current profile, so a
	// cold-attached client can render both settings and populate pickers
	// with no prior thread/model/changed or thread/reasoning-effort/changed
	// notification. ModelProvider (on Thread, not here) stays the model field.
	ReasoningEffort       string   `json:"reasoningEffort,omitempty"`
	ReasoningEffortLevels []string `json:"reasoningEffortLevels,omitempty"`
	SupportsReasoning     bool     `json:"supportsReasoning,omitempty"`
}

// GoalState is the wire representation of a session's /goal. Status is the
// lifecycle status ("active", "complete", "blocked"); Iterations is the number
// of continuation turns taken. A nil *GoalState on SerfThread means no goal is
// set.
type GoalState struct {
	Status     string `json:"status"`
	Iterations int    `json:"iterations"`
}

// SerfUsage carries a serf session's cumulative self-only token totals for
// the status row. A nil *SerfUsage on SerfThread means no token data (old
// daemon, Codex thread, or a session with zero usage) — the clusters hide
// rather than render ↑0 ↓0.
type SerfUsage struct {
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
}

// ThreadQueueChangedParams is the params shape for thread/queueChanged
// (kata r80p). It mirrors the queue field on SerfThread so consumers can
// store it verbatim on the cached thread state.
type ThreadQueueChangedParams struct {
	ThreadID string     `json:"threadId"`
	Ref      string     `json:"ref"`
	Queue    QueueState `json:"queue"`
}

// TaskUpdatedParams is the params shape for serf/task/updated: the session's
// task-list progress after a change, so a client refreshes the status row
// event-driven instead of polling serf/tasks/list.
type TaskUpdatedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
}

// TurnCompletedParams is the payload of a turn/completed notification: the
// completed turn and its ID.
type TurnCompletedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	TurnID   string `json:"turnId"`
	Turn     Turn   `json:"turn"`
}

// SandboxEscalationRequested is the payload of a
// serf/sandbox/escalation/requested notification (M7): a harness-raised approval
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

// SandboxEscalationResolved is the payload of a serf/sandbox/escalation/resolved
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

// SandboxEscalationResolveParams is the request shape for
// serf/sandbox/escalation/resolve (M7): the human's approve/deny decision for a
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
	// Queue advertises support for turn/queue (kata 111a). True when a turn
	// is currently in flight and the session can accept enqueued user
	// messages for processing after the active turn completes.
	Queue bool `json:"queue"`
	// Goal advertises support for goal/set (the /goal objective engine). True
	// for a serf session that can accept a goal; false for sources without the
	// engine (e.g. codex), so goal/set is gated like every other thread action.
	Goal bool `json:"goal"`
	// Rename advertises support for serf/thread/name/set. True for a live serf
	// session (the daemon method) and for ended local sessions (the hub edits
	// meta); false for Codex-bridged threads.
	Rename bool `json:"rename"`
}

type SerfDiagnostics struct {
	Tools   []SerfToolInfo      `json:"tools,omitempty"`
	MCP     []SerfMCPServerInfo `json:"mcp,omitempty"`
	Skills  []SerfSkillInfo     `json:"skills,omitempty"`
	Plugins []SerfPluginInfo    `json:"plugins,omitempty"`
	Hooks   map[string]int      `json:"hooks,omitempty"`
	Jobs    []SerfJobInfo       `json:"jobs,omitempty"`
	Agents  []string            `json:"agents,omitempty"`
}

type SerfToolInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type SerfMCPServerInfo struct {
	Name   string   `json:"name"`
	Tools  []string `json:"tools"`
	Status string   `json:"status,omitempty"`
	Error  string   `json:"error,omitempty"`
}

type SerfSkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SerfPluginInfo struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	SkillCount int    `json:"skillCount"`
	AgentCount int    `json:"agentCount"`
	HookCount  int    `json:"hookCount"`
	MCPCount   int    `json:"mcpCount"`
}

type SerfJobInfo struct {
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
	DelegateID       string `json:"delegateId,omitempty"`
	Task             string `json:"task,omitempty"`
	OriginTurnID     string `json:"originTurnId,omitempty"`
	OriginToolCallID string `json:"originToolCallId,omitempty"`
	OriginItemID     string `json:"originItemId,omitempty"`
}

type Turn struct {
	ID        string       `json:"id"`
	Items     []ThreadItem `json:"items,omitempty"`
	ItemsView string       `json:"itemsView"`
	Status    string       `json:"status"`
	Error     *TurnError   `json:"error,omitempty"`
	// StartedAt and CompletedAt are Unix epoch MILLISECONDS (nil/0 when unset),
	// the same scale as DurationMS and the web reducer's epoch-ms read. The
	// appprojector/apptranscript producers stamp them via time.Time.UnixMilli.
	StartedAt   *int64 `json:"startedAt,omitempty"`
	CompletedAt *int64 `json:"completedAt,omitempty"`
	DurationMS  *int64 `json:"durationMs,omitempty"`
	// Usage and Cost are the turn's own (not cumulative-session) token totals
	// and estimated dollar cost — nil/empty when not computable (no usage
	// data for this turn, or an uncataloged model). Populated live by
	// summing EventAssistantTextEnd's per-round usage across the turn
	// (internal/appprojector), and for ended sessions by reading the
	// persisted per-round schema.Turn.Usage (internal/apptranscript).
	Usage *SerfUsage `json:"usage,omitempty"`
	Cost  string     `json:"cost,omitempty"`
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
// unrelated early entry already owns once a restart reseeds the served
// snapshot from the transcript. The reply then merges into that entry's turn,
// taking the whole agent response with it (kata rk09).
//
// Raising the counter the way internal/appprojector fences its own live
// counter (SeedPersistedTurns, kata eptj) cannot fix this: the entry index
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
// warning/error notification. Today the only Kind is "provider" (an HTTP
// failure from an LLM adapter); consumers can typed-branch on Kind
// instead of substring-matching the message (kata cmfz). The agent's
// events.ErrorCause projects to this shape; absence is signaled by an
// omitted/nil pointer on the carrying envelope.
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
}

type ThreadItem struct {
	Type                 string        `json:"type"`
	ID                   string        `json:"id"`
	TurnID               string        `json:"turnId,omitempty"`
	TranscriptEntryIndex int           `json:"transcriptEntryIndex,omitempty"`
	Text                 string        `json:"text,omitempty"`
	Delta                string        `json:"delta,omitempty"`
	Images               []InputItem   `json:"images,omitempty"`
	ToolName             string        `json:"toolName,omitempty"`
	CallID               string        `json:"callId,omitempty"`
	ArgumentsJSON        string        `json:"argumentsJson,omitempty"`
	Description          string        `json:"description,omitempty"`
	Output               string        `json:"output,omitempty"`
	Error                string        `json:"error,omitempty"`
	OutputImages         []OutputImage `json:"outputImages,omitempty"`
	Status               string        `json:"status,omitempty"`
	// PrevalOnly is true when Error came from a pre-dispatch rejection (an
	// unknown tool name, or arguments that failed schema validation even
	// after repair) rather than the tool's own execution - the call never
	// reached ExecuteCall (kata hgm1). A client uses this to tell a
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
	// timestamps; nil when no honest span was recorded (issue #37: the web
	// hover meta shows real times or nothing).
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
	// daemon/system steering (issue #24).
	Source string `json:"source,omitempty"`
	// SteeringKind names what a daemon-originated steering item was
	// (events.SteeringKind*), set at the injection site so a client labels it
	// from ground truth instead of guessing from Text's prose. Empty on
	// non-steering items and on steering items the daemon didn't classify.
	SteeringKind     string `json:"steeringKind,omitempty"`
	ClientMutationID string `json:"clientMutationId,omitempty"`
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

type InputItem struct {
	Type      string            `json:"type"`
	Text      string            `json:"text,omitempty"`
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
}

type ThreadListResponse struct {
	Data            []Thread `json:"data"`
	NextCursor      string   `json:"nextCursor,omitempty"`
	BackwardsCursor string   `json:"backwardsCursor,omitempty"`
}

type ThreadReadParams struct {
	ThreadID            string `json:"threadId,omitempty"`
	Ref                 string `json:"ref,omitempty"`
	IncludeTurns        bool   `json:"includeTurns"`
	ItemsView           string `json:"itemsView,omitempty"`
	Subscribe           bool   `json:"subscribe,omitempty"`
	ReplaceSubscription bool   `json:"replaceSubscription,omitempty"`
	// TurnLimit bounds includeTurns to the latest N turns for windowed
	// (lazy) loading; 0 means unbounded (the full transcript). When it
	// truncates, the response carries OlderCursor for paging back via
	// thread/turns/list.
	TurnLimit int `json:"turnLimit,omitempty"`
}

type ThreadReadResponse struct {
	Thread Thread `json:"thread"`
	// OlderCursor is set when TurnLimit truncated the returned turns; pass it
	// to thread/turns/list to fetch the page of turns just before the window.
	// Empty means the response already includes the oldest turn.
	OlderCursor string `json:"olderCursor,omitempty"`
}

type ThreadTurnsListParams struct {
	ThreadID  string `json:"threadId,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	ItemsView string `json:"itemsView,omitempty"`
}

type ThreadTurnsListResponse struct {
	Data       []Turn `json:"data"`
	NextCursor string `json:"nextCursor,omitempty"`
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

type SerfSubagentPreviewParams struct {
	Ref   string `json:"ref"`
	Limit int    `json:"limit,omitempty"`
}

type SerfSubagentPreviewResponse struct {
	Ref       string       `json:"ref"`
	Items     []ThreadItem `json:"items"`
	Truncated bool         `json:"truncated"`
}

type ThreadStartParams struct {
	Harness         string             `json:"harness,omitempty"`
	CWD             string             `json:"cwd"`
	Input           []InputItem        `json:"input,omitempty"`
	ModelProvider   string             `json:"modelProvider,omitempty"`
	Model           string             `json:"model,omitempty"`
	Profile         string             `json:"profile,omitempty"`
	ReasoningEffort string             `json:"reasoningEffort,omitempty"`
	NonInteractive  *bool              `json:"nonInteractive,omitempty"`
	LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
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
	Ref string `json:"ref"`
	// SourceTurnID names the divergence position as a 1-based index into the
	// parent transcript's ENTRY list — every entry, not just the ones that
	// opened a turn — optionally spelled with a "turn_" prefix. Despite the
	// name it is NOT a turn id: the hub parses it with parseSourceTurnID and
	// hands the number straight to agent.ForkSessionAtUserTurn. Send
	// ThreadItem.TranscriptEntryIndex, never Turn.ID; the two coincide only on
	// a transcript replayed from disk, because every live turn minter numbers
	// turns off its own counter (kata 0jhh).
	SourceTurnID  string `json:"sourceTurnId"`
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
	// Aside forks a local serf thread at its tip instead of at a source turn:
	// the child is a complete copy of the parent session (same permissions and
	// config via the inherited session meta) and opens as a side thread. Aside
	// is mutually exclusive with SourceTurnID, EditedInput, DeferInput, and
	// Label, and is only supported for local serf threads.
	Aside bool `json:"aside,omitempty"`
}

type ThreadForkResponse struct {
	Thread Thread `json:"thread"`
	// OriginalInput is the source turn's original user text, set only when
	// the fork was requested with DeferInput.
	OriginalInput string `json:"originalInput,omitempty"`
}

type TurnStartParams struct {
	Ref              string      `json:"ref,omitempty"`
	ThreadID         string      `json:"threadId,omitempty"`
	ClientMutationID string      `json:"clientMutationId"`
	Input            []InputItem `json:"input,omitempty"`
}

type TurnStartResponse struct {
	Turn    Turn            `json:"turn"`
	Receipt MutationReceipt `json:"receipt"`
}

type TurnSteerParams struct {
	Ref              string      `json:"ref,omitempty"`
	ThreadID         string      `json:"threadId,omitempty"`
	ClientMutationID string      `json:"clientMutationId"`
	ExpectedTurnID   string      `json:"expectedTurnId"`
	Input            []InputItem `json:"input,omitempty"`
}

type TurnInterruptParams struct {
	Ref              string `json:"ref,omitempty"`
	ThreadID         string `json:"threadId,omitempty"`
	ClientMutationID string `json:"clientMutationId"`
	ExpectedTurnID   string `json:"expectedTurnId"`
}

// TurnQueueParams queues a user message during a running turn for processing
// after the active turn completes. The daemon enqueues immediately and returns;
// no turn id is reserved or returned.
type TurnQueueParams struct {
	Ref              string      `json:"ref"`
	ClientMutationID string      `json:"clientMutationId"`
	ExpectedTurnID   string      `json:"expectedTurnId"`
	Input            []InputItem `json:"input,omitempty"`
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
	ClientMutationID string                  `json:"clientMutationId"`
	Disposition      MutationDisposition     `json:"disposition"`
	ThreadID         string                  `json:"threadId"`
	TurnID           string                  `json:"turnId,omitempty"`
	QueueEntryIDs    []string                `json:"queueEntryIds,omitempty"`
	ProjectionState  MutationProjectionState `json:"projectionState"`
}

type PendingMutation struct {
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
	Ref                   string      `json:"ref"`
	ClientMutationID      string      `json:"clientMutationId"`
	ExpectedTurnID        string      `json:"expectedTurnId"`
	ExpectedQueueRevision uint64      `json:"expectedQueueRevision"`
	Input                 []InputItem `json:"input,omitempty"`
}

type TurnDrainAsSteerResponse struct {
	Receipt MutationReceipt `json:"receipt"`
}

// TurnPromoteQueuedAsSteerParams is the wire shape for
// turn/promoteQueuedAsSteer (issue #22 per-message promote). Index selects
// one entry of the session's FIFO input queue (matching the position shown
// in the queue preview); the daemon removes just that entry and injects it
// as a user-sourced steering message into the in-flight turn, leaving the
// other queued messages in place. ExpectedEntryID, when non-empty, must
// match the id the daemon minted for that entry (surfaced via
// QueueState.IDs): the queue head can be consumed mid-turn, so a bare index
// from an older snapshot may point at a different message — a mismatch is a
// Conflict, not a wrong-message promote (review F1). The daemon returns
// Conflict when no turn is in flight, the index is out of range, or the
// expected id no longer matches.
type TurnPromoteQueuedAsSteerParams struct {
	Ref              string `json:"ref"`
	Index            int    `json:"index"`
	ClientMutationID string `json:"clientMutationId"`
	ExpectedTurnID   string `json:"expectedTurnId"`
	ExpectedEntryID  string `json:"expectedEntryId"`
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
	Ref              string `json:"ref"`
	Index            int    `json:"index"`
	ClientMutationID string `json:"clientMutationId"`
	ExpectedEntryID  string `json:"expectedEntryId"`
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

type ThreadShutdownParams struct {
	Ref string `json:"ref"`
}

type ThreadClearParams struct {
	Ref string `json:"ref"`
}

type ThreadClearResponse struct {
	Thread Thread `json:"thread"`
	Ref    string `json:"ref"`
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

type TaskListParams struct {
	Ref string `json:"ref,omitempty"`
}

type TaskListResponse struct {
	Data any `json:"data"`
}

type JobsListParams struct {
	Ref          string `json:"ref,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}

type JobsListResponse struct {
	Data any `json:"data"`
}

type JobsTreeUpdatedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	Revision uint64 `json:"revision"`
}

type JobActivityCounts struct {
	Active    int  `json:"active"`
	Failed    int  `json:"failed"`
	Completed int  `json:"completed"`
	Complete  bool `json:"complete"`
}

type JobActivityBranchState struct {
	Error        string `json:"error,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}

type JobActivityJob struct {
	JobID          string `json:"jobId"`
	OwnerSessionID string `json:"ownerSessionId"`
	OwnerRef       string `json:"ownerRef"`
	TranscriptRef  string `json:"transcriptRef,omitempty"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	Outcome        string `json:"outcome,omitempty"`
	Terminal       bool   `json:"terminal"`
	Background     bool   `json:"background"`
	HasOutput      bool   `json:"hasOutput"`
	Description    string `json:"description"`
	Command        string `json:"command,omitempty"`
	Task           string `json:"task,omitempty"`
	Reason         string `json:"reason,omitempty"`
	StartedAt      string `json:"startedAt"`
	EndedAt        string `json:"endedAt,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	OutputBytes    int64  `json:"outputBytes"`
}

type JobActivityDelegate struct {
	DelegateID     string                 `json:"delegateId"`
	ChildSessionID string                 `json:"childSessionId"`
	ChildRef       string                 `json:"childRef"`
	Mandate        string                 `json:"mandate,omitempty"`
	Turns          []JobActivityJob       `json:"turns"`
	Child          *JobActivitySession    `json:"child,omitempty"`
	Branch         JobActivityBranchState `json:"branch"`
}

type JobActivityEntry struct {
	Kind     string               `json:"kind"` // shell | delegate
	Job      *JobActivityJob      `json:"job,omitempty"`
	Delegate *JobActivityDelegate `json:"delegate,omitempty"`
}

type JobActivitySession struct {
	SessionID string                 `json:"sessionId"`
	Ref       string                 `json:"ref"`
	Label     string                 `json:"label"`
	Aggregate string                 `json:"aggregate"`
	Counts    JobActivityCounts      `json:"counts"`
	Entries   []JobActivityEntry     `json:"entries"`
	Branch    JobActivityBranchState `json:"branch"`
}

type JobActivityTree struct {
	Revision uint64             `json:"revision"`
	Root     JobActivitySession `json:"root"`
}

// AllJobActivityTypes is the explicit reachability root for the replacement
// jobs activity-tree contract. The AppWire generators walk this list in
// addition to Methods and Notifications so the JobActivity* wire types stay
// emitted even though serf/jobs/list itself keeps JobsListResponse.Data as any.
var AllJobActivityTypes = []any{
	JobActivityTree{},
	JobActivitySession{},
	JobActivityEntry{},
	JobActivityJob{},
	JobActivityDelegate{},
	JobActivityCounts{},
	JobActivityBranchState{},
}

// JobOutputTail is the serf/jobs/output payload: the last bytes of a job's
// durable output plus the bookkeeping a client needs to say "showing last N
// of M bytes".
type JobOutputTail struct {
	Tail          string `json:"tail"`
	TotalBytes    int64  `json:"totalBytes"`
	RetainedStart int64  `json:"retainedStart"`
	Truncated     bool   `json:"truncated"`
}

// JobsOutputParams reads a byte tail of one job's durable output. MaxBytes
// defaults server-side (4 KiB) and is capped (64 KiB).
type JobsOutputParams struct {
	Ref      string `json:"ref,omitempty"`
	JobID    string `json:"jobId"`
	MaxBytes int64  `json:"maxBytes,omitempty"`
}

type JobsOutputResponse struct {
	Data any `json:"data"`
}

// PathsCompleteParams asks for path completions of Prefix. IncludeFiles adds
// regular files to the directory-only default; in that mode directory entries
// come back with a trailing separator so the client can tell the two apart.
type PathsCompleteParams struct {
	Prefix       string `json:"prefix"`
	Limit        int    `json:"limit,omitempty"`
	IncludeFiles bool   `json:"includeFiles,omitempty"`
}

type PathsCompleteResponse struct {
	Data []string `json:"data"`
}

// ProjectsRecentParams selects how many recent project directories the hub
// returns. Limit <= 0 means the hub default (the session creation flows'
// 15-option dropdown cap).
type ProjectsRecentParams struct {
	Limit int `json:"limit,omitempty"`
}

// ProjectsRecentResponse lists distinct project working directories ordered
// by actual recency of use (most recently active session first).
type ProjectsRecentResponse struct {
	Data []string `json:"data"`
}

type PathValidateParams struct {
	Path string `json:"path"`
	Kind string `json:"kind,omitempty"`
}

type PathValidateResponse struct {
	Path  string `json:"path"`
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

type HarnessListParams struct{}

type HarnessDescriptor struct {
	ID                             string `json:"id"`
	Label                          string `json:"label"`
	Kind                           string `json:"kind,omitempty"`
	EmptyTaskUnsupportedReason     string `json:"emptyTaskUnsupportedReason,omitempty"`
	EmptyTaskUnsupportedNextAction string `json:"emptyTaskUnsupportedNextAction,omitempty"`
}

type HarnessListResponse struct {
	Data []HarnessDescriptor `json:"data"`
}

type UpgradeParams struct {
	Requested string `json:"requested,omitempty"`
}

type UpgradeResponse struct {
	Release        string   `json:"release"`
	Channel        string   `json:"channel"`
	URL            string   `json:"url"`
	Archive        string   `json:"archive"`
	Prefix         string   `json:"prefix"`
	BinDir         string   `json:"binDir"`
	ShareBinDir    string   `json:"shareBinDir"`
	Installed      []string `json:"installed"`
	RestartMessage string   `json:"restartMessage"`
}

type AuthStatusParams struct {
	Provider string `json:"provider"`
}

const (
	AuthTestStatusSuccess              = "success"
	AuthTestStatusMissing              = "missing"
	AuthTestStatusAuthRejected         = "auth_rejected"
	AuthTestStatusEndpointFailure      = "endpoint_failure"
	AuthTestStatusConfigurationFailure = "configuration_failure"
	AuthTestStatusUnsupported          = "unsupported"
)

type AuthTestParams struct {
	Provider string `json:"provider"`
}

type AuthTestResponse struct {
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

type AuthStatusResponse struct {
	Provider       string   `json:"provider"`
	Supported      bool     `json:"supported"`
	SignedIn       bool     `json:"signedIn"`
	ActiveSource   string   `json:"activeSource"`
	AuthModes      []string `json:"authModes,omitempty"`
	HasStoredOAuth bool     `json:"hasStoredOAuth"`
	// HasStoredFile is true when a key exists in credentials.toml.
	HasStoredFile bool `json:"hasStoredFile,omitempty"`
	// EnvVar is the name of the env var that supplies a key, when present.
	EnvVar       string `json:"envVar,omitempty"`
	Email        string `json:"email,omitempty"`
	StoredEmail  string `json:"storedEmail,omitempty"`
	AccountID    string `json:"accountId,omitempty"`
	WorkspaceID  string `json:"workspaceId,omitempty"`
	NeedsRefresh bool   `json:"needsRefresh,omitempty"`
	NeedsLogin   bool   `json:"needsLogin,omitempty"`
	Error        string `json:"error,omitempty"`
}

type AuthLoginStartParams struct {
	Provider string `json:"provider"`
}

type AuthLoginStartResponse struct {
	Provider string `json:"provider"`
	FlowID   string `json:"flowId"`
	URL      string `json:"url"`
}

type AuthLoginCompleteParams struct {
	Provider    string `json:"provider"`
	FlowID      string `json:"flowId"`
	RedirectURL string `json:"redirectUrl"`
}

type AuthLoginCompleteResponse struct {
	Status AuthStatusResponse `json:"status"`
}

type AuthLogoutParams struct {
	Provider string `json:"provider"`
}

type AuthLogoutResponse struct {
	Removed bool               `json:"removed"`
	Status  AuthStatusResponse `json:"status"`
}

type ModelListParams struct {
	Harness string `json:"harness,omitempty"`
	CWD     string `json:"cwd,omitempty"`
}

type ModelDescriptor struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type ModelListDiagnostic struct {
	Provider string `json:"provider,omitempty"`
	Source   string `json:"source,omitempty"`
	Title    string `json:"title,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

type ModelListResponse struct {
	Data        []ModelDescriptor     `json:"data"`
	Diagnostics []ModelListDiagnostic `json:"diagnostics,omitempty"`
	// Recent carries the model picker's "Recent" group: the last N distinct
	// models across all sessions, globally by recency (not scoped to the
	// currently selected harness/project), derived from the Past index. Empty
	// on a fresh install with no session history. A struct field, not a new
	// appwire method — no dual-router catalog change required.
	Recent []ModelDescriptor `json:"recent,omitempty"`
}

type EmptyResponse struct{}

type ThreadStatusChangedParams struct {
	ThreadID string       `json:"threadId"`
	Ref      string       `json:"ref"`
	Status   ThreadStatus `json:"status"`
	// FailedToolCalls carries the session's running failure count (see
	// SerfThread.FailedToolCalls) so a client WATCHING a session sees it move.
	// The figure is otherwise snapshot-only, refreshed by thread/read — which
	// means a session that was clean when the client attached and failed later
	// would keep saying nothing, which is precisely the reader the count was
	// built for. Every status transition is a turn boundary, so riding along
	// here refreshes it exactly when it can have changed, with no polling.
	//
	// ABSENT MEANS "NO UPDATE", not "nobody counted" — an old daemon omits it,
	// and a client that cleared its count on absence would blank a figure the
	// hydrate legitimately gave it. Absence at HYDRATE is where "nobody
	// counted" is expressed.
	FailedToolCalls *int `json:"failedToolCalls,omitempty"`
	// Capabilities carries the action set that goes WITH the status being
	// announced (see SerfThread.Capabilities), for the same reason the failure
	// count rides along above: it is otherwise snapshot-only, and three of its
	// entries — Send, Steer, Queue — are defined by whether a turn is in
	// flight. A client that read the thread while it was idle therefore holds
	// steer=false/queue=false for the whole turn that follows, and renders a
	// session it KNOWS is active with no Steer, no Stop and a dead Send until
	// the page is reloaded (kata 06t8). A status transition is exactly when
	// those flip, so the set refreshes there and nowhere else — no polling, no
	// re-read of the transcript.
	//
	// ABSENT MEANS "NO UPDATE", same as the count: a source that does not
	// state-gate its capabilities (the Codex bridge) omits it, and a client
	// that cleared its set on absence would strip a session of every action
	// its hydrate legitimately advertised.
	//
	// A CLOSE frame is the one status a daemon does not fill in: what a thread
	// can still be asked to do once its daemon is gone is the hub's answer, not
	// the departing daemon's. The hub stamps that frame as it relays it
	// (cmd/serf-hub/app_relay.go's stampClosedThreadCapabilities), so a close
	// still arrives carrying a set — the same one the next thread/read returns.
	// A client that read the daemon's own all-false set there would lose the
	// follow-up composer for a session the hub would happily resume (kata pk2d).
	Capabilities *ThreadCapabilities `json:"capabilities,omitempty"`
}

type AgentMessageDeltaParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// ReasoningSummaryDeltaParams is the params shape for the
// item/reasoning/summaryTextDelta notification: an incremental chunk of the
// model's reasoning summary for the named reasoning item. Mirrors the Codex
// app-server reasoning stream so the web UI can render thinking live.
type ReasoningSummaryDeltaParams struct {
	ThreadID     string `json:"threadId"`
	Ref          string `json:"ref"`
	TurnID       string `json:"turnId"`
	ItemID       string `json:"itemId"`
	SummaryIndex int    `json:"summaryIndex"`
	Delta        string `json:"delta"`
}

// AgentMessageResetParams is the params shape for the item/agentMessage/reset
// notification: the named in-progress assistant item should be discarded so a
// retried model call's output replaces, rather than appends to, the partial
// that was already streamed.
type AgentMessageResetParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
}

// ThreadModelRetryParams is the params shape for the serf/thread/modelRetry
// notification: the session's model call failed with a retryable error and will
// be tried again after DelayMS.
//
// Thread-scoped and deliberately item-less. One rate-limited session produced 91
// retries in four hours; as transcript items that is noise, and the reader's
// actual question ("is this alive, and when does it come back?") is a question
// about now, not about history. Clients render it as ephemeral liveness state,
// which the next retry or the turn's settlement supersedes.
//
// Attempt counts retries, so the first retry is 1. MaxAttempts is the whole
// budget including the initial try, so "attempt 9 of 11" renders without the
// client knowing the retry policy.
type ThreadModelRetryParams struct {
	ThreadID    string `json:"threadId"`
	Ref         string `json:"ref"`
	TurnID      string `json:"turnId,omitempty"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	DelayMS     int64  `json:"delayMs"`
	ErrorClass  string `json:"errorClass,omitempty"`
	StatusCode  int    `json:"statusCode,omitempty"`
	Message     string `json:"message,omitempty"`
	Model       string `json:"model,omitempty"`
}

// ToolOutputDeltaParams is the params shape for the item/toolOutput/delta
// notification. ItemID identifies the tool-call item; CallID is the legacy
// alias kept for clients that still key on it.
type ToolOutputDeltaParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	TurnID   string `json:"turnId,omitempty"`
	ItemID   string `json:"itemId"`
	CallID   string `json:"callId"`
	Delta    string `json:"delta"`
}

// ThreadStartedParams is the params shape for the thread/started
// notification: the new session's initial Thread snapshot, so a client can
// render the session without a follow-up thread/read.
type ThreadStartedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	Thread   Thread `json:"thread"`
}

// ThreadClosedParams is the params shape for the thread/closed notification.
// Reason is the session's shutdown reason, empty when the source reported
// none.
type ThreadClosedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	Reason   string `json:"reason,omitempty"`
}

type ThreadResyncParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
}

// TurnStartedParams is the params shape for the turn/started notification:
// the newly opened (inProgress) turn.
type TurnStartedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	Turn     Turn   `json:"turn"`
}

// ItemLifecycleParams is the params shape shared by the item/started and
// item/completed notifications — one thread item entering or leaving its
// streaming state. Both carry the identical envelope, so they share one type
// rather than two copies that could drift; consumers distinguish them by the
// notification method, not by shape.
type ItemLifecycleParams struct {
	ThreadID string     `json:"threadId"`
	Ref      string     `json:"ref"`
	TurnID   string     `json:"turnId"`
	Item     ThreadItem `json:"item"`
	// FailedToolCalls carries the session's running failure count (kata 895d),
	// same field and meaning as ThreadStatusChangedParams.FailedToolCalls —
	// only ever populated on item/completed (never item/started: a failure
	// lands at completion), and only on the item whose completion actually
	// moved the figure since the last one that carried it. thread/status/
	// changed already carries the count unconditionally at every turn
	// boundary, but a live watcher on a long turn sees nothing move however
	// many tool calls fail inside it; this rides the finer-grained
	// per-item notification instead so the count moves the instant a failure
	// lands. Gating on "changed since last stamp" is what keeps this from
	// resending an unchanged figure on the many item/completed notifications
	// a turn with no new failures still produces.
	//
	// Absent means "no change" here, same as on ThreadStatusChangedParams —
	// never "nobody counted".
	FailedToolCalls *int `json:"failedToolCalls,omitempty"`
}

// WarningParams is the params shape for the warning notification: a
// non-fatal diagnostic, also used for cancelled turns and relay-attach
// failures. Message/Source/Title/Hint are the human-facing diagnostic;
// Warning carries the raw producer-side event payload and Cause the
// structured error cause (present only on the cancelled-turn path), neither
// of which has a UI consumer today.
//
// A genuine turn failure sends no warning at all — only a failed
// turn/completed carrying the same diagnostic on its TurnError — so clients
// that render both channels do not show one error twice.
type WarningParams struct {
	ThreadID string           `json:"threadId"`
	Ref      string           `json:"ref"`
	Message  string           `json:"message,omitempty"`
	Source   string           `json:"source,omitempty"`
	Title    string           `json:"title,omitempty"`
	Hint     string           `json:"hint,omitempty"`
	Warning  any              `json:"warning,omitempty"`
	Cause    *DiagnosticCause `json:"cause,omitempty"`
}

// SerfSteeringInjectedParams is the params shape for the
// serf/steering/injected notification. Text is pre-substituted server-side
// with an image placeholder when a steer carries only images. Source is
// "user" for human-sent steering (rendered as a user message) and omitted
// entirely for daemon-originated steering (issue #24).
type SerfSteeringInjectedParams struct {
	ThreadID         string      `json:"threadId"`
	Ref              string      `json:"ref"`
	Text             string      `json:"text,omitempty"`
	Images           []InputItem `json:"images,omitempty"`
	Source           string      `json:"source,omitempty"`
	Kind             string      `json:"kind,omitempty"`
	ClientMutationID string      `json:"clientMutationId,omitempty"`
}

// SerfJobParams is the params shape shared by the serf/job/started and
// serf/job/finished notifications. Both carry the same envelope around a
// SerfJobInfo; which of its fields are populated is what differs (a finished
// job adds status/reason/exitCode/output), so one type describes both.
type SerfJobParams struct {
	ThreadID string      `json:"threadId"`
	Ref      string      `json:"ref"`
	Job      SerfJobInfo `json:"job"`
}

// SerfAuthUpdatedParams is the params shape for the serf/auth/updated
// notification. Both fields are absent when the broadcast follows a
// provider-instance mutation, which no single provider/activeSource pair
// honestly summarizes; clients treat this notification as payload-agnostic
// ("credentials or instances changed, refetch") either way.
type SerfAuthUpdatedParams struct {
	Provider     string `json:"provider,omitempty"`
	ActiveSource string `json:"activeSource,omitempty"`
}

// SerfLaunchUpdatedParams is the params shape for the serf/launch/updated
// notification: which working directory's launch config changed, and at
// which layer.
type SerfLaunchUpdatedParams struct {
	CWD   string `json:"cwd"`
	Layer string `json:"layer"`
}

// NotificationRef carries just the routing fields shared by most
// notifications (ref + threadId). Use it when you only need to know which
// session a notification belongs to.
type NotificationRef struct {
	Ref      string `json:"ref"`
	ThreadID string `json:"threadId"`
}

// EmptyParams is the typed-empty params shape used by methods that take none.
type EmptyParams struct{}

type LaunchOptionChoice struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type LaunchOptionEnvFallback struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret,omitempty"`
}

type LaunchOption struct {
	Field             string                   `json:"field"`
	WireField         string                   `json:"wireField"`
	Label             string                   `json:"label"`
	Description       string                   `json:"description,omitempty"`
	Group             string                   `json:"group"`
	Kind              string                   `json:"kind"`
	PathKind          string                   `json:"pathKind,omitempty"`
	Repeatable        bool                     `json:"repeatable,omitempty"`
	DefaultableLayers []string                 `json:"defaultableLayers,omitempty"`
	PerLaunch         bool                     `json:"perLaunch"`
	DebugOnly         bool                     `json:"debugOnly,omitempty"`
	EnvFallback       *LaunchOptionEnvFallback `json:"envFallback,omitempty"`
	Choices           []LaunchOptionChoice     `json:"choices,omitempty"`
	DriverSupport     map[string]bool          `json:"driverSupport,omitempty"`
}

type LaunchOptionSchemaResponse struct {
	Options  []LaunchOption    `json:"options"`
	Excluded map[string]string `json:"excluded,omitempty"`
}

// AuthListResponse is the result of serf/auth/list.
type AuthListResponse struct {
	Providers []AuthStatusResponse `json:"providers"`
}

// AuthApiKeySetParams is the params for serf/auth/apiKey/set.
type AuthApiKeySetParams struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

// AuthDeviceStartParams is the params for serf/auth/device/start.
type AuthDeviceStartParams struct {
	Provider string `json:"provider"`
}

// AuthDeviceStartResponse carries the device code to display, or Fallback=true
// when the client doesn't support device-code and the caller should use the
// redirect/paste-back flow instead.
type AuthDeviceStartResponse struct {
	Provider        string `json:"provider"`
	FlowID          string `json:"flowId"`
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	IntervalSeconds int    `json:"intervalSeconds"`
	Fallback        bool   `json:"fallback,omitempty"`
}

// AuthDevicePollParams is the params for serf/auth/device/poll.
type AuthDevicePollParams struct {
	Provider string `json:"provider"`
	FlowID   string `json:"flowId"`
}

// AuthDevicePollResponse reports one poll attempt. State is "pending",
// "authorized", or "expired". Status is nil (the "status" key is absent from
// the wire) except when authorized.
type AuthDevicePollResponse struct {
	State  string              `json:"state"`
	Status *AuthStatusResponse `json:"status,omitempty"`
}

// InstanceEntry is the wire representation of one configured provider instance
// and its current credential status. The credential-status fields mirror
// AuthStatusResponse so the existing web credential-source rendering can be
// reused without additional translation.
type InstanceEntry struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	APIStyle       string   `json:"apiStyle"`
	BaseURL        string   `json:"baseUrl"`
	IsDefault      bool     `json:"isDefault"`
	AuthModes      []string `json:"authModes,omitempty"`
	ActiveSource   string   `json:"activeSource"`
	HasStoredFile  bool     `json:"hasStoredFile,omitempty"`
	HasStoredOAuth bool     `json:"hasStoredOAuth"`
	EnvVar         string   `json:"envVar,omitempty"`
	StoredEmail    string   `json:"storedEmail,omitempty"`
	// CredentialRequired is false when this instance has no credential to
	// look for at all — an auth-none provider, or a gateway that inherits no
	// type-level key — so an absent credential is not a missing one. It is
	// never omitted: false is the meaningful value, and a client reading an
	// absent field as false would call every instance optional.
	CredentialRequired bool `json:"credentialRequired"`
}

// InstanceListResponse is the result of serf/instance/list.
type InstanceListResponse struct {
	Instances      []InstanceEntry `json:"instances"`
	AvailableTypes []string        `json:"availableTypes"`
}

// InstanceCreateParams is the params for serf/instance/create.
type InstanceCreateParams struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	APIStyle string `json:"apiStyle"`
	BaseURL  string `json:"baseUrl"`
}

// InstanceEditParams is the params for serf/instance/edit.
type InstanceEditParams struct {
	Name     string `json:"name"`
	APIStyle string `json:"apiStyle"`
	BaseURL  string `json:"baseUrl"`
}

// InstanceRemoveParams is the params for serf/instance/remove.
type InstanceRemoveParams struct {
	Name string `json:"name"`
}

// InstanceSetDefaultParams is the params for serf/instance/setDefault.
type InstanceSetDefaultParams struct {
	Name string `json:"name"`
}

// CommandDescriptor describes one plugin-provided slash command for catalog
// display / autocomplete (serf/command/list). Name is unqualified; PluginName
// disambiguates when more than one plugin defines the same command name.
type CommandDescriptor struct {
	Name         string `json:"name"`
	PluginName   string `json:"pluginName"`
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"`
}

// CommandListResponse is the result of serf/command/list.
type CommandListResponse struct {
	Commands []CommandDescriptor `json:"commands"`
}

// LaunchConfigLayer is the wire-level partial layer (every field optional;
// pointer-typed scalars so "not set" is distinguishable from zero).
type LaunchConfigLayer struct {
	Schema                      *int              `json:"schema,omitempty"`
	Model                       string            `json:"model,omitempty"`
	FastCheapModel              string            `json:"fastCheapModel,omitempty"`
	Agent                       string            `json:"agent,omitempty"`
	ReasoningEffort             string            `json:"reasoningEffort,omitempty"`
	ContextStrategy             string            `json:"contextStrategy,omitempty"`
	OpenAIResponsesContinuation string            `json:"openAIResponsesContinuation,omitempty"`
	Sandbox                     string            `json:"sandbox,omitempty"`
	SandboxNet                  *bool             `json:"sandboxNet,omitempty"`
	MaxRounds                   *int              `json:"maxRounds,omitempty"`
	MaxSubagentDepth            *int              `json:"maxSubagentDepth,omitempty"`
	MaxConcurrentDelegateTurns  *int              `json:"maxConcurrentDelegateTurns,omitempty"`
	MaxRetainedTerminal         *int              `json:"maxRetainedTerminal,omitempty"`
	NoProjectPrompts            *bool             `json:"noProjectPrompts,omitempty"`
	NonInteractive              *bool             `json:"nonInteractive,omitempty"`
	AppReplaySize               *int              `json:"appReplaySize,omitempty"`
	SkillsDirs                  []string          `json:"skillsDirs,omitempty"`
	PluginDirs                  []string          `json:"pluginDirs,omitempty"`
	MCPConfigs                  []string          `json:"mcpConfigs,omitempty"`
	SystemPromptMode            string            `json:"systemPromptMode,omitempty"`
	SystemPromptFile            string            `json:"systemPromptFile,omitempty"`
	SystemPromptText            string            `json:"systemPromptText,omitempty"`
	SystemPromptAppendMode      string            `json:"systemPromptAppendMode,omitempty"`
	SystemPromptAppendFile      string            `json:"systemPromptAppendFile,omitempty"`
	SystemPromptAppendText      string            `json:"systemPromptAppendText,omitempty"`
	SystemPromptAppend          []string          `json:"systemPromptAppend,omitempty"`
	ModelFallbacks              []string          `json:"modelFallbacks,omitempty"`
	MCPs                        []MCPServerSpec   `json:"mcps,omitempty"`
	Env                         map[string]string `json:"env,omitempty"`
	Verbose                     *bool             `json:"verbose,omitempty"`
	TraceFile                   string            `json:"traceFile,omitempty"`
	CPUProfile                  string            `json:"cpuProfile,omitempty"`
	ExportATIFPath              string            `json:"exportATIFPath,omitempty"`
	ExportATIFProviderHandles   string            `json:"exportATIFProviderHandles,omitempty"`
}

func (l LaunchConfigLayer) MarshalJSON() ([]byte, error) {
	type alias LaunchConfigLayer
	a := alias(l)
	a.ModelFallbacks = nil
	raw, err := marshalLaunchConfig(a)
	if err != nil {
		return nil, err
	}
	if l.ModelFallbacks == nil {
		return raw, nil
	}
	var obj map[string]json.RawMessage
	if err := unmarshalLaunchConfig(raw, &obj); err != nil {
		return nil, err
	}
	modelFallbacks, err := marshalModelFallbacks(l.ModelFallbacks)
	if err != nil {
		return nil, err
	}
	obj["modelFallbacks"] = modelFallbacks
	return marshalLaunchConfig(obj)
}

var (
	marshalLaunchConfig   = json.Marshal
	unmarshalLaunchConfig = json.Unmarshal
	marshalModelFallbacks = json.Marshal
)

// MCPServerSpec mirrors launchconfig.MCPServerSpec on the wire.
type MCPServerSpec struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// LaunchConfigResolved is the wire representation of launchconfig.Resolved.
type LaunchConfigResolved struct {
	Effective   LaunchConfigLayer            `json:"effective"`
	Layers      map[string]LaunchConfigLayer `json:"layers"`
	Provenance  map[string]string            `json:"provenance"`
	Repo        *RepoLaunchConfigStatus      `json:"repo,omitempty"`
	Diagnostics []LaunchConfigDiagnostic     `json:"diagnostics,omitempty"`
}

type RepoLaunchConfigStatus struct {
	Path    string `json:"path"`
	Hash    string `json:"hash,omitempty"`
	Trust   string `json:"trust"`
	Preview string `json:"preview,omitempty"`
}

type LaunchConfigDiagnostic struct {
	Layer   string `json:"layer"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

type LaunchConfigResolveParams struct {
	CWD             string             `json:"cwd"`
	LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}

type LaunchConfigGetLayerParams struct {
	CWD   string `json:"cwd"`
	Layer string `json:"layer"` // "global" | "project"
}

type LaunchConfigSetLayerParams struct {
	CWD    string            `json:"cwd"`
	Layer  string            `json:"layer"`
	Config LaunchConfigLayer `json:"config"`
}

type LaunchConfigTrustRepoParams struct {
	CWD  string `json:"cwd"`
	Hash string `json:"hash"`
}

// PluginCheckNowResponse is the result of serf/plugin/checkNow: it runs one
// auto-upgrade daemon pass (refresh every marketplace, then upgrade every
// autoUpgrade-enabled plugin) on demand and reports what happened. Updated
// holds "<plugin>@<marketplace>" refs actually upgraded (no-ops omitted);
// Errors holds any per-marketplace/per-plugin failures — failures are
// isolated and never fail the request itself.
type PluginCheckNowResponse struct {
	Updated []string `json:"updated,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

// MarketplaceSourceInput is the wire shape of a marketplace source. Kind
// selects which of Repo/URL/Path applies: "github" (Repo, e.g. "owner/repo"),
// "url" (URL, a git remote), "directory" (Path, referenced in place, no
// clone), or "git-subdir" (URL+Path, a sparse clone of one subdirectory).
// Ref/Sha optionally pin a git-backed source to a branch/tag or commit.
type MarketplaceSourceInput struct {
	Kind string `json:"kind"`
	Repo string `json:"repo,omitempty"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
	Ref  string `json:"ref,omitempty"`
	Sha  string `json:"sha,omitempty"`
}

// MarketplaceEntry is the wire representation of one registered marketplace.
type MarketplaceEntry struct {
	Name            string                 `json:"name"`
	Source          MarketplaceSourceInput `json:"source"`
	InstallLocation string                 `json:"installLocation,omitempty"`
	LastUpdated     int64                  `json:"lastUpdated"`
}

// MarketplaceListResponse is the result of serf/marketplace/list. Every
// marketplace mutation (add/remove/refresh) also returns this, so a client
// can re-render from the response without a separate list round-trip.
type MarketplaceListResponse struct {
	Marketplaces []MarketplaceEntry `json:"marketplaces"`
}

// MarketplaceAddParams is the params for serf/marketplace/add. Name is
// optional; when empty, the marketplace manifest's own name is used.
type MarketplaceAddParams struct {
	Name   string                 `json:"name,omitempty"`
	Source MarketplaceSourceInput `json:"source"`
}

// MarketplaceNameParams identifies one registered marketplace by name — the
// params shape for serf/marketplace/remove and serf/marketplace/refresh.
type MarketplaceNameParams struct {
	Name string `json:"name"`
}

// MarketplaceCatalogPlugin is one plugin entry parsed from a marketplace's
// catalog (.claude-plugin/marketplace.json), as returned by
// serf/marketplace/browse.
type MarketplaceCatalogPlugin struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Author      string `json:"author,omitempty"`
}

// MarketplaceBrowseParams is the params for serf/marketplace/browse.
type MarketplaceBrowseParams struct {
	Name string `json:"name"`
}

// MarketplaceBrowseResponse is the result of serf/marketplace/browse: the
// marketplace's catalog metadata plus its plugin list.
type MarketplaceBrowseResponse struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Plugins     []MarketplaceCatalogPlugin `json:"plugins"`
}

// PluginEntry is the wire representation of one installed plugin.
type PluginEntry struct {
	Plugin       string `json:"plugin"`
	Marketplace  string `json:"marketplace"`
	Version      string `json:"version"`
	Enabled      bool   `json:"enabled"`
	AutoUpgrade  bool   `json:"autoUpgrade"`
	Broken       bool   `json:"broken"`
	InstallPath  string `json:"installPath"`
	GitCommitSha string `json:"gitCommitSha,omitempty"`
	InstalledAt  int64  `json:"installedAt"`
	LastUpdated  int64  `json:"lastUpdated"`
}

// PluginListResponse is the result of serf/plugin/list. Every plugin
// mutation (install/upgrade/remove/enable/disable/setAutoUpgrade) also
// returns this, so a client can re-render from the response without a
// separate list round-trip.
type PluginListResponse struct {
	Plugins []PluginEntry `json:"plugins"`
}

// PluginRefParams identifies one plugin by its registry key (plugin name +
// marketplace name) — the params shape for serf/plugin/install (naming the
// catalog entry to install), and serf/plugin/{upgrade,remove,enable,disable}
// (naming the already-installed entry to act on).
type PluginRefParams struct {
	Plugin      string `json:"plugin"`
	Marketplace string `json:"marketplace"`
}

// PluginSetAutoUpgradeParams is the params for serf/plugin/setAutoUpgrade.
type PluginSetAutoUpgradeParams struct {
	Plugin      string `json:"plugin"`
	Marketplace string `json:"marketplace"`
	AutoUpgrade bool   `json:"autoUpgrade"`
}

// SettingsOverviewResponse is the result of serf/settings/overview: the field
// bag behind six settings sections whose only data path today is Go-template
// variables rendered server-side — General, Hub, Storage, Agents, Codex
// launch, and the probed half of MCP servers (cmd/serf-hub/templates/
// partials/settings/{general,hub,storage,agents,launch-codex,mcp}.html) —
// replacing cmd/serf-hub/web_settings.go's settingsData for exactly those six
// (the deletion wave removes the template path once the frontend ports off
// it). Every field is sourced from the same computation the legacy template
// used; see each sub-type's doc comment for the exact web_settings.go
// citation. A field the legacy template never rendered is left off rather
// than invented — also noted on the sub-type that would otherwise carry it.
//
// The other ten settings sections (providers/credentials, serf launch,
// in-repo trust, per-project override, marketplaces/plugins, plugin/skill
// dirs, the MCP config editable half, theme, transcript, display,
// notifications) are out of scope: they already have their own wire methods
// or land on a different task's new store. Nothing here is per-project.
type SettingsOverviewResponse struct {
	Hub           *SettingsHubOverview       `json:"hub,omitempty"`
	Storage       *SettingsStorageOverview   `json:"storage,omitempty"`
	Agents        []SettingsAgentEntry       `json:"agents,omitempty"`
	CodexLaunches []SettingsCodexLaunchEntry `json:"codexLaunches,omitempty"`
	McpDiscovered *SettingsMCPOverview       `json:"mcpDiscovered,omitempty"`
}

// SettingsHubOverview is the Settings → General / Settings → Hub section
// (cmd/serf-hub/templates/partials/settings/{general,hub}.html). Fields
// mirror cmd/serf-hub/web_settings.go's renderSettingsPartial settingsData
// construction. General.html's "State dir" row is not a field here — see
// SettingsStorageOverview.StateDir, which the frontend reads for it instead.
type SettingsHubOverview struct {
	// Version is the running hub's version string.
	// Source: web_settings.go settingsData.HubVersion (the package Version constant).
	Version string `json:"version,omitempty"`
	// Commit is the git commit the binary was built from; empty in dev builds.
	// Source: web_settings.go settingsData.HubCommit (buildinfo.GitSHA).
	Commit string `json:"commit,omitempty"`
	// ListenAddr is the hub HTTP server's bind address.
	// Source: web_settings.go settingsData.HubAddr (cfg.HubAddr).
	ListenAddr string `json:"listenAddr,omitempty"`
	// RunDir is the per-PID rendezvous directory the hub watches for live daemons.
	// Source: web_settings.go settingsData.RunDir (cfg.RunDir).
	RunDir string `json:"runDir,omitempty"`
	// SpawnTimeout is how long the hub waits for a daemon to report ready after
	// spawn. Source: web_settings.go settingsData.SpawnTimeout — today a
	// hardcoded "30s" literal, not derived from live spawner config (there is
	// no configurable spawn timeout yet).
	SpawnTimeout string `json:"spawnTimeout,omitempty"`
	// BearerTokenAge is a human-readable age of the hub's auth-token file (e.g.
	// "created 3d ago" / "just now"), empty if unavailable.
	// Source: web_settings.go settingsData.BearerTokenAge (fileAgeHuman over
	// hubedge.TokenFileName under HubStateRoot).
	BearerTokenAge string `json:"bearerTokenAge,omitempty"`
	// PastIndex is nil only when no past-session index is configured
	// (cfg.Past == nil) — e.g. a minimal/test hub config.
	PastIndex *SettingsPastIndexOverview `json:"pastIndex,omitempty"`
}

// SettingsPastIndexOverview describes the past-session SQLite index. Settings
// → General renders Path/Size/PerPage; Settings → Storage renders Path/Size/
// Count — both from this same object (the frontend reads hub.pastIndex for
// both pages; the value is not duplicated under Storage).
// Source: web_settings.go settingsData.PastIndexPath/PastIndexSize/
// PastPerPage/PastCount.
type SettingsPastIndexOverview struct {
	// Path is the past-index SQLite file path, tilde-shortened against $HOME.
	// Source: web_settings.go tildeHome(cfg.PastIndexPath).
	Path string `json:"path,omitempty"`
	// Size is a human-readable file size (e.g. "48 MB"), empty if the file
	// does not exist yet. Source: web_settings.go fileSizeHuman(cfg.PastIndexPath).
	Size string `json:"size,omitempty"`
	// PerPage is the configured /past results-per-page.
	// Source: web_settings.go settingsData.PastPerPage (cfg.PastPerPage).
	PerPage int `json:"perPage,omitempty"`
	// Count is the total number of indexed session metas, all-time — NOT a
	// count of currently-live/running sessions. The legacy storage.html
	// template's own copy calls this "currently tracking N sessions", which is
	// this same all-time indexed total. A genuine live-daemon count exists
	// (cfg.Roster) but is intentionally not surfaced here: no legacy settings
	// template ever rendered one.
	// Source: web_settings.go settingsData.PastCount (len(cfg.Past.AllMetas())).
	Count int `json:"count,omitempty"`
}

// SettingsStorageOverview is the Settings → Storage section (cmd/serf-hub/
// templates/partials/settings/storage.html). RunDir and the past-index
// path/size/count that storage.html also renders are not duplicated here —
// see SettingsHubOverview / SettingsPastIndexOverview, which the frontend
// reads for those (single source of truth; both live in the one overview
// response already).
type SettingsStorageOverview struct {
	// StateDir is the root directory for hub state: auth token, credentials,
	// and project sub-directories.
	// Source: web_settings.go settingsData.StateDir (cfg.StateDir).
	StateDir string `json:"stateDir,omitempty"`
}

// SettingsAgentEntry is one row in Settings → Agents (cmd/serf-hub/templates/
// partials/settings/agents.html) — today always exactly the three built-in
// agent names compiled into the binary (defaultPersona.txt etc.).
// Source: web_settings.go renderSettingsPartial's agentNames/agents
// construction.
type SettingsAgentEntry struct {
	Name string `json:"name"`
	// EditPath is an editor:// deep link to the agent's definition file. Always
	// empty today: built-in agents have no on-disk file to open. Kept for
	// shape parity with web_settings.go's agentDisplay.EditPath in case a
	// future on-disk agent source populates it.
	EditPath string `json:"editPath,omitempty"`
}

// SettingsCodexLaunchEntry is one row in Settings → Codex launch config
// (cmd/serf-hub/templates/partials/settings/launch-codex.html): a read-only
// display of one [[codex_launches]] hub.toml entry.
// Source: web_settings.go settingsData.CodexLaunches (cfg.CodexLaunches,
// codexlaunch.CodexLaunchConfig).
//
// Binary/WorkingDir/Listen/Timeout ride over the wire as their raw configured
// value (empty/zero when unset) rather than the template's display-fallback
// text ("codex", "(inherited)", "ws://127.0.0.1:0", "30s") — the frontend
// applies the same fallback at render time, so the wire stays truthful about
// what is actually configured.
//
// Args, BearerToken, and BearerTokenFile are intentionally excluded: Args is
// never rendered by the template, and the bearer-token fields are live
// secrets the credential-never-echo invariant forbids sending to the browser
// — the template never renders them either. EnvKeys carries only the Env
// map's keys, sorted, matching the template's own redaction ("Values are
// redacted here.").
type SettingsCodexLaunchEntry struct {
	ID         string `json:"id"`
	Binary     string `json:"binary,omitempty"`
	WorkingDir string `json:"workingDir,omitempty"`
	Listen     string `json:"listen,omitempty"`
	// TimeoutMillis is Timeout in milliseconds, 0 when unset (the template's
	// 30s default applies client-side, matching the other display fallbacks).
	TimeoutMillis int64    `json:"timeoutMillis,omitempty"`
	EnvKeys       []string `json:"envKeys,omitempty"`
}

// SettingsMCPServerEntry is one probed MCP server row in Settings → MCP
// servers' "Discovered servers" list (cmd/serf-hub/templates/partials/
// settings/mcp.html) — the probed/read-only half; the editable half (MCP
// config file list, inline server CRUD) rides the existing launch-config
// wire (serf/launch/getLayer + serf/launch/setLayer), not this method.
// Source: web_settings.go discoverMCPsForSettings's mcpDisplay, itself
// sourced from agent/mcpprobe.Result. Command, Args, Tools, Agents, and
// EditPath exist on mcpDisplay but are never rendered by mcp.html's
// discovered-servers block, so they are omitted here too.
type SettingsMCPServerEntry struct {
	Name      string `json:"name"`
	Transport string `json:"transport,omitempty"`
	Status    string `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SettingsMCPOverview is the Settings → MCP servers section's probed half.
// Source: web_settings.go mcpConfigPathForSettings + discoverMCPsForSettings.
// A missing MCP config file is the empty state (Servers empty, Error ""),
// matching discoverMCPsForSettings; Error is populated only on a real parse
// failure (e.g. malformed mcp.json), mirroring settingsData.McpsError.
//
// Each server probe (agent/mcpprobe.Probe) runs under its own bounded
// per-server timeout in parallel with the others, so this section's total
// latency stays bounded regardless of server count — see mcpprobe's package
// doc for the exact bound; this handler adds no further timeout on top of it.
type SettingsMCPOverview struct {
	Servers []SettingsMCPServerEntry `json:"servers,omitempty"`
	Error   string                   `json:"error,omitempty"`
}
