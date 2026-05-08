package main

import (
	"sync"

	"primeradiant.com/serf/rendezvous"
)

// LiveEntry is the hub's view of a single live daemon, combining
// rendezvous-file metadata with the dynamic SessionID resolved via /status.
type LiveEntry struct {
	rendezvous.Entry
	SessionID string
}

// Prober is implemented by liveness-checking strategies.
//
// A Prober verifies a daemon is reachable AND returns its current
// session_id (which may have changed under POST /clear since the
// rendezvous file was written).
type Prober interface {
	Probe(addr string) (sessionID string, ok bool)
}

// Roster maintains the live-daemon set on the host. Reads of the underlying
// rendezvous directory are decoupled from network probes via the Prober
// interface so unit tests can substitute a stub.
type Roster struct {
	runDir string
	prober Prober

	mu      sync.RWMutex
	bySess  map[string]LiveEntry // session_id -> entry
	byPID   map[int]LiveEntry    // pid -> entry (for fsnotify event correlation)
}

// NewRoster returns a Roster that scans runDir on demand.
//
// If prober is nil, liveness is assumed (used for tests).
func NewRoster(runDir string, prober Prober) *Roster {
	return &Roster{
		runDir: runDir,
		prober: prober,
		bySess: make(map[string]LiveEntry),
		byPID:  make(map[int]LiveEntry),
	}
}

// refresh re-scans the rendezvous dir and updates the in-memory roster.
// Entries that fail liveness are dropped.
func (r *Roster) refresh() {
	entries, err := rendezvous.List(r.runDir)
	if err != nil {
		return
	}
	bySess := make(map[string]LiveEntry, len(entries))
	byPID := make(map[int]LiveEntry, len(entries))
	for _, e := range entries {
		var sessID string
		ok := true
		if r.prober != nil {
			sessID, ok = r.prober.Probe(e.Address)
			if !ok {
				continue
			}
		}
		live := LiveEntry{Entry: e, SessionID: sessID}
		if sessID != "" {
			bySess[sessID] = live
		}
		byPID[e.PID] = live
	}
	r.mu.Lock()
	r.bySess = bySess
	r.byPID = byPID
	r.mu.Unlock()
}

// List returns all live entries.
func (r *Roster) List() []LiveEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]LiveEntry, 0, len(r.byPID))
	for _, e := range r.byPID {
		out = append(out, e)
	}
	return out
}

// Find returns the entry with the given session_id, or false if not present.
func (r *Roster) Find(sessionID string) (LiveEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.bySess[sessionID]
	return e, ok
}
