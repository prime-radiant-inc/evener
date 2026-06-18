package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"primeradiant.com/serf/appwire"
)

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
	w.WriteHeader(http.StatusAccepted)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go fn()
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
	_ = json.NewEncoder(w).Encode(ModelsResponse{Models: models})
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
	cmfn := s.contextMetricsFn
	dfn := s.detailedStatusFn
	processing := s.processing
	closed := appStatus(status.State, processing) == appwire.ThreadStatusClosed
	steerAvailable := s.steerFunc != nil || s.steerWithImagesFunc != nil
	capabilities := ActionCapabilities{
		Send:        !processing && !closed,
		Steer:       steerAvailable,
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
	if cmfn != nil {
		metrics := cmfn()
		status.ContextUsed = metrics.Used
		status.ContextWindow = metrics.Window
		status.ContextRemaining = metrics.Remaining
	}
	if dfn != nil {
		ds := dfn()
		status.Detailed = &ds
	}
	status.Capabilities = capabilities

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
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
