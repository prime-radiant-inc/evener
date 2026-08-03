package server

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appprojector"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/httpguard"
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
	SessionID           string
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

// SkillInfo describes a discovered skill.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
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
}

// DetailedStatus captures the full session configuration for /status display.
type DetailedStatus struct {
	Tools   []ToolInfo         `json:"tools,omitempty"`
	MCP     []MCPServerInfo    `json:"mcp,omitempty"`
	Skills  []SkillInfo        `json:"skills,omitempty"`
	Plugins []PluginStatusInfo `json:"plugins,omitempty"`
	Hooks   map[string]int     `json:"hooks,omitempty"`
	Jobs    []JobStatusInfo    `json:"jobs,omitempty"`
	Agents  []string           `json:"agents,omitempty"`
}

// StatusInfo is the JSON response for GET /status.
type StatusInfo struct {
	SessionID        string             `json:"session_id"`
	State            string             `json:"state"`
	Turns            int                `json:"turns"`
	Model            string             `json:"model"`
	Profile          string             `json:"profile"`
	WorkingDir       string             `json:"working_dir,omitempty"`
	ContextPressure  float64            `json:"context_pressure"`
	ContextUsed      int                `json:"context_used,omitempty"`
	ContextWindow    int                `json:"context_window,omitempty"`
	ContextRemaining int                `json:"context_remaining,omitempty"`
	Detailed         *DetailedStatus    `json:"detailed,omitempty"`
	Capabilities     ActionCapabilities `json:"capabilities"`
	// Usage, WorkMillis, and ActiveTurnStartedAt are the daemon's live
	// working-state/token metrics (WS2 A7), served from the materialized thread
	// envelope. Usage is a pointer (unlike the other
	// two scalars) because appwire.SerfUsage is a value struct whose
	// omitempty would never omit — nil is how a fresh/unwired daemon signals
	// "no token data" rather than rendering ↑0 ↓0.
	// ActiveTurnStartedAt is Unix epoch MILLISECONDS (like WorkMillis's scale
	// and the web reducer's epoch-ms read), 0 when no turn is running.
	Usage               *appwire.SerfUsage `json:"usage,omitempty"`
	WorkMillis          int64              `json:"work_millis,omitempty"`
	ActiveTurnStartedAt int64              `json:"active_turn_started_at,omitempty"`
	// FailedToolCalls is how many of the session's tool calls have failed, over
	// the WHOLE session — counted by the transcript writer as it records them
	// and seeded on resume from the file, so a running session's figure is
	// complete rather than a floor (kata 12rq). A pointer because 0 and unknown
	// are different claims: 0 means the session was measured and nothing failed,
	// nil means nobody counted (no transcript, an old daemon, a Codex thread).
	// Consumers render nil as nothing, never as a fabricated zero.
	FailedToolCalls *int `json:"failed_tool_calls,omitempty"`
	// PendingAsk mirrors the session's HasPendingAsk() — true while an
	// ask_user question is unanswered (Track A §2 ask-tiering). Additive,
	// daemon-truth: Codex-sourced threads and old daemons never set it, so
	// absence decodes as false everywhere downstream.
	PendingAsk bool `json:"pending_ask,omitempty"`
	// PendingEscalation mirrors the session's HasPendingEscalations() — true while a
	// sandbox-exemption escalation (M7) is blocked awaiting a human. The hub's
	// prober polls it so the owning session lights up cross-session (needs-you
	// badge) even mid-turn (an escalation blocks WHILE the status is "active").
	// Additive/daemon-truth, absent-decodes-false like PendingAsk.
	PendingEscalation bool `json:"pending_escalation,omitempty"`
}

// ContextMetrics describes the estimated size of the active session context.
type ContextMetrics struct {
	Used      int
	Window    int
	Remaining int
}

// ActionCapabilities reports which mutating session actions are currently
// supported by this daemon.
type ActionCapabilities struct {
	Send           bool   `json:"send"`
	Steer          bool   `json:"steer"`
	Interrupt      bool   `json:"interrupt"`
	Compact        bool   `json:"compact"`
	Clear          bool   `json:"clear"`
	Shutdown       bool   `json:"shutdown"`
	ChangeModel    bool   `json:"change_model"`
	Queue          bool   `json:"queue"`
	ReadOnlyReason string `json:"read_only_reason,omitempty"`
}

