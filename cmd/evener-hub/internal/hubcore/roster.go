package hubcore

import (
	"context"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
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
	SessionID         string
	Status            string   // most-recent daemon state ("active", "idle", "awaiting", etc.)
	ActiveFlags       []string // the status flags the daemon reported alongside Status
	Crashed           bool     // true only for a retained record whose daemon PID is confirmed gone
	PendingAsk        bool     // true while the daemon reports an unanswered ask_user question
	PendingEscalation bool     // true while the daemon reports a blocked sandbox-exemption escalation (M7)
	// Capabilities mirrors the daemon's own Evener capability set from the
	// probe that produced this entry, so list projections can advertise the
	// daemon's answer instead of a hand approximation (#1840's one-answer
	// rule: the same session must not read differently from ListThreads and
	// from ThreadRead). CapabilitiesKnown false means no probe read one and
	// the approximation takes over; the zero value alone is not that signal,
	// because a daemon can legitimately answer an all-false set.
	Capabilities       appwire.ThreadCapabilities
	CapabilitiesKnown  bool
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
	// Watches contains the live watches this daemon reports. The rows are this
	// session's own; a receiver watch that two sessions can see is carried by
	// each session's own diagnostics and is never aggregated across sessions,
	// so a tree rollup cannot count one watch twice. An older daemon omits the
	// source field entirely, which lands here as an empty list.
	Watches []appwire.EvenerWatchInfo
	// ChildWatches carries each listed in-process child's own live watches,
	// keyed by child session ID. A child has no LiveEntry of its own (children
	// are not independently routable), so without this a child row read its
	// watches from a zero LiveEntry and showed none. The rows are the child's
	// own, never merged across sessions, matching Watches' rollup rule. An older
	// daemon that carries no per-child watches omits the entry entirely.
	ChildWatches map[string][]appwire.EvenerWatchInfo
	Project      identifier.Project // canonical identity resolved at hub ingestion, when available
	// Lifecycle is the daemon's retirement lifecycle as of the probe that
	// produced this entry; nil when the daemon could not answer
	// evener/daemon/status (capability unknown). A failed lifecycle probe
	// clears capability, never process ownership.
	Lifecycle *appwire.DaemonLifecycle
	// LifecycleFresh reports whether Lifecycle (or its absence) came from a
	// daemon/status answer in that same probe.
	LifecycleFresh bool
}

