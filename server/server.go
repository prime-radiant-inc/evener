package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// StatusInfo is the JSON response for GET /status.
type StatusInfo struct {
	SessionID       string  `json:"session_id"`
	State           string  `json:"state"`
	Turns           int     `json:"turns"`
	Model           string  `json:"model"`
	Profile         string  `json:"profile"`
	ContextPressure float64 `json:"context_pressure"`
}

// ServerConfig holds configuration for the HTTP server.
type ServerConfig struct {
	RingBufferSize int // default: 1000
}

// Server is the HTTP server that bridges an agent.Session to REST+SSE clients.
type Server struct {
	mux         *http.ServeMux
	broadcaster *Broadcaster

	mu          sync.RWMutex
	status      StatusInfo
	cancelFunc  context.CancelFunc
	compactFunc func(context.Context) error
	clearFunc   func(context.Context) error
	pressureFn  func() float64
	modelFunc   func(string)
	processing  bool
	inputCh     chan string
}

// NewServer creates a new Server.
func NewServer(cfg ServerConfig) *Server {
	ringSize := cfg.RingBufferSize
	if ringSize <= 0 {
		ringSize = 1000
	}

	s := &Server{
		mux:         http.NewServeMux(),
		broadcaster: NewBroadcaster(ringSize),
		inputCh:     make(chan string, 1),
	}
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/interrupt", s.handleInterrupt)
	s.mux.HandleFunc("/compact", s.handleCompact)
	s.mux.HandleFunc("/model", s.handleModel)
	s.mux.HandleFunc("/clear", s.handleClear)
	s.mux.HandleFunc("/input", s.handleInput)
	s.mux.HandleFunc("/events", s.handleEvents)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

// SetContextPressureFunc sets a callback to retrieve live context pressure.
func (s *Server) SetContextPressureFunc(fn func() float64) {
	s.mu.Lock()
	s.pressureFn = fn
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
	fn := s.clearFunc
	s.mu.RUnlock()

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

// SetModelFunc sets the function called by POST /model.
func (s *Server) SetModelFunc(fn func(string)) {
	s.mu.Lock()
	s.modelFunc = fn
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

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	status := s.status
	pfn := s.pressureFn
	s.mu.RUnlock()

	if pfn != nil {
		status.ContextPressure = pfn()
	}

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

	if cancel != nil {
		cancel()
	}
	w.WriteHeader(http.StatusNoContent)
}

// InputRequest is the JSON body for POST /input.
type InputRequest struct {
	Text string `json:"text"`
}

// SetProcessing marks whether the session is currently processing input.
func (s *Server) SetProcessing(processing bool) {
	s.mu.Lock()
	s.processing = processing
	s.mu.Unlock()
}

// InputCh returns the channel that receives user input text.
func (s *Server) InputCh() <-chan string {
	return s.inputCh
}

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	processing := s.processing
	s.mu.RUnlock()

	if processing {
		http.Error(w, "session is processing", http.StatusConflict)
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

	select {
	case s.inputCh <- req.Text:
		w.WriteHeader(http.StatusAccepted)
	default:
		http.Error(w, "input buffer full", http.StatusConflict)
	}
}

// sseEvent wraps an event type and data for SSE serialization.
type sseEvent struct {
	Type string
	Data any
}

// Broadcast sends an event to all SSE subscribers via the broadcaster.
// The event type becomes the SSE `event:` field, and data is JSON-encoded for the `data:` field.
func (s *Server) Broadcast(eventType string, data any) {
	s.broadcaster.Send(sseEvent{Type: eventType, Data: data})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Parse Last-Event-ID for catchup
	var lastID uint64
	if idStr := r.Header.Get("Last-Event-ID"); idStr != "" {
		fmt.Sscanf(idStr, "%d", &lastID)
	}

	ch, unsub := s.broadcaster.Subscribe(lastID)
	defer unsub()

	for {
		select {
		case <-r.Context().Done():
			return
		case item, ok := <-ch:
			if !ok {
				return
			}
			ev, _ := item.Value.(sseEvent)
			data, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", item.ID, ev.Type, data)
			flusher.Flush()
		}
	}
}
