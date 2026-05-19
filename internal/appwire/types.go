package appwire

import "encoding/json"

const ProtocolVersion = "serf-appwire-v1"

const (
	MethodInitialize                = "initialize"
	MethodInitialized               = "initialized"
	MethodThreadList                = "thread/list"
	MethodThreadRead                = "thread/read"
	MethodThreadTurnsList           = "thread/turns/list"
	MethodThreadTurnItemsList       = "thread/turns/items/list"
	MethodThreadStart               = "thread/start"
	MethodThreadResume              = "thread/resume"
	MethodThreadFork                = "thread/fork"
	MethodThreadClear               = "thread/clear"
	MethodThreadModelSet            = "thread/model/set"
	MethodThreadCompactStart        = "thread/compact/start"
	MethodThreadShutdown            = "thread/shutdown"
	MethodTurnStart                 = "turn/start"
	MethodTurnSteer                 = "turn/steer"
	MethodTurnInterrupt             = "turn/interrupt"
	MethodTurnQueue                 = "turn/queue"
	MethodTurnDrainAsSteer          = "turn/drainAsSteer"
	MethodSerfTasksList             = "serf/tasks/list"
	MethodSerfThreadTranscriptsList = "serf/thread/transcripts/list"
	MethodSerfDirsComplete          = "serf/dirs/complete"
	MethodSerfHarnessesList         = "serf/harnesses/list"
	MethodSerfAuthStatus            = "serf/auth/status"
	MethodSerfAuthLoginStart        = "serf/auth/login/start"
	MethodSerfAuthLoginComplete     = "serf/auth/login/complete"
	MethodSerfAuthLogout            = "serf/auth/logout"
	MethodSerfAuthList              = "serf/auth/list"
	MethodSerfAuthApiKeySet         = "serf/auth/apiKey/set"
	MethodSerfLaunchResolve         = "serf/launch/resolve"
	MethodSerfLaunchGetLayer        = "serf/launch/getLayer"
	MethodSerfLaunchSetLayer        = "serf/launch/setLayer"
	MethodSerfLaunchTrustRepo       = "serf/launch/trustRepo"
	MethodModelList                 = "model/list"
)

const (
	NotifyThreadStarted        = "thread/started"
	NotifyThreadClosed         = "thread/closed"
	NotifyThreadStatusChanged  = "thread/status/changed"
	NotifyThreadQueueChanged   = "thread/queueChanged"
	NotifyTurnStarted          = "turn/started"
	NotifyTurnCompleted        = "turn/completed"
	NotifyItemStarted          = "item/started"
	NotifyItemCompleted        = "item/completed"
	NotifyAgentMessageDelta    = "item/agentMessage/delta"
	NotifyToolOutputDelta      = "item/toolOutput/delta"
	NotifyWarning              = "warning"
	NotifySerfContextPressure  = "serf/thread/contextPressure/updated"
	NotifySerfTaskUpdated      = "serf/task/updated"
	NotifySerfSteeringInjected = "serf/steering/injected"
	NotifySerfSubagentStarted  = "serf/subagent/started"
	NotifySerfSubagentEnded    = "serf/subagent/completed"
	NotifySerfAuthUpdated      = "serf/auth/updated"
	NotifySerfLaunchUpdated    = "serf/launch/updated"
)

const (
	ThreadStatusIdle        = "idle"
	ThreadStatusActive      = "active"
	ThreadStatusProcessing  = ThreadStatusActive
	ThreadStatusAwaiting    = "awaiting"
	ThreadStatusWarning     = "warning"
	ThreadStatusClosed      = "closed"
	ThreadStatusNotLoaded   = "notLoaded"
	ThreadStatusEnded       = ThreadStatusNotLoaded
	ThreadStatusSystemError = "systemError"
	ThreadStatusError       = ThreadStatusSystemError
)

const (
	TurnStatusInProgress  = "inProgress"
	TurnStatusRunning     = TurnStatusInProgress
	TurnStatusCompleted   = "completed"
	TurnStatusFailed      = "failed"
	TurnStatusInterrupted = "interrupted"
	TurnStatusCanceled    = TurnStatusInterrupted
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
	// TurnDrainAsSteerInput means turn/drainAsSteer accepts Text/Items and
	// atomically appends them before draining.
	TurnDrainAsSteerInput bool `json:"turnDrainAsSteerInput"`
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
	Ref             string             `json:"ref"`
	ParentRef       string             `json:"parentRef,omitempty"`
	Kind            string             `json:"kind,omitempty"`
	Profile         string             `json:"profile,omitempty"`
	ContextPressure float64            `json:"contextPressure,omitempty"`
	Capabilities    ThreadCapabilities `json:"capabilities"`
	Diagnostics     *SerfDiagnostics   `json:"diagnostics,omitempty"`
	// Queue carries authoritative queue depth + preview for the per-session
	// input queue (kata r80p). Both UIs derive their queue-preview chrome
	// from this field rather than mirroring queue mutations locally, which
	// fixes multi-client incoherence and post-reload state. The empty zero
	// value (Depth==0, Preview==nil) means "no queued messages".
	Queue QueueState `json:"queue"`
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
}

