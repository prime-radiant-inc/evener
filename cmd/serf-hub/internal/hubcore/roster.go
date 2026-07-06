package hubcore

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/afero"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
	"primeradiant.com/serf/rendezvous"
)

// LiveEntry is the hub's view of a single live daemon, combining
// rendezvous-file metadata with the dynamic SessionID resolved via /status.
type LiveEntry struct {
	rendezvous.Entry
	SessionID  string
	Status     string // most-recent daemon state ("active", "idle", "awaiting", etc.)
	PendingAsk bool   // true while the daemon reports an unanswered ask_user question
}

// Prober is implemented by liveness-checking strategies.
//
// A Prober verifies a daemon is reachable AND returns its current
// session_id (which may have changed under POST /clear since the
// rendezvous file was written) and the daemon's current state.
type Prober interface {
	Probe(entry rendezvous.Entry) (sessionID, status string, pendingAsk, ok bool)
}

// Roster maintains the live-daemon set on the host. Reads of the underlying
// rendezvous directory are decoupled from network probes via the Prober
// interface so unit tests can substitute a stub.
type Roster struct {
	runDir string
	prober Prober

	// fs is the filesystem the roster creates runDir through. It defaults to
	// afero.NewOsFs() (whose calls forward straight to the os package, so
	// behavior is identical to a direct os.MkdirAll); tests and fuzzers inject
	// an in-memory or sandboxed filesystem via SetFs.
	fs afero.Fs

	mu     sync.RWMutex
	bySess map[string]LiveEntry // session_id -> entry
	byPID  map[int]LiveEntry    // pid -> entry (for fsnotify event correlation)

	// procAlive reports whether a daemon PID is still running. A failed /status
	// probe to a live process means the daemon is busy, not gone, so its session
	// is kept; injectable for tests.
	procAlive func(pid int) bool

	// watchReadyFn is called by Watch immediately after the fsnotify watcher has
	// been registered on runDir. Nil in production; injected by tests to
	// synchronize file-creation events without wall-clock sleeps.
	watchReadyFn func()

	// onChange, when set via SetOnChange, is fired by Refresh only when the
	// live set's membership or per-session status actually changes.
	onChange func()
	// fingerprint is the live-set hash from the most recent Refresh (see
	// rosterFingerprint), used to gate onChange against no-op refreshes.
	fingerprint uint64

	// onStatusChange, when set via SetOnStatusChange, is fired once per
	// session id by Refresh whenever that session's Status differs from the
	// prior snapshot (a session present in both snapshots with a changed
	// Status). It exists so a status transition can drive a targeted
	// past-index re-read (PastIndex.RefreshOne) instead of waiting for the
	// next full rebuild.
	onStatusChange func(sessionID string)
}

// NewRoster returns a Roster that scans runDir on demand.
//
// If prober is nil, liveness is assumed (used for tests).
func NewRoster(runDir string, prober Prober) *Roster {
	return &Roster{
		runDir:    runDir,
		prober:    prober,
		fs:        afero.NewOsFs(),
		bySess:    make(map[string]LiveEntry),
		byPID:     make(map[int]LiveEntry),
		procAlive: processAlive,
	}
}

// SetFs overrides the roster's filesystem. Production defaults to
// afero.NewOsFs() (identical to direct os calls); tests and fuzzers inject an
// in-memory or sandboxed filesystem. Returns the roster for call chaining.
func (r *Roster) SetFs(fs afero.Fs) *Roster {
	r.fs = fs
	return r
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

// SetOnChange registers a callback fired by Refresh only when the live set's
// membership or per-session status actually changes. Nil disables the hook.
func (r *Roster) SetOnChange(fn func()) { r.onChange = fn }

// SetOnStatusChange registers a callback fired once per session id, by
// Refresh, whenever that session's Status transitions between two
// consecutive snapshots. Nil disables the hook.
func (r *Roster) SetOnStatusChange(fn func(sessionID string)) { r.onStatusChange = fn }

func rosterFingerprint(bySess map[string]LiveEntry) uint64 {
	ids := make([]string, 0, len(bySess))
	for id := range bySess {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := fnv.New64a()
	for _, id := range ids {
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(bySess[id].Status))
		_, _ = h.Write([]byte{0})
		if bySess[id].PendingAsk {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// Refresh re-scans the rendezvous dir and updates the in-memory roster.
//
// Probes run concurrently and WITHOUT the roster lock held, so List() never
// blocks on network I/O and always returns the last good snapshot; the new
// snapshot is swapped in atomically at the end. A daemon that fails its
// liveness probe is kept as long as its process is still alive — a transient
// probe miss (busy daemon, overloaded host) must not blank the session from the
// UI. It is dropped only when its process is gone (a stale rendezvous file).
func (r *Roster) Refresh() {
	entries, err := rendezvous.List(r.runDir)
	if err != nil {
		return
	}

	// Snapshot the previous PID map for the keep-alive fallback, and the
	// previous per-session map for the status-transition diff below. Reading
	// them under a brief lock (rather than holding the lock across the
	// probes) keeps List() responsive while a slow probe pass runs.
	r.mu.RLock()
	prevByPID := r.byPID
	prevBySess := r.bySess
	r.mu.RUnlock()

	type probeResult struct {
		entry      rendezvous.Entry
		sessID     string
		status     string
		pendingAsk bool
		ok         bool
	}
	results := make([]probeResult, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		if r.prober == nil {
			results[i] = probeResult{entry: e, ok: true}
			continue
		}
		wg.Add(1)
		go func(i int, e rendezvous.Entry) {
			defer wg.Done()
			sessID, status, pendingAsk, ok := r.prober.Probe(e)
			results[i] = probeResult{entry: e, sessID: sessID, status: status, pendingAsk: pendingAsk, ok: ok}
		}(i, e)
	}
	wg.Wait()

	bySess := make(map[string]LiveEntry, len(entries))
	byPID := make(map[int]LiveEntry, len(entries))
	for _, res := range results {
		e := res.entry
		if !res.ok {
			// The rendezvous file plus a live PID are the authoritative "this
			// session exists" signal; keep the previously-seen entry while its
			// process is alive, and prune only when the process is gone.
			if prev, had := prevByPID[e.PID]; had && r.procAlive(e.PID) {
				byPID[e.PID] = prev
				if prev.SessionID != "" {
					bySess[prev.SessionID] = prev
				}
			}
			continue
		}
		live := LiveEntry{Entry: e, SessionID: res.sessID, Status: res.status, PendingAsk: res.pendingAsk}
		if res.sessID != "" {
			if prev, ok := bySess[res.sessID]; !ok || preferLiveEntry(live, prev) {
				bySess[res.sessID] = live
			}
		}
		byPID[e.PID] = live
	}

	r.mu.Lock()
	r.bySess = bySess
	r.byPID = byPID
	r.mu.Unlock()

	if r.onStatusChange != nil {
		for id, cur := range bySess {
			if prev, had := prevBySess[id]; had && prev.Status != cur.Status {
				r.onStatusChange(id)
			}
		}
	}

	fp := rosterFingerprint(bySess)
	r.mu.Lock()
	changed := fp != r.fingerprint
	r.fingerprint = fp
	r.mu.Unlock()
	if changed && r.onChange != nil {
		r.onChange()
	}
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

func ensureDir(fs afero.Fs, dir string) error {
	return fs.MkdirAll(dir, 0o700)
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
	_ = ensureDir(r.fs, r.runDir)
	if err := w.Add(r.runDir); err != nil {
		return err
	}
	if r.watchReadyFn != nil {
		r.watchReadyFn()
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