// ProbeResult is the dynamic session state returned by a daemon liveness probe.
type ProbeResult struct {
	SessionID         string
	Status            string
	ActiveFlags       []string
	PendingAsk        bool
	PendingEscalation bool
	// Capabilities is the daemon's own Evener capability set from the same
	// projection cut as Status. CapabilitiesKnown reports whether this probe
	// read one: a failed, protocol-mismatched, or legacy probe leaves the set
	// absent, and consumers fall back to their approximation — never to an
	// empty set they would mistake for the daemon's answer.
	Capabilities          appwire.ThreadCapabilities
	CapabilitiesKnown     bool
	RunningSubagentIDs    []string
	RunningSubagentStates map[string]string
	RunningJobs           []appwire.EvenerJobInfo
	CompletedJobs         []appwire.EvenerJobInfo
	// Lifecycle mirrors LiveEntry.Lifecycle: the retirement lifecycle from
	// this probe, nil when the daemon could not answer daemon/status.
	Lifecycle *appwire.DaemonLifecycle
	// LifecycleFresh reports whether daemon/status answered in this probe.
	LifecycleFresh bool
	Watches        []appwire.EvenerWatchInfo
	ChildWatches   map[string][]appwire.EvenerWatchInfo
	// ProtocolMismatch: the endpoint answered, but as a daemon this hub cannot
	// talk to (restart required). Such an answer names no session of its own,
	// so it does not vouch for the entry's PID the way a bound answer does.
	ProtocolMismatch bool
	OK               bool
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

func cloneWatches(in []appwire.EvenerWatchInfo) []appwire.EvenerWatchInfo {
	return appwire.CloneEvenerWatches(in)
}

func cloneChildWatches(in map[string][]appwire.EvenerWatchInfo) map[string][]appwire.EvenerWatchInfo {
	if in == nil {
		return nil
	}
	out := make(map[string][]appwire.EvenerWatchInfo, len(in))
	for childID, watches := range in {
		out[childID] = appwire.CloneEvenerWatches(watches)
	}
	return out
}

func cloneLiveEntry(in LiveEntry) LiveEntry {
	out := in
	out.ActiveFlags = append([]string(nil), in.ActiveFlags...)
	out.RunningSubagentIDs = append([]string(nil), in.RunningSubagentIDs...)
	out.RunningSubagentStates = cloneSubagentStates(in.RunningSubagentStates)
	out.RunningJobs = cloneRunningJobs(in.RunningJobs)
	out.CompletedJobs = cloneRunningJobs(in.CompletedJobs)
	out.Lifecycle = cloneDaemonLifecycle(in.Lifecycle)
	out.Watches = cloneWatches(in.Watches)
	out.ChildWatches = cloneChildWatches(in.ChildWatches)
	return out
}

// cloneDaemonLifecycle deep-copies a lifecycle snapshot (including its blocker
// list) so a roster hand-off can never alias the probe's copy; nil stays nil
// so "capability unknown" survives intact.
func cloneDaemonLifecycle(in *appwire.DaemonLifecycle) *appwire.DaemonLifecycle {
	if in == nil {
		return nil
	}
	out := *in
	out.Blockers = append([]appwire.DaemonBlocker(nil), in.Blockers...)
	return &out
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

	mu           sync.RWMutex
	bySess       map[string]LiveEntry // session_id -> entry
	byPID        map[int]LiveEntry    // pid -> entry (for fsnotify event correlation)
	unconfirmed  []rendezvous.Entry   // live PIDs whose daemon ownership has not been established
	ownershipErr error
	// A completed pass may publish unless a newer pass already published.
	refreshGen              uint64
	publishedGen            uint64
	entryPublishedGen       map[int]uint64
	ownershipRefreshRunning bool
	queuedOwnershipRefresh  *rosterRefreshBatch

	// procIdentity is what the host says about the process a rendezvous
	// entry names: gone or verifiably another process (NotOwner, the crashed
	// path), the daemon itself, or nothing it can vouch for. Consulted only
	// when the probe did not answer for the entry's own session.
	procIdentity func(rendezvous.Entry) ProcessIdentity

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
	// onSessionGone, when set via SetOnSessionGone, is fired by Refresh for a
	// session that left the listing for good: its daemon's process is gone
	// (the crashed path) or its rendezvous file is (a clean exit). Never for
	// a claim parked unresolved - that daemon may still be there.
	onSessionGone func(gone LiveEntry)
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
		procIdentity:      processIdentity,
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

// SetProcessAlive overrides process liveness alone: a PID the probe calls dead
// is NotOwner, one it calls alive is Unknown. Tests with synthetic rendezvous
// claims must not depend on whether their PIDs exist on the host.
func (r *Roster) SetProcessAlive(alive func(pid int) bool) *Roster {
	return r.SetProcessIdentity(func(entry rendezvous.Entry) ProcessIdentity {
		if !alive(entry.PID) {
			return ProcessNotOwner
		}
		return ProcessIdentityUnknown
	})
}

// ProcessIdentity is what the host can say about the process behind a
// rendezvous entry whose daemon stopped answering.
//
// Why the roster asks. A daemon that crashed leaves its rendezvous file, and
// the kernel may hand its PID to anything. To liveness (signal 0) that reuse
// looks exactly like a busy daemon missing one probe, so a confirmed entry
// would stay listed for as long as the unrelated process lived, its relay
// would keep dialling a dead endpoint, and the session's subscribers would
// never be told the daemon is gone. The probe tells the two apart through the
// same verifier force-stop binds to (daemonprocess.Identify): only positive
// evidence - the process is gone, or its owner or earliest possible start
// cannot be the daemon's - counts against the entry; a busy daemon that is
// still itself, an entry without the fields verification needs, and a host
// that cannot inspect all keep the route exactly as liveness alone would.
type ProcessIdentity int

const (
	// ProcessIdentityUnknown: the host cannot verify ownership (platform
	// without generation-bound process inspection, or an entry from a daemon
	// that recorded no state directory or start time). Liveness alone decides,
	// as it always has.
	ProcessIdentityUnknown ProcessIdentity = iota
	// ProcessOwnsEntry: the live process is the daemon that wrote the entry.
	ProcessOwnsEntry
	// ProcessNotOwner: the PID is alive but belongs to something else, or the
	// daemon has exited.
	ProcessNotOwner
)

// SetProcessIdentity overrides the process-identity probe, for the same reason
// SetProcessAlive exists.
func (r *Roster) SetProcessIdentity(probe func(rendezvous.Entry) ProcessIdentity) *Roster {
	r.procIdentity = probe
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

// SetOnSessionGone registers a callback fired by Refresh, once, for each
// session whose daemon has left for good. The relay uses it to tell that
// session's subscribers to re-read: the daemon's own close frame is not
// guaranteed to reach them (it may be revoked with the connection), and
// nothing else would.
func (r *Roster) SetOnSessionGone(fn func(gone LiveEntry)) { r.onSessionGone = fn }

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
		// A recovery flag raised while the status itself holds still is what
		// hides the fork action, so it has to move the fingerprint. Sorted on a
		// copy: which order a daemon happens to list its flags in is not a
		// change, and the caller's slice is not this function's to reorder.
		activeFlags := append([]string(nil), bySess[id].ActiveFlags...)
		sort.Strings(activeFlags)
		for _, flag := range activeFlags {
			_, _ = h.Write([]byte(flag))
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte{0})
		if bySess[id].Crashed {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
		if bySess[id].PendingAsk {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
		// An escalation moving while the status holds still changes the
		// Clear bit list rows advertise — the fallback folds it out the way
		// the daemon's clear gate does — so it must bump the fingerprint like
		// its sibling ask flag, or onChange never invalidates.
		if bySess[id].PendingEscalation {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
		// The daemon's capability answer is per-session observable state in
		// the same sense the status is: bits fold daemon state the status
		// string itself does not (Clear folds the clear-blocked reason, Send
		// folds activity), so a bit can flip while the status holds still.
		// Reflection walks the whole set so a capability bit added later
		// moves the fingerprint without anyone having to remember this site.
		capsV := reflect.ValueOf(bySess[id].Capabilities)
		for _, fieldValue := range capsV.Fields() {
			// Hash every field whatever its kind: Bool() would panic on a
			// future non-bool ThreadCapabilities field, and skipping a field
			// would silently drop it from the fingerprint.
			if fieldValue.Kind() != reflect.Bool {
				_, _ = fmt.Fprintf(h, "%v", fieldValue.Interface())
			} else if fieldValue.Bool() {
				_, _ = h.Write([]byte{1})
			}
			_, _ = h.Write([]byte{0})
		}
		if bySess[id].CapabilitiesKnown {
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
			// A child's watches render on the child's row, so a new watch, a
			// delivery, or a flip to inactive on that child must bump the
			// fingerprint just like the child's own state, or navigation never
			// invalidates and the child keeps a stale watch list.
			writeWatchFingerprint(h, bySess[id].ChildWatches[childID])
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
		// Lifecycle phase transitions (resident -> preparing -> retiring) and
		// blocker changes must bump the fingerprint exactly like child state:
		// they change what resident UI surfaces for this daemon.
		if bySess[id].LifecycleFresh {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
		if lifecycle := bySess[id].Lifecycle; lifecycle != nil {
			_, _ = h.Write([]byte(lifecycle.Phase))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(strconv.FormatInt(lifecycle.TimeoutMillis, 10)))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(lifecycle.EligibleSince))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(lifecycle.Deadline))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(lifecycle.Failure))
			_, _ = h.Write([]byte{0})
			for _, blocker := range lifecycle.Blockers {
				_, _ = h.Write([]byte(blocker.Category))
				_, _ = h.Write([]byte{0})
				_, _ = h.Write([]byte(blocker.SessionID))
				_, _ = h.Write([]byte{0})
				_, _ = h.Write([]byte(blocker.DelegateID))
				_, _ = h.Write([]byte{0})
			}
		}
		_, _ = h.Write([]byte{0})
		// Watches are rendered on the session row, so any change the sidebar
		// shows — a new watch, a delivery, a flip to inactive — must move the
		// fingerprint or onChange never invalidates navigation. Sorted on a
		// copy: a daemon listing its watches in another order is not a change.
		writeWatchFingerprint(h, bySess[id].Watches)
	}
	return h.Sum64()
}

// writeWatchFingerprint folds one session's watch inventory into the roster
// hash. Sorted on a copy: a daemon listing its watches in another order is not a
// change. The same routine hashes a root's own Watches and each child's
// ChildWatches, so the two can never cover different fields.
func writeWatchFingerprint(h hash.Hash64, watches []appwire.EvenerWatchInfo) {
	ordered := append([]appwire.EvenerWatchInfo(nil), watches...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, watch := range ordered {
		for _, field := range []string{
			watch.ID, watch.Source, watch.Target, watch.SendTo, watch.Note,
			watch.OutputMatch, watch.CreatedAt, watch.EndReason,
		} {
			_, _ = h.Write([]byte(field))
			_, _ = h.Write([]byte{0})
		}
		if watch.WildcardEvents {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
		if watch.Active {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strconv.Itoa(watch.Deliveries)))
		_, _ = h.Write([]byte{0})
		for _, cadence := range watch.Cadence {
			_, _ = h.Write([]byte(cadence.Kind))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(strconv.FormatFloat(cadence.Seconds, 'g', -1, 64)))
			_, _ = h.Write([]byte{0})
			// The derived next-fire instant changes only when the ring or the
			// interval changes (both already hashed above), but hashing it here
			// keeps the field explicitly covered if its derivation ever moves.
			_, _ = h.Write([]byte(cadence.DerivedNextFireAt))
			_, _ = h.Write([]byte{0})
			// Every throttles an event watch and Filter narrows what it
			// matches, so either changing must move the fingerprint or the
			// sidebar keeps a row whose cadence no longer matches. Null
			// separators keep the field boundary unambiguous.
			_, _ = h.Write([]byte(strconv.Itoa(cadence.Every)))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(cadence.Filter))
			_, _ = h.Write([]byte{0})
		}
		for _, event := range watch.Events {
			_, _ = h.Write([]byte(event))
			_, _ = h.Write([]byte{0})
		}
		// DeliveryTimes feeds the activity panel's timeline, and the
		// daemon-restore case rebuilds the ring empty. A delivery-only change
		// (same count, new instants) must still move the fingerprint or that
		// timeline never invalidates.
		for _, at := range watch.DeliveryTimes {
			_, _ = h.Write([]byte(at))
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte{0})
	}
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
			r.recordOwnershipError(generation, err)
			return err
		}
	}

	// Snapshot the previous PID map for the keep-alive fallback. Reading
	// it under a brief lock (rather than holding the lock across the
	// probes) keeps List() responsive while a slow probe pass runs.
	r.mu.RLock()
	prevByPID := r.byPID
	previousUnconfirmed := slices.Clone(r.unconfirmed)
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
	retainUnconfirmed := func(entry rendezvous.Entry) {
		if !slices.Contains(unconfirmed, entry) {
			unconfirmed = append(unconfirmed, entry)
		}
	}
	for _, res := range results {
		e := res.entry
		// An answering probe is bound to the entry's session (StatusProber)
		// and vouches for it; a protocol-mismatch answer names no session and
		// vouches for nothing. Otherwise the process behind the PID is asked
		// once per entry per refresh (see ProcessIdentity for why): gone or
		// verifiably another process takes the crashed path, never the
		// unconfirmed one. A roster without a prober asks nothing.
		identity := ProcessIdentityUnknown
		if r.prober != nil && (!res.OK || res.ProtocolMismatch) {
			identity = r.procIdentity(e)
		}
		if identity == ProcessNotOwner {
			res.OK = false
		}
		if !res.OK {
			alive := identity != ProcessNotOwner
			if alive {
				for _, claim := range previousUnconfirmed {
					if claim.PID == e.PID {
						retainUnconfirmed(claim)
					}
				}
			}
			// A transient probe miss preserves a route only while the complete
			// rendezvous identity is unchanged. PID liveness cannot confirm a
			// replacement's ownership of either the old or the new session.
			//
			// Nor can it tell a busy daemon from a PID the kernel reused after
			// the daemon crashed: both miss the probe, both answer signal 0,
			// and the crashed daemon's file is byte-identical. The identity
			// verdict taken above folds into `alive`, so a PID verified not
			// to be the daemon's never reaches this branch: it takes the
			// crashed path below and leaves the listing, which is what lets
			// the relay announce the daemon gone.
			if prev, had := prevByPID[e.PID]; had && alive {
				if !sameDaemonIdentity(prev.Entry, e) {
					retainUnconfirmed(prev.Entry)
					resolved := prev.Entry
					resolved.SessionID = prev.SessionID
					retainUnconfirmed(resolved)
					retainUnconfirmed(e)
					continue
				}
				// The exact identity still matches, so keep the last-known route
				// and status. The probe itself failed, though, so this reused entry
				// carries no fresh lifecycle observation: clear the capability and
				// mark it stale. Stale lifecycle data must never present as current
				// eligibility, and a stale row must not offer a retire action.
				prev.Lifecycle = nil
				prev.LifecycleFresh = false
				byPID[e.PID] = prev
				if prev.SessionID != "" {
					if current, ok := bySess[prev.SessionID]; !ok || preferLiveEntry(prev, current) {
						bySess[prev.SessionID] = prev
					}
				}
				continue
			}
			if alive {
				retainUnconfirmed(e)
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
				// just survives until a later refresh. Removal is exact-owned:
				// if a replacement daemon has since reused this PID and
				// rewritten the file, it is not ours to unlink, so the guard's
				// refusal is a normal no-op rather than an error to surface.
				_ = rendezvous.RemoveIfOwned(r.runDir, e)
				continue
			}
			if time.Since(e.StartedAt) > crashedFileRetention {
				// Old enough that crash reporting no longer matters: drop the
				// entry and unlink the file so dead-pid rendezvous files stop
				// accumulating forever. Fresh crashes keep the retained
				// "errored" contract below untouched.
				_ = rendezvous.RemoveIfOwned(r.runDir, e)
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
			if current, ok := bySess[sessionID]; !ok || preferLiveEntry(crashed, current) {
				bySess[sessionID] = crashed
			}
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
	}
	r.publishedGen = generation
	ownershipCleared := r.ownershipErr != nil
	r.ownershipErr = nil
	publication := r.publishListingLocked(bySess, byPID, unconfirmed)
	publication.changed = publication.changed || ownershipCleared
	r.mu.Unlock()
	publication.fire()
	return nil
}

// listingPublication is one swap of the listing and what it owes the hooks:
// which sessions changed status, which left, and whether anything observable
// moved at all.
type listingPublication struct {
	changed        bool
	statusChanges  []string
	gone           []LiveEntry
	onStatusChange func(sessionID string)
	onSessionGone  func(gone LiveEntry)
	onChange       func()
}

// publishListingLocked replaces the listing and accounts for it against the
// publication immediately before, under r.mu (the caller holds it, and fires
// the result after unlocking). It is the one place a session can leave the
// listing, however the publication came about - a full refresh or a single
// spawned daemon's confirmation - so a departure is announced from here and
// nowhere else, and measured against the state actually being replaced:
// two publications that overlapped each snapshot the same earlier state, and
// would otherwise both announce one session gone.
func (r *Roster) publishListingLocked(bySess map[string]LiveEntry, byPID map[int]LiveEntry, unconfirmed []rendezvous.Entry) listingPublication {
	prevBySess, prevUnconfirmed := r.bySess, r.unconfirmed
	fp := rosterFingerprint(bySess)
	publication := listingPublication{
		changed:        fp != r.fingerprint || !slices.Equal(prevUnconfirmed, unconfirmed),
		gone:           sessionsGone(prevBySess, prevUnconfirmed, bySess, unconfirmed),
		onStatusChange: r.onStatusChange,
		onSessionGone:  r.onSessionGone,
		onChange:       r.onChange,
	}
	for id, cur := range bySess {
		if prev, had := prevBySess[id]; had && prev.Status != cur.Status {
			publication.statusChanges = append(publication.statusChanges, id)
		}
	}
	sort.Strings(publication.statusChanges)
	r.bySess, r.byPID, r.unconfirmed, r.fingerprint = bySess, byPID, unconfirmed, fp
	return publication
}

// fire runs the hooks a publication owes, outside the lock.
func (p listingPublication) fire() {
	if p.onStatusChange != nil {
		for _, id := range p.statusChanges {
			p.onStatusChange(id)
		}
	}
	if p.onSessionGone != nil {
		for _, entry := range p.gone {
			p.onSessionGone(entry)
		}
	}
	if p.changed && p.onChange != nil {
		p.onChange()
	}
}

// sessionsGone is every session the previous publication still held - listed
// live, or parked as an unresolved claim - that this one holds as neither:
// crashed (its process gone) or absent (its file gone). A session whose claim
// is parked unresolved now is not gone - a probe was missed while its process
// answers - and a session already crashed was announced when it crashed.
func sessionsGone(prev map[string]LiveEntry, prevUnconfirmed []rendezvous.Entry, cur map[string]LiveEntry, unconfirmed []rendezvous.Entry) []LiveEntry {
	named := func(claims []rendezvous.Entry, id string) bool {
		return slices.ContainsFunc(claims, func(claim rendezvous.Entry) bool {
			return claim.SessionID == id || claim.ThreadID == id
		})
	}
	held := map[string]LiveEntry{}
	for id, was := range prev {
		if !was.Crashed {
			held[id] = was
		}
	}
	for _, claim := range prevUnconfirmed {
		id := envvars.FirstNonEmpty(claim.SessionID, claim.ThreadID)
		if _, ok := held[id]; id != "" && !ok {
			held[id] = LiveEntry{Entry: claim, SessionID: id}
		}
	}
	var gone []LiveEntry
	for id, was := range held {
		if now, listed := cur[id]; listed && !now.Crashed {
			continue
		}
		if named(unconfirmed, id) {
			continue
		}
		gone = append(gone, cloneLiveEntry(was))
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].SessionID < gone[j].SessionID })
	return gone
}

