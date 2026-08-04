package server

import (
	"encoding/json"
	"net/http"
	"sort"
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
	queueDepth := s.appEnvelope.Queue.Depth
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
	// A preflight courtesy, not the authority: the session's own drain rejects an
	// empty queue. Reading the materialized depth keeps this endpoint on the one
	// queue value the daemon publishes rather than a second live read.
	if !hasInput && queueDepth == 0 {
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

	// Deciding and claiming are ONE critical section. A clear replaces the live
	// session: the daemon's callback builds a fresh one, publishes an identity
	// for it and swaps it in (cmd/serf/serve.go). Two of those running at once
	// each read the same session as the one they replace and each install their
	// own, so one replacement ends up current and the other is reachable from
	// nothing -- nothing closes it, so its env's Cleanup() never runs and the
	// scratch directory it owns outlives the daemon (kata mz2f, and kata x058
	// for the same consequence reached through shutdown).
	//
	// `processing` alone never covered this: it is false for the whole of a
	// clear, so it gates a clear against a TURN and not against another clear.
	//
	// The loser is refused rather than queued behind the winner. Running both
	// spends a whole session lifecycle -- SessionStart and SessionEnd hooks, a
	// provisioned sandbox, a transcript -- on a thread every subscriber watches
	// open and close in the same breath, and parking the second request here
	// would block it inside Session.Close()'s SessionEnd hooks, which run
	// arbitrary user commands under a 10s timeout. A refused clear is also the
	// only honest answer when the winner FAILS: a second request coalesced onto
	// it would have to report an error for work it never attempted, where 409
	// says exactly what happened and leaves retrying to the client.
	s.mu.Lock()
	processing := s.processing
	clearing := s.clearing
	fn := s.clearFunc
	if !processing && !clearing && fn != nil {
		s.clearing = true
	}
	s.mu.Unlock()

	if processing {
		http.Error(w, "session is processing", http.StatusConflict)
		return
	}

	if fn == nil {
		http.Error(w, "clear not available", http.StatusServiceUnavailable)
		return
	}
	if clearing {
		http.Error(w, "clear already in progress", http.StatusConflict)
		return
	}
	// Deferred so a clear that fails hands the endpoint back. Every fallible
	// step in the daemon's clear returns with the old session still live and
	// still clearable, so a gate released only on success would turn one
	// recoverable failure into an endpoint refused forever.
	defer func() {
		s.mu.Lock()
		s.clearing = false
		s.mu.Unlock()
	}()
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
	w.Header().Set("Content-Length", "0")
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
	processing := s.processing
	reservedTurnID := strings.TrimSpace(s.appReservedTurnID)
	fn := s.modelFunc
	s.mu.RUnlock()

	if processing || reservedTurnID != "" {
		msg := "session is processing"
		if reservedTurnID != "" {
			msg = "turn " + reservedTurnID + " is active"
		}
		http.Error(w, msg, http.StatusConflict)
		return
	}

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
	if err := fn(req.Model); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
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
	envelope := s.appEnvelope
	descendantSessionIDs := make([]string, 0, len(s.appDescendants))
	for id, projection := range s.appDescendants {
		if projection != nil && projection.thread.Status.Type != appwire.ThreadStatusClosed {
			descendantSessionIDs = append(descendantSessionIDs, id)
		}
	}
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
	sort.Strings(descendantSessionIDs)

	// /status answers from the same materialized envelope thread/read does. The
	// two used to pull the same seven session callbacks independently, which is
	// two sources for one value; now there is one, and the endpoints cannot
	// disagree.
	status.ContextPressure = envelope.ContextPressure
	status.ContextUsed = envelope.ContextMetrics.Used
	status.ContextWindow = envelope.ContextMetrics.Window
	status.ContextRemaining = envelope.ContextMetrics.Remaining
	status.Detailed = envelope.Detailed
	status.DescendantSessionIDs = descendantSessionIDs
	status.WorkMillis = envelope.WorkMillis
	status.Usage = envelope.Usage
	status.ActiveTurnStartedAt = envelope.ActiveTurnStartedAt
	status.FailedToolCalls = envelope.FailedToolCalls
	status.PendingAsk = envelope.AskPending
	// The pending-escalation BIT is the snapshot's own emptiness. Keeping a
	// separate callback for it would be a second source for one fact.
	status.PendingEscalation = len(envelope.PendingEscalations) > 0
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
