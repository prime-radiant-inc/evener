package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// StatusInfo is the JSON response for GET /status.
type StatusInfo struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	Turns     int    `json:"turns"`
	Model     string `json:"model"`
	Profile   string `json:"profile"`
}

// ServerConfig holds configuration for the HTTP server.
type ServerConfig struct {
	RingBufferSize int // default: 1000
}

// Server is the HTTP server that bridges an agent.Session to REST+SSE clients.
type Server struct {
	mux         *http.ServeMux
	broadcaster *Broadcaster

	mu         sync.RWMutex
	status     StatusInfo
	cancelFunc context.CancelFunc
	processing bool
	inputCh    chan string
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
	s.mux.HandleFunc("/input", s.handleInput)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// SetStatus updates the current session status.
func (s *Server) SetStatus(info StatusInfo) {
	s.mu.Lock()
	s.status = info
	s.mu.Unlock()
}

// SetCancelFunc sets the cancel function called by POST /interrupt.
func (s *Server) SetCancelFunc(cancel context.CancelFunc) {
	s.mu.Lock()
	s.cancelFunc = cancel
	s.mu.Unlock()
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	status := s.status
	s.mu.RUnlock()

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