// OwnershipError reports an incomplete full scan. Individual daemon
// confirmations cannot establish that the remaining ownership claims are absent.
func (r *Roster) OwnershipError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ownershipErr
}

// DaemonOwnershipAbsent reports whether the roster excludes every possible
// daemon owner, including unresolved claims. This permits retained sessions
// with deleted ancestry to recover without guessing which daemon owns them.
func (r *Roster) DaemonOwnershipAbsent() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.ownershipErr != nil || len(r.unconfirmed) != 0 {
		return false
	}
	for _, entry := range r.byPID {
		if !entry.Crashed {
			return false
		}
	}
	return true
}

func (r *Roster) recordOwnershipError(generation uint64, err error) {
	r.mu.Lock()
	if generation < r.publishedGen {
		r.mu.Unlock()
		return
	}
	changed := r.ownershipErr == nil || r.ownershipErr.Error() != err.Error()
	r.publishedGen = generation
	r.ownershipErr = err
	onChange := r.onChange
	r.mu.Unlock()
	if changed && onChange != nil {
		onChange()
	}
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
	return r.listLocked()
}

// RosterSnapshot is one publication's answer to the questions a navigation
// read asks together, taken under one lock so the three cannot describe
// different moments.
type RosterSnapshot struct {
	OwnershipError error
	Live           []LiveEntry
	Unconfirmed    []rendezvous.Entry
}

