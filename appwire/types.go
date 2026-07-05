package appwire

import "encoding/json"

const ProtocolVersion = "serf-appwire-v1"

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
	MethodGoalSet                   = "goal/set"
	MethodSerfTasksList             = "serf/tasks/list"
	MethodSerfThreadTranscriptsList = "serf/thread/transcripts/list"
	MethodSerfSubagentPreview       = "serf/subagentPreview"
	MethodSerfDirsComplete          = "serf/dirs/complete"
	MethodSerfPathValidate          = "serf/path/validate"
	MethodSerfHarnessesList         = "serf/harnesses/list"
	MethodSerfUpgrade               = "serf/upgrade"
	MethodSerfAuthStatus            = "serf/auth/status"
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
)

const (
	NotifyThreadStarted          = "thread/started"
	NotifyThreadClosed           = "thread/closed"
	NotifyThreadStatusChanged    = "thread/status/changed"
	NotifyThreadQueueChanged     = "thread/queueChanged"
	NotifyTurnStarted            = "turn/started"
	NotifyTurnCompleted          = "turn/completed"
	NotifyItemStarted            = "item/started"
	NotifyItemCompleted          = "item/completed"
	NotifyAgentMessageDelta      = "item/agentMessage/delta"
	NotifyAgentMessageReset      = "item/agentMessage/reset"
	NotifyReasoningSummaryDelta  = "item/reasoning/summaryTextDelta"
	NotifyToolOutputDelta        = "item/toolOutput/delta"
	NotifyWarning                = "warning"
	NotifySerfContextPressure    = "serf/thread/contextPressure/updated"
	NotifySerfTaskUpdated        = "serf/task/updated"
	NotifySerfSteeringInjected   = "serf/steering/injected"
	NotifySerfJobStarted         = "serf/job/started"
	NotifySerfJobFinished        = "serf/job/finished"
	NotifySerfAuthUpdated        = "serf/auth/updated"
	NotifySerfLaunchUpdated      = "serf/launch/updated"
	NotifySerfAttentionChanged   = "serf/attention/changed"
	NotifySerfMarketplaceUpdated = "serf/marketplace/updated"
	NotifySerfPluginUpdated      = "serf/plugin/updated"
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
	ClientInfo   ClientInfo   `json:"clientInfo"`
	Capabilities Capabilities `json:"capabilities"`
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
	ID            string       `json:"id"`
	SessionID     string       `json:"sessionId"`
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
	Queue QueueState `json:"queue"`
	// Goal carries the session's /goal state when a goal is set, else nil.
	// It powers `/goal status` and a future status-bar indicator without a
	// bespoke transport — like Queue, it is structured per-session state read
	// from the already-fetched thread snapshot.
	Goal *GoalState `json:"goal,omitempty"`
}

// GoalState is the wire representation of a session's /goal. Status is the
// lifecycle status ("active", "complete", "blocked"); Iterations is the number
// of continuation turns taken. A nil *GoalState on SerfThread means no goal is
// set.
type GoalState struct {
	Status     string `json:"status"`
	Iterations int    `json:"iterations"`
}

// QueueState is the wire representation of a session's per-input queue
// (kata r80p). Depth is len(Preview) at projection time; Preview entries
// are FIFO with the head at index 0 and have been truncated to a single
// line so the UI can render them without further processing.
type QueueState struct {
	Depth   int      `json:"depth,omitempty"`
	Preview []string `json:"preview,omitempty"`
}

// ThreadQueueChangedParams is the params shape for thread/queueChanged
// (kata r80p). It mirrors the queue field on SerfThread so consumers can
// store it verbatim on the cached thread state.
type ThreadQueueChangedParams struct {
	ThreadID string     `json:"threadId,omitempty"`
	Ref      string     `json:"ref,omitempty"`
	Queue    QueueState `json:"queue"`
}

