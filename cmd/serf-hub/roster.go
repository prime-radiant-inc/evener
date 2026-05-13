package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/rendezvous"
)

// LiveEntry is the hub's view of a single live daemon, combining
// rendezvous-file metadata with the dynamic SessionID resolved via /status.
type LiveEntry struct {
	rendezvous.Entry
	SessionID string
	Status    string // most-recent daemon state ("processing", "idle", "awaiting", etc.)
}

// Prober is implemented by liveness-checking strategies.
//
// A Prober verifies a daemon is reachable AND returns its current
// session_id (which may have changed under POST /clear since the
// rendezvous file was written) and the daemon's current state.
type Prober interface {
	Probe(addr string) (sessionID, status string, ok bool)
}

// Roster maintains the live-daemon set on the host. Reads of the underlying
// rendezvous directory are decoupled from network probes via the Prober
// interface so unit tests can substitute a stub.
type Roster struct {
	runDir string
	prober Prober

	mu        sync.RWMutex
	bySess    map[string]LiveEntry // session_id -> entry
	byPID     map[int]LiveEntry    // pid -> entry (for fsnotify event correlation)
	failCount map[int]int          // pid -> consecutive prober failures
}

// NewRoster returns a Roster that scans runDir on demand.
//
// If prober is nil, liveness is assumed (used for tests).
func NewRoster(runDir string, prober Prober) *Roster {
	return &Roster{
		runDir:    runDir,
		prober:    prober,
		bySess:    make(map[string]LiveEntry),
		byPID:     make(map[int]LiveEntry),
		failCount: make(map[int]int),
	}
}

// Refresh re-scans the rendezvous dir and updates the in-memory roster.
// An entry is only pruned after two consecutive prober failures, giving
// transient blips a chance to recover without dropping the daemon from the UI.
func (r *Roster) Refresh() {
	entries, err := rendezvous.List(r.runDir)
	if err != nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	bySess := make(map[string]LiveEntry, len(entries))
	byPID := make(map[int]LiveEntry, len(entries))
	seen := make(map[int]bool, len(entries))

	for _, e := range entries {
		seen[e.PID] = true
		var sessID, status string
		ok := true
		if r.prober != nil {
			sessID, status, ok = r.prober.Probe(e.Address)
		}
		if !ok {
			r.failCount[e.PID]++
			if r.failCount[e.PID] < 2 {
				// First failure: keep the previous entry if we had one.
				if prev, had := r.byPID[e.PID]; had {
					byPID[e.PID] = prev
					if prev.SessionID != "" {
						bySess[prev.SessionID] = prev
					}
				}
				continue
			}
			// Second consecutive failure: prune.
			delete(r.failCount, e.PID)
			continue
		}
		r.failCount[e.PID] = 0
		live := LiveEntry{Entry: e, SessionID: sessID, Status: status}
		if sessID != "" {
			if prev, ok := bySess[sessID]; !ok || preferLiveEntry(live, prev) {
				bySess[sessID] = live
			}
		}
		byPID[e.PID] = live
	}

	// Reap fail-counts for PIDs whose rendezvous file is gone.
	for pid := range r.failCount {
		if !seen[pid] {
			delete(r.failCount, pid)
		}
	}

	r.bySess = bySess
	r.byPID = byPID
}

// List returns all live entries.
func (r *Roster) List() []LiveEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	bySession := make(map[string]LiveEntry, len(r.byPID))
	out := make([]LiveEntry, 0, len(r.byPID))
	for _, e := range r.byPID {
		sessionID := firstNonEmpty(e.SessionID, e.Entry.SessionID, e.ThreadID)
		if sessionID == "" {
			out = append(out, e)
			continue
		}
		if prev, ok := bySession[sessionID]; !ok || preferLiveEntry(e, prev) {
			bySession[sessionID] = e
		}
	}
	for _, e := range bySession {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return liveEntryLess(out[i], out[j])
	})
	return out
}

func preferLiveEntry(candidate, current LiveEntry) bool {
	candidateAppWire := candidate.Protocol == appwire.ProtocolVersion && candidate.Endpoint != "" && candidate.ThreadID != ""
	currentAppWire := current.Protocol == appwire.ProtocolVersion && current.Endpoint != "" && current.ThreadID != ""
	if candidateAppWire != currentAppWire {
		return candidateAppWire
	}
	if !candidate.StartedAt.Equal(current.StartedAt) {
		return candidate.StartedAt.After(current.StartedAt)
	}
	return candidate.PID > current.PID
}

// Find returns the entry with the given session_id, or false if not present.
func (r *Roster) Find(sessionID string) (LiveEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.bySess[sessionID]
	return e, ok
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// Watch blocks: it scans once, then refreshes on every fsnotify event and
// at a 5-second tick (cheap belt-and-suspenders against missed events).
//
// Cancellation of ctx returns from Watch.
func (r *Roster) Watch(ctx context.Context) error {
	r.Refresh()

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	// Add the runDir; create it if absent so the watcher can attach.
	_ = ensureDir(r.runDir)
	if err := w.Add(r.runDir); err != nil {
		return err
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-w.Events:
			if !ok {
				return nil
			}
			r.Refresh()
		case err := <-w.Errors:
			if err != nil {
				fmt.Fprintf(os.Stderr, "[hub] fsnotify error on %s: %v\n", r.runDir, err)
			}
			r.Refresh()
		case <-ticker.C:
			r.Refresh()
		}
	}
}
