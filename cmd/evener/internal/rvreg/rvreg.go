package rvreg

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
	removed    bool
}

func (r *Registration) Register(runDir string, entry rendezvous.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.removed {
		return errors.New("registration has been removed")
	}
	if _, err := rendezvous.Write(runDir, entry); err != nil {
		return err
	}
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
	if r.removed {
		return errors.New("registration has been removed")
	}
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
	r.removed = true
	err := rendezvous.RemoveIfOwned(r.runDir, r.entry)
	if err == nil {
		r.registered = false
		return nil
	}
	// The ownership guard protects a live replacement's rendezvous entry, and
	// Write always publishes that entry as a regular file under the same lock.
	// An artifact at <pid>.json that is not a regular file therefore cannot be
	// a replacement's entry to protect, so fall back to the plain PID-scoped
	// removal: a cleanup that failed on a transient filesystem condition must
	// still finish when a later attempt retries it, which is the contract the
	// shutdown loop relies on. A regular file that no longer matches this
	// process's identity is refused by RemoveIfOwned above and left untouched.
	artifact := filepath.Join(r.runDir, strconv.Itoa(r.entry.PID)+".json")
	if fi, statErr := os.Stat(artifact); statErr != nil || !fi.Mode().IsRegular() {
		fallbackErr := rendezvous.Remove(r.runDir, r.entry.PID)
		if fallbackErr == nil {
			r.registered = false
			return nil
		}
		err = fallbackErr
	}
	return err
}