// ServerConfig holds configuration for the HTTP server.
type ServerConfig struct {
	AppReplaySize int // default: 1000
	HubToken      string
	AllowedHost   string
}

// Server is the HTTP server that bridges an agent.Session to REST and appwire clients.
type Server struct {
	mux         *http.ServeMux
	appServer   *appserver.Server
	appNotifier *appserver.Notifier

	mu           sync.RWMutex
	status       StatusInfo
	appSourceID  string
	appThreadID  string
	appProjector *appprojector.AppEventProjector
	// appTurns is the daemon's one materialized turn authority. Every turn read
	// -- thread/read, the latest window, an older page -- clones or windows this
	// and nothing else.
	appTurns        *appTurnSnapshot
	appActiveTurnID string
	// appEnvelope is the daemon's one materialized thread envelope: every value
	// a thread snapshot reports about the live session other than its identity
	// and its turns. Reads copy it; nothing on a read path reaches the session.
	// See server/thread_envelope.go.
	appEnvelope threadEnvelope
	// appEnvelopeSource is the seam the bridge samples session state through at
	// the moments it changes. It is NEVER consulted by a read.
	appEnvelopeSource         ThreadEnvelopeSource
	appReservedTurnID         string
	beforeAppProjectionCommit func()
	// insideAppProjectionCommit is a test seam invoked from within
	// RecordAppEvent's commit callback, i.e. while the projection gate is held.
	// Production leaves it nil.
	insideAppProjectionCommit func()
	// appLastStampedFailedToolCalls is the failure count most recently
	// stamped onto an item/completed notification (kata 895d) — nil means
	// nothing has been stamped yet for the current identity. It exists so
	// item/completed only carries the figure on the item whose completion
	// actually moved it, not on every tool call: the running count already
	// rides thread/status/changed unconditionally (every status change is a
	// turn boundary, so it can only have moved there), but that leaves a live
	// watcher unable to see a failure land partway through a long turn.
	// Reset to nil on SetAppIdentity so a new session's first observation is
	// never suppressed as "unchanged" by whatever the previous session on
	// this server left behind.
	appLastStampedFailedToolCalls *int
	retrySafeTurns                RetrySafeTurnFunctions
	cancelFunc                    context.CancelFunc
	steerFunc                     func(string)
	steerWithImagesFunc           func(string, []ImageAttachment)
	queueFunc                     func(string) error
	queueWithImagesFunc           func(string, []ImageAttachment) error
	goalFunc                      func(objective string) (bool, error)
	drainSteerFunc                func() error
	drainSteerInputFunc           func(string, []ImageAttachment) error
	promoteSteerFunc              func(int, string) error
	cancelQueuedFunc              func(int, string) (string, int, error)
	compactFunc                   func(context.Context) error
	clearFunc                     func(context.Context) error
	modelFunc                     func(string) error
	nameFunc                      func(string)
	reasoningEffortFunc           func(string)
	listModelsFunc                func(context.Context) ([]ModelsResponseItem, error)
	tasksFn                       func() any
	jobsFn                        func(appwire.JobsListParams) (any, error)
	jobOutputFn                   func(jobID string, maxBytes int64) (data any, found bool, err error)
	shutdownFunc                  func()
	// sandboxEscalationResolveFunc delivers a human's approve/deny decision for a
	// pending sandbox-exemption escalation (M7) to the session, unblocking the
	// waiting tool-exec goroutine. nil when no session is attached.
	sandboxEscalationResolveFunc func(escalationID string, approve bool) error
	processing                   bool
	// clearing is held for the duration of one POST /clear, which is the only
	// caller of clearFunc. It gates a clear against another clear the way
	// processing gates one against a turn: see handleClear for why a second
	// concurrent clear is refused rather than queued.
	clearing   bool
	inputCh    chan InputMessage
	hubToken   string
	sameOrigin httpguard.SameOriginPolicy
}

