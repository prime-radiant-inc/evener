package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/internal/appprojector"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/httpguard"
	"primeradiant.com/evener/llm/registry"
)

// ImageAttachment is re-exported from package agent so HTTP clients and the
// session layer share a single type.
type ImageAttachment = agent.ImageAttachment

// InputMessage is delivered on InputCh() carrying user text plus any
// attached images. Kind classifies the turn (user input vs. a goal-engine
// continuation); its zero value is agent.EntryUserInput.
type InputMessage struct {
	Text                string
	Images              []ImageAttachment
	Kind                agent.EntryKind
	ClientMutationStart bool
	// QueuedInput asks the loop to claim and run the named session's queue
	// head as user input. It is distinct from Kind: a notification is
	// autonomous work the entry gate refuses while a question is pending,
	// whereas queued input is the user speaking and must run.
	QueuedInput bool
	SessionID   string
}

// RetrySafeTurnFunctions is the daemon's typed bridge to the authoritative
// per-session client-mutation state machine.
type RetrySafeTurnFunctions struct {
	Start     func(appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	Steer     func(appwire.TurnSteerParams) (appwire.TurnSteerResponse, error)
	Queue     func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error)
	Drain     func(appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error)
	Promote   func(appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error)
	Cancel    func(appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error)
	Interrupt func(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error)
}

// ToolInfo describes a registered tool and its source.
type ToolInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// MCPServerInfo describes a connected MCP server.
type MCPServerInfo struct {
	Name   string   `json:"name"`
	Tools  []string `json:"tools"`
	Status string   `json:"status,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// SkillInfo describes a discovered skill. The invocation-control and
// availability fields are copies of the Stage 1 skill-catalog values
// (agent/skill); the server adds no policy of its own.
type SkillInfo struct {
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	DisableModelInvocation bool     `json:"disable_model_invocation"`
	UserInvocable          bool     `json:"user_invocable"`
	Available              bool     `json:"available"`
	AllowedTools           []string `json:"allowed_tools,omitempty"`
}

// PluginStatusInfo summarizes a loaded plugin.
type PluginStatusInfo struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	SkillCount int    `json:"skill_count"`
	AgentCount int    `json:"agent_count"`
	HookCount  int    `json:"hook_count"`
	MCPCount   int    `json:"mcp_count"`
}

// JobStatusInfo describes an active or recent job.
type JobStatusInfo struct {
	JobID            string `json:"job_id"`
	JobType          string `json:"job_type"`
	Status           string `json:"status"`
	Reason           string `json:"reason,omitempty"`
	ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
	Resumable        *bool  `json:"resumable,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	OutputBytes      int64  `json:"output_bytes"`
	TranscriptRef    string `json:"transcript_ref,omitempty"`
	Command          string `json:"command,omitempty"`
	Intent           string `json:"intent,omitempty"`
	Task             string `json:"task,omitempty"`
}

// DelegateStatusInfo is the agent's delegate read model, carried unchanged on
// the daemon's detailed status.
type DelegateStatusInfo = agent.DelegateStatusInfo

type TurnSlotStatus struct {
	InUse  int64 `json:"in_use"`
	Cap    int64 `json:"cap"`
	Jobs   int64 `json:"jobs"`
	Drives int64 `json:"drive_turns"`
}

// HookEventStatus describes a single hook event's registration state.
type HookEventStatus struct {
	Event     string `json:"event"`
	Count     int    `json:"count"`
	Tier      string `json:"tier,omitempty"`
	Supported bool   `json:"supported"`
}

// DetailedStatus captures the full session configuration for AppWire diagnostics.
type DetailedStatus struct {
	Commands         []appwire.CommandDescriptor `json:"commands,omitzero"`
	Tools            []ToolInfo                  `json:"tools,omitempty"`
	MCP              []MCPServerInfo             `json:"mcp,omitempty"`
	Skills           []SkillInfo                 `json:"skills,omitempty"`
	SkillDiagnostics []SkillDiagnosticInfo       `json:"skill_diagnostics,omitempty"`
	Plugins          []PluginStatusInfo          `json:"plugins,omitempty"`
	HookEvents       []HookEventStatus           `json:"hook_events,omitempty"`
	Jobs             []JobStatusInfo             `json:"jobs,omitempty"`
	Delegates        []DelegateStatusInfo        `json:"delegates,omitempty"`
	Watches          []agent.WatchStatusInfo     `json:"watches,omitempty"`
	TurnSlots        *TurnSlotStatus             `json:"turn_slots,omitempty"`
	Agents           []string                    `json:"agents,omitempty"`
}

