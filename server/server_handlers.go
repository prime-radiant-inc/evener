package server

import (
	"net/http"
)

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Deciding and claiming are ONE critical section. A clear replaces the live
	// session: the daemon's callback builds a fresh one, publishes an identity
	// for it and swaps it in (cmd/evener/serve.go). Two of those running at once
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