// SetRetrySafeTurnFunctions installs the authoritative retry-safe mutation
// callbacks for the seven v2 turn methods.
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

	s := &Server{
		mux: http.NewServeMux(),
		appServer: appserver.NewServer(appserver.ServerConfig{
			ServerName: "serf-serve",
			SourceID:   "local",
			Features: appwire.FeatureSet{
				ThreadList:        true,
				ThreadTurnsList:   true,
				TurnStart:         true,
				TurnSteer:         true,
				ThreadClear:       false,
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
		appNotifier: appserver.NewNotifier(replaySize),
		appSourceID: "local",
		appTurns:    &appTurnSnapshot{},
		inputCh:     make(chan InputMessage, 1),
		hubToken:    strings.TrimSpace(cfg.HubToken),
		sameOrigin:  httpguard.NewSameOriginPolicy(cfg.AllowedHost),
	}
	s.registerAppWireHandlers()
	s.mux.HandleFunc("/rpc", s.appServer.ServeWebSocket)
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/interrupt", s.handleInterrupt)
	s.mux.HandleFunc("/steer", s.handleSteer)
	s.mux.HandleFunc("/queue", s.handleQueue)
	s.mux.HandleFunc("/drain-as-steer", s.handleDrainAsSteer)
	s.mux.HandleFunc("/compact", s.handleCompact)
	s.mux.HandleFunc("/model", s.handleModel)
	s.mux.HandleFunc("/models", s.handleModels)
	s.mux.HandleFunc("/clear", s.handleClear)
	s.mux.HandleFunc("/input", s.handleInput)
	s.mux.HandleFunc("/tasks", s.handleTasks)
	s.mux.HandleFunc("/shutdown", s.handleShutdown)
	return s
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

// SetStatus replaces the full session status.
func (s *Server) SetStatus(info StatusInfo) {
	s.mu.Lock()
	s.status = info
	s.mu.Unlock()
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

func (s *Server) updateSessionInfoLocked(sessionID, model, profile string) {
	s.status.SessionID = sessionID
	s.status.Model = model
	s.status.Profile = profile
}

// SetWorkingDir sets the working directory exposed in /status.
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

// SetCancelFunc sets the cancel function called by POST /interrupt.
func (s *Server) SetCancelFunc(cancel context.CancelFunc) {
	s.mu.Lock()
	s.cancelFunc = cancel
	s.mu.Unlock()
}

// SetSandboxEscalationResolveFunc sets the callback that delivers a human's
// approve/deny decision for a pending sandbox-exemption escalation (M7) to the
// session. It is invoked by the serf/sandbox/escalation/resolve daemon handler.
func (s *Server) SetSandboxEscalationResolveFunc(fn func(escalationID string, approve bool) error) {
	s.mu.Lock()
	s.sandboxEscalationResolveFunc = fn
	s.mu.Unlock()
}

// SetSteerFunc sets the function called by POST /steer. It is invoked
// regardless of whether the session is currently processing.
func (s *Server) SetSteerFunc(fn func(string)) {
	s.mu.Lock()
	s.steerFunc = fn
	s.mu.Unlock()
}

// SetSteerWithImagesFunc sets the function called by AppWire turn/steer when
// the input carries image attachments.
func (s *Server) SetSteerWithImagesFunc(fn func(string, []ImageAttachment)) {
	s.mu.Lock()
	s.steerWithImagesFunc = fn
	s.mu.Unlock()
}

// SetQueueFunc sets the function called by POST /queue (kata 111a). The
// callback should append the message to the underlying session's input
// queue. Returns an error when the session refuses the message.
func (s *Server) SetQueueFunc(fn func(string) error) {
	s.mu.Lock()
	s.queueFunc = fn
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

// SetQueueWithImagesFunc sets the function called when the appwire
// turn/queue request carries image attachments (kata t5j6). The callback
// should append a queued entry that pairs the text with the attached
// images. When unset, image-bearing queue requests fall back to the
// text-only queueFunc (text portion only — image bytes are dropped). Wire
// callers must therefore set this function whenever they accept
// image-bearing queue requests.
func (s *Server) SetQueueWithImagesFunc(fn func(string, []ImageAttachment) error) {
	s.mu.Lock()
	s.queueWithImagesFunc = fn
	s.mu.Unlock()
}

// SetDrainAsSteerFunc sets the function called by POST /drain-as-steer
// (kata 0bq1). The callback should pop every queued message and inject
// them as a single STEERING message to the in-flight turn.
func (s *Server) SetDrainAsSteerFunc(fn func() error) {
	s.mu.Lock()
	s.drainSteerFunc = fn
	s.mu.Unlock()
}

// SetDrainAsSteerWithInputFunc sets the function called when drain-as-steer
// carries a composer payload. The callback must append and drain atomically.
func (s *Server) SetDrainAsSteerWithInputFunc(fn func(string, []ImageAttachment) error) {
	s.mu.Lock()
	s.drainSteerInputFunc = fn
	s.mu.Unlock()
}

// SetPromoteQueuedAsSteerFunc sets the function called by appwire
// turn/promoteQueuedAsSteer (issue #22). The callback should remove the
// queued message at the given FIFO index and inject it as a user-sourced
// STEERING message into the in-flight turn, leaving the rest of the queue
// untouched. A non-empty expectedID must match the queue-entry id minted at
// enqueue time so a queue that shifted under the client's snapshot is
// rejected rather than promoting the wrong message (review F1).
func (s *Server) SetPromoteQueuedAsSteerFunc(fn func(int, string) error) {
	s.mu.Lock()
	s.promoteSteerFunc = fn
	s.mu.Unlock()
}

// SetCancelQueuedFunc sets the function called by appwire
// turn/cancelQueued (issue #23). The callback should remove the queued
// message at the given FIFO index so it is never consumed, returning the
// removed entry's full text and image count. A non-empty expectedID must
// match the queue-entry id minted at enqueue time so a queue that shifted
// under the client's snapshot is rejected rather than removing the wrong
// message (review F1). Unlike promote, no active turn is required.
func (s *Server) SetCancelQueuedFunc(fn func(int, string) (string, int, error)) {
	s.mu.Lock()
	s.cancelQueuedFunc = fn
	s.mu.Unlock()
}

// SetCompactFunc sets the function called by POST /compact.
func (s *Server) SetCompactFunc(fn func(context.Context) error) {
	s.mu.Lock()
	s.compactFunc = fn
	s.mu.Unlock()
}

// SetClearFunc sets the function called by POST /clear.
func (s *Server) SetClearFunc(fn func(context.Context) error) {
	s.mu.Lock()
	s.clearFunc = fn
	s.mu.Unlock()
}

// SetModelFunc sets the function called by POST /model.
func (s *Server) SetModelFunc(fn func(string) error) {
	s.mu.Lock()
	s.modelFunc = fn
	s.mu.Unlock()
}

// SetNameFunc sets the function called by the rename appwire method.
func (s *Server) SetNameFunc(fn func(string)) {
	s.mu.Lock()
	s.nameFunc = fn
	s.mu.Unlock()
}

// SetReasoningEffortFunc sets the function called to change the reasoning effort
// of the running session.
func (s *Server) SetReasoningEffortFunc(fn func(string)) {
	s.mu.Lock()
	s.reasoningEffortFunc = fn
	s.mu.Unlock()
}

// SetShutdownFunc sets the function called by POST /shutdown.
// It must initiate graceful termination of the daemon process.
// The handler returns 202 immediately after invoking the callback.
func (s *Server) SetShutdownFunc(fn func()) {
	s.mu.Lock()
	s.shutdownFunc = fn
	s.mu.Unlock()
}

// ModelRequest is the JSON body for POST /model.
type ModelRequest struct {
	Model string `json:"model"`
}

// ModelsResponseItem is a single model entry in the GET /models response.
type ModelsResponseItem struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ModelsResponse is the JSON response for GET /models.
type ModelsResponse struct {
	Models []ModelsResponseItem `json:"models"`
}

// SetListModelsFunc sets the function called by GET /models.
func (s *Server) SetListModelsFunc(fn func(context.Context) ([]ModelsResponseItem, error)) {
	s.mu.Lock()
	s.listModelsFunc = fn
	s.mu.Unlock()
}

// SetTasksFunc sets the function called by GET /tasks. The function should
// return a JSON-serializable slice (typically []task.Task).
func (s *Server) SetTasksFunc(fn func() any) {
	s.mu.Lock()
	s.tasksFn = fn
	s.mu.Unlock()
}

// SetJobsFunc sets the function backing serf/jobs/list. The function should
// return a JSON-serializable payload (typically appwire.JobActivityTree), or
// an error if it cannot read one: an unreadable durable root must reach the
// caller as a failure, never as the empty success a job-less session answers
// with.
func (s *Server) SetJobsFunc(fn func(appwire.JobsListParams) (any, error)) {
	s.mu.Lock()
	s.jobsFn = fn
	s.mu.Unlock()
}

// SetJobOutputFunc sets the function backing serf/jobs/output. found=false
// maps to an invalid-params wire error (the caller guessed a job id).
func (s *Server) SetJobOutputFunc(fn func(jobID string, maxBytes int64) (data any, found bool, err error)) {
	s.mu.Lock()
	s.jobOutputFn = fn
	s.mu.Unlock()
}

// InputRequest is the JSON body for POST /input.
type InputRequest struct {
	Text   string            `json:"text"`
	Images []ImageAttachment `json:"images,omitempty"`
}

// SetProcessing marks whether the session is currently processing input.
//
// Going processing also guarantees an ActiveTurnID, because those two are one
// fact and a reader that sees them disagree is misinformed. Status.Type and
// Serf.ActiveTurnID were separately-written fields: turn/start reserves an id
// before flipping processing, but cmd/serf/serve.go's queued-input
// auto-continuation flips processing with no reservation at all, and learns
// the id only later when the next SessionStart event crosses the bridge
// goroutine. A thread/read landing in that window returned status "active"
// with an empty ActiveTurnID, and the composer's isTurnActive gate requires
// both — so it offered idle Send-only controls for a session that was really
// working (kata c2ty).
//
// This does NOT add a second turn-id minter, which would be the eptj failure
// shape: two paths numbering into one turn_N namespace, where a collision let
// turn/completed overwrite a persisted turn with unrelated content.
// ReserveTurnID is the same call turn/start makes, and the projector's
// startTurn CONSUMES an outstanding reservation rather than incrementing past
// it — so the id minted here is the one the real turn goes on to use.
func (s *Server) SetProcessing(processing bool) {
	s.mu.Lock()
	s.setProcessingLocked(processing)
	s.mu.Unlock()
}

func (s *Server) setProcessingLocked(processing bool) {
	s.processing = processing
	if processing {
		// The empty check is belt-and-braces, not load-bearing: ReserveTurnID
		// returns an outstanding reservation rather than minting a second one,
		// so removing this guard changes nothing today (mutation-verified —
		// the "mint unconditionally" mutant survives both tests). Kept because
		// it states the intent at the call site, and because it is what keeps
		// this correct if ReserveTurnID ever stops being idempotent.
		if strings.TrimSpace(s.appActiveTurnID) == "" {
			s.ensureAppProjectorLocked("")
			s.appActiveTurnID = s.appProjector.ReserveTurnID()
		}
		s.appReservedTurnID = ""
	}
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

// SubmitNotification wakes an idle session to drain its pending subagent
// notifications. It pushes a text-less EntryNotification-kind message onto the
// 1-slot input channel with non-blocking/drop-if-full semantics: a dropped kick
// is safe because the durable notification queue and the tail-drain suppress
// cover it (spec §N1). It is wired from serve.go via Session.SetNotifyFunc
// (the agent module must not import server).
func (s *Server) SubmitNotification() {
	select {
	case s.inputCh <- InputMessage{Kind: agent.EntryNotification}:
	default:
	}
}

// SubmitClientMutationStart wakes the serve loop after a turn/start intent is
// durably accepted. The loop claims the stored payload and identity from the
// named session rather than carrying either through this process-local signal.
func (s *Server) SubmitClientMutationStart(sessionID string) {
	select {
	case s.inputCh <- InputMessage{ClientMutationStart: true, SessionID: sessionID}:
	default:
	}
}
