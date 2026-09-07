package appwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ProtocolVersion is compared exactly at the handshake
// (internal/appserver/server.go), so bumping it makes a mixed pair of binaries
// fail once, loudly, at initialize -- rather than agreeing there and then
// disagreeing on every request.
//
// v4 makes transcript reads item-only and rejects retired paging fields. v3
// dropped expectedTurnId from turn/steer, turn/queue, turn/interrupt,
// turn/drainAsSteer and turn/promoteQueuedAsSteer: control is session-scoped and
// names no turn. A v2 daemon still requires that field, so a v3 client talking
// to one would get a Conflict per button press and the user would read it as
// "Steer and Stop are broken again" instead of as a version skew. The pair is
// reachable in ordinary operation because daemons outlive the hub that spawned
// them, so an operator who rebuilds and restarts the hub has one.
const ProtocolVersion = "evener-appwire-v4"

// ThreadStatusRestartRequired identifies a live daemon that cannot serve this
// hub's protocol. Its current activity is unavailable until explicitly restarted.
const ThreadStatusRestartRequired = "restartRequired"

const (
	MethodInitialize                  = "initialize"
	MethodInitialized                 = "initialized"
	MethodPing                        = "ping"
	MethodThreadList                  = "thread/list"
	MethodThreadRead                  = "thread/read"
	MethodThreadUnsubscribe           = "thread/unsubscribe"
	MethodThreadTurnsList             = "thread/turns/list"
	MethodThreadTurnItemsList         = "thread/turns/items/list"
	MethodThreadStart                 = "thread/start"
	MethodThreadResume                = "thread/resume"
	MethodThreadFork                  = "thread/fork"
	MethodThreadClear                 = "thread/clear"
	MethodThreadModelSet              = "thread/model/set"
	MethodThreadReasoningEffortSet    = "thread/reasoning-effort/set"
	MethodThreadVisionModelSet        = "thread/vision-model/set"
	MethodThreadCompactStart          = "thread/compact/start"
	MethodThreadShutdown              = "thread/shutdown"
	MethodTurnStart                   = "turn/start"
	MethodTurnSteer                   = "turn/steer"
	MethodTurnInterrupt               = "turn/interrupt"
	MethodTurnQueue                   = "turn/queue"
	MethodTurnDrainAsSteer            = "turn/drainAsSteer"
	MethodTurnPromoteQueuedAsSteer    = "turn/promoteQueuedAsSteer"
	MethodTurnCancelQueued            = "turn/cancelQueued"
	MethodGoalSet                     = "goal/set"
	MethodEvenerTasksList             = "evener/tasks/list"
	MethodEvenerJobsList              = "evener/jobs/list"
	MethodEvenerJobsOutput            = "evener/jobs/output"
	MethodEvenerThreadNameSet         = "evener/thread/name/set"
	MethodEvenerThreadTranscriptsList = "evener/thread/transcripts/list"
	MethodEvenerSubagentPreview       = "evener/subagentPreview"
	MethodEvenerPathsComplete         = "evener/paths/complete"
	MethodEvenerDirsCreate            = "evener/dirs/create"
	MethodEvenerProjectsRecent        = "evener/projects/recent"
	MethodEvenerPathValidate          = "evener/path/validate"
	MethodEvenerGitHead               = "evener/git/head"
	MethodEvenerMobilePairing         = "evener/mobile/pairing"
	MethodEvenerNavigationRead        = "evener/navigation/read"
	MethodEvenerFavoriteSet           = "evener/favorite/set"
	MethodEvenerArchiveSet            = "evener/archive/set"
	MethodEvenerProjectDelete         = "evener/project/delete"
	MethodEvenerSessionDelete         = "evener/session/delete"
	MethodEvenerPinSectionRename      = "evener/pin-section/rename"
	MethodEvenerPinSectionDelete      = "evener/pin-section/delete"
	MethodEvenerSessionPinAssign      = "evener/session-pin/assign"
	MethodEvenerSessionPinUnpin       = "evener/session-pin/unpin"
	MethodEvenerSearch                = "evener/search"
	MethodEvenerHarnessesList         = "evener/harnesses/list"
	MethodEvenerUpgrade               = "evener/upgrade"
	MethodEvenerAuthStatus            = "evener/auth/status"
	MethodEvenerAuthTest              = "evener/auth/test"
	MethodEvenerAuthLoginStart        = "evener/auth/login/start"
	MethodEvenerAuthLoginComplete     = "evener/auth/login/complete"
	MethodEvenerAuthLogout            = "evener/auth/logout"
	MethodEvenerAuthList              = "evener/auth/list"
	MethodEvenerAuthApiKeySet         = "evener/auth/apiKey/set"
	MethodEvenerAuthApiKeyClear       = "evener/auth/apiKey/clear"
	MethodEvenerAuthCredentialJsonSet = "evener/auth/credentialJson/set"
	MethodEvenerAuthDeviceStart       = "evener/auth/device/start"
	MethodEvenerAuthDevicePoll        = "evener/auth/device/poll"
	MethodEvenerLaunchResolve         = "evener/launch/resolve"
	MethodEvenerLaunchSchema          = "evener/launch/schema"
	MethodEvenerLaunchGetLayer        = "evener/launch/getLayer"
	MethodEvenerLaunchSetLayer        = "evener/launch/setLayer"
	MethodEvenerLaunchTrustRepo       = "evener/launch/trustRepo"
	MethodModelList                   = "model/list"
	MethodEvenerInstanceList          = "evener/instance/list"
	MethodEvenerInstanceCreate        = "evener/instance/create"
	MethodEvenerInstanceEdit          = "evener/instance/edit"
	MethodEvenerInstanceRemove        = "evener/instance/remove"
	MethodEvenerInstanceSetDefault    = "evener/instance/setDefault"
	MethodEvenerPluginCheckNow        = "evener/plugin/checkNow"
	MethodEvenerPluginPreview         = "evener/plugin/preview"
	MethodEvenerMarketplaceList       = "evener/marketplace/list"
	MethodEvenerMarketplaceAdd        = "evener/marketplace/add"
	MethodEvenerMarketplaceRemove     = "evener/marketplace/remove"
	MethodEvenerMarketplaceRefresh    = "evener/marketplace/refresh"
	MethodEvenerMarketplaceBrowse     = "evener/marketplace/browse"
	MethodEvenerPluginList            = "evener/plugin/list"
	MethodEvenerPluginInstall         = "evener/plugin/install"
	MethodEvenerPluginUpgrade         = "evener/plugin/upgrade"
	MethodEvenerPluginRemove          = "evener/plugin/remove"
	MethodEvenerPluginEnable          = "evener/plugin/enable"
	MethodEvenerPluginDisable         = "evener/plugin/disable"
	MethodEvenerPluginSetAutoUpgrade  = "evener/plugin/setAutoUpgrade"
	MethodEvenerCommandList           = "evener/command/list"
	// MethodEvenerSettingsOverview returns the field bag behind five settings
	// sections whose only data path today is Go-template variables:
	// hub/runtime, storage, agent roster, and probed MCP servers. See
	// SettingsOverviewResponse's doc comment.
	MethodEvenerSettingsOverview = "evener/settings/overview"
	// MethodEvenerSandboxEscalationResolve delivers a human's approve/deny decision
	// for a pending sandbox-exemption escalation (M7). Client→server; ScopeBoth
	// (daemon serves it; hub relays). It is a UI-only request, never advertised to
	// the model.
	MethodEvenerSandboxEscalationResolve = "evener/sandbox/escalation/resolve"
)