// Snapshot is OwnershipError, List and UnconfirmedEntries from one publication.
func (r *Roster) Snapshot() RosterSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return RosterSnapshot{OwnershipError: r.ownershipErr, Live: r.listLocked(), Unconfirmed: slices.Clone(r.unconfirmed)}
}

func (r *Roster) listLocked() []LiveEntry {
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
//
// A crash-retained record keeps the child list its daemon reported before the
// process died, and that daemon runs nothing any more. Skipping those entries
// is what lets a stopped persisted delegate stop reading as daemon-owned the
// moment its parent dies, rather than when crash retention expires.
func (r *Roster) SubagentState(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.byPID {
		if entry.Crashed {
			continue
		}
		if slices.Contains(entry.RunningSubagentIDs, sessionID) {
			return entry.RunningSubagentStates[sessionID], true
		}
	}
	return "", false
}

func preferLiveEntry(candidate, current LiveEntry) bool {
	if candidate.Crashed != current.Crashed {
		return !candidate.Crashed
	}
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

// sameDaemonIdentity compares the process identity of two rendezvous entries
// through the single exact-ownership authority, rendezvous.OwnershipFingerprint.
// A hand-picked field list drifts from that authority (the previous copy here
// omitted WorkingDir and StateDir while including HubToken), so the roster
// would confirm or preserve the wrong owner after an identity change.
func sameDaemonIdentity(a, b rendezvous.Entry) bool {
	return rendezvous.OwnershipFingerprint(a) == rendezvous.OwnershipFingerprint(b)
}

// HasConfirmedEntry reports whether the exact daemon identity has a live route.
func (r *Roster) HasConfirmedEntry(entry rendezvous.Entry) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hasConfirmedEntry(entry)
}

func (r *Roster) hasConfirmedEntry(entry rendezvous.Entry) bool {
	live, ok := r.byPID[entry.PID]
	if !ok {
		// No live daemon holds this PID: there is no session key to route by,
		// and indexing bySess with the zero-value LiveEntry's empty SessionID
		// would look up an unrelated key instead of failing fast.
		return false
	}
	routed, found := r.bySess[live.SessionID]
	return found && !live.Crashed && routed.PID == entry.PID && sameDaemonIdentity(live.Entry, entry)
}

// RestartRequiredRootRef resolves metadata-only admission from one roster
// snapshot. It never reads persisted ancestry or probes daemon endpoints.
func (r *Roster) RestartRequiredRootRef(rawRef string) (string, bool) {
	ref, err := appwire.ParseRef(rawRef)
	if err != nil || ref.SourceID != "local" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.ownershipErr != nil || len(r.unconfirmed) != 0 {
		return "", false
	}
	aliases := func(entry LiveEntry) []string {
		refs := []string{entry.WorkspaceRef}
		for _, id := range []string{entry.SessionID, entry.Entry.SessionID, entry.ThreadID} {
			if id != "" {
				refs = append(refs, appwire.Ref{SourceID: "local", ThreadID: id}.String())
			}
		}
		return refs
	}
	var owner LiveEntry
	found := false
	for _, entry := range r.byPID {
		if entry.Crashed || (entry.SourceID != "" && entry.SourceID != "local") {
			continue
		}
		ids := aliases(entry)
		if !slices.Contains(ids, rawRef) {
			continue
		}
		if found || entry.Status != appwire.ThreadStatusRestartRequired {
			return "", false
		}
		owner, found = entry, true
	}
	if !found {
		return "", false
	}
	ownerAliases := aliases(owner)
	for _, entry := range r.byPID {
		if entry.PID == owner.PID || entry.Crashed {
			continue
		}
		for _, alias := range aliases(entry) {
			if alias != "" && slices.Contains(ownerAliases, alias) {
				return "", false
			}
		}
	}
	if workspace, err := appwire.ParseRef(owner.WorkspaceRef); err == nil && workspace.SourceID == "local" {
		return owner.WorkspaceRef, true
	}
	return appwire.Ref{SourceID: "local", ThreadID: owner.SessionID}.String(), true
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

// ResidentEntry is one discovered daemon process identity: the rendezvous
// claim plus, when established, the roster's confirmed view of that same
// process. Confirmed is nil for claims whose process is alive but whose
// ownership could not be verified. A Confirmed entry with Crashed set is
// retained crash evidence, not a resident; the caller decides whether dead
// markers belong in its view.
type ResidentEntry struct {
	Entry     rendezvous.Entry
	Confirmed *LiveEntry
}

// ResidentEntries snapshots every discovered process identity under the
// roster lock: confirmed entries (sorted by PID for determinism) followed by
// unconfirmed claims. It performs no OS process scan and no archive
// filtering, and it clones every slice so callers cannot mutate roster state.
func (r *Roster) ResidentEntries() []ResidentEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ResidentEntry, 0, len(r.byPID)+len(r.unconfirmed))
	pids := make([]int, 0, len(r.byPID))
	for pid := range r.byPID {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		live := cloneLiveEntry(r.byPID[pid])
		out = append(out, ResidentEntry{Entry: live.Entry, Confirmed: &live})
	}
	for _, claim := range r.unconfirmed {
		out = append(out, ResidentEntry{Entry: claim})
	}
	return out
}

