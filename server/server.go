package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

// ImageAttachment is re-exported from package agent so HTTP clients and the
// session layer share a single type.
type ImageAttachment = agent.ImageAttachment

// InputMessage is delivered on InputCh() carrying user text plus any
// attached images.
type InputMessage struct {
	Text   string
	Images []ImageAttachment
}

// ToolInfo describes a registered tool and its source.
type ToolInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// MCPServerInfo describes a connected MCP server.
type MCPServerInfo struct {
	Name  string   `json:"name"`
	Tools []string `json:"tools"`
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

// SubagentStatusInfo describes an active sub-agent.
type SubagentStatusInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	TurnsUsed int    `json:"turns_used"`
}

// DetailedStatus captures the full session configuration for /status display.
type DetailedStatus struct {
	Tools     []ToolInfo           `json:"tools,omitempty"`
	MCP       []MCPServerInfo      `json:"mcp,omitempty"`
	Skills    []SkillInfo          `json:"skills,omitempty"`
	Plugins   []PluginStatusInfo   `json:"plugins,omitempty"`
	Hooks     map[string]int       `json:"hooks,omitempty"`
	Subagents []SubagentStatusInfo `json:"subagents,omitempty"`
	Agents    []string             `json:"agents,omitempty"`
}

// StatusInfo is the JSON response for GET /status.
type StatusInfo struct {
	SessionID       string             `json:"session_id"`
	State           string             `json:"state"`
	Turns           int                `json:"turns"`
	Model           string             `json:"model"`
	Profile         string             `json:"profile"`
	WorkingDir      string             `json:"working_dir,omitempty"`
	ContextPressure float64            `json:"context_pressure"`
	Detailed        *DetailedStatus    `json:"detailed,omitempty"`
	Capabilities    ActionCapabilities `json:"capabilities"`
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
	appProjector        *AppEventProjector
	appActiveTurnID     string
	appReservedTurnID   string
	cancelFunc          context.CancelFunc
	steerFunc           func(string)
	queueFunc           func(string) error
	queueWithImagesFunc func(string, []ImageAttachment) error
	drainSteerFunc      func() error
	drainSteerInputFunc func(string, []ImageAttachment) error
	queueDepthFn        func() int
	queuePreviewFn      func() []string
	compactFunc         func(context.Context) error
	clearFunc           func(context.Context) error
	pressureFn          func() float64
	detailedStatusFn    func() DetailedStatus
	modelFunc           func(string)
	listModelsFunc      func(context.Context) ([]ModelsResponseItem, error)
	tasksFn             func() any
	shutdownFunc        func()
	processing          bool
	inputCh             chan InputMessage
	hubToken            string
	sameOrigin          sameOriginPolicy
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
				ThreadList:            true,
				ThreadTurnsList:       false,
				TurnStart:             true,
				TurnSteer:             true,
				ThreadClear:           true,
				ThreadShutdown:        true,
				ForkFromTurn:          false,
				Tasks:                 true,
				ModelList:             true,
				DirectoryComplete:     false,
				},
			}),
		appNotifier: appserver.NewNotifier(replaySize),
		appSourceID: "local",
		inputCh:     make(chan InputMessage, 1),
		hubToken:    strings.TrimSpace(cfg.HubToken),
		sameOrigin:  newSameOriginPolicy(cfg.AllowedHost),
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
	if message := s.sameOrigin.rejection(r); message != "" {
		http.Error(w, message, http.StatusForbidden)
		return
	}
	if !hubTokenAuthorized(s.hubToken, r.Header.Get("Authorization")) {
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

// SetState updates the session state string (IDLE, PROCESSING, CLOSED).
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

func (s *Server) handleSteer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req InputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	fn := s.steerFunc
	s.mu.RUnlock()
	if fn != nil {
		fn(req.Text)
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetQueueFunc sets the function called by POST /queue (kata 111a). The
// callback should append the message to the underlying session's input
// queue. Returns an error when the session refuses the message.
func (s *Server) SetQueueFunc(fn func(string) error) {
	s.mu.Lock()
	s.queueFunc = fn
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

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	processing := s.processing
	closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
	fn := s.queueFunc
	s.mu.RUnlock()
	if closed {
		http.Error(w, "session is closed", http.StatusConflict)
		return
	}
	if !processing {
		// turn/queue is only meaningful while a turn is in flight; when
		// the session is idle the caller should use /input instead.
		http.Error(w, "no active turn to queue against", http.StatusConflict)
		return
	}
	if fn == nil {
		http.Error(w, "queue not available", http.StatusServiceUnavailable)
		return
	}
	var req InputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	if err := fn(req.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleDrainAsSteer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	processing := s.processing
	closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
	fn := s.drainSteerFunc
	inputFn := s.drainSteerInputFunc
	depthFn := s.queueDepthFn
	s.mu.RUnlock()
	if closed {
		http.Error(w, "session is closed", http.StatusConflict)
		return
	}
	if !processing {
		http.Error(w, "no active turn to steer", http.StatusConflict)
		return
	}
	if fn == nil {
		http.Error(w, "drain-as-steer not available", http.StatusServiceUnavailable)
		return
	}
	var req InputRequest
	hasBody := r.ContentLength != 0
	if hasBody {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	}
	hasInput := strings.TrimSpace(req.Text) != "" || len(req.Images) > 0
	if hasInput && inputFn == nil {
		http.Error(w, "drain-as-steer with input not available", http.StatusServiceUnavailable)
		return
	}
	if !hasInput && depthFn != nil && depthFn() == 0 {
		http.Error(w, "queue is empty", http.StatusConflict)
		return
	}
	var err error
	if hasInput {
		err = inputFn(req.Text, req.Images)
	} else {
		err = fn()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetContextPressureFunc sets a callback to retrieve live context pressure.
func (s *Server) SetContextPressureFunc(fn func() float64) {
	s.mu.Lock()
	s.pressureFn = fn
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

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	fn := s.compactFunc
	s.mu.RUnlock()

	if fn == nil {
		http.Error(w, "compact not available", http.StatusServiceUnavailable)
		return
	}
	if err := fn(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetClearFunc sets the function called by POST /clear.
func (s *Server) SetClearFunc(fn func(context.Context) error) {
	s.mu.Lock()
	s.clearFunc = fn
	s.mu.Unlock()
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	processing := s.processing
	fn := s.clearFunc
	s.mu.RUnlock()

	if processing {
		http.Error(w, "session is processing", http.StatusConflict)
		return
	}

	if fn == nil {
		http.Error(w, "clear not available", http.StatusServiceUnavailable)
		return
	}
	if err := fn(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	fn := s.shutdownFunc
	s.mu.RUnlock()

	if fn == nil {
		http.Error(w, "shutdown not available", http.StatusServiceUnavailable)
		return
	}
	go fn()
	w.WriteHeader(http.StatusAccepted)
}

// SetModelFunc sets the function called by POST /model.
func (s *Server) SetModelFunc(fn func(string)) {
	s.mu.Lock()
	s.modelFunc = fn
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

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	fn := s.modelFunc
	s.mu.RUnlock()

	if fn == nil {
		http.Error(w, "model change not available", http.StatusServiceUnavailable)
		return
	}

	var req ModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}
	fn(req.Model)
	w.WriteHeader(http.StatusNoContent)
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

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	fn := s.listModelsFunc
	s.mu.RUnlock()

	if fn == nil {
		http.Error(w, "model listing not available", http.StatusServiceUnavailable)
		return
	}

	models, err := fn(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ModelsResponse{Models: models})
}

// SetTasksFunc sets the function called by GET /tasks. The function should
// return a JSON-serializable slice (typically []agent.Task).
func (s *Server) SetTasksFunc(fn func() any) {
	s.mu.Lock()
	s.tasksFn = fn
	s.mu.Unlock()
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	fn := s.tasksFn
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if fn == nil {
		_, _ = w.Write([]byte("[]\n"))
		return
	}
	_ = json.NewEncoder(w).Encode(fn())
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	status := s.status
	pfn := s.pressureFn
	dfn := s.detailedStatusFn
	processing := s.processing
	closed := appStatus(status.State, processing) == appwire.ThreadStatusClosed
	capabilities := ActionCapabilities{
		Send:        !processing && !closed,
		Steer:       s.steerFunc != nil,
		Interrupt:   s.cancelFunc != nil,
		Compact:     s.compactFunc != nil && !closed,
		Clear:       s.clearFunc != nil && !processing && !closed,
		Shutdown:    s.shutdownFunc != nil,
		ChangeModel: s.modelFunc != nil && !closed,
		Queue:       s.queueFunc != nil && processing && !closed,
	}
	s.mu.RUnlock()

	if pfn != nil {
		status.ContextPressure = pfn()
	}
	if dfn != nil {
		ds := dfn()
		status.Detailed = &ds
	}
	status.Capabilities = capabilities

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	cancel := s.cancelFunc
	s.mu.RUnlock()

	if cancel == nil {
		// Mirror the appwire path's Unavailable semantics so direct
		// daemon callers aren't misled into thinking the turn was
		// cancelled when no cancel function is wired up.
		http.Error(w, "interrupt not available", http.StatusServiceUnavailable)
		return
	}
	cancel()
	w.WriteHeader(http.StatusNoContent)
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

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	processing := s.processing
	closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
	s.mu.RUnlock()

	if closed {
		http.Error(w, "session is closed", http.StatusConflict)
		return
	}
	if processing {
		http.Error(w, "session is processing", http.StatusConflict)
		return
	}

	var req InputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Images) == 0 {
		http.Error(w, "text or images required", http.StatusBadRequest)
		return
	}

	select {
	case s.inputCh <- InputMessage{Text: req.Text, Images: req.Images}:
		w.WriteHeader(http.StatusAccepted)
	default:
		http.Error(w, "input buffer full", http.StatusConflict)
	}
}