const (
	NotifyThreadStarted       = "thread/started"
	NotifyThreadClosed        = "thread/closed"
	NotifyThreadStatusChanged = "thread/status/changed"
	NotifyThreadQueueChanged  = "thread/queueChanged"
	NotifyThreadNameChanged   = "evener/thread/name/changed"
	// NotifyThreadModelChanged pushes a mid-session model/provider switch so
	// clients converge without re-reading the thread. See ThreadModelChangedParams.
	NotifyThreadModelChanged = "thread/model/changed"
	// NotifyThreadReasoningEffortChanged pushes a mid-session reasoning-effort
	// change. See ThreadReasoningEffortChangedParams.
	NotifyThreadReasoningEffortChanged = "thread/reasoning-effort/changed"
	// NotifyThreadVisionModelChanged pushes a mid-session vision-model change.
	// See ThreadVisionModelChangedParams.
	NotifyThreadVisionModelChanged    = "thread/vision-model/changed"
	NotifyTurnStarted                 = "turn/started"
	NotifyTurnCompleted               = "turn/completed"
	NotifyItemStarted                 = "item/started"
	NotifyItemCompleted               = "item/completed"
	NotifyAgentMessageDelta           = "item/agentMessage/delta"
	NotifyAgentMessageReset           = "item/agentMessage/reset"
	NotifyReasoningSummaryDelta       = "item/reasoning/summaryTextDelta"
	NotifyToolOutputDelta             = "item/toolOutput/delta"
	NotifyWarning                     = "warning"
	NotifyEvenerContextPressure       = "evener/thread/contextPressure/updated"
	NotifyEvenerThreadModelRetry      = "evener/thread/modelRetry"
	NotifyEvenerThreadResync          = "evener/thread/resync"
	NotifyEvenerTaskUpdated           = "evener/task/updated"
	NotifyEvenerGoalUpdated           = "evener/goal/updated"
	NotifyEvenerSteeringInjected      = "evener/steering/injected"
	NotifyEvenerJobStarted            = "evener/job/started"
	NotifyEvenerJobFinished           = "evener/job/finished"
	NotifyEvenerDelegateUpdated       = "evener/delegate/updated"
	NotifyEvenerJobsTreeUpdated       = "evener/jobs/treeUpdated"
	NotifyEvenerAuthUpdated           = "evener/auth/updated"
	NotifyEvenerLaunchUpdated         = "evener/launch/updated"
	NotifyEvenerAttentionChanged      = "evener/attention/changed"
	NotifyEvenerNavigationInvalidated = "evener/navigation/invalidated"
	NotifyEvenerMarketplaceUpdated    = "evener/marketplace/updated"
	NotifyEvenerPluginUpdated         = "evener/plugin/updated"
	// NotifyEvenerSandboxEscalationRequested pushes a harness-raised, human-gated
	// sandbox-exemption approval card to the client (M7). The tool-exec goroutine
	// blocks until the client answers with MethodEvenerSandboxEscalationResolve.
	NotifyEvenerSandboxEscalationRequested = "evener/sandbox/escalation/requested"
	// NotifyEvenerSandboxEscalationResolved pushes notice that a previously-raised
	// escalation left the pending set (M7, wire-honesty spec Part B): resolved
	// explicitly, cleared by turn-interrupt, or cleared by session close. Every
	// OTHER subscribed client uses it to clear its own now-stale copy of the
	// card. Emitted exactly once per escalation, from the convergence point in
	// agent/session_escalation.go's escalateOnSandboxDenial.
	NotifyEvenerSandboxEscalationResolved = "evener/sandbox/escalation/resolved"
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
	ServerInfo      ServerInfo            `json:"serverInfo"`
	ProtocolVersion string                `json:"protocolVersion"`
	SourceID        string                `json:"sourceId"`
	Features        FeatureSet            `json:"features"`
	Navigation      *NavigationCapability `json:"navigation,omitempty"`
}

// NavigationCapability advertises the version and current ordered AppWire
// invalidation stream for the hub's navigation resources.
type NavigationCapability struct {
	Version      int    `json:"version"`
	GenerationID string `json:"generationId"`
	Sequence     uint64 `json:"sequence"`
	ReadVersions []int  `json:"readVersions,omitempty"`
}

// NavigationReadParams selects one bounded hub navigation resource. Offset
// and Limit are pointers so an explicit zero remains distinguishable from an
// omitted page parameter on the wire.
type NavigationReadParams struct {
	RepresentationVersion uint8               `json:"representationVersion"`
	Resource              string              `json:"resource"`
	Section               string              `json:"section,omitempty"`
	SectionID             string              `json:"sectionId,omitempty"`
	Catalog               string              `json:"catalog,omitempty"`
	ProjectKey            string              `json:"projectKey,omitempty"`
	Tier                  string              `json:"tier,omitempty"`
	Ref                   string              `json:"ref,omitempty"`
	Offset                *uint32             `json:"offset,omitempty"`
	Limit                 *uint32             `json:"limit,omitempty"`
	Base                  *NavigationReadBase `json:"base,omitempty"`
}

func (params *NavigationReadParams) UnmarshalJSON(data []byte) error {
	type wire NavigationReadParams
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if base, present := fields["base"]; present {
		var baseFields map[string]json.RawMessage
		if err := json.Unmarshal(base, &baseFields); err != nil || baseFields == nil || decoded.Base == nil {
			return errors.New("base must be an object when present")
		}
		if len(baseFields) != 3 {
			return errors.New("invalid navigation base")
		}
		for _, name := range []string{"generationId", "revision", "etag"} {
			value, ok := baseFields[name]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return errors.New("invalid navigation base")
			}
		}
		if decoded.Base.GenerationID == "" || decoded.Base.ETag == "" || len(decoded.Base.GenerationID) > 256 || len(decoded.Base.ETag) > 1024 || decoded.Base.Revision > 9007199254740991 {
			return errors.New("invalid navigation base")
		}
	}
	if decoded.RepresentationVersion != 2 {
		return errors.New("representationVersion must be 2")
	}
	if _, present := fields["etag"]; present {
		return errors.New("etag is not a v2 field")
	}
	*params = NavigationReadParams(decoded)
	return nil
}

type NavigationReadBase struct {
	GenerationID string `json:"generationId"`
	Revision     uint64 `json:"revision"`
	ETag         string `json:"etag"`
}

// NavigationReadResponse carries one revisioned navigation resource. Data is
// raw JSON because the resource discriminator selects among several existing
// projection shapes.
type NavigationReadResponse struct {
	Status         string                   `json:"status"`
	Representation NavigationRepresentation `json:"representation,omitempty"`
	GenerationID   string                   `json:"generationId"`
	Revision       uint64                   `json:"revision"`
	ETag           string                   `json:"etag"`
	Base           *NavigationReadBase      `json:"base,omitempty"`
	Data           json.RawMessage          `json:"data,omitempty"`
}

type NavigationRepresentation string

const (
	NavigationRepresentationSnapshot NavigationRepresentation = "snapshot"
	NavigationRepresentationDelta    NavigationRepresentation = "delta"
)

// FavoriteSetParams selects the project favorite decision to persist. Kind is
// retained so the typed method preserves the explicit rejection for the
// obsolete session-favorite request shape.
type FavoriteSetParams struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Favorited bool   `json:"favorited"`
}

// FavoriteSetResponse acknowledges the committed favorite decision and gives
// clients the exact navigation targets to converge before the invalidation
// notification arrives.
type FavoriteSetResponse struct {
	OK         bool               `json:"ok"`
	Navigation NavigationMutation `json:"navigation"`
}

// NavigationMutation is returned by hub-owned mutations after navigation
// state has committed, so clients can converge before the matching AppWire
// event. Targets is intentionally a plain slice so existing hubapi callers
// can assign their named NavigationArray values to it.
type NavigationMutation struct {
	GenerationID string                         `json:"generation_id"` //nolint:tagliatelle // wire field uses the established snake_case name
	Targets      []NavigationInvalidationTarget `json:"targets"`
}

// MarshalJSON keeps the navigation wire contract's arrays non-null even for a
// zero-value mutation.
func (mutation NavigationMutation) MarshalJSON() ([]byte, error) {
	targets := mutation.Targets
	if targets == nil {
		targets = []NavigationInvalidationTarget{}
	}
	return json.Marshal(struct {
		GenerationID string                         `json:"generation_id"` //nolint:tagliatelle // wire field uses the established snake_case name
		Targets      []NavigationInvalidationTarget `json:"targets"`
	}{GenerationID: mutation.GenerationID, Targets: targets})
}

// ArchiveTargetKind identifies the hub-owned object whose explicit archive
// decision is being changed.
type ArchiveTargetKind string

const (
	ArchiveTargetSession ArchiveTargetKind = "session"
	ArchiveTargetProject ArchiveTargetKind = "project"
)

// ArchiveParams sets or clears an explicit archive decision. Project targets
// must include the working directory used to resolve and verify their
// canonical project ID; session targets omit it.
type ArchiveParams struct {
	Kind       ArchiveTargetKind `json:"kind"`
	ID         string            `json:"id"`
	WorkingDir string            `json:"workingDir,omitempty"`
	Archived   bool              `json:"archived"`
}

// ArchiveResponse confirms the durable decision and returns the navigation
// receipt committed by the same refresh that publishes its invalidation.
type ArchiveResponse struct {
	OK         bool               `json:"ok"`
	Navigation NavigationMutation `json:"navigation"`
}

// ProjectDeleteParams identifies the exact local project to delete. WorkingDir
// is resolved independently and must produce Key before any destructive work.
type ProjectDeleteParams struct {
	Key        string `json:"key"`
	WorkingDir string `json:"workingDir"`
}

