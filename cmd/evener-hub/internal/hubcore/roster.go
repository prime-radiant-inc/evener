package hubcore

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/afero"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

// LiveEntry is the hub's view of a single live daemon, combining
// rendezvous-file metadata with dynamic state resolved via AppWire.
type LiveEntry struct {
	rendezvous.Entry
	SessionID          string
	Status             string   // most-recent daemon state ("active", "idle", "awaiting", etc.)
	Crashed            bool     // true only for a retained record whose daemon PID is confirmed gone
	PendingAsk         bool     // true while the daemon reports an unanswered ask_user question
	PendingEscalation  bool     // true while the daemon reports a blocked sandbox-exemption escalation (M7)
	RunningSubagentIDs []string // in-process children reported by this daemon; not independently routable
	// RunningSubagentStates carries each listed child's projected status
	// ("active", "idle", ...) when the daemon reports it. Retained stable
	// delegates with no current run are projected as idle even if their child
	// thread has a stale active status. A listed child with no entry has an
	// unknown state (old daemon) — callers must NOT treat liveness as activity,
	// and fold a no-state child to idle rather than active. Keyed by child
	// session ID; a defensive copy rides every List/Find like the IDs slice.
	RunningSubagentStates map[string]string
	// RunningJobs contains non-terminal, non-agent work reported by the daemon,
	// such as shell and watch jobs. Delegate jobs stay represented by the
	// descendant fields above so consumers do not render duplicate agent rows.
	RunningJobs []appwire.EvenerJobInfo
	// CompletedJobs contains recent terminal non-agent jobs. Delegate jobs stay
	// represented by descendant sessions for the same reason as RunningJobs.
	CompletedJobs []appwire.EvenerJobInfo
	Project       identifier.Project // canonical identity resolved at hub ingestion, when available
}

// ProbeResult is the dynamic session state returned by a daemon liveness probe.
type ProbeResult struct {
	SessionID             string
	Status                string
	PendingAsk            bool
	PendingEscalation     bool
	RunningSubagentIDs    []string
	RunningSubagentStates map[string]string
	RunningJobs           []appwire.EvenerJobInfo
	CompletedJobs         []appwire.EvenerJobInfo
	OK                    bool
}

// Prober is implemented by liveness-checking strategies.
//
// A Prober verifies a daemon is reachable AND returns its current
// session_id (which may have changed under thread/clear since the
// rendezvous file was written) and the daemon's current state.
type Prober interface {
	Probe(entry rendezvous.Entry) ProbeResult
}