type SerfDiagnostics struct {
	Tools     []SerfToolInfo      `json:"tools,omitempty"`
	MCP       []SerfMCPServerInfo `json:"mcp,omitempty"`
	Skills    []SerfSkillInfo     `json:"skills,omitempty"`
	Plugins   []SerfPluginInfo    `json:"plugins,omitempty"`
	Hooks     map[string]int      `json:"hooks,omitempty"`
	Subagents []SerfSubagentInfo  `json:"subagents,omitempty"`
	Agents    []string            `json:"agents,omitempty"`
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

type SerfSubagentInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	TurnsUsed int    `json:"turnsUsed"`
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
	Message string           `json:"message"`
	Source  string           `json:"source,omitempty"`
	Title   string           `json:"title,omitempty"`
	Hint    string           `json:"hint,omitempty"`
	Cause   *DiagnosticCause `json:"cause,omitempty"`
}

// DiagnosticCause is the wire-level structured cause attached to a
// warning/error notification. Today the only Kind is "provider" (an HTTP
// failure from an LLM adapter); consumers can typed-branch on Kind
// instead of substring-matching the message (kata cmfz). The agent's
// agent.ErrorCause projects to this shape; absence is signaled by an
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
}

type ThreadReadResponse struct {
	Thread Thread `json:"thread"`
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

type ThreadStartParams struct {
	Harness         string             `json:"harness,omitempty"`
	CWD             string             `json:"cwd"`
	Prompt          string             `json:"prompt,omitempty"`
	Items           []InputItem        `json:"items,omitempty"`
	Input           []InputItem        `json:"input,omitempty"`
	ModelProvider   string             `json:"modelProvider,omitempty"`
	Model           string             `json:"model,omitempty"`
	Profile         string             `json:"profile,omitempty"`
	ReasoningEffort string             `json:"reasoningEffort,omitempty"`
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
	Prompt   string      `json:"prompt,omitempty"`
	Items    []InputItem `json:"items,omitempty"`
	Input    []InputItem `json:"input,omitempty"`
}

type TurnStartResponse struct {
	Turn Turn `json:"turn"`
}

type TurnSteerParams struct {
	Ref            string      `json:"ref,omitempty"`
	ThreadID       string      `json:"threadId,omitempty"`
	TurnID         string      `json:"turnId"`
	ExpectedTurnID string      `json:"expectedTurnId,omitempty"`
	Text           string      `json:"text"`
	Input          []InputItem `json:"input,omitempty"`
}

type TurnInterruptParams struct {
	Ref            string `json:"ref,omitempty"`
	ThreadID       string `json:"threadId,omitempty"`
	TurnID         string `json:"turnId,omitempty"`
	ExpectedTurnID string `json:"expectedTurnId,omitempty"`
}

// TurnQueueParams is the wire shape for turn/queue (kata 111a). Queues a
// user message during a running turn for processing after the active turn
// completes. The daemon enqueues immediately and returns; no turn id is
// reserved or returned. Items optionally carries attachments (e.g. image
// bytes — kata t5j6) that flow through to the drained user turn alongside
// the text.
type TurnQueueParams struct {
	Ref   string      `json:"ref"`
	Text  string      `json:"text"`
	Items []InputItem `json:"items,omitempty"`
}

// TurnDrainAsSteerParams is the wire shape for turn/drainAsSteer (kata
// 0bq1 force-steer combined action). Pops every queued message and sends
// them to the in-flight turn as a single STEERING message. Text/Items let
// clients atomically append the current composer payload before the drain.
type TurnDrainAsSteerParams struct {
	Ref   string      `json:"ref"`
	Text  string      `json:"text,omitempty"`
	Items []InputItem `json:"items,omitempty"`
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

// AuthListResponse is the result of serf/auth/list.
type AuthListResponse struct {
	Providers []AuthStatusResponse `json:"providers"`
}

// AuthApiKeySetParams is the params for serf/auth/apiKey/set.
type AuthApiKeySetParams struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

// LaunchConfigLayer is the wire-level partial layer (every field optional;
// pointer-typed scalars so "not set" is distinguishable from zero).
type LaunchConfigLayer struct {
	Schema             *int              `json:"schema,omitempty"`
	Model              string            `json:"model,omitempty"`
	Agent              string            `json:"agent,omitempty"`
	ReasoningEffort    string            `json:"reasoningEffort,omitempty"`
	ContextStrategy    string            `json:"contextStrategy,omitempty"`
	MaxRounds          *int              `json:"maxRounds,omitempty"`
	MaxSubagentDepth   *int              `json:"maxSubagentDepth,omitempty"`
	NoProjectPrompts   *bool             `json:"noProjectPrompts,omitempty"`
	AppReplaySize      *int              `json:"appReplaySize,omitempty"`
	SkillsDirs         []string          `json:"skillsDirs,omitempty"`
	PluginDirs         []string          `json:"pluginDirs,omitempty"`
	MCPConfigs         []string          `json:"mcpConfigs,omitempty"`
	SystemPromptAppend []string          `json:"systemPromptAppend,omitempty"`
	ModelFallbacks     []string          `json:"modelFallbacks,omitempty"`
	MCPs               []MCPServerSpec   `json:"mcps,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
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