// ProjectDeleteSkip records one session that a project deletion could not own
// or remove without weakening its live-session and deletion-fence protections.
type ProjectDeleteSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// ProjectDeleteResponse reports exactly which project sessions were removed or
// skipped and the navigation mutation committed for the resulting tree.
type ProjectDeleteResponse struct {
	Deleted    []string            `json:"deleted"`
	Skipped    []ProjectDeleteSkip `json:"skipped"`
	Navigation NavigationMutation  `json:"navigation"`
}

// ProjectDeleteConflictData preserves the live-session details returned when
// a whole-project delete refuses to begin.
type ProjectDeleteConflictData struct {
	ErrorData
	Live []string `json:"live"`
}

// SessionDeleteParams selects one local session by its qualified AppWire ref.
type SessionDeleteParams struct {
	Ref string `json:"ref"`
}

// DeletionSkip reports a target that could not be deleted without falsely
// presenting it as deleted. Live and concurrently reserved sessions use this
// completed response path rather than a wire error.
type DeletionSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// SessionDeleteResponse reports the committed single-session deletion outcome
// and the navigation mutation clients must converge before clearing overlays.
type SessionDeleteResponse struct {
	Deleted    []string           `json:"deleted"`
	Skipped    []DeletionSkip     `json:"skipped"`
	Navigation NavigationMutation `json:"navigation"`
}

// PinSection is one named navigation group and its current durable membership.
type PinSection struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"memberCount"`
}

type PinSectionRenameParams struct {
	SectionID string `json:"sectionId"`
	Name      string `json:"name"`
}

type PinSectionRenameResponse struct {
	OK         bool               `json:"ok"`
	Changed    bool               `json:"changed"`
	Section    PinSection         `json:"section"`
	Navigation NavigationMutation `json:"navigation"`
}

type PinSectionDeleteParams struct {
	SectionID string `json:"sectionId"`
}

type PinSectionDeleteResponse struct {
	OK          bool               `json:"ok"`
	Changed     bool               `json:"changed"`
	MemberCount int                `json:"memberCount"`
	Navigation  NavigationMutation `json:"navigation"`
}

type SessionPinAssignParams struct {
	SessionRef  string  `json:"sessionRef"`
	SectionID   *string `json:"sectionId,omitempty"`
	SectionName *string `json:"sectionName,omitempty"`
}

type SessionPinUnpinParams struct {
	SessionRef string `json:"sessionRef"`
}

type SessionPinAssignment struct {
	SessionRef string     `json:"sessionRef"`
	Section    PinSection `json:"section"`
}

type SessionPinAssignResponse struct {
	OK         bool                 `json:"ok"`
	Changed    bool                 `json:"changed"`
	Assignment SessionPinAssignment `json:"assignment"`
	Navigation NavigationMutation   `json:"navigation"`
}

type SessionPinUnpinAssignment struct {
	SessionRef string `json:"sessionRef"`
}

type SessionPinUnpinResponse struct {
	OK         bool                      `json:"ok"`
	Changed    bool                      `json:"changed"`
	Assignment SessionPinUnpinAssignment `json:"assignment"`
	Navigation NavigationMutation        `json:"navigation"`
}

// SearchParams selects matching live and past sessions for the hub command
// palette. An empty query returns the most recent past sessions and all live
// sessions, matching the palette's initial result set.
type SearchParams struct {
	Query string `json:"query,omitempty"`
}

// SearchResult is one session hit from the hub's live or past search index.
// Ref is the qualified session reference that clients use to open the hit.
type SearchResult struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
	State   string `json:"state"`
	Age     string `json:"age"`
	Ref     string `json:"ref"`
}

// SearchResponse groups matching live sessions separately from persisted
// sessions so the command palette can render its two result sections.
type SearchResponse struct {
	Live []SearchResult `json:"live"`
	Past []SearchResult `json:"past"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type FeatureSet struct {
	ThreadList                bool `json:"threadList"`
	ThreadTurnsList           bool `json:"threadTurnsList"`
	TurnStart                 bool `json:"turnStart"`
	TurnSteer                 bool `json:"turnSteer"`
	ThreadClear               bool `json:"threadClear"`
	ThreadShutdown            bool `json:"threadShutdown"`
	ForkFromTurn              bool `json:"forkFromTurn"`
	Tasks                     bool `json:"tasks"`
	TranscriptList            bool `json:"transcriptList"`
	ModelList                 bool `json:"modelList"`
	DirectoryComplete         bool `json:"directoryComplete"`
	Auth                      bool `json:"auth"`
	TranscriptDisplaySettings bool `json:"transcriptDisplaySettings,omitempty"`
	KeybindingsSettings       bool `json:"keybindingsSettings,omitempty"`
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
	Ref        string `json:"ref"`
	InstanceID string `json:"instanceId,omitempty"`
	ParentRef  string `json:"parentRef,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Profile    string `json:"profile,omitempty"`
	// TurnCount is the daemon's total completed model-response count. It stays
	// independent of Turns so a bounded metadata read never loads the transcript.
	TurnCount        int                `json:"turnCount,omitempty"`
	ActiveTurnID     string             `json:"activeTurnId,omitempty"`
	ContextPressure  float64            `json:"contextPressure,omitempty"`
	ContextUsed      int                `json:"contextUsed,omitempty"`
	ContextWindow    int                `json:"contextWindow,omitempty"`
	ContextRemaining int                `json:"contextRemaining,omitempty"`
	Capabilities     ThreadCapabilities `json:"capabilities"`
	Diagnostics      *EvenerDiagnostics `json:"diagnostics,omitempty"`
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
	// source-backed thread that omits the field, or the hub's per-entry list
	// sweeps, which cannot afford a scan per session. Consumers render nil as
	// nothing, never as a fabricated zero.
	FailedToolCalls *int `json:"failedToolCalls,omitempty"`
	// AskPending is true while an ask_user question is unanswered. Additive:
	// absent on old daemons and source-backed threads that omit the field,
	// decoding as false.
	AskPending bool `json:"askPending,omitempty"`
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
}

// ThreadQueueChangedParams is the params shape for thread/queueChanged
// (kata r80p). It mirrors the queue field on EvenerThread so consumers can
// store it verbatim on the cached thread state.
type ThreadQueueChangedParams struct {
	ThreadID string     `json:"threadId"`
	Ref      string     `json:"ref"`
	Queue    QueueState `json:"queue"`
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

// TurnCompletedParams is the payload of a turn/completed notification: the
// completed turn and its ID.
type TurnCompletedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	TurnID   string `json:"turnId"`
	Turn     Turn   `json:"turn"`
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
	// Queue advertises support for turn/queue (kata 111a). True when a turn
	// is currently in flight and the session can accept enqueued user
	// messages for processing after the active turn completes.
	Queue bool `json:"queue"`
	// Goal advertises support for goal/set (the /goal objective engine). True
	// for a evener session that can accept a goal; false for sources that do not
	// advertise the capability, so goal/set is gated like every other thread action.
	Goal bool `json:"goal"`
	// Rename advertises support for evener/thread/name/set. True for a live evener
	// session (the daemon method) and for ended local sessions (the hub edits
	// meta); false for non-local/source-backed threads that do not advertise it.
	Rename bool `json:"rename"`
}

// EvenerHookEventStatus describes a single hook event's registration state.
type EvenerHookEventStatus struct {
	Event     string `json:"event"`
	Count     int    `json:"count"`
	Tier      string `json:"tier,omitempty"`
	Supported bool   `json:"supported"`
}

type EvenerDiagnostics struct {
	Tools      []EvenerToolInfo        `json:"tools,omitempty"`
	MCP        []EvenerMCPServerInfo   `json:"mcp,omitempty"`
	Skills     []EvenerSkillInfo       `json:"skills,omitempty"`
	Plugins    []EvenerPluginInfo      `json:"plugins,omitempty"`
	HookEvents []EvenerHookEventStatus `json:"hookEvents,omitempty"`
	Jobs       []EvenerJobInfo         `json:"jobs,omitempty"`
	Delegates  []EvenerDelegateInfo    `json:"delegates,omitempty"`
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
}

// MarshalJSON preserves an explicit empty plugin inventory while keeping a
// nil inventory absent for old or unwired sources that cannot report it.
func (d EvenerDiagnostics) MarshalJSON() ([]byte, error) {
	type alias EvenerDiagnostics
	a := alias(d)
	a.Plugins = nil
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if d.Plugins == nil {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	plugins, err := json.Marshal(d.Plugins)
	if err != nil {
		return nil, err
	}
	fields["plugins"] = plugins
	return json.Marshal(fields)
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

// EvenerDelegateInfo is the turn-free stable delegate projection shared by live
// notifications and thread diagnostics. It contains no activation job fields
// and no call-scoped wait result.
type EvenerDelegateInfo struct {
	DelegateID          string               `json:"delegateId"`
	OwnerSessionID      string               `json:"ownerSessionId"`
	RootSessionID       string               `json:"rootSessionId"`
	ChildSessionID      string               `json:"childSessionId"`
	TranscriptRef       string               `json:"transcriptRef"`
	ParentDelegateID    string               `json:"parentDelegateId,omitempty"`
	Type                string               `json:"type"`
	Lifecycle           string               `json:"lifecycle"`
	Phase               string               `json:"phase"`
	Status              string               `json:"status"`
	Outcome             string               `json:"outcome,omitempty"`
	Reason              string               `json:"reason,omitempty"`
	Terminal            bool                 `json:"terminal,omitempty"`
	Resumable           bool                 `json:"resumable"`
	NeedsAttention      bool                 `json:"needsAttention"`
	NotResumableReason  string               `json:"notResumableReason,omitempty"`
	ProjectionRevision  uint64               `json:"projectionRevision"`
	Task                string               `json:"task,omitempty"`
	Description         string               `json:"description,omitempty"`
	AgentType           string               `json:"agentType,omitempty"`
	RequestedModel      string               `json:"requestedModel,omitempty"`
	ResolvedProfileID   string               `json:"resolvedProfileId,omitempty"`
	ResolvedModel       string               `json:"resolvedModel,omitempty"`
	Model               string               `json:"model,omitempty"`
	ReasoningEffort     string               `json:"reasoningEffort,omitempty"`
	OriginTurnID        string               `json:"originTurnId,omitempty"`
	OriginToolCallID    string               `json:"originToolCallId,omitempty"`
	OriginItemID        string               `json:"originItemId,omitempty"`
	RunStartedAt        string               `json:"runStartedAt,omitempty"`
	RunEndedAt          string               `json:"runEndedAt,omitempty"`
	LatestActivityAt    string               `json:"latestActivityAt,omitempty"`
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
// the final visible projected item slice for that entry.
type ThreadItemPosition struct {
	Entry uint64 `json:"entry"`
	Item  uint32 `json:"item"`
}

type Turn struct {
	ID        string        `json:"id"`
	Items     []ThreadItem  `json:"items,omitempty"`
	ItemsView TurnItemsView `json:"itemsView"`
	Status    string        `json:"status"`
	Error     *TurnError    `json:"error,omitempty"`
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
	// ThreadItemEventKindEnvironment marks the systemMessage item a reloaded
	// transcript renders for a schema.TurnEnvironment turn: the harness's
	// diff-rendered cwd/git-branch/pressure block, not a settings-governed
	// toggle — no visibility preference hides it (transcriptVisibility.ts's
	// "no toggle governs it" default applies).
	ThreadItemEventKindEnvironment ThreadItemEventKind = "environment"
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
}

type ThreadItem struct {
	Type                 string              `json:"type"`
	ID                   string              `json:"id"`
	TranscriptKey        string              `json:"transcriptKey,omitempty"`
	Position             *ThreadItemPosition `json:"position,omitempty"`
	TurnID               string              `json:"turnId,omitempty"`
	TranscriptEntryIndex int                 `json:"transcriptEntryIndex,omitempty"`
	Text                 string              `json:"text,omitempty"`
	Delta                string              `json:"delta,omitempty"`
	Images               []InputItem         `json:"images,omitempty"`
	ToolName             string              `json:"toolName,omitempty"`
	CallID               string              `json:"callId,omitempty"`
	ArgumentsJSON        string              `json:"argumentsJson,omitempty"`
	Description          string              `json:"description,omitempty"`
	Output               string              `json:"output,omitempty"`
	Error                string              `json:"error,omitempty"`
	OutputImages         []OutputImage       `json:"outputImages,omitempty"`
	Status               string              `json:"status,omitempty"`
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
	ItemLimit           int    `json:"itemLimit,omitempty"`
}

type ThreadReadResponse struct {
	Thread Thread `json:"thread"`
	// OlderCursor is set when itemLimit truncated the returned items; pass it
	// to thread/turns/list to fetch the page just before the window. Empty means
	// the response already includes the oldest item.
	OlderCursor string `json:"olderCursor,omitempty"`
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
	ThreadID  string `json:"threadId,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	ItemsView string `json:"itemsView,omitempty"`
	ItemLimit int    `json:"itemLimit,omitempty"`
}

