package appwire

import "encoding/json"

const ProtocolVersion = "serf-appwire-v1"

const (
	MethodInitialize          = "initialize"
	MethodThreadList          = "thread/list"
	MethodThreadRead          = "thread/read"
	MethodThreadTurnsList     = "thread/turns/list"
	MethodThreadTurnItemsList = "thread/turns/items/list"
	MethodThreadStart         = "thread/start"
	MethodThreadResume        = "thread/resume"
	MethodThreadFork          = "thread/fork"
	MethodThreadClear         = "thread/clear"
	MethodThreadModelSet      = "thread/model/set"
	MethodThreadCompactStart  = "thread/compact/start"
	MethodThreadShutdown      = "thread/shutdown"
	MethodTurnStart           = "turn/start"
	MethodTurnSteer           = "turn/steer"
	MethodTurnInterrupt       = "turn/interrupt"
	MethodSerfTasksList       = "serf/tasks/list"
	MethodSerfDirsComplete    = "serf/dirs/complete"
	MethodSerfHarnessesList   = "serf/harnesses/list"
	MethodModelList           = "model/list"
)

const (
	NotifyThreadStarted        = "thread/started"
	NotifyThreadStatusChanged  = "thread/status/changed"
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
)

const (
	ThreadStatusIdle       = "idle"
	ThreadStatusProcessing = "processing"
	ThreadStatusAwaiting   = "awaiting"
	ThreadStatusWarning    = "warning"
	ThreadStatusClosed     = "closed"
	ThreadStatusEnded      = "ended"
	ThreadStatusError      = "error"
)

const (
	TurnStatusRunning   = "running"
	TurnStatusCompleted = "completed"
	TurnStatusFailed    = "failed"
	TurnStatusCanceled  = "canceled"
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
	ModelList         bool `json:"modelList"`
	DirectoryComplete bool `json:"directoryComplete"`
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
	Profile         string             `json:"profile,omitempty"`
	ContextPressure float64            `json:"contextPressure,omitempty"`
	Capabilities    ThreadCapabilities `json:"capabilities"`
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
	Message string `json:"message"`
	Source  string `json:"source,omitempty"`
	Title   string `json:"title,omitempty"`
	Hint    string `json:"hint,omitempty"`
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
	ThreadID     string `json:"threadId,omitempty"`
	Ref          string `json:"ref,omitempty"`
	IncludeTurns bool   `json:"includeTurns"`
	ItemsView    string `json:"itemsView,omitempty"`
	Subscribe    bool   `json:"subscribe,omitempty"`
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

type ThreadStartParams struct {
	Harness         string      `json:"harness,omitempty"`
	CWD             string      `json:"cwd"`
	Prompt          string      `json:"prompt,omitempty"`
	Items           []InputItem `json:"items,omitempty"`
	ModelProvider   string      `json:"modelProvider,omitempty"`
	Model           string      `json:"model,omitempty"`
	Profile         string      `json:"profile,omitempty"`
	ReasoningEffort string      `json:"reasoningEffort,omitempty"`
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
	Ref    string      `json:"ref"`
	Prompt string      `json:"prompt,omitempty"`
	Items  []InputItem `json:"items,omitempty"`
}

type TurnStartResponse struct {
	Turn Turn `json:"turn"`
}

type TurnSteerParams struct {
	Ref    string `json:"ref"`
	TurnID string `json:"turnId"`
	Text   string `json:"text"`
}

type TurnInterruptParams struct {
	Ref    string `json:"ref"`
	TurnID string `json:"turnId,omitempty"`
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
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind,omitempty"`
}

type HarnessListResponse struct {
	Data []HarnessDescriptor `json:"data"`
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
