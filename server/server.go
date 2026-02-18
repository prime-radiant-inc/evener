package server

import (
	"context"
	"encoding/json"
	"net/http"
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