// SkillDiagnosticInfo is one Stage 1 skill-discovery diagnostic (collision,
// unreadable source, invalid metadata or controls), copied verbatim from the
// agent's catalog — the explicit source-detail view the path-free Skills
// catalog never carries.
type SkillDiagnosticInfo struct {
	Category    string `json:"category"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	OtherSource string `json:"other_source,omitempty"`
	Field       string `json:"field,omitempty"`
	Message     string `json:"message"`
}

// MarshalJSON preserves an explicit empty plugin inventory while keeping a
// nil inventory absent for old or unwired sources that cannot report it.
func (s DetailedStatus) MarshalJSON() ([]byte, error) {
	type alias DetailedStatus
	a := alias(s)
	a.Plugins = nil
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if s.Plugins == nil {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	plugins, err := json.Marshal(s.Plugins)
	if err != nil {
		return nil, err
	}
	fields["plugins"] = plugins
	return json.Marshal(fields)
}

// StatusInfo is the daemon's materialized root-session state used to build
// AppWire snapshots and notifications.
type StatusInfo struct {
	SessionID  string
	State      string
	Turns      int
	Model      string
	Profile    string
	WorkingDir string
}

// ContextMetrics describes the estimated size of the active session context.
type ContextMetrics struct {
	Used      int
	Window    int
	Remaining int
	Assumed   bool
}

// ServerConfig holds configuration for the HTTP server.
type ServerConfig struct {
	AppReplaySize int // default: 1000
	HubToken      string
	AllowedHost   string
	StateDir      string
}

// appDescendantProjection is the in-memory AppWire view of one in-process
// descendant. Descendants share the root daemon's transport, but each keeps an
// independent projector (and history, in appHistories) so their notification
// streams and thread/read cuts remain isolated.
type appDescendantProjection struct {
	projector *appprojector.AppEventProjector
	thread    appwire.Thread
}

type taskPublicationCursor struct {
	epoch    uint64
	revision uint64
}

// Server is the HTTP server that bridges an agent.Session to AppWire clients.
type Server struct {
	mux         *http.ServeMux
	appServer   *appserver.Server
	appNotifier *appserver.Notifier

	mu             sync.RWMutex
	status         StatusInfo
	appSourceID    string
	appThreadID    string
	appRef         string
	appProjector   *appprojector.AppEventProjector
	appDescendants map[string]*appDescendantProjection
	// appTaskPublications is the active TaskStore incarnation and newest applied
	// revision per owner for the current root identity. It is internal routing
	// state and resets with that identity.
	appTaskPublications map[string]taskPublicationCursor
	// appBootGeneration is the served identity's boot generation, stamped on
	// every history message (appwire.CompareBootGeneration).
	appBootGeneration string
	// appHistories holds the history of every thread served: the root's and
	// each descendant's, projected from their transcripts.
	appHistories *threadHistories
	// appDescendantHistorySource is the descendants' recorded-entry hook's
	// lock-free view of the served root (see descendantHistorySource).
	appDescendantHistorySource atomic.Pointer[descendantHistorySource]
	// appActiveTurnID is the running execution SetProcessingTurn published.
	appActiveTurnID string
	// appPendingStableTurnID is the execution SetProcessingTurn published
	// until its EXECUTION_STARTED is projected, while the ordered event
	// consumer may still drain the input before it. It is not an admission
	// lock.
	appPendingStableTurnID string
	// appDeferredTerminalNotifications retains only the status/closed frames
	// from a terminal event that raced a published execution. They are
	// published if the execution is abandoned, and discarded when its
	// EXECUTION_STARTED arrives.
	appDeferredTerminalNotifications []pendingAppNotification
	// appHeldSettledEffect is the status effect of a resting state the session
	// settled outside any turn (EventStatusSettled) while an input was being
	// taken. It is applied when processing ends, unless a turn is published
	// first (SetProcessingTurn; the turn's own end restates the state), the
	// input ends on a SESSION_END, or the root is replaced.
	appHeldSettledEffect func(*Server)
	// appTurnEndStated records that the published turn's interrupted
	// SESSION_END already stated the resting state while processing stays set
	// for the turn to unwind, so the end of processing publishes that state
	// rather than its idle placeholder.
	appTurnEndStated bool
	// appEnvelope is the daemon's one materialized thread envelope: every value
	// a thread snapshot reports about the live session other than its identity
	// and its turns. Reads copy it; nothing on a read path reaches the session.
	// See server/thread_envelope.go.
	appEnvelope threadEnvelope
	// appEnvelopeSource is the seam the bridge samples session state through at
	// the moments it changes. It is NEVER consulted by a read.
	appEnvelopeSource ThreadEnvelopeSource
	// appActivity is the root tree's pulse meter (S5). The projection commits
	// feed it and the thread list reads it, all under mu; see activityMeter.
	appActivity activityMeter
	// appPushedFailedToolCalls is the failure count the root's last
	// thread/status/changed carried, nil when none carried one: a running
	// execution pushes the count when the envelope's moves past it.
	appPushedFailedToolCalls  *int
	beforeAppProjectionCommit func()
	// appDescendantTranscriptPathFunc resolves a descendant thread ID to its
	// backing transcript file path, when the caller has one: the descendant's
	// history is projected from it (ledger #110/#111). Nil, or "", gives the
	// descendant no history.
	appDescendantTranscriptPathFunc func(threadID string) string
	// appDescendantLiveWatchesFunc resolves the row IDs of one thread LIST page
	// to the live watch rows that belong on each of them, when the daemon can
	// reach the child session. The list path consults it once per page to attach
	// a child's watches to the child's row; a descendant projection carries no
	// diagnostics block of its own, so without this a subagent's watches appear
	// on no row. An ID the answer omits keeps its cached projection, which is the
	// historical behavior when no watch source is wired.
	appDescendantLiveWatchesFunc func(threadIDs []string) map[string][]agent.WatchStatusInfo
	// appSubagentTallyFunc reads the root session's whole-tree subagent tally
	// for the thread list's root row (S3); nil disables it.
	appSubagentTallyFunc func() (appwire.SubagentTally, bool)
	retrySafeTurns       RetrySafeTurnFunctions
	cancelFunc           context.CancelFunc
	goalFunc             func(objective string) (bool, error)
	// notesHumanSetFunc is called by the appwire notes/human/set method. The
	// callback returns the full atomic acceptance result, including its receipt.
	notesHumanSetFunc func(outerID, note string) (appwire.NotesHumanSetResponse, error)
	// urlsRemoveFunc is called by the appwire urls/remove method. The callback
	// removes one URL list entry by id, reporting whether one was found. The
	// outer clientMutationId travels with it so the session can journal the
	// outer mutation (success replays after the entry is gone; a reused ID
	// with a different entry id conflicts without mutating).
	urlsRemoveFunc           func(outerID, id string) (bool, error)
	compactFunc              func(context.Context) error
	clearFunc                func(context.Context, appwire.ThreadClearParams) error
	clearJournal             threadClearJournalFile
	modelFunc                func(string) error
	visionModelFunc          func(string) error
	nameFunc                 func(string) error
	reasoningEffortFunc      func(string) error
	listModelsFunc           func(context.Context) ([]appwire.ModelDescriptor, error)
	appSessionActivity       sessionActivityHooks
	tasksFn                  func() []appwire.Task
	jobsFn                   func(context.Context, appwire.JobsListParams) (any, error)
	jobOutputFn              func(jobID string, beforeBytes *int64, maxBytes int64) (data appwire.JobOutputPage, found bool, err error)
	jobGetFn                 func(jobID string) (data appwire.JobActivityJob, found bool, err error)
	shutdownFunc             func()
	daemonStatusFunc         func() appwire.DaemonLifecycle
	daemonRetireFunc         func(context.Context, appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error)
	daemonIdleTimeoutSetFunc func(context.Context, appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error)

	// costLookupMu guards costLookup. It is deliberately NOT s.mu: the turn
	// projector calls the lookup from inside Project, which RecordAppEvent
	// runs while s.mu is held, so a lookup reaching for s.mu would deadlock.
	costLookupMu sync.RWMutex
	costLookup   func(ref string) *registry.Cost

	// sandboxEscalationResolveFunc delivers a human's approve/deny decision for a
	// pending sandbox-exemption escalation (M7) to the session, unblocking the
	// waiting tool-exec goroutine. nil when no session is attached.
	sandboxEscalationResolveFunc func(escalationID string, approve bool) error
	// delegateStopFunc ends one delegate's current run at the user's request
	// (S6). nil when no session is attached.
	delegateStopFunc func(delegateID string) (appwire.DelegateStopOutcome, error)
	processing       bool
	// appMutationGate serializes retry-safe turn mutations with thread/clear.
	// A clear must not rotate the live instance while an old-generation turn
	// callback is still admitted, and a delayed turn must not enter after clear
	// has acquired the write side of this gate.
	appMutationGate sync.RWMutex
	// notifyWakeRearmed is true while a goroutine is parked on the send of a
	// notification kick that found the input slot full. It coalesces further
	// drops into that one parked send. See SubmitNotification.
	notifyWakeRearmed bool
	// pendingUserInputWakeRearmed is notifyWakeRearmed's counterpart for the
	// queued-input wake. Each guaranteed wake needs its own flag: one shared
	// flag would let a parked notification swallow a queued-input kick, which is
	// the very drop both are here to prevent. See SubmitPendingUserInput.
	pendingUserInputWakeRearmed bool
	inputCh                     chan InputMessage
	hubToken                    string
	sameOrigin                  httpguard.SameOriginPolicy
}

// SetRetrySafeTurnFunctions installs the authoritative retry-safe mutation
// callbacks for the seven v2 turn methods.
//
// These are what appCapabilities reads: an Interrupt callback here is what
// makes the daemon advertise Stop, and Steer/Queue likewise gate their bits, so
// a harness is only advertised for the mutations its handlers will actually
// dispatch.
func (s *Server) SetRetrySafeTurnFunctions(functions RetrySafeTurnFunctions) {
	s.mu.Lock()
	s.retrySafeTurns = functions
	s.mu.Unlock()
}

// NewServer creates a new Server.
func NewServer(cfg ServerConfig) *Server {
	replaySize := cfg.AppReplaySize
	if replaySize <= 0 {
		replaySize = 1000
	}

	var runtime *Server
	s := &Server{
		mux: http.NewServeMux(),
		appServer: appserver.NewServer(appserver.ServerConfig{
			ServerName: "evener-serve",
			Version:    buildinfo.Version(),
			SourceID:   "local",
			SubscriptionAdmissionResolver: func(msg appwire.Message) appserver.SubscriptionAdmissionResolution {
				return runtime.resolveSubscriptionAdmission(msg)
			},
			Features: appwire.FeatureSet{
				ThreadList:        true,
				ThreadTurnsList:   true,
				TurnStart:         true,
				TurnSteer:         true,
				ThreadClear:       true,
				ThreadShutdown:    true,
				ForkFromTurn:      false,
				Tasks:             true,
				ModelList:         true,
				DirectoryComplete: false,
			},
		}),
		// AppReplaySize bounds notification RETENTION, which is transport
		// replay for a reconnecting subscriber. It does not bound the turn
		// snapshot: eviction changes how far a client can catch up from
		// deltas, never what the thread contains.
		appNotifier:         appserver.NewNotifier(replaySize),
		appSourceID:         "local",
		appHistories:        newAppHistories(),
		appDescendants:      make(map[string]*appDescendantProjection),
		appTaskPublications: make(map[string]taskPublicationCursor),
		clearJournal:        openThreadClearJournal(cfg.StateDir),
		inputCh:             make(chan InputMessage, 1),
		hubToken:            strings.TrimSpace(cfg.HubToken),
		sameOrigin:          httpguard.NewSameOriginPolicy(cfg.AllowedHost),
	}
	runtime = s
	s.registerAppWireHandlers()
	s.mux.HandleFunc("/rpc", s.appServer.ServeWebSocket)
	return s
}

// resolveSubscriptionAdmission classifies a thread/read or thread/unsubscribe
// request for ordered dispatch. Params that do not decode are Invalid, as on
// the hub; a request that is not a subscribe at all is NotSubscribe.
func (s *Server) resolveSubscriptionAdmission(msg appwire.Message) appserver.SubscriptionAdmissionResolution {
	params, ok, malformed := decodeSubscriptionAdmissionParams(msg)
	if malformed {
		return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionInvalid}
	}
	if !ok {
		return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionNotSubscribe}
	}
	threadID, target := s.appReadTarget(params)
	if threadID == "" {
		return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionUnresolved}
	}
	return appserver.SubscriptionAdmissionResolution{Key: target, Intent: appserver.SubscriptionAdmissionResolved}
}

// decodeSubscriptionAdmissionParams decodes msg's thread/read or
// thread/unsubscribe request params into a ThreadReadParams, the wire decode
// step of resolveSubscriptionAdmission: ok is false for a message the
// resolver does not apply to (not a request, a method other than the two it
// admits, or a read that does not subscribe), and malformed is true for
// params it cannot decode. Pulled out as its own function so the decode of
// client-supplied JSON, not the admission semantics that need a live Server
// to resolve against, is what a fuzz target exercises
// (FuzzDecodeSubscriptionAdmissionParams).
func decodeSubscriptionAdmissionParams(msg appwire.Message) (params appwire.ThreadReadParams, ok, malformed bool) {
	if msg.Request == nil || (msg.Request.Method != appwire.MethodThreadRead && msg.Request.Method != appwire.MethodThreadUnsubscribe) {
		return appwire.ThreadReadParams{}, false, false
	}
	if msg.Request.Method == appwire.MethodThreadUnsubscribe {
		var unsubscribe appwire.ThreadUnsubscribeParams
		if json.Unmarshal(msg.Request.Params, &unsubscribe) != nil {
			return appwire.ThreadReadParams{}, false, true
		}
		params.ThreadID, params.Ref = unsubscribe.ThreadID, unsubscribe.Ref
		return params, true, false
	}
	if json.Unmarshal(msg.Request.Params, &params) != nil {
		return appwire.ThreadReadParams{}, false, true
	}
	if !params.Subscribe {
		return appwire.ThreadReadParams{}, false, false
	}
	return params, true, false
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if message := s.sameOrigin.Rejection(r); message != "" {
		http.Error(w, message, http.StatusForbidden)
		return
	}
	if !httpguard.HubTokenAuthorized(s.hubToken, r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// GetStatus returns the current session status.
func (s *Server) GetStatus() StatusInfo {
	s.mu.RLock()
	status := s.status
	s.mu.RUnlock()
	return status
}

// UpdateSessionInfo sets session identity fields.
func (s *Server) UpdateSessionInfo(sessionID, model, profile string) {
	s.mu.Lock()
	s.updateSessionInfoLocked(sessionID, model, profile)
	s.mu.Unlock()
}

// UpdateSessionInfoFrom sets session identity fields from a sample that runs
// under the server lock. A caller whose read raced a newer model-changed
// projection - the daemon's model-set hook reading the profile while a turn
// boundary commits a queued switch - would otherwise install a stale snapshot
// after the projection already installed the new model, regressing the cache
// with no corrective event behind it. Sampling inside the write's critical
// section installs whatever the session reports at write time, so the cache
// can only move forward.
func (s *Server) UpdateSessionInfoFrom(sessionID string, sample func() (model, profile string)) {
	s.mu.Lock()
	model, profile := sample()
	s.updateSessionInfoLocked(sessionID, model, profile)
	s.mu.Unlock()
}

func (s *Server) updateSessionInfoLocked(sessionID, model, profile string) {
	s.status.SessionID = sessionID
	s.status.Model = model
	s.status.Profile = profile
}

// SetWorkingDir sets the working directory exposed in AppWire thread snapshots.
func (s *Server) SetWorkingDir(dir string) {
	s.mu.Lock()
	s.status.WorkingDir = dir
	s.mu.Unlock()
}

// SetState updates the session state string.
func (s *Server) SetState(state string) {
	s.mu.Lock()
	s.status.State = state
	s.mu.Unlock()
}

// IncrementTurns increments the turn counter.
func (s *Server) IncrementTurns() {
	s.mu.Lock()
	s.status.Turns++
	s.mu.Unlock()
}

// SetCancelFunc sets the cancel function used by turn/interrupt.
// The session loop arms it per turn and clears it between turns, so it answers
// "is a cancel armed right now" and nothing more durable than that.
//
// It is deliberately not what appCapabilities reads for Interrupt: that bit
// follows the retry-safe handler the RPC dispatches through
// (SetRetrySafeTurnFunctions), because a harness wired only with a cancel
// would answer turn/interrupt with Unavailable. Reading this per-turn field
// for the capability also published "a turn is running and cannot be stopped"
// in the gaps between arming and publishing (kata 5gdv).
func (s *Server) SetCancelFunc(cancel context.CancelFunc) {
	s.mu.Lock()
	s.cancelFunc = cancel
	s.mu.Unlock()
}

// SetSandboxEscalationResolveFunc sets the callback that delivers a human's
// approve/deny decision for a pending sandbox-exemption escalation (M7) to the
// session. It is invoked by the evener/sandbox/escalation/resolve daemon handler.
func (s *Server) SetSandboxEscalationResolveFunc(fn func(escalationID string, approve bool) error) {
	s.mu.Lock()
	s.sandboxEscalationResolveFunc = fn
	s.mu.Unlock()
}

// SetDelegateStopFunc sets the callback evener/delegate/stop hands a delegate
// id to (S6). Wiring it advertises the stopSubagent capability.
func (s *Server) SetDelegateStopFunc(fn func(delegateID string) (appwire.DelegateStopOutcome, error)) {
	s.mu.Lock()
	s.delegateStopFunc = fn
	s.mu.Unlock()
}

// SetGoalFunc sets the function called by the appwire goal/set method. The
// callback sets (or, for an empty objective, clears) the session's /goal and
// returns whether the goal loop started immediately.
func (s *Server) SetGoalFunc(fn func(objective string) (bool, error)) {
	s.mu.Lock()
	s.goalFunc = fn
	s.mu.Unlock()
}

// SetNotesHumanSetFunc sets the function called by the appwire notes/human/set
// method. The callback stores the human's session whiteboard and returns the
// stored (post-clamp) value and receipt; the session emits EventNotesUpdated after its
// successful store mutation for the projector to derive the push from.
func (s *Server) SetNotesHumanSetFunc(fn func(outerID, note string) (appwire.NotesHumanSetResponse, error)) {
	s.mu.Lock()
	s.notesHumanSetFunc = fn
	s.mu.Unlock()
}

// SetUrlsRemoveFunc sets the function called by the appwire urls/remove
// method. The callback removes one URL list entry by id for the given outer
// clientMutationId, reporting whether one was found; the session emits
// EventUrlsUpdated after a successful removal for the projector to derive
// the push from.
func (s *Server) SetUrlsRemoveFunc(fn func(outerID, id string) (bool, error)) {
	s.mu.Lock()
	s.urlsRemoveFunc = fn
	s.mu.Unlock()
}

// SetCompactFunc sets the function called by thread/compact/start.
func (s *Server) SetCompactFunc(fn func(context.Context) error) {
	s.mu.Lock()
	s.compactFunc = fn
	s.mu.Unlock()
}

// SetClearFunc sets the operation called by the typed thread/clear method.
// The server owns mutation identity, fencing, and the durable receipt; the
// callback only performs the session replacement.
func (s *Server) SetClearFunc(fn func(context.Context, appwire.ThreadClearParams) error) {
	s.mu.Lock()
	s.clearFunc = fn
	s.mu.Unlock()
}

// SetModelFunc sets the function called by thread/model/set.
func (s *Server) SetModelFunc(fn func(string) error) {
	s.mu.Lock()
	s.modelFunc = fn
	s.mu.Unlock()
}

// SetVisionModelFunc sets the function called by thread/vision-model/set.
func (s *Server) SetVisionModelFunc(fn func(string) error) {
	s.mu.Lock()
	s.visionModelFunc = fn
	s.mu.Unlock()
}

// SetNameFunc sets the function called by the rename appwire method.
func (s *Server) SetNameFunc(fn func(string) error) {
	s.mu.Lock()
	s.nameFunc = fn
	s.mu.Unlock()
}

// SetReasoningEffortFunc sets the function called to change the reasoning effort
// of the running session.
func (s *Server) SetReasoningEffortFunc(fn func(string) error) {
	s.mu.Lock()
	s.reasoningEffortFunc = fn
	s.mu.Unlock()
}

// SetShutdownFunc sets the function called by thread/shutdown.
// It must initiate graceful termination of the daemon process.
func (s *Server) SetShutdownFunc(fn func()) {
	s.mu.Lock()
	s.shutdownFunc = fn
	s.mu.Unlock()
}

// SetCostLookupFunc installs the $/Mtok cost source every priced figure this
// daemon reports is derived from: the live session's registry resolution of an
// "instance/model" reference (spec §7.5). Without one the daemon reports usage
// and no cost, never a bundled pricing table's guess.
func (s *Server) SetCostLookupFunc(fn func(ref string) *registry.Cost) {
	s.costLookupMu.Lock()
	s.costLookup = fn
	s.costLookupMu.Unlock()
}

// costFor is the cost of ref under the installed lookup; nil when none is
// installed or the row carries no cost. The pointer aliases the registry's
// own Cost (registry.Resolved's alias-don't-mutate rule,
// llm/registry/types.go), so every caller treats it as read-only.
func (s *Server) costFor(ref string) *registry.Cost {
	s.costLookupMu.RLock()
	fn := s.costLookup
	s.costLookupMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(ref)
}

// SetListModelsFunc sets the function used by the typed model/list method.
func (s *Server) SetListModelsFunc(fn func(context.Context) ([]appwire.ModelDescriptor, error)) {
	s.mu.Lock()
	s.listModelsFunc = fn
	s.mu.Unlock()
}

// SetTasksFunc sets the task-list reader called by evener/tasks/list. A nil
// slice means no data; a non-nil slice is authoritative, including an empty one.
func (s *Server) SetTasksFunc(fn func() []appwire.Task) {
	s.mu.Lock()
	s.tasksFn = fn
	s.mu.Unlock()
}

// SetJobsFunc sets the function backing evener/jobs/list. The function should
// return a JSON-serializable payload (typically appwire.JobActivityTree), or
// an error if it cannot read one: an unreadable durable root must reach the
// caller as a failure, never as the empty success a job-less session answers
// with.
func (s *Server) SetJobsFunc(fn func(context.Context, appwire.JobsListParams) (any, error)) {
	s.mu.Lock()
	s.jobsFn = fn
	s.mu.Unlock()
}

// SetJobOutputFunc sets the function backing evener/jobs/output. found=false
// maps to an invalid-params wire error (the caller guessed a job id).
// beforeBytes > 0 pages backwards through the job's output log.
func (s *Server) SetJobOutputFunc(fn func(jobID string, beforeBytes *int64, maxBytes int64) (data appwire.JobOutputPage, found bool, err error)) {
	s.mu.Lock()
	s.jobOutputFn = fn
	s.mu.Unlock()
}

// SetJobGetFunc sets the function backing evener/jobs/get. found=false maps to
// an invalid-params wire error (the caller guessed a job id).
func (s *Server) SetJobGetFunc(fn func(jobID string) (data appwire.JobActivityJob, found bool, err error)) {
	s.mu.Lock()
	s.jobGetFn = fn
	s.mu.Unlock()
}

// SetProcessing marks whether the session is currently processing input. It
// publishes nothing when processing starts: the running execution's TurnID is
// published by SetProcessingTurn, which the session calls before recording the
// execution's first entry. When processing ends, finishProcessing publishes the
// state the session's events stored (finishProcessing).
func (s *Server) SetProcessing(processing bool) {
	if !processing {
		s.finishProcessing()
		return
	}
	s.mu.Lock()
	s.processing = true
	s.mu.Unlock()
}

// SetProcessingTurn publishes turnID as the running execution: the thread is
// active with activeTurnId turnID, in one projection commit. serve installs it
// as the session's Session.SetExecutionStartedFunc, which runs before the
// execution's first entry is recorded, so before any history/updated for it.
func (s *Server) SetProcessingTurn(turnID string) {
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		s.processing = true
		s.appActiveTurnID = turnID
		// A new turn's end has not been stated yet, whatever the turn before
		// it stated.
		s.appTurnEndStated = false
		// A rest held while the input was being taken settled before this
		// turn started (Session.restMu), and the turn's end restates it.
		s.appHeldSettledEffect = nil
		// Until the execution's own EXECUTION_STARTED is projected, a
		// terminal status still queued from the input before it must not be
		// published over this one (RecordAppEvent defers it).
		s.appPendingStableTurnID = turnID
		threadID, ref := s.appRootIdentityLocked()
		s.mu.Unlock()
		return s.recordAppNotifications(threadID, []pendingAppNotification{{
			threadID: threadID,
			ref:      ref,
			method:   appwire.NotifyThreadStatusChanged,
			params:   appwire.ThreadStatusChangedParams{ThreadID: threadID, Ref: ref, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
		}})
	})
}

// endProcessingLocked ends the running input: nothing runs, so no turn is
// active and no execution is pending its start. Callers hold s.mu.
func (s *Server) endProcessingLocked() {
	s.processing = false
	s.appActiveTurnID = ""
	s.appPendingStableTurnID = ""
	s.appTurnEndStated = false
}

// InputCh returns the channel that receives user input messages.
func (s *Server) InputCh() <-chan InputMessage {
	return s.inputCh
}

// SubmitContinuation feeds a goal continuation prompt into the input channel as
// an EntryContinuation-kind message. The send is non-blocking: if the 1-slot
// buffer is full a turn is already pending, whose drain-loop gate is the
// reliable backstop for the goal, so dropping the kick is safe (spec §7). It is
// the send-side counterpart to InputCh, used by the idle-kick callback wired
// from serve.go (the agent module must not import server, so the kick is a
// callback that lands here).
func (s *Server) SubmitContinuation(prompt string) {
	select {
	case s.inputCh <- InputMessage{Kind: agent.EntryContinuation, Text: prompt}:
	default:
	}
}

// SubmitNotification wakes an idle session to drain its pending job
// notifications. It pushes a text-less EntryNotification-kind message onto the
// 1-slot input channel. It is wired from serve.go via Session.SetNotifyFunc
// (the agent module must not import server).
//
// The wake is GUARANTEED, not best-effort (see submitGuaranteedWake for how).
// Dropping it outright is only safe for a session that is mid-turn, where the
// drain loop's tail gate re-checks the queue; an idle session has no tail to
// run, so a dropped kick leaves a finished job unheard until some unrelated
// input happens along.
//
// Repeated drops coalescing into one parked send is safe here because the queue
// that wake drains is drained whole, so one wake settles any number of dropped
// kicks.
func (s *Server) SubmitNotification() {
	s.submitGuaranteedWake(InputMessage{Kind: agent.EntryNotification}, &s.notifyWakeRearmed)
}

// SubmitClientMutationStart wakes the serve loop after a turn/start intent is
// durably accepted. The loop claims the stored payload and identity from the
// named session rather than carrying either through this process-local signal.
//
// This wake is best-effort, and deliberately so: the serve loop probes for a
// runnable durable start at the top of every iteration, before it blocks on this
// channel (processNextServeInput). That probe is the backstop, and it is a
// complete one — for a kick to be dropped here the channel must be non-empty, so
// the loop cannot be blocked indefinitely; it consumes that message and probes
// again. Re-arming this wake the way the two guaranteed ones are re-armed would
// make it worse rather than redundant: the parked message names a session, the
// loop discards a start message whose SessionID is not the live session, and the
// coalescing flag would meanwhile have suppressed the later, correct kick.
func (s *Server) SubmitClientMutationStart(sessionID string) {
	select {
	case s.inputCh <- InputMessage{ClientMutationStart: true, SessionID: sessionID}:
	default:
	}
}

// SubmitPendingUserInput wakes the serve loop to run a durably queued message. Like
// SubmitClientMutationStart it names the session rather than carrying the
// payload, so the loop reads whatever the journal actually owns at the moment
// it claims.
//
// The wake is GUARANTEED, for the same reason SubmitNotification's is and with
// more at stake. A QueuedInput message is the only thing that reaches
// ProcessPendingUserInput; unlike a durable start there is no probe, so nothing
// comes back for a dropped kick. The stranding is real and quiet: a job
// notification occupying the slot while a question is pending is refused by the
// entry gate, so no turn runs, so no drain tail sweeps the queue — and the
// client has already been handed a receipt saying its message was accepted.
//
// One wake settles any number of dropped ones. The wake runs the queue head as
// its own turn and that turn's drain tail runs the rest: selectDrainNextAction
// returns runQueued at every tail, and its awaiting branch keeps the
// queued-input rung live while holding every other rung.
func (s *Server) SubmitPendingUserInput(sessionID string) {
	s.submitGuaranteedWake(
		InputMessage{QueuedInput: true, SessionID: sessionID},
		&s.pendingUserInputWakeRearmed,
	)
}

// submitGuaranteedWake delivers msg on the 1-slot input channel without ever
// blocking its caller and without ever dropping the wake. It is the shared shape
// behind SubmitNotification and SubmitPendingUserInput; each passes its own
// re-arm flag, which must be guarded by s.mu and used by that wake alone.
//
// A kick that finds the slot occupied is re-armed on a goroutine that parks on
// the send and completes it the moment the slot frees. There is no timer and no
// poll: the parked send IS the wait for channel availability. Not blocking the
// caller is a requirement, not an optimization — these wakes run synchronously
// on the AppWire request handler goroutine, and a blocking send there would
// stall a client's response behind the serve loop.
//
// rearmed is cleared only after the send lands, so every kick dropped while this
// one was parked is covered by it. That coalescing is safe only for a wake whose
// work is drained whole rather than one item at a time; both callers document
// why theirs is.
//
// The parked goroutine outlives a serve loop that stops reading, for the life of
// the process. Server has no shutdown lifecycle to select against, and inventing
// one to close a per-process-exit goroutine is a worse trade than the goroutine.
// Consolidating both wakes here means that exposure is one fact about this
// codebase, and one line to change if Server ever grows such a lifecycle.
func (s *Server) submitGuaranteedWake(msg InputMessage, rearmed *bool) {
	select {
	case s.inputCh <- msg:
		return
	default:
	}
	s.mu.Lock()
	if *rearmed {
		s.mu.Unlock()
		return
	}
	*rearmed = true
	s.mu.Unlock()
	go func() {
		s.inputCh <- msg
		s.mu.Lock()
		*rearmed = false
		s.mu.Unlock()
	}()
}