type ThreadTurnsListResponse struct {
	Data       []Turn `json:"data"`
	NextCursor string `json:"nextCursor,omitempty"`
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
	// Aside forks a local evener thread at its tip instead of at a source turn:
	// the child is a complete copy of the parent session (same permissions and
	// config via the inherited session meta) and opens as a side thread. Aside
	// is mutually exclusive with SourceTurnID, EditedInput, DeferInput, and
	// Label, and is only supported for local evener threads.
	Aside bool `json:"aside,omitempty"`
}

type ThreadForkResponse struct {
	Thread Thread `json:"thread"`
	// OriginalInput is the source turn's original user text, set only when
	// the fork was requested with DeferInput.
	OriginalInput string `json:"originalInput,omitempty"`
}

type TurnStartParams struct {
	Ref                string      `json:"ref,omitempty"`
	ThreadID           string      `json:"threadId,omitempty"`
	ClientMutationID   string      `json:"clientMutationId"`
	ExpectedInstanceID string      `json:"expectedInstanceId"`
	Input              []InputItem `json:"input,omitempty"`
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
	Ref                string      `json:"ref,omitempty"`
	ThreadID           string      `json:"threadId,omitempty"`
	ClientMutationID   string      `json:"clientMutationId"`
	ExpectedInstanceID string      `json:"expectedInstanceId"`
	Input              []InputItem `json:"input,omitempty"`
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
	Ref                string      `json:"ref"`
	ClientMutationID   string      `json:"clientMutationId"`
	ExpectedInstanceID string      `json:"expectedInstanceId"`
	Input              []InputItem `json:"input,omitempty"`
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
	InstanceID       string                  `json:"instanceId,omitempty"`
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
	ExpectedInstanceID    string      `json:"expectedInstanceId"`
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
	Ref                string `json:"ref"`
	Index              int    `json:"index"`
	ClientMutationID   string `json:"clientMutationId"`
	ExpectedInstanceID string `json:"expectedInstanceId"`
	ExpectedEntryID    string `json:"expectedEntryId"`
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
	// LastOutputAt is the RFC3339 timestamp of the job's most recent
	// parent-observable output/activity. Live-only: retained jobs omit it, and
	// clients fall back to startedAt (or hide quiet time for terminal rows).
	LastOutputAt string `json:"lastOutputAt,omitempty"`
}

type JobActivityDelegate struct {
	DelegateID          string                 `json:"delegateId"`
	OwnerSessionID      string                 `json:"ownerSessionId,omitempty"`
	RootSessionID       string                 `json:"rootSessionId,omitempty"`
	ChildSessionID      string                 `json:"childSessionId"`
	ChildRef            string                 `json:"childRef"`
	ParentDelegateID    string                 `json:"parentDelegateId,omitempty"`
	Type                string                 `json:"type,omitempty"`
	Lifecycle           string                 `json:"lifecycle,omitempty"`
	Phase               string                 `json:"phase,omitempty"`
	Status              string                 `json:"status,omitempty"`
	ProjectionRevision  uint64                 `json:"projectionRevision,omitempty"`
	Outcome             string                 `json:"outcome,omitempty"`
	Reason              string                 `json:"reason,omitempty"`
	Terminal            bool                   `json:"terminal,omitempty"`
	Resumable           bool                   `json:"resumable,omitempty"`
	NotResumableReason  string                 `json:"notResumableReason,omitempty"`
	Mandate             string                 `json:"mandate,omitempty"`
	Task                string                 `json:"task,omitempty"`
	Description         string                 `json:"description,omitempty"`
	AgentType           string                 `json:"agentType,omitempty"`
	RequestedModel      string                 `json:"requestedModel,omitempty"`
	ResolvedProfileID   string                 `json:"resolvedProfileId,omitempty"`
	ResolvedModel       string                 `json:"resolvedModel,omitempty"`
	Model               string                 `json:"model,omitempty"`
	ReasoningEffort     string                 `json:"reasoningEffort,omitempty"`
	OriginTurnID        string                 `json:"originTurnId,omitempty"`
	OriginToolCallID    string                 `json:"originToolCallId,omitempty"`
	OriginItemID        string                 `json:"originItemId,omitempty"`
	RunStartedAt        string                 `json:"runStartedAt,omitempty"`
	RunEndedAt          string                 `json:"runEndedAt,omitempty"`
	LatestActivityAt    string                 `json:"latestActivityAt,omitempty"`
	RunningForMS        *int64                 `json:"runningForMs,omitempty"`
	QuietForMS          *int64                 `json:"quietForMs,omitempty"`
	DurationMS          *int64                 `json:"durationMs,omitempty"`
	PacketKind          string                 `json:"packetKind,omitempty"`
	Message             json.RawMessage        `json:"message,omitempty"`
	StructuredResult    json.RawMessage        `json:"structuredResult,omitempty"`
	StructuredValid     *bool                  `json:"structuredResultValid,omitempty"`
	StructuredReason    string                 `json:"structuredResultReason,omitempty"`
	Warnings            []string               `json:"warnings,omitempty"`
	Diagnostics         []string               `json:"diagnostics,omitempty"`
	ExhaustionBudget    string                 `json:"exhaustionBudget,omitempty"`
	ExhaustionLimit     int                    `json:"exhaustionLimit,omitempty"`
	ExhaustionResumable *bool                  `json:"exhaustionResumable,omitempty"`
	DelegationAllowance int                    `json:"delegationAllowance,omitempty"`
	ParentWatchGranted  bool                   `json:"parentWatchGranted,omitempty"`
	Worktree            *JobActivityWorktree   `json:"worktree,omitempty"`
	Turns               []JobActivityJob       `json:"turns"`
	Child               *JobActivitySession    `json:"child,omitempty"`
	Branch              JobActivityBranchState `json:"branch"`
	// Usage is the child session's cumulative self-only token totals. Nil when
	// the child has no token data (fresh session, old daemon, shell-only work).
	Usage *EvenerUsage `json:"usage,omitempty"`
}