func liveEntryFromProbe(e rendezvous.Entry, result ProbeResult) LiveEntry {
	return LiveEntry{
		Entry:                 e,
		SessionID:             result.SessionID,
		Status:                result.Status,
		ActiveFlags:           append([]string(nil), result.ActiveFlags...),
		PendingAsk:            result.PendingAsk,
		PendingEscalation:     result.PendingEscalation,
		Capabilities:          result.Capabilities,
		CapabilitiesKnown:     result.CapabilitiesKnown,
		RunningSubagentIDs:    append([]string(nil), result.RunningSubagentIDs...),
		RunningSubagentStates: cloneSubagentStates(result.RunningSubagentStates),
		RunningJobs:           cloneRunningJobs(result.RunningJobs),
		CompletedJobs:         cloneRunningJobs(result.CompletedJobs),
		Lifecycle:             cloneDaemonLifecycle(result.Lifecycle),
		LifecycleFresh:        result.LifecycleFresh,
		Watches:               cloneWatches(result.Watches),
		ChildWatches:          cloneChildWatches(result.ChildWatches),
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
	return r.publishConfirmedEntry(entry, result, generation)
}

// ReadSpawnedThread confirms a fresh endpoint through the caller's direct read.
// Initial delivery does not depend on collecting the full status inventory.
func (r *Roster) ReadSpawnedThread(ctx context.Context, entry rendezvous.Entry, read func(context.Context) (appwire.ThreadReadResponse, error)) (appwire.ThreadReadResponse, error) {
	r.mu.Lock()
	r.refreshGen++
	generation := r.refreshGen
	r.mu.Unlock()
	response, err := read(ctx)
	if err != nil {
		return response, err
	}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	root := response.Thread
	if entry.Protocol != appwire.ProtocolVersion || entry.Endpoint == "" || root.ID != entry.ThreadID || statusThreadID(root) == "" || (entry.SessionID != "" && statusThreadID(root) != entry.SessionID) {
		return response, errors.New("spawned daemon read did not confirm its identity")
	}
	runningJobs, completedJobs := splitNonAgentJobs(root.Evener.Diagnostics)
	result := ProbeResult{OK: true, SessionID: statusThreadID(root), Status: root.Status.Type,
		ActiveFlags: append([]string(nil), root.Status.ActiveFlags...),
		PendingAsk:  root.Evener.AskPending, PendingEscalation: len(root.Evener.PendingEscalations) > 0,
		RunningJobs: runningJobs, CompletedJobs: completedJobs,
		Watches: diagnosticsWatches(root.Evener.Diagnostics),
		// The identity checks above already require a current-protocol daemon,
		// and every current daemon stamps its capability set on the thread
		// projection this read answered from, so the caps beside the status
		// are the daemon's own answer — not an approximation.
		Capabilities: root.Evener.Capabilities, CapabilitiesKnown: true}
	if root.Evener.Diagnostics != nil {
		result.RunningSubagentStates = make(map[string]string)
		for _, delegate := range root.Evener.Diagnostics.Delegates {
			if delegate.ChildSessionID == "" || delegate.Lifecycle == "closed" {
				continue
			}
			state := ""
			switch delegate.Lifecycle {
			case "idle":
				state = appwire.ThreadStatusIdle
			case "running":
				state = appwire.ThreadStatusActive
				if delegate.NeedsAttention {
					state = appwire.ThreadStatusAwaiting
				}
			}
			result.RunningSubagentStates[delegate.ChildSessionID] = state
		}
		for childID := range result.RunningSubagentStates {
			result.RunningSubagentIDs = append(result.RunningSubagentIDs, childID)
		}
		sort.Strings(result.RunningSubagentIDs)
	}
	return response, r.publishConfirmedEntry(entry, result, generation)
}

func (r *Roster) publishConfirmedEntry(entry rendezvous.Entry, result ProbeResult, generation uint64) error {
	live := liveEntryFromProbe(entry, result)
	r.mu.Lock()
	if generation < r.publishedGen || generation < r.entryPublishedGen[entry.PID] {
		confirmed := r.hasConfirmedEntry(entry)
		r.mu.Unlock()
		if !confirmed {
			return fmt.Errorf("spawned daemon %s confirmation superseded without a route", live.SessionID)
		}
		return nil
	}
	bySess, byPID := maps.Clone(r.bySess), maps.Clone(r.byPID)
	if old, ok := byPID[entry.PID]; ok && bySess[old.SessionID].PID == entry.PID {
		// A prior session on the same PID leaves the listing here, and
		// publishListingLocked announces it exactly as a refresh would.
		delete(bySess, old.SessionID)
	}
	bySess[live.SessionID], byPID[entry.PID] = live, live
	unconfirmed := make([]rendezvous.Entry, 0, len(r.unconfirmed))
	for _, claim := range r.unconfirmed {
		if claim.PID != entry.PID {
			unconfirmed = append(unconfirmed, claim)
		}
	}
	publication := r.publishListingLocked(bySess, byPID, unconfirmed)
	r.entryPublishedGen[entry.PID] = generation
	r.mu.Unlock()
	publication.fire()
	return nil
}
