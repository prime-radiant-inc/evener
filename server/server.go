package server

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/schema"
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
	Text   string
	Images []ImageAttachment
	Kind   agent.EntryKind
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
	JobID         string `json:"job_id"`
	JobType       string `json:"job_type"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
	OutputBytes   int64  `json:"output_bytes"`
	TranscriptRef string `json:"transcript_ref,omitempty"`
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
	// working-state/token metrics (WS2 A7), read from workMetricsFn on demand
	// rather than pushed on every event. Usage is a pointer (unlike the other
	// two scalars) because appwire.SerfUsage is a value struct whose
	// omitempty would never omit — nil is how a fresh/unwired daemon signals
	// "no token data" rather than rendering ↑0 ↓0.
	Usage               *appwire.SerfUsage `json:"usage,omitempty"`
	WorkMillis          int64              `json:"work_millis,omitempty"`
	ActiveTurnStartedAt int64              `json:"active_turn_started_at,omitempty"`
	// PendingAsk mirrors the session's HasPendingAsk() — true while an
	// ask_user question is unanswered (Track A §2 ask-tiering). Additive,
	// daemon-truth: Codex-sourced threads and old daemons never set it, so
	// absence decodes as false everywhere downstream.
	PendingAsk bool `json:"pending_ask,omitempty"`
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

	mu                  sync.RWMutex
	status              StatusInfo
	appSourceID         string
	appThreadID         string
	appProjector        *appprojector.AppEventProjector
	appActiveTurnID     string
	appReservedTurnID   string
	cancelFunc          context.CancelFunc
	steerFunc           func(string)
	steerWithImagesFunc func(string, []ImageAttachment)
	queueFunc           func(string) error
	queueWithImagesFunc func(string, []ImageAttachment) error
	goalFunc            func(objective string) (bool, error)
	goalStatusFn        func() (status string, iterations int, ok bool)
	drainSteerFunc      func() error
	drainSteerInputFunc func(string, []ImageAttachment) error
	queueDepthFn        func() int
	queuePreviewFn      func() []string
	compactFunc         func(context.Context) error
	clearFunc           func(context.Context) error
	pressureFn          func() float64
	pendingAskFn        func() bool
	contextMetricsFn    func() ContextMetrics
	// workMetricsFn returns the live working-state/token metrics (WS2 A7):
	// accumulated wall-clock work time, cumulative token usage (nil when
	// there is none to report), and the in-flight turn's start time (0 when
	// idle). Read by both /status and the appwire appThread() projection.
	workMetricsFn       func() (workMillis int64, usage *appwire.SerfUsage, activeTurnStartedAt int64)
	sessionMetaFn       func() schema.SessionMeta
	detailedStatusFn    func() DetailedStatus
	modelFunc           func(string)
	nameFunc            func(string)
	reasoningEffortFunc func(string)
	listModelsFunc      func(context.Context) ([]ModelsResponseItem, error)
	tasksFn             func() any
	shutdownFunc        func()
	transcriptPathFn    func() string
	processing          bool
	inputCh             chan InputMessage
	hubToken            string
	sameOrigin          httpguard.SameOriginPolicy
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
				ThreadClear:       true,
				ThreadShutdown:    true,
				ForkFromTurn:      false,
				Tasks:             true,
				ModelList:         true,
				DirectoryComplete: false,
			},
		}),
		appNotifier: appserver.NewNotifier(replaySize),
		appSourceID: "local",
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
	s.status.SessionID = sessionID
	s.status.Model = model
	s.status.Profile = profile
	s.mu.Unlock()
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

// SetGoalStatusFunc sets the callback that reports the session's current /goal
// state for the thread-read projection (the SerfThread.Goal field). ok is false
// when no goal is set.
func (s *Server) SetGoalStatusFunc(fn func() (status string, iterations int, ok bool)) {
	s.mu.Lock()
	s.goalStatusFn = fn
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

// SetQueueDepthFunc sets a callback returning the current queue depth so
// capability projection can advertise Queue accurately.
func (s *Server) SetQueueDepthFunc(fn func() int) {
	s.mu.Lock()
	s.queueDepthFn = fn
	s.mu.Unlock()
}

// SetQueuePreviewFunc sets a callback returning a FIFO snapshot of queued
// user messages (first-line truncated). Used by appwire ReadThread to
// populate SerfThread.Queue so clients can render the queue preview
// without maintaining their own mirror (kata r80p).
func (s *Server) SetQueuePreviewFunc(fn func() []string) {
	s.mu.Lock()
	s.queuePreviewFn = fn
	s.mu.Unlock()
}

// SetContextPressureFunc sets a callback to retrieve live context pressure.
func (s *Server) SetContextPressureFunc(fn func() float64) {
	s.mu.Lock()
	s.pressureFn = fn
	s.mu.Unlock()
}

// SetPendingAskFunc sets a callback to retrieve the live pending-ask bit
// (Track A §2). Read by both /status (handleStatus) and the appwire thread
// projection (appThread).
func (s *Server) SetPendingAskFunc(fn func() bool) {
	s.mu.Lock()
	s.pendingAskFn = fn
	s.mu.Unlock()
}

// SetWorkMetricsFunc sets a callback to retrieve the live working-state/token
// metrics (WS2 A7): accumulated wall-clock work time, cumulative token usage
// (nil when there is none to report), and the in-flight turn's Unix start
// time (0 when idle). Read by both /status (handleStatus) and the appwire
// thread projection (appThread).
func (s *Server) SetWorkMetricsFunc(fn func() (workMillis int64, usage *appwire.SerfUsage, activeTurnStartedAt int64)) {
	s.mu.Lock()
	s.workMetricsFn = fn
	s.mu.Unlock()
}

// SetSessionMetaFunc sets a callback returning current session metadata for
// appwire thread snapshots, including generated/user-chosen display titles.
func (s *Server) SetSessionMetaFunc(fn func() schema.SessionMeta) {
	s.mu.Lock()
	s.sessionMetaFn = fn
	s.mu.Unlock()
}

// SetContextMetricsFunc sets a callback to retrieve live context size metrics.
func (s *Server) SetContextMetricsFunc(fn func() ContextMetrics) {
	s.mu.Lock()
	s.contextMetricsFn = fn
	s.mu.Unlock()
}

// SetDetailedStatusFunc sets a callback to retrieve detailed session status.
func (s *Server) SetDetailedStatusFunc(fn func() DetailedStatus) {
	s.mu.Lock()
	s.detailedStatusFn = fn
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
func (s *Server) SetModelFunc(fn func(string)) {
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

// InputRequest is the JSON body for POST /input.
type InputRequest struct {
	Text   string            `json:"text"`
	Images []ImageAttachment `json:"images,omitempty"`
}

// SetProcessing marks whether the session is currently processing input.
func (s *Server) SetProcessing(processing bool) {
	s.mu.Lock()
	s.processing = processing
	if processing {
		s.appReservedTurnID = ""
	}
	s.mu.Unlock()
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