type JobActivityWorktree struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"headSha"`
	Ahead   int    `json:"ahead"`
	Dirty   bool   `json:"dirty"`
}

type JobActivityEntry struct {
	Kind     string               `json:"kind"` // shell | delegate
	Job      *JobActivityJob      `json:"job,omitempty"`
	Delegate *JobActivityDelegate `json:"delegate,omitempty"`
}

type JobActivitySession struct {
	SessionID   string                 `json:"sessionId"`
	Ref         string                 `json:"ref"`
	Label       string                 `json:"label"`
	Aggregate   string                 `json:"aggregate"`
	Counts      JobActivityCounts      `json:"counts"`
	Entries     []JobActivityEntry     `json:"entries"`
	Diagnostics []string               `json:"diagnostics,omitempty"`
	Branch      JobActivityBranchState `json:"branch"`
}

type JobActivityTree struct {
	Revision uint64             `json:"revision"`
	Root     JobActivitySession `json:"root"`
}

// AllJobActivityTypes is the explicit reachability root for the replacement
// jobs activity-tree contract. The AppWire generators walk this list in
// addition to Methods and Notifications so the JobActivity* wire types stay
// emitted even though evener/jobs/list itself keeps JobsListResponse.Data as any.
var AllJobActivityTypes = []any{
	JobActivityTree{},
	JobActivitySession{},
	JobActivityEntry{},
	JobActivityJob{},
	JobActivityDelegate{},
	JobActivityWorktree{},
	JobActivityCounts{},
	JobActivityBranchState{},
}

// JobOutputTail is the evener/jobs/output payload: one window of a job's
// durable output plus the bookkeeping a client needs to say "showing last N
// of M bytes" and to page backwards through the log.
type JobOutputTail struct {
	Tail          string `json:"tail"`
	TotalBytes    int64  `json:"totalBytes"`
	RetainedStart int64  `json:"retainedStart"`
	Truncated     bool   `json:"truncated"`
	// HasEarlier is true when retained output exists before the window: a
	// follow-up read with beforeBytes=RetainedStart returns the previous page.
	HasEarlier bool `json:"hasEarlier,omitempty"`
}

// JobsOutputParams reads a byte window of one job's durable output. MaxBytes
// defaults server-side (4 KiB) and is capped (64 KiB). BeforeBytes > 0 pages
// backwards: the window ends at that lifetime output offset (exclusive)
// instead of at the end of the log.
type JobsOutputParams struct {
	Ref         string `json:"ref,omitempty"`
	JobID       string `json:"jobId"`
	MaxBytes    int64  `json:"maxBytes,omitempty"`
	BeforeBytes int64  `json:"beforeBytes,omitempty"`
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

// DirsCreateParams requests creation of a working directory and any missing
// parents for a Spawn preflight.
type DirsCreateParams struct {
	Path string `json:"path"`
}

type DirsCreateResponse struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
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

// GitHeadParams selects the working directory whose git HEAD should be read.
type GitHeadParams struct {
	CWD string `json:"cwd"`
}

// GitHeadResponse reports a branch name, detached short SHA, or an empty
// string when the working directory has no readable git HEAD.
type GitHeadResponse struct {
	Head string `json:"head"`
}

// MobilePairingParams supplies the authenticated web application's explicit
// origin. The hub validates it before embedding its auth token in a pairing
// URL, unless configured MobileBaseURL takes precedence.
type MobilePairingParams struct {
	Origin string `json:"origin"`
}

// MobilePairingResponse carries the reusable /auth bootstrap URL scanned by
// the phone.
type MobilePairingResponse struct {
	AuthURL string `json:"authUrl"`
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
	EnvVar string `json:"envVar,omitempty"`
	// ShadowedEnvVar names an environment variable that is set but loses to
	// a higher-precedence credential (api_key, credential_headers, or
	// store, spec §10); empty when no such variable is set, including when
	// an env source is itself what resolves.
	ShadowedEnvVar string `json:"shadowedEnvVar,omitempty"`
	Email          string `json:"email,omitempty"`
	StoredEmail    string `json:"storedEmail,omitempty"`
	AccountID      string `json:"accountId,omitempty"`
	WorkspaceID    string `json:"workspaceId,omitempty"`
	NeedsRefresh   bool   `json:"needsRefresh,omitempty"`
	NeedsLogin     bool   `json:"needsLogin,omitempty"`
	Error          string `json:"error,omitempty"`
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
	// The remaining fields are optional because daemon and remote-source model
	// lists may know only the launchable identity. Hub responses fill these from
	// the embedded catalog when available. Pointer scalars preserve an explicit
	// false/zero from a live provider instead of treating it as unknown.
	DisplayName           string   `json:"displayName,omitempty"`
	ContextWindow         *int     `json:"contextWindow,omitempty"`
	MaxInputTokens        *int     `json:"maxInputTokens,omitempty"`
	SupportsTools         *bool    `json:"supportsTools,omitempty"`
	SupportsVision        *bool    `json:"supportsVision,omitempty"`
	MaxOutputTokens       *int     `json:"maxOutputTokens,omitempty"`
	SupportsWebSearch     *bool    `json:"supportsWebSearch,omitempty"`
	SupportsReasoning     *bool    `json:"supportsReasoning,omitempty"`
	InputCostPerMillion   *float64 `json:"inputCostPerMillion,omitempty"`
	OutputCostPerMillion  *float64 `json:"outputCostPerMillion,omitempty"`
	ReasoningEffortLevels []string `json:"reasoningEffortLevels,omitempty"`
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
	// EvenerThread.FailedToolCalls) so a client WATCHING a session sees it move.
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
	// announced (see EvenerThread.Capabilities), for the same reason the failure
	// count rides along above: it is otherwise snapshot-only, and three of its
	// entries — Send, Steer, Queue — are defined by whether a turn is in
	// flight. A client that read the thread while it was idle therefore holds
	// steer=false/queue=false for the whole turn that follows, and renders a
	// session it KNOWS is active with no Steer, no Stop and a dead Send until
	// the page is reloaded (kata 06t8). A status transition is exactly when
	// those flip, so the set refreshes there and nowhere else — no polling, no
	// re-read of the transcript.
	//
	// ABSENT MEANS "NO UPDATE", same as the count. Non-local/source-backed
	// threads may omit capabilities their source does not advertise. A client
	// that cleared its set on absence would strip a session of every action its
	// hydrate legitimately advertised.
	//
	// A CLOSE frame is the one status a daemon does not fill in: what a thread
	// can still be asked to do once its daemon is gone is the hub's answer, not
	// the departing daemon's. The hub stamps that frame as it relays it
	// (cmd/evener-hub/app_relay.go's stampClosedThreadCapabilities), so a close
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
// model's reasoning summary for the named reasoning item. The hub preserves
// source-provided compatible fields without claiming a Codex bridge, so the web
// UI can render thinking live.
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

// ThreadModelRetryParams is the params shape for the evener/thread/modelRetry
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
//
// AttemptCap is the honest denominator to render instead of MaxAttempts once
// it differs: the full policy budget until the current retry group has a
// consume-phase failure, then the early-stop bound that will actually govern
// it. Both are zero when a rate limit is being retried against a wall-clock
// budget instead of an attempt count, so clients should render the bare attempt
// number. GroupElapsedMS is wall-clock time since the retry group's first
// attempt (one model call), so a client can render how long the call has
// been running.
type ThreadModelRetryParams struct {
	ThreadID       string `json:"threadId"`
	Ref            string `json:"ref"`
	TurnID         string `json:"turnId,omitempty"`
	Attempt        int    `json:"attempt"`
	MaxAttempts    int    `json:"maxAttempts"`
	DelayMS        int64  `json:"delayMs"`
	ErrorClass     string `json:"errorClass,omitempty"`
	StatusCode     int    `json:"statusCode,omitempty"`
	Message        string `json:"message,omitempty"`
	Model          string `json:"model,omitempty"`
	GroupElapsedMS int64  `json:"groupElapsedMs"`
	AttemptCap     int    `json:"attemptCap"`
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

// EvenerSteeringInjectedParams is the params shape for the
// evener/steering/injected notification. Text is pre-substituted server-side
// with an image placeholder when a steer carries only images. Source is
// "user" for human-sent steering (rendered as a user message) and omitted
// entirely for daemon-originated steering (issue #24).
type EvenerSteeringInjectedParams struct {
	// StartedAt is the server event timestamp in epoch milliseconds.
	StartedAt        *int64      `json:"startedAt,omitempty"`
	ThreadID         string      `json:"threadId"`
	Ref              string      `json:"ref"`
	Text             string      `json:"text,omitempty"`
	Images           []InputItem `json:"images,omitempty"`
	Source           string      `json:"source,omitempty"`
	Kind             string      `json:"kind,omitempty"`
	ClientMutationID string      `json:"clientMutationId,omitempty"`
}

// EvenerJobParams is the params shape shared by the evener/job/started and
// evener/job/finished notifications. Both carry the same envelope around a
// EvenerJobInfo; which of its fields are populated is what differs (a finished
// job adds status/reason/exitCode/output), so one type describes both.
type EvenerJobParams struct {
	ThreadID string        `json:"threadId"`
	Ref      string        `json:"ref"`
	Job      EvenerJobInfo `json:"job"`
}

// EvenerAuthUpdatedParams is the params shape for the evener/auth/updated
// notification. Both fields are absent when the broadcast follows a
// provider-instance mutation, which no single provider/activeSource pair
// honestly summarizes; clients treat this notification as payload-agnostic
// ("credentials or instances changed, refetch") either way.
type EvenerAuthUpdatedParams struct {
	Provider     string `json:"provider,omitempty"`
	ActiveSource string `json:"activeSource,omitempty"`
}

// NavigationTargetKind identifies one exact invalidation target variant.
type NavigationTargetKind string

const (
	NavigationTargetManifest          NavigationTargetKind = "manifest"
	NavigationTargetSection           NavigationTargetKind = "section"
	NavigationTargetPinCatalog        NavigationTargetKind = "pin_catalog"
	NavigationTargetPinSection        NavigationTargetKind = "pin_section"
	NavigationTargetCatalog           NavigationTargetKind = "catalog"
	NavigationTargetProject           NavigationTargetKind = "project"
	NavigationTargetAllLoadedProjects NavigationTargetKind = "all_loaded_projects"
)

// AllNavigationTargetKinds is the complete wire-level target-kind set.
var AllNavigationTargetKinds = []NavigationTargetKind{
	NavigationTargetManifest,
	NavigationTargetSection,
	NavigationTargetPinCatalog,
	NavigationTargetPinSection,
	NavigationTargetCatalog,
	NavigationTargetProject,
	NavigationTargetAllLoadedProjects,
}

// NavigationInvalidationTarget identifies one loaded navigation resource that
// clients must revalidate. Revision is omitted only by the wildcard target.
type NavigationInvalidationTarget struct {
	Kind       NavigationTargetKind `json:"kind"`
	Section    string               `json:"section,omitempty"`
	SectionID  string               `json:"sectionId,omitempty"`
	Catalog    string               `json:"catalog,omitempty"`
	ProjectKey string               `json:"projectKey,omitempty"`
	Revision   uint64               `json:"revision,omitempty"`
}

// MarshalJSON emits only the fields valid for target's kind. Every scoped
// target includes a revision, including revision zero; the wildcard has no
// revision or selector.
func (target NavigationInvalidationTarget) MarshalJSON() ([]byte, error) {
	if err := target.validate(false); err != nil {
		return nil, err
	}
	if target.Kind == NavigationTargetAllLoadedProjects {
		return json.Marshal(struct {
			Kind NavigationTargetKind `json:"kind"`
		}{Kind: target.Kind})
	}
	return json.Marshal(struct {
		Kind       NavigationTargetKind `json:"kind"`
		Section    string               `json:"section,omitempty"`
		SectionID  string               `json:"sectionId,omitempty"`
		Catalog    string               `json:"catalog,omitempty"`
		ProjectKey string               `json:"projectKey,omitempty"`
		Revision   uint64               `json:"revision"`
	}{
		Kind:       target.Kind,
		Section:    target.Section,
		SectionID:  target.SectionID,
		Catalog:    target.Catalog,
		ProjectKey: target.ProjectKey,
		Revision:   target.Revision,
	})
}

// UnmarshalJSON rejects unknown fields and every invalid target variant so an
// incomplete or widened invalidation cannot silently reach a client.
func (target *NavigationInvalidationTarget) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("navigation invalidation target must be an object")
	}
	for name := range fields {
		switch name {
		case "kind", "section", "sectionId", "catalog", "projectKey", "revision":
		default:
			return fmt.Errorf("navigation invalidation target has unknown field %q", name)
		}
	}
	var decoded struct {
		Kind       NavigationTargetKind `json:"kind"`
		Section    string               `json:"section"`
		SectionID  string               `json:"sectionId"`
		Catalog    string               `json:"catalog"`
		ProjectKey string               `json:"projectKey"`
		Revision   uint64               `json:"revision"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	_, hasRevision := fields["revision"]
	if decoded.Kind == NavigationTargetAllLoadedProjects && hasRevision {
		return fmt.Errorf("navigation target %q permits no selector or revision", decoded.Kind)
	}
	candidate := NavigationInvalidationTarget{
		Kind:       decoded.Kind,
		Section:    decoded.Section,
		SectionID:  decoded.SectionID,
		Catalog:    decoded.Catalog,
		ProjectKey: decoded.ProjectKey,
		Revision:   decoded.Revision,
	}
	if err := candidate.validate(!hasRevision); err != nil {
		return err
	}
	*target = candidate
	return nil
}

