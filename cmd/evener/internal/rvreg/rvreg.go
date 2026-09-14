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
	// a replacement's entry to protect, so reconcile it: RemoveUnlessRegular
	// re-checks and unlinks under the same per-PID ownership lock, so a
	// replacement reusing this PID cannot have its live entry deleted by a
	// check-then-unlink that is not atomic. That keeps the contract the
	// shutdown loop relies on -- a cleanup that failed on a transient
	// filesystem condition still finishes when a later attempt retries it. A
	// regular file is refused by RemoveUnlessRegular as well, and is left
	// untouched by both.
	fallbackErr := rendezvous.RemoveUnlessRegular(r.runDir, r.entry.PID)
	if fallbackErr == nil {
		r.registered = false
		return nil
	}
	// Both refused, so a regular file remains at <pid>.json. If it now carries
	// a different daemon's identity, a replacement reused this PID and rewrote
	// the record: this process's own entry is already gone, making the guard's
	// refusal a completed no-op rather than a failure to retry.
	if diskHoldsReplacement(r.runDir, r.entry) {
		r.registered = false
		return nil
	}
	// Otherwise this process's own artifact survived a real failure. Return the
	// original cause, not RemoveUnlessRegular's secondary regular-file refusal,
	// so the shutdown loop logs and retries what actually went wrong.
	return err
}

// diskHoldsReplacement reports whether <pid>.json currently carries a valid
// rendezvous entry for this PID that is not this registration's identity --
// i.e. a replacement daemon reused the PID and rewrote the record. It uses the
// canonical ownership fingerprint (rendezvous.OwnershipFingerprint) so the
// decision matches the identity authority the daemon and Hub enforce for exact
// ownership. A missing, unreadable or unparseable artifact reports false, which
// keeps the caller's original (retryable) error rather than declaring a no-op.
func diskHoldsReplacement(runDir string, own rendezvous.Entry) bool {
	entries, err := rendezvous.List(runDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.PID != own.PID {
			continue
		}
		return rendezvous.OwnershipFingerprint(entry) != rendezvous.OwnershipFingerprint(own)
	}
	return false
}
