package rvreg

import (
	"errors"
	"sync"

	"primeradiant.com/evener/rendezvous"
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
		return errors.New("session id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.registered {
		return nil
	}
	r.entry.ThreadID = sessionID
	r.entry.SessionID = sessionID
	r.entry.InstanceID = sessionID
	_, err := rendezvous.Write(r.runDir, r.entry)
	return err
}

// Entry returns a detached copy of the registered rendezvous record. The
// retire path uses it to revalidate a caller's claimed ownership generation
// against what this process actually published. The copy is taken under the
// registration mutex so a concurrent UpdateSessionID cannot tear it.
func (r *Registration) Entry() (rendezvous.Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.registered {
		return rendezvous.Entry{}, false
	}
	return r.entry, true
}

func (r *Registration) Remove() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.registered {
		return nil
	}
	// Exact-ownership removal: if a replacement daemon has rewritten the
	// entry for this PID, the file on disk is no longer this process's to
	// delete, and the stale cleanup must leave it in place.
	return rendezvous.RemoveIfOwned(r.runDir, r.entry)
}