// cloneSubagentStates defensive-copies a running-subagent state map; nil
// stays nil so "daemon carried no states" survives every hand-off intact.
func cloneSubagentStates(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func cloneRunningJobs(in []appwire.EvenerJobInfo) []appwire.EvenerJobInfo {
	return appwire.CloneEvenerJobs(in)
}

func cloneLiveEntry(in LiveEntry) LiveEntry {
	out := in
	out.RunningSubagentIDs = append([]string(nil), in.RunningSubagentIDs...)
	out.RunningSubagentStates = cloneSubagentStates(in.RunningSubagentStates)
	out.RunningJobs = cloneRunningJobs(in.RunningJobs)
	out.CompletedJobs = cloneRunningJobs(in.CompletedJobs)
	return out
}

// crashedFileRetention is how long Refresh keeps a dead PID's rendezvous file
// on disk (and its "errored" entry in the roster, kata zm6s) after the
// daemon's StartedAt. Past that, the crash is old news and the file is
// garbage-collected so dead-pid files don't accumulate forever.
const crashedFileRetention = 24 * time.Hour

type rosterWatcher interface {
	Add(string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

type fsnotifyWatcher struct{ *fsnotify.Watcher }

func (w fsnotifyWatcher) Events() <-chan fsnotify.Event { return w.Watcher.Events }
func (w fsnotifyWatcher) Errors() <-chan error          { return w.Watcher.Errors }

type rosterTicker interface {
	C() <-chan time.Time
	Stop()
}

type timeTicker struct{ *time.Ticker }

func (t timeTicker) C() <-chan time.Time { return t.Ticker.C }

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

	mu          sync.RWMutex
	bySess      map[string]LiveEntry // session_id -> entry
	byPID       map[int]LiveEntry    // pid -> entry (for fsnotify event correlation)
	unconfirmed []rendezvous.Entry   // live PIDs whose daemon ownership has not been established
	// A completed pass may publish unless a newer pass already published.
	refreshGen              uint64
	publishedGen            uint64
	entryPublishedGen       map[int]uint64
	ownershipRefreshRunning bool
	queuedOwnershipRefresh  *rosterRefreshBatch

	// procAlive reports whether a daemon PID is still running. A failed AppWire
	// probe to a live process means the daemon is busy, not gone, so its session
	// is kept; injectable for tests.
	procAlive func(pid int) bool

	// watchReadyFn is called by Watch immediately after the fsnotify watcher has
	// been registered on runDir. Nil in production; injected by tests to
	// synchronize file-creation events without wall-clock sleeps.
	watchReadyFn func()
	newWatcher   func() (rosterWatcher, error)
	newTicker    func(time.Duration) rosterTicker

	// onChange, when set via SetOnChange, is fired by Refresh only when the
	// live set's membership, per-session status, running-child set, or unresolved ownership changes.
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
		runDir:            runDir,
		prober:            prober,
		fs:                afero.NewOsFs(),
		bySess:            make(map[string]LiveEntry),
		byPID:             make(map[int]LiveEntry),
		entryPublishedGen: make(map[int]uint64),
		procAlive:         processAlive,
		newWatcher: func() (rosterWatcher, error) {
			w, err := fsnotify.NewWatcher()
			return fsnotifyWatcher{w}, err
		},
		newTicker: func(d time.Duration) rosterTicker { return timeTicker{time.NewTicker(d)} },
	}
}

// SetFs overrides the roster's filesystem. Production defaults to
// afero.NewOsFs() (identical to direct os calls); tests and fuzzers inject an
// in-memory or sandboxed filesystem. Returns the roster for call chaining.
func (r *Roster) SetFs(fs afero.Fs) *Roster {
	r.fs = fs
	return r
}

// NewRosterWithEntries returns a Roster pre-seeded with the given live entries,
// bypassing the rendezvous-dir scan. Each entry is indexed by its PID (for List)
// and, when non-empty, by its SessionID (for Find), mirroring how Refresh
// populates the roster. It exists so callers in other packages can stand up a
// roster with synthetic live entries (the rendezvous dir and prober are empty).
func NewRosterWithEntries(entries ...LiveEntry) *Roster {
	r := NewRoster("", nil)
	for _, e := range entries {
		e = cloneLiveEntry(e)
		r.byPID[e.PID] = e
		if e.SessionID != "" {
			r.bySess[e.SessionID] = e
		}
	}
	return r
}

// SetOnChange registers a callback fired by Refresh only when the live set's
// membership or observable per-session state actually changes. Nil disables
// the hook.
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
		if bySess[id].Crashed {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
		if bySess[id].PendingAsk {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
		runningSubagentIDs := append([]string(nil), bySess[id].RunningSubagentIDs...)
		sort.Strings(runningSubagentIDs)
		for _, childID := range runningSubagentIDs {
			_, _ = h.Write([]byte(childID))
			_, _ = h.Write([]byte{0})
			// A child's own state transition (working -> settled) must bump the
			// fingerprint just like its arrival or departure: it changes what
			// the sidebar renders for that child.
			_, _ = h.Write([]byte(bySess[id].RunningSubagentStates[childID]))
			_, _ = h.Write([]byte{0})
		}
		writeJobs := func(jobs []appwire.EvenerJobInfo) {
			sort.SliceStable(jobs, func(i, j int) bool {
				if jobs[i].JobID != jobs[j].JobID {
					return jobs[i].JobID < jobs[j].JobID
				}
				if jobs[i].JobType != jobs[j].JobType {
					return jobs[i].JobType < jobs[j].JobType
				}
				return jobs[i].Status < jobs[j].Status
			})
			for _, job := range jobs {
				_, _ = h.Write([]byte(job.JobID))
				_, _ = h.Write([]byte{0})
				_, _ = h.Write([]byte(job.JobType))
				_, _ = h.Write([]byte{0})
				_, _ = h.Write([]byte(job.Status))
				_, _ = h.Write([]byte{0})
				_, _ = h.Write([]byte(job.Command))
				_, _ = h.Write([]byte{0})
				_, _ = h.Write([]byte(job.Task))
				_, _ = h.Write([]byte{0})
				_, _ = h.Write([]byte(job.Reason))
				_, _ = h.Write([]byte{0})
			}
		}
		runningJobs := append([]appwire.EvenerJobInfo(nil), bySess[id].RunningJobs...)
		writeJobs(runningJobs)
		_, _ = h.Write([]byte{0})
		completedJobs := append([]appwire.EvenerJobInfo(nil), bySess[id].CompletedJobs...)
		writeJobs(completedJobs)
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
	_ = r.refresh()
}

func (r *Roster) refresh() error {
	r.mu.Lock()
	r.refreshGen++
	generation := r.refreshGen
	r.mu.Unlock()

	var entries []rendezvous.Entry
	// An unconfigured roster has no discovery directory. A configured path
	// disappearing is an incomplete read and must preserve existing ownership.
	if r.runDir != "" {
		var err error
		entries, err = rendezvous.ListStrict(r.runDir)
		if err != nil {
			return err
		}
	}

	// Snapshot the previous PID map for the keep-alive fallback. Reading
	// it under a brief lock (rather than holding the lock across the
	// probes) keeps List() responsive while a slow probe pass runs.
	r.mu.RLock()
	prevByPID := r.byPID
	r.mu.RUnlock()

	type probeResult struct {
		entry rendezvous.Entry
		ProbeResult
	}
	results := make([]probeResult, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		if r.prober == nil {
			results[i] = probeResult{entry: e, OK: true}
			continue
		}
		wg.Add(1)
		go func(i int, e rendezvous.Entry) {
			defer wg.Done()
			results[i] = probeResult{entry: e, ProbeResult: r.prober.Probe(e)}
		}(i, e)
	}
	wg.Wait()

	bySess := make(map[string]LiveEntry, len(entries))
	byPID := make(map[int]LiveEntry, len(entries))
	var unconfirmed []rendezvous.Entry
	for _, res := range results {
		e := res.entry
		if !res.OK {
			// The rendezvous file plus a live PID are the authoritative "this
			// session exists" signal; keep the previously-seen entry while its
			// process is alive (a transient probe miss).
			if prev, had := prevByPID[e.PID]; had && r.procAlive(e.PID) {
				byPID[e.PID] = prev
				if prev.SessionID != "" {
					bySess[prev.SessionID] = prev
				}
				continue
			}
			if r.procAlive(e.PID) {
				unconfirmed = append(unconfirmed, e)
				continue // ownership is unresolved; do not publish it as a live daemon
			}
			// The process is confirmed GONE, yet its rendezvous file is still
			// on disk. The rendezvous package writes that file on startup and
			// removes it only on graceful shutdown (rvreg.Registration.Remove);
			// a file surviving its own PID's death means the daemon never got
			// to run that cleanup - a crash, not a normal exit (kata zm6s: a
			// SIGKILLed session read identically to one that finished its
			// task). Surface it as "errored" instead of silently dropping it,
			// using whichever session id the file itself carries - it was
			// written before the daemon could die, so this needs no in-memory
			// history and survives a hub restart discovering an
			// already-stale file just as well as watching the crash live.
			sessionID := envvars.FirstNonEmpty(e.SessionID, e.ThreadID)
			if sessionID == "" {
				// Never resolved an id; nothing to attribute the crash to. The
				// file on disk is pure garbage, so reclaim it instead of
				// rescanning it on every refresh forever. Removal failure is
				// non-fatal (same stance as the List error above): the file
				// just survives until a later refresh.
				_ = rendezvous.Remove(r.runDir, e.PID)
				continue
			}
			if time.Since(e.StartedAt) > crashedFileRetention {
				// Old enough that crash reporting no longer matters: drop the
				// entry and unlink the file so dead-pid rendezvous files stop
				// accumulating forever. Fresh crashes keep the retained
				// "errored" contract below untouched.
				_ = rendezvous.Remove(r.runDir, e.PID)
				continue
			}
			crashed := LiveEntry{Entry: e, SessionID: sessionID}
			if prev, had := prevByPID[e.PID]; had {
				// Prefer the roster's own richer last-seen snapshot (ask/
				// subagent state) when available; a crash always overrides
				// whatever status the daemon last reported before dying.
				crashed = prev
			}
			crashed.Status = "errored"
			crashed.Crashed = true
			byPID[e.PID] = crashed
			bySess[sessionID] = crashed
			continue
		}
		live := liveEntryFromProbe(e, res.ProbeResult)
		if res.SessionID != "" {
			if prev, ok := bySess[res.SessionID]; !ok || preferLiveEntry(live, prev) {
				bySess[res.SessionID] = live
			}
		}
		byPID[e.PID] = live
	}

	fp := rosterFingerprint(bySess)
	r.mu.Lock()
	if generation < r.publishedGen {
		r.mu.Unlock()
		return nil
	}
	// A newer single-daemon confirmation supersedes only that daemon's
	// observation. Keep the complete scan's findings for every other PID.
	merged := false
	for pid, confirmed := range r.entryPublishedGen {
		if confirmed <= generation {
			delete(r.entryPublishedGen, pid)
			continue
		}
		if live, ok := r.byPID[pid]; ok {
			byPID[pid] = live
			merged = true
		}
		unconfirmed = slices.DeleteFunc(unconfirmed, func(claim rendezvous.Entry) bool { return claim.PID == pid })
	}
	if merged {
		bySess = make(map[string]LiveEntry, len(byPID))
		for _, live := range byPID {
			if live.SessionID == "" {
				continue
			}
			if previous, ok := bySess[live.SessionID]; !ok || preferLiveEntry(live, previous) {
				bySess[live.SessionID] = live
			}
		}
		fp = rosterFingerprint(bySess)
	}
	r.publishedGen = generation
	prevBySess := r.bySess
	r.bySess = bySess
	r.byPID = byPID
	ownershipChanged := !slices.Equal(r.unconfirmed, unconfirmed)
	r.unconfirmed = unconfirmed
	changed := fp != r.fingerprint || ownershipChanged
	r.fingerprint = fp
	statusChanges := make([]string, 0)
	for id, cur := range bySess {
		if prev, had := prevBySess[id]; had && prev.Status != cur.Status {
			statusChanges = append(statusChanges, id)
		}
	}
	sort.Strings(statusChanges)
	onStatusChange := r.onStatusChange
	onChange := r.onChange
	r.mu.Unlock()

	if onStatusChange != nil {
		for _, id := range statusChanges {
			onStatusChange(id)
		}
	}
	if changed && onChange != nil {
		onChange()
	}
	return nil
}

type rosterRefreshBatch struct {
	done chan struct{}
	err  error
}

// RefreshAndWait waits for a scan that starts after this request. Requests
// arriving during a scan share the next scan, so ongoing traffic cannot move
// an existing caller's completion target. Cancellation releases the caller;
// the shared scan continues for the other callers.
func (r *Roster) RefreshAndWait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	batch := r.queuedOwnershipRefresh
	if batch == nil {
		batch = &rosterRefreshBatch{done: make(chan struct{})}
		r.queuedOwnershipRefresh = batch
	}
	if !r.ownershipRefreshRunning {
		r.ownershipRefreshRunning = true
		go r.refreshOwnership()
	}
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-batch.done:
		return batch.err
	}
}

