package hubcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
	"primeradiant.com/serf/rendezvous"
)

// LiveEntry is the hub's view of a single live daemon, combining
// rendezvous-file metadata with the dynamic SessionID resolved via /status.
type LiveEntry struct {
	rendezvous.Entry
	SessionID string
	Status    string // most-recent daemon state ("active", "idle", "awaiting", etc.)
}

// Prober is implemented by liveness-checking strategies.
//
// A Prober verifies a daemon is reachable AND returns its current
// session_id (which may have changed under POST /clear since the
// rendezvous file was written) and the daemon's current state.
type Prober interface {
	Probe(entry rendezvous.Entry) (sessionID, status string, ok bool)
}

// Roster maintains the live-daemon set on the host. Reads of the underlying
// rendezvous directory are decoupled from network probes via the Prober
// interface so unit tests can substitute a stub.
type Roster struct {
	runDir string
	prober Prober

	mu     sync.RWMutex
	bySess map[string]LiveEntry // session_id -> entry
	byPID  map[int]LiveEntry    // pid -> entry (for fsnotify event correlation)

	// procAlive reports whether a daemon PID is still running. A failed /status
	// probe to a live process means the daemon is busy, not gone, so its session
	// is kept; injectable for tests.
	procAlive func(pid int) bool
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
		procAlive: processAlive,
	}
}

// processAlive reports whether a process with the given PID currently exists.
// Hub-spawned daemons run on the same host, so signal 0 is a reliable presence
// check; EPERM (the process exists but is owned by another user) counts as
// alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// NewRosterWithEntries returns a Roster pre-seeded with the given live entries,
// bypassing the rendezvous-dir scan. Each entry is indexed by its PID (for List)
// and, when non-empty, by its SessionID (for Find), mirroring how Refresh
// populates the roster. It exists so callers in other packages can stand up a
// roster with synthetic live entries (the rendezvous dir and prober are empty).
func NewRosterWithEntries(entries ...LiveEntry) *Roster {
	r := NewRoster("", nil)
	for _, e := range entries {
		r.byPID[e.PID] = e
		if e.SessionID != "" {
			r.bySess[e.SessionID] = e
		}
	}
	return r
}

// Refresh re-scans the rendezvous dir and updates the in-memory roster.
// A daemon that fails its liveness probe is kept as long as its process is
// still alive — a transient probe miss (busy daemon, overloaded host) must not
// blank the session from the UI. It is dropped only when its process is gone
// (a stale rendezvous file).
func (r *Roster) Refresh() {
	entries, err := rendezvous.List(r.runDir)
	if err != nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	bySess := make(map[string]LiveEntry, len(entries))
	byPID := make(map[int]LiveEntry, len(entries))

	for _, e := range entries {
		var sessID, status string
		ok := true
		if r.prober != nil {
			sessID, status, ok = r.prober.Probe(e)
		}
		if !ok {
			// The probe failed, but the rendezvous file plus a live PID are the
			// authoritative "this session exists" signal: a transient miss (a
			// busy daemon, a briefly overloaded host) must not blank the sidebar.
			// Keep the previously-seen entry while its process is alive; prune
			// only when the process is gone (a stale rendezvous file left by a
			// crashed daemon).
			if prev, had := r.byPID[e.PID]; had && r.procAlive(e.PID) {
				byPID[e.PID] = prev
				if prev.SessionID != "" {
					bySess[prev.SessionID] = prev
				}
			}
			continue
		}
		live := LiveEntry{Entry: e, SessionID: sessID, Status: status}
		if sessID != "" {
			if prev, ok := bySess[sessID]; !ok || preferLiveEntry(live, prev) {
				bySess[sessID] = live
			}
		}
		byPID[e.PID] = live
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
		sessionID := strutil.FirstNonEmpty(e.SessionID, e.Entry.SessionID, e.ThreadID)
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
	return os.MkdirAll(dir, 0o700)
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
	defer w.Close() //nolint:errcheck // watcher cleanup; close error is not actionable

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