// TurnCompletedParams is the payload of a turn/completed notification: the
// completed turn and its ID.
type TurnCompletedParams struct {
	TurnID string `json:"turnId"`
	Turn   Turn   `json:"turn"`
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
	Name  string   `json:"name"`
	Tools []string `json:"tools"`
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
	ID          string       `json:"id"`
	Items       []ThreadItem `json:"items,omitempty"`
	ItemsView   string       `json:"itemsView"`
	Status      string       `json:"status"`
	Error       *TurnError   `json:"error,omitempty"`
	StartedAt   *int64       `json:"startedAt,omitempty"`
	CompletedAt *int64       `json:"completedAt,omitempty"`
	DurationMS  *int64       `json:"durationMs,omitempty"`
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

type ThreadItem struct {
	Type                 string          `json:"type"`
	ID                   string          `json:"id"`
	TurnID               string          `json:"turnId,omitempty"`
	TranscriptEntryIndex int             `json:"transcriptEntryIndex,omitempty"`
	Text                 string          `json:"text,omitempty"`
	Delta                string          `json:"delta,omitempty"`
	Images               []InputItem     `json:"images,omitempty"`
	ToolName             string          `json:"toolName,omitempty"`
	CallID               string          `json:"callId,omitempty"`
	ArgumentsJSON        string          `json:"argumentsJson,omitempty"`
	Description          string          `json:"description,omitempty"`
	Output               string          `json:"output,omitempty"`
	Error                string          `json:"error,omitempty"`
	Status               string          `json:"status,omitempty"`
	StartedAt            *int64          `json:"startedAt,omitempty"`
	CompletedAt          *int64          `json:"completedAt,omitempty"`
	Raw                  json.RawMessage `json:"raw,omitempty"`
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
	Ref           string `json:"ref"`
	SourceTurnID  string `json:"sourceTurnId"`
	EditedInput   string `json:"editedInput,omitempty"`
	Label         string `json:"label,omitempty"`
	ModelProvider string `json:"modelProvider,omitempty"`
	Model         string `json:"model,omitempty"`
}

type ThreadForkResponse struct {
	Thread Thread `json:"thread"`
}

type TurnStartParams struct {
	Ref      string      `json:"ref,omitempty"`
	ThreadID string      `json:"threadId,omitempty"`
	Input    []InputItem `json:"input,omitempty"`
}

type TurnStartResponse struct {
	Turn Turn `json:"turn"`
}

type TurnSteerParams struct {
	Ref            string      `json:"ref,omitempty"`
	ThreadID       string      `json:"threadId,omitempty"`
	ExpectedTurnID string      `json:"expectedTurnId,omitempty"`
	Input          []InputItem `json:"input,omitempty"`
}

type TurnInterruptParams struct {
	Ref            string `json:"ref,omitempty"`
	ThreadID       string `json:"threadId,omitempty"`
	ExpectedTurnID string `json:"expectedTurnId,omitempty"`
}

// TurnQueueParams queues a user message during a running turn for processing
// after the active turn completes. The daemon enqueues immediately and returns;
// no turn id is reserved or returned.
type TurnQueueParams struct {
	Ref   string      `json:"ref"`
	Input []InputItem `json:"input,omitempty"`
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
	Ref   string      `json:"ref"`
	Input []InputItem `json:"input,omitempty"`
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

type DirsCompleteParams struct {
	Prefix string `json:"prefix"`
	Limit  int    `json:"limit,omitempty"`
}

type DirsCompleteResponse struct {
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
}

type EmptyResponse struct{}

type ThreadStatusChangedParams struct {
	ThreadID string       `json:"threadId"`
	Ref      string       `json:"ref,omitempty"`
	Status   ThreadStatus `json:"status"`
}

type AgentMessageDeltaParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref,omitempty"`
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
	Ref          string `json:"ref,omitempty"`
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
	Ref      string `json:"ref,omitempty"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
}

// ToolOutputDeltaParams is the params shape for the item/toolOutput/delta
// notification. ItemID identifies the tool-call item; CallID is the legacy
// alias kept for clients that still key on it.
type ToolOutputDeltaParams struct {
	ThreadID string `json:"threadId,omitempty"`
	Ref      string `json:"ref,omitempty"`
	TurnID   string `json:"turnId,omitempty"`
	ItemID   string `json:"itemId"`
	CallID   string `json:"callId"`
	Delta    string `json:"delta"`
}

// NotificationRef carries just the routing fields shared by most
// notifications (ref + threadId). Use it when you only need to know which
// session a notification belongs to.
type NotificationRef struct {
	Ref      string `json:"ref,omitempty"`
	ThreadID string `json:"threadId,omitempty"`
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
// "authorized", or "expired". Status is populated only when authorized.
type AuthDevicePollResponse struct {
	State  string             `json:"state"`
	Status AuthStatusResponse `json:"status,omitempty"`
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
	MaxRounds                   *int              `json:"maxRounds,omitempty"`
	MaxSubagentDepth            *int              `json:"maxSubagentDepth,omitempty"`
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
	RawHTTPLogging              *bool             `json:"rawHTTPLogging,omitempty"`
	TraceFile                   string            `json:"traceFile,omitempty"`
	CPUProfile                  string            `json:"cpuProfile,omitempty"`
	ExportATIFPath              string            `json:"exportATIFPath,omitempty"`
	ExportATIFProviderHandles   string            `json:"exportATIFProviderHandles,omitempty"`
}

func (l LaunchConfigLayer) MarshalJSON() ([]byte, error) {
	type alias LaunchConfigLayer
	a := alias(l)
	a.ModelFallbacks = nil
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if l.ModelFallbacks == nil {
		return raw, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	modelFallbacks, err := json.Marshal(l.ModelFallbacks)
	if err != nil {
		return nil, err
	}
	obj["modelFallbacks"] = modelFallbacks
	return json.Marshal(obj)
}

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