func (r *Roster) refreshOwnership() {
	for {
		r.mu.Lock()
		batch := r.queuedOwnershipRefresh
		r.queuedOwnershipRefresh = nil
		if batch == nil {
			r.ownershipRefreshRunning = false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		batch.err = r.refresh()
		close(batch.done)
	}
}

// List returns all live entries.
func (r *Roster) List() []LiveEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	bySession := make(map[string]LiveEntry, len(r.byPID))
	out := make([]LiveEntry, 0, len(r.byPID))
	for _, e := range r.byPID {
		e = cloneLiveEntry(e)
		sessionID := envvars.FirstNonEmpty(e.SessionID, e.Entry.SessionID, e.ThreadID)
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

// IsSubagentActive reports whether a live parent daemon currently owns a
// running in-process child. Child IDs remain outside bySess so callers cannot
// mistake the parent's endpoint for an independently routable child daemon.
func (r *Roster) IsSubagentActive(sessionID string) bool {
	_, live := r.SubagentState(sessionID)
	return live
}

// SubagentState resolves a running in-process child's own projected status.
// live is true when a live parent daemon currently lists the child (it is
// non-closed and resumable); the returned state is the child's own reported
// status, or "" when its daemon carried no per-descendant states (an old
// daemon — unknown, NOT settled). Callers deciding what to render should
// keep their pre-states fallback for the "" case.
func (r *Roster) SubagentState(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.byPID {
		if slices.Contains(entry.RunningSubagentIDs, sessionID) {
			return entry.RunningSubagentStates[sessionID], true
		}
	}
	return "", false
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
	e = cloneLiveEntry(e)
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

	w, err := r.newWatcher()
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

	ticker := r.newTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-w.Events():
			if !ok {
				return nil
			}
			r.Refresh()
		case err := <-w.Errors():
			if err != nil {
				fmt.Fprintf(os.Stderr, "[hub] fsnotify error on %s: %v\n", r.runDir, err)
			}
			r.Refresh()
		case <-ticker.C():
			r.Refresh()
		}
	}
}