func (target NavigationInvalidationTarget) validate(revisionMissing bool) error {
	if target.Kind == NavigationTargetAllLoadedProjects {
		if target.Section != "" || target.SectionID != "" || target.Catalog != "" || target.ProjectKey != "" || target.Revision != 0 {
			return fmt.Errorf("navigation target %q permits no selector or revision", target.Kind)
		}
		return nil
	}
	if revisionMissing {
		return fmt.Errorf("navigation target %q requires revision", target.Kind)
	}
	switch target.Kind {
	case NavigationTargetManifest, NavigationTargetPinCatalog:
		if target.Section != "" || target.SectionID != "" || target.Catalog != "" || target.ProjectKey != "" {
			return fmt.Errorf("navigation target %q permits no selector", target.Kind)
		}
	case NavigationTargetSection:
		if target.Section == "" || target.SectionID != "" || target.Catalog != "" || target.ProjectKey != "" {
			return fmt.Errorf("navigation target %q requires only section", target.Kind)
		}
	case NavigationTargetPinSection:
		if target.Section != "" || target.SectionID == "" || target.Catalog != "" || target.ProjectKey != "" {
			return fmt.Errorf("navigation target %q requires only sectionId", target.Kind)
		}
	case NavigationTargetCatalog:
		if target.Section != "" || target.SectionID != "" || target.Catalog == "" || target.ProjectKey != "" {
			return fmt.Errorf("navigation target %q requires only catalog", target.Kind)
		}
	case NavigationTargetProject:
		if target.Section != "" || target.SectionID != "" || target.Catalog != "" || target.ProjectKey == "" {
			return fmt.Errorf("navigation target %q requires only projectKey", target.Kind)
		}
	default:
		return fmt.Errorf("unknown navigation target kind %q", target.Kind)
	}
	return nil
}

// NavigationInvalidatedPayload is the evener/navigation/invalidated
// notification body. Sequence orders notifications within GenerationID.
type NavigationInvalidatedPayload struct {
	GenerationID string                         `json:"generationId"`
	Sequence     uint64                         `json:"sequence"`
	Targets      []NavigationInvalidationTarget `json:"targets"`
}

