package rvreg

import (
	"fmt"
	"sync"

	"primeradiant.com/serf/rendezvous"
)

// Registration tracks a serve process's rendezvous entry on disk, keeping the
// in-memory copy in sync so the session identity can be updated and the entry
// removed on shutdown.
type Registration struct {
	mu         sync.Mutex
	runDir     string
	entry      rendezvous.Entry
	registered bool
}

func (r *Registration) Register(runDir string, entry rendezvous.Entry) error {
	if _, err := rendezvous.Write(runDir, entry); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runDir = runDir
	r.entry = entry
	r.registered = true
	return nil
}

func (r *Registration) UpdateSessionID(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.registered {
		return nil
	}
	r.entry.ThreadID = sessionID
	r.entry.SessionID = sessionID
	_, err := rendezvous.Write(r.runDir, r.entry)
	return err
}

func (r *Registration) Remove() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.registered {
		return nil
	}
	return rendezvous.Remove(r.runDir, r.entry.PID)
}