// UnconfirmedEntries returns rendezvous claims whose processes are alive but
// whose daemon identity could not be established. They are not live sessions,
// but callers must not treat their absence from List as proof of released ownership.
func (r *Roster) UnconfirmedEntries() []rendezvous.Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.unconfirmed)
}

func liveEntryFromProbe(e rendezvous.Entry, result ProbeResult) LiveEntry {
	return LiveEntry{
		Entry:                 e,
		SessionID:             result.SessionID,
		Status:                result.Status,
		PendingAsk:            result.PendingAsk,
		PendingEscalation:     result.PendingEscalation,
		RunningSubagentIDs:    append([]string(nil), result.RunningSubagentIDs...),
		RunningSubagentStates: cloneSubagentStates(result.RunningSubagentStates),
		RunningJobs:           cloneRunningJobs(result.RunningJobs),
		CompletedJobs:         cloneRunningJobs(result.CompletedJobs),
	}
}

// RefreshEntry confirms one freshly spawned daemon without depending on other
// rendezvous files. Publishing it makes the ordinary source and relay paths
// available for the pending mutation after a successful resume.
func (r *Roster) RefreshEntry(ctx context.Context, entry rendezvous.Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.Protocol != appwire.ProtocolVersion || entry.Endpoint == "" {
		return errors.New("spawned daemon has no current protocol endpoint")
	}
	r.mu.Lock()
	r.refreshGen++
	generation := r.refreshGen
	r.mu.Unlock()
	results := make(chan ProbeResult, 1)
	go func() {
		if r.prober == nil {
			results <- ProbeResult{OK: true, SessionID: envvars.FirstNonEmpty(entry.SessionID, entry.ThreadID)}
		} else {
			results <- r.prober.Probe(entry)
		}
	}()
	var result ProbeResult
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result = <-results:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !result.OK || result.SessionID == "" {
		return fmt.Errorf("cannot confirm spawned daemon %s", entry.SessionID)
	}
	live := liveEntryFromProbe(entry, result)
	r.mu.Lock()
	if generation < r.publishedGen || generation < r.entryPublishedGen[entry.PID] {
		r.mu.Unlock()
		return nil
	}
	previous, hadPrevious := r.bySess[live.SessionID]
	bySess, byPID := maps.Clone(r.bySess), maps.Clone(r.byPID)
	if old, ok := byPID[entry.PID]; ok && bySess[old.SessionID].PID == entry.PID {
		delete(bySess, old.SessionID)
	}
	bySess[live.SessionID], byPID[entry.PID] = live, live
	unconfirmed := make([]rendezvous.Entry, 0, len(r.unconfirmed))
	for _, claim := range r.unconfirmed {
		if claim.PID != entry.PID {
			unconfirmed = append(unconfirmed, claim)
		}
	}
	fp := rosterFingerprint(bySess)
	changed := fp != r.fingerprint || !slices.Equal(unconfirmed, r.unconfirmed)
	r.bySess, r.byPID, r.unconfirmed = bySess, byPID, unconfirmed
	r.entryPublishedGen[entry.PID] = generation
	r.fingerprint = fp
	onChange, onStatusChange := r.onChange, r.onStatusChange
	r.mu.Unlock()
	if hadPrevious && previous.Status != live.Status && onStatusChange != nil {
		onStatusChange(live.SessionID)
	}
	if changed && onChange != nil {
		onChange()
	}
	return nil
}