// EvenerLaunchUpdatedParams is the params shape for the evener/launch/updated
// notification: which working directory's launch config changed, and at
// which layer.
type EvenerLaunchUpdatedParams struct {
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
	Name string `json:"name"`
}

type LaunchOption struct {
	Field               string                   `json:"field"`
	WireField           string                   `json:"wireField"`
	Label               string                   `json:"label"`
	Description         string                   `json:"description,omitempty"`
	Group               string                   `json:"group"`
	Kind                string                   `json:"kind"`
	PathKind            string                   `json:"pathKind,omitempty"`
	Repeatable          bool                     `json:"repeatable,omitempty"`
	DefaultableLayers   []string                 `json:"defaultableLayers,omitempty"`
	PerLaunch           bool                     `json:"perLaunch"`
	DebugOnly           bool                     `json:"debugOnly,omitempty"`
	EnvFallback         *LaunchOptionEnvFallback `json:"envFallback,omitempty"`
	Choices             []LaunchOptionChoice     `json:"choices,omitempty"`
	DriverSupport       map[string]bool          `json:"driverSupport,omitempty"`
	BuiltinDefault      string                   `json:"builtinDefault,omitempty"`
	BuiltinDefaultInt   *int                     `json:"builtinDefaultInt,omitempty"`
	BuiltinDefaultBool  *bool                    `json:"builtinDefaultBool,omitempty"`
	BuiltinDefaultLabel string                   `json:"builtinDefaultLabel,omitempty"`
}

type LaunchOptionSchemaResponse struct {
	Options  []LaunchOption    `json:"options"`
	Excluded map[string]string `json:"excluded,omitempty"`
}

// AuthListResponse is the result of evener/auth/list.
type AuthListResponse struct {
	Providers []AuthStatusResponse `json:"providers"`
}

// AuthApiKeySetParams is the params for evener/auth/apiKey/set.
type AuthApiKeySetParams struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

// AuthApiKeyClearParams is the params for evener/auth/apiKey/clear.
type AuthApiKeyClearParams struct {
	Provider string `json:"provider"`
}

// AuthCredentialJsonSetParams is the params for evener/auth/credentialJson/set:
// a Google credential JSON (service-account key or application-default
// authorized_user file) for a gcp-adc instance.
type AuthCredentialJsonSetParams struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

// AuthDeviceStartParams is the params for evener/auth/device/start.
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

// AuthDevicePollParams is the params for evener/auth/device/poll.
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

// InstanceEntry is one registry instance with its credential status
// (spec §11.3). ActiveSource and AuthModes speak the registry's vocabulary:
// a source is one of api_key, credential_headers, store, env:<VAR>, oauth,
// adc, or none. A credential value never appears here.
type InstanceEntry struct {
	Name string `json:"name"`
	// Base is the registry id an explicitly-named instance is built on;
	// empty when the instance name is itself the registry id.
	Base       string            `json:"base,omitempty"`
	ProviderID string            `json:"providerId"`
	Protocol   string            `json:"protocol"`
	Surface    string            `json:"surface,omitempty"`
	Auth       string            `json:"auth"`
	BaseURL    string            `json:"baseUrl,omitempty"`
	Vars       map[string]string `json:"vars,omitempty"`
	// Implicit is true for an instance that exists from the environment
	// alone: it has no entry in providers.toml, so it cannot be removed.
	Implicit bool `json:"implicit"`
	// Hidden marks a provider with no resolvable base URL in this
	// environment (its *_BASE_URL variable is unset).
	Hidden         bool     `json:"hidden,omitempty"`
	IsDefault      bool     `json:"isDefault"`
	AuthModes      []string `json:"authModes,omitempty"`
	ActiveSource   string   `json:"activeSource"`
	HasStoredFile  bool     `json:"hasStoredFile,omitempty"`
	HasStoredOAuth bool     `json:"hasStoredOAuth"`
	EnvVar         string   `json:"envVar,omitempty"`
	// ShadowedEnvVar names an environment variable that is set but loses to
	// a higher-precedence credential (api_key, credential_headers, or
	// store, spec §10); empty when no such variable is set, including when
	// an env source is itself what resolves.
	ShadowedEnvVar string `json:"shadowedEnvVar,omitempty"`
	StoredEmail    string `json:"storedEmail,omitempty"`
	// CredentialRequired is false when this instance has no credential to
	// look for at all — auth = none or optional-bearer — so an absent
	// credential is not a missing one. It is never omitted: false is the
	// meaningful value, and a client reading an absent field as false would
	// call every instance optional.
	CredentialRequired bool `json:"credentialRequired"`
	// Warnings are the registry's own notes about this instance, chiefly
	// what is missing and how to supply it.
	Warnings []string `json:"warnings,omitempty"`
}

// ProviderDescriptor is a registry provider the add form can build on: its
// id and display name, the protocol and auth scheme it defaults to, and the
// variables its URL templates read. Vars maps a template placeholder name
// to the environment variable name it is fed by, so a typed override can be
// sent keyed by the name the registry actually substitutes. It is
// registry.Transport.VarsEnv restricted to the placeholders some URL
// template reads or a host rule consumes (Registry.TemplateVarsEnv): a
// vars_env entry nothing substitutes, such as a credential's own variable,
// gets no input. VarsEnv is the same environment-variable names alone,
// sorted. It stays a list because v3
// peers — a TUI built before Vars existed — decode it as one, and
// ProtocolVersion is compared exactly, so a wire shape cannot change under
// v3; new readers use Vars.
type ProviderDescriptor struct {
	ID        string            `json:"id"`
	Name      string            `json:"name,omitempty"`
	Protocol  string            `json:"protocol"`
	Auth      string            `json:"auth"`
	VarsEnv   []string          `json:"varsEnv,omitempty"`
	Vars      map[string]string `json:"vars,omitempty"`
	APIKeyEnv []string          `json:"apiKeyEnv,omitempty"`
	Implicit  bool              `json:"implicit"`
}

// InstanceListResponse is the result of evener/instance/list. Diagnostics
// carries the providers.toml load error, the user-layer note, stray OAuth
// records and load warnings; WritesRefused says the hub has no registry to
// write against — the file could not be read, or none has loaded yet — so no
// instance may be written until that is fixed (spec §10).
type InstanceListResponse struct {
	Instances          []InstanceEntry      `json:"instances"`
	AvailableProviders []ProviderDescriptor `json:"availableProviders"`
	Diagnostics        []string             `json:"diagnostics,omitempty"`
	UserLayer          string               `json:"userLayer,omitempty"`
	WritesRefused      bool                 `json:"writesRefused,omitempty"`
}

// InstanceCreateParams is the params for evener/instance/create. APIKeyEnv
// is a variable name and CredentialHeader must reference a $VAR: secrets
// never cross this boundary (spec §11.2).
type InstanceCreateParams struct {
	Name             string            `json:"name"`
	Base             string            `json:"base"`
	BaseURL          string            `json:"baseUrl,omitempty"`
	Protocol         string            `json:"protocol,omitempty"`
	Surface          string            `json:"surface,omitempty"`
	Vars             map[string]string `json:"vars,omitempty"`
	APIKeyEnv        string            `json:"apiKeyEnv,omitempty"`
	CredentialHeader string            `json:"credentialHeader,omitempty"`
}

// InstanceEditParams is the params for evener/instance/edit. Editing an
// implicit instance writes a shadowing entry carrying only these fields
// (spec §11.3). This wire shape is part of evener-appwire-v4; fields carried
// forward retain their established meanings, and v4 peers negotiate this
// shape before using it.
//
// EMPTY MEANS UNCHANGED, not "clear", for BaseURL, Protocol and Surface: an
// empty value leaves the stored one alone. That preserves the pre-v4 field
// semantics (#711) — BaseURL cannot be emptied to mean "clear" without
// changing what a v3 `baseUrl: ""` meant to a peer before the v4 upgrade.
//
// ClearBaseURL is the v4 way to reach a clear: when true, it drops the
// authored base_url override and goes back to the registry default, lifting
// spec §10's credential-inheritance stop, which keys on a literal base_url.
// BaseURL and ClearBaseURL are never both meaningful in the same request: send
// one or the other.
//
// Protocol and Surface have no clear operation yet — Name identifies the
// instance and an empty Vars map is a no-op edit either way, so those two
// are the only fields still unreachable. Giving them the same ClearXxx
// treatment as BaseURL is ledgered for whenever a form needs to clear one.
type InstanceEditParams struct {
	Name         string            `json:"name"`
	BaseURL      string            `json:"baseUrl,omitempty"`
	ClearBaseURL bool              `json:"clearBaseUrl,omitempty"`
	Protocol     string            `json:"protocol,omitempty"`
	Surface      string            `json:"surface,omitempty"`
	Vars         map[string]string `json:"vars,omitempty"`
}

// InstanceRemoveParams is the params for evener/instance/remove.
type InstanceRemoveParams struct {
	Name string `json:"name"`
}

// InstanceSetDefaultParams is the params for evener/instance/setDefault.
type InstanceSetDefaultParams struct {
	Name string `json:"name"`
}

// CommandDescriptor describes one slash command — plugin-provided or
// evener-wide — for catalog/autocomplete display. Name is unqualified;
// PluginName disambiguates when more than one plugin defines the same command
// name.
type CommandDescriptor struct {
	Name         string `json:"name"`
	PluginName   string `json:"pluginName,omitempty"`
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	// Source is "plugin" or "user"; "project" is reserved for a future
	// project-scoped catalog (project commands are cwd-dependent and never
	// appear in the hub-wide catalog).
	Source string `json:"source,omitempty"`
}

// CommandListResponse is the result of evener/command/list.
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
	OpenAIResponsesContinuation string            `json:"openAIResponsesContinuation,omitempty"` //nolint:tagliatelle // codex wire spells the AI/ATIF initialisms all-caps
	ProviderIdleTimeout         string            `json:"providerIdleTimeout,omitempty"`
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
	EnabledPlugins              *[]string         `json:"enabledPlugins,omitempty"`
	MCPs                        []MCPServerSpec   `json:"mcps,omitempty"`
	Env                         map[string]string `json:"env,omitempty"`
	Verbose                     *bool             `json:"verbose,omitempty"`
	TraceFile                   string            `json:"traceFile,omitempty"`
	CPUProfile                  string            `json:"cpuProfile,omitempty"`
	ExportATIFPath              string            `json:"exportATIFPath,omitempty"`            //nolint:tagliatelle // codex wire spells the AI/ATIF initialisms all-caps
	ExportATIFProviderHandles   string            `json:"exportATIFProviderHandles,omitempty"` //nolint:tagliatelle // codex wire spells the AI/ATIF initialisms all-caps
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

// PluginCheckNowResponse is the result of evener/plugin/checkNow: it runs one
// auto-upgrade daemon pass (refresh every marketplace, then upgrade every
// autoUpgrade-enabled plugin) on demand and reports what happened. Updated
// holds "<plugin>@<marketplace>" refs actually upgraded (no-ops omitted);
// Errors holds any per-marketplace/per-plugin failures — failures are
// isolated and never fail the request itself.
type PluginCheckNowResponse struct {
	Updated []string `json:"updated,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

// PluginPreviewParams requests the launch plugin inventory for a working
// directory and optional per-launch overrides. Preview starts no session and
// runs no plugin code; for a requested bundled plugin it readies the same
// store a launch publishes into, staging and removing a marked copy, so it
// fails wherever the launch it describes would. Readying that store creates
// the bundled directory under the plugin root when it is missing, and the
// directory stays behind — with the lock file the cache keeps in it — once the
// staged copy is removed; a destination holding content the running binary did
// not publish is reported as the conflict a launch would set aside, and left
// exactly where it is.
type PluginPreviewParams struct {
	CWD             string             `json:"cwd"`
	LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}

// PluginPreviewResponse is the launch plugin inventory and structured
// diagnostics returned by evener/plugin/preview.
type PluginPreviewResponse struct {
	Plugins         []PluginLaunchCandidate `json:"plugins"`
	Diagnostics     []PluginDiagnostic      `json:"diagnostics,omitempty"`
	SelectionErrors []PluginSelectionError  `json:"selectionErrors,omitempty"`
}

type PluginLaunchCandidate struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	Description  string `json:"description,omitempty"`
	Source       string `json:"source"`
	Marketplace  string `json:"marketplace,omitempty"`
	Path         string `json:"path,omitempty"`
	Selected     bool   `json:"selected"`
	SkillCount   int    `json:"skillCount"`
	AgentCount   int    `json:"agentCount"`
	CommandCount int    `json:"commandCount"`
	HookCount    int    `json:"hookCount"`
	MCPCount     int    `json:"mcpCount"`
}

type PluginDiagnostic struct {
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
}

type PluginSelectionError struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
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

// MarketplaceListResponse is the result of evener/marketplace/list. Every
// marketplace mutation (add/remove/refresh) also returns this, so a client
// can re-render from the response without a separate list round-trip.
type MarketplaceListResponse struct {
	Marketplaces []MarketplaceEntry `json:"marketplaces"`
}

// MarketplaceAddParams is the params for evener/marketplace/add. Name is
// optional; when empty, the marketplace manifest's own name is used.
type MarketplaceAddParams struct {
	Name   string                 `json:"name,omitempty"`
	Source MarketplaceSourceInput `json:"source"`
}

// MarketplaceNameParams identifies one registered marketplace by name — the
// params shape for evener/marketplace/remove and evener/marketplace/refresh.
type MarketplaceNameParams struct {
	Name string `json:"name"`
}

// MarketplaceCatalogPlugin is one plugin entry parsed from a marketplace's
// catalog (.claude-plugin/marketplace.json), as returned by
// evener/marketplace/browse.
type MarketplaceCatalogPlugin struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Author      string `json:"author,omitempty"`
}

// MarketplaceBrowseParams is the params for evener/marketplace/browse.
type MarketplaceBrowseParams struct {
	Name string `json:"name"`
}

// MarketplaceBrowseResponse is the result of evener/marketplace/browse: the
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

// PluginListResponse is the result of evener/plugin/list. Every plugin
// mutation (install/upgrade/remove/enable/disable/setAutoUpgrade) also
// returns this, so a client can re-render from the response without a
// separate list round-trip.
type PluginListResponse struct {
	Plugins []PluginEntry `json:"plugins"`
}

// PluginRefParams identifies one plugin by its registry key (plugin name +
// marketplace name) — the params shape for evener/plugin/install (naming the
// catalog entry to install), and evener/plugin/{upgrade,remove,enable,disable}
// (naming the already-installed entry to act on).
type PluginRefParams struct {
	Plugin      string `json:"plugin"`
	Marketplace string `json:"marketplace"`
}

// PluginSetAutoUpgradeParams is the params for evener/plugin/setAutoUpgrade.
type PluginSetAutoUpgradeParams struct {
	Plugin      string `json:"plugin"`
	Marketplace string `json:"marketplace"`
	AutoUpgrade bool   `json:"autoUpgrade"`
}

// SettingsOverviewResponse is the result of evener/settings/overview: the field
// bag behind five settings sections whose only data path today is Go-template
// variables rendered server-side — General, Hub, Storage, Agents, and the
// probed half of MCP servers (cmd/evener-hub/templates/partials/settings/
// {general,hub,storage,agents,mcp}.html) — replacing cmd/evener-hub/
// web_settings.go's settingsData for exactly those five. Every field is sourced
// from the same computation the legacy template used; see each sub-type's doc
// comment for the exact web_settings.go citation. A field the legacy template
// never rendered is left off rather than invented — also noted on the sub-type
// that would otherwise carry it.
//
// The other ten settings sections (providers/credentials, evener launch,
// in-repo trust, per-project override, marketplaces/plugins, plugin/skill dirs,
// the MCP config editable half, theme, transcript, display, notifications) are
// out of scope: they already have their own wire methods or land on a different
// task's new store. Nothing here is per-project.
type SettingsOverviewResponse struct {
	Hub           *SettingsHubOverview     `json:"hub,omitempty"`
	Storage       *SettingsStorageOverview `json:"storage,omitempty"`
	Agents        []SettingsAgentEntry     `json:"agents,omitempty"`
	McpDiscovered *SettingsMCPOverview     `json:"mcpDiscovered,omitempty"`
}

// SettingsHubOverview is the Settings → General / Settings → Hub section
// (cmd/evener-hub/templates/partials/settings/{general,hub}.html). Fields
// mirror cmd/evener-hub/web_settings.go's renderSettingsPartial settingsData
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

// SettingsStorageOverview is the Settings → Storage section (cmd/evener-hub/
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

// SettingsAgentEntry is one row in Settings → Agents (cmd/evener-hub/templates/
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

// SettingsMCPServerEntry is one probed MCP server row in Settings → MCP
// servers' "Discovered servers" list (cmd/evener-hub/templates/partials/
// settings/mcp.html) — the probed/read-only half; the editable half (MCP
// config file list, inline server CRUD) rides the existing launch-config
// wire (evener/launch/getLayer + evener/launch/setLayer), not this method.
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
