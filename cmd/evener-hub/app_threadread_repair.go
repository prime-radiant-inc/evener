package hub

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/transcriptindex"
)

// transcriptRepairBudget is how many consecutive failed repairs of one
// unchanged transcript the registry counts before reads answer
// history-failed instead of arming another, mirroring the daemon's
// threadHistoryMaxRebuilds (server/thread_history.go).
const transcriptRepairBudget = 3

// transcriptRepairLaneExhaustedInterval paces a refresher-fed re-arm of a
// path whose repair budget is exhausted: the lane skips the path until this
// much time has passed since its last refresher re-arm, so a
// deterministically failing path cannot monopolize the single lane cycle
// after cycle. A variable only so tests can shrink it.
var transcriptRepairLaneExhaustedInterval = 10 * time.Minute

// transcriptIndexRepairNow is the registry's clock, so pacing tests advance
// it explicitly instead of sleeping past a real interval: a scheduling pause
// must never decide whether an ask is inside or past one. Defaults to the
// real clock; a variable for tests.
var transcriptIndexRepairNow = time.Now

// transcriptIndexRepairLogSink is the repair registry's log destination,
// resolved when the registry is armed — hubLogfFor's rule: a detached
// repair outlives the startup and shutdown windows where os.Stderr gets
// redirected, so reading the mutable global per line would follow a late
// reassign (captureHubStderr, issue #2783) into an unintended or closed
// sink. runMain arms before any redirect runs, so the captured destination
// is the one the hub's own startup chose.
var transcriptIndexRepairLogSink = os.Stderr

// transcriptIndexRepairLog is the repair registry's advisory log line, in the
// established [hub] style, naming the session's transcript. A package
// variable so tests can capture the lines.
var transcriptIndexRepairLog = func(format string, args ...any) {
	_, _ = fmt.Fprintf(transcriptIndexRepairLogSink, "[hub] "+format+"\n", args...)
}

// runTranscriptIndexRepair is the repair recipe the registry runs: rebuild
// the sidecar from the transcript, off the request path, through the
// exported Rebuild — the one place a full rebuild legitimately runs on the
// hub — revalidated against the refused build the registration carried, so
// a registration that lands after another repair already committed skips
// its rebuild. A package variable so tests can hold a repair open, fail it,
// or observe it.
var runTranscriptIndexRepair = repairTranscriptIndex

// transcriptIndexRepairRecorded, when set, runs after one armed repair's
// outcome is recorded into the registry and its log lines are written —
// after run's whole critical section, never inside it. A package variable
// so the tests' seam can signal completion exactly when the registry's
// state has settled: a signal from inside the recipe itself would fire
// before the recording and let a test assert against a registry that has
// not decided yet.
var transcriptIndexRepairRecorded func(path string)

// transcriptIndexRepairPreOpen, when set, runs between the recipe's
// transcript stat and its sidecar open — the window where a concurrent
// deletion can remove the transcript and its sidecar directory. A package
// variable so tests can inject that deletion deterministically; nil in
// production.
var transcriptIndexRepairPreOpen func(path string)

// repairTranscriptIndex rebuilds path's transcript index sidecar from the
// transcript — the authority — under the blocking exclusive lock, in the
// caller's goroutine, revalidated against refusedBuild: the name of the live
// build the registration's refusal stood on, so a repair armed late — after
// another reader's repair already committed a fresh build — adopts that
// outcome instead of rebuilding the healthy sidecar again, re-paying the full
// cost and rotating an incarnation readers hold. It stats the transcript
// first and aborts without creating anything when it is already gone. The
// stat narrows but cannot close the race with a concurrent deletion: the
// open's MkdirAll and lock-file creation can still land after a deletion
// removed the transcript and its sidecar directory, resurrecting them. Both
// outcome paths close it instead — a rebuild that fails while the
// transcript is gone removes the sidecar directory, finishing deletion's
// contract, and so does a revalidation no-op, whose resurrected open left
// the directory behind the same way; with no transcript at the path no live
// session can own the directory, and a transcript that exists again is a
// new session's, left entirely to it. It opens an ordinary handle
// without catching up, so this rebuild is the one that runs — an open
// with a built-in catch-up would either duplicate the rebuild for identity
// triggers or no-op for corruption. It must not use the hub's cached
// handles: they carry the refusing policy, which would refuse this rebuild
// too.
func repairTranscriptIndex(transcriptPath, refusedBuild string) error {
	info, err := os.Stat(transcriptPath)
	if err != nil {
		return err
	}
	if transcriptIndexRepairPreOpen != nil {
		transcriptIndexRepairPreOpen(transcriptPath)
	}
	dir := transcriptindex.DirFor(transcriptPath)
	index, err := transcriptindex.OpenWithoutCatchUp(transcriptPath, dir)
	if err != nil {
		return err
	}
	defer index.Close() //nolint:errcheck // the rebuild has committed or not; close is hygiene
	// finishDeletion removes the sidecar directory a resurrecting open
	// recreated after a deletion won the race the recipe's first stat only
	// narrows, closing the lock file first: on Windows the still-open lock
	// file keeps the directory from being removed, and the deferred Close
	// tolerates this second, idempotent call. A cleanup failure is logged,
	// never hidden — the registry's own log carries it the way it carries
	// repairs.
	finishDeletion := func() {
		_ = index.Close()
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			transcriptIndexRepairLog("transcript index repair cleanup failed (%s): %v",
				filepath.Base(transcriptPath), rmErr)
		}
	}
	// The deletion contract holds on both outcomes: a rebuild that fails
	// while the transcript is gone removes the sidecar directory, finishing
	// the deletion the race interrupted — and so does a revalidation no-op,
	// whose resurrected open left the directory behind the same way. With no
	// transcript at the path no live session can own the directory, and a
	// transcript that exists again is a new session's, left entirely to it.
	if err := index.RebuildIfStillRefused(info.Size(), refusedBuild); err != nil {
		if _, serr := os.Stat(transcriptPath); os.IsNotExist(serr) {
			finishDeletion()
		}
		return err
	}
	if _, serr := os.Stat(transcriptPath); os.IsNotExist(serr) {
		finishDeletion()
		return serr
	}
	return nil
}

// transcriptIndexRepairEntry is the registry's record for one transcript
// path: whether a repair is in flight, and the consecutive-failure budget
// keyed by the transcript's (size, mtime) stamp, so a transcript that
// changed since the failures starts the count over.
type transcriptIndexRepairEntry struct {
	inFlight bool
	failures int
	// stamp is the transcript stamp the failures were counted under; nil
	// until a repair fails.
	stamp *hubcore.TranscriptStamp
	// lastLaneRearm is when the refresher lane last re-armed this path while
	// its budget was exhausted.
	lastLaneRearm time.Time
}

// transcriptIndexRepairRegistry owns the hub's background repairs of
// transcript index sidecars: at most one repair per transcript path at a
// time, started detached — the hub's background group is awaited at
// shutdown, and a multi-minute rebuild must not hold hub exit — and only
// while the hub's background context is live. Registrants never block: a
// read that finds the sidecar needing a rebuild registers or joins the
// path's single repair and answers immediately.
type transcriptIndexRepairRegistry struct {
	mu sync.Mutex
	// ctx is the hub's background context; nil until wire runs, and until
	// then nothing arms.
	ctx context.Context
	// entries is the registry's records, keyed by transcript path: the
	// in-flight repairs and the paths carrying a failure budget. A
	// successfully repaired path leaves no entry; a deleted one loses its
	// entry, and a completion landing on the dropped entry records nothing.
	entries map[string]*transcriptIndexRepairEntry
	// laneBusy marks the refresher lane's single slot: at most one
	// refresher-fed repair runs hub-wide, so a fleet-wide staleness (a
	// schema-fingerprint change) trickles in rather than bursting.
	laneBusy bool
	// laneQueue holds the refresher asks that landed while the lane was
	// busy, one per path, taken FIFO as each lane repair retires: dropping
	// them instead would let an early session that goes stale again every
	// cycle take the lane each time — its successful repairs delete its
	// entry, so the exhaustion pacing never applies to it — and the
	// sessions behind it stay unindexed indefinitely.
	laneQueue []transcriptIndexLaneAsk
}

// transcriptIndexLaneAsk is one refresher ask retained while the lane was
// busy: the path asking, and the refused build its refusal named — the
// identity the armed repair revalidates against.
type transcriptIndexLaneAsk struct {
	path    string
	refused string
}

var transcriptIndexRepairs = newTranscriptIndexRepairRegistry()

func newTranscriptIndexRepairRegistry() *transcriptIndexRepairRegistry {
	return &transcriptIndexRepairRegistry{entries: map[string]*transcriptIndexRepairEntry{}}
}

// wire arms the registry with the hub's background context: repairs start
// only while it is live, so shutdown abandons an in-flight repair exactly as
// a crash would, which the sidecar's commit rule makes harmless. The repair
// log's sink resolves here too, once per arming.
func (r *transcriptIndexRepairRegistry) wire(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctx = ctx
	transcriptIndexRepairLogSink = os.Stderr
}

// armedLocked reports whether a repair may start. Callers hold r.mu.
func (r *transcriptIndexRepairRegistry) armedLocked() bool {
	return r.ctx != nil && r.ctx.Err() == nil
}

// statLocked reads the transcript's (size, mtime) stamp — the failure
// budget's key — for a path the registry holds an entry for, so healthy
// reads never pay the stat. Callers hold r.mu.
func (r *transcriptIndexRepairRegistry) statLocked(path string) (hubcore.TranscriptStamp, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return hubcore.TranscriptStamp{}, false
	}
	return hubcore.TranscriptStamp{Size: info.Size(), ModTimeNS: info.ModTime().UnixNano()}, true
}

// exhaustedLocked reports whether path's repair budget is exhausted for the
// transcript's current stamp. Callers hold r.mu.
func (r *transcriptIndexRepairRegistry) exhaustedLocked(path string, e *transcriptIndexRepairEntry) bool {
	if e.stamp == nil || e.failures < transcriptRepairBudget {
		return false
	}
	stamp, ok := r.statLocked(path)
	return ok && stamp == *e.stamp
}

// answerRead answers a read whose refusing handle refused a rebuild of
// path's sidecar — refused naming the live build the refusal stood on, the
// identity the armed repair revalidates against. It arms the path's single
// repair when none is in flight (never blocking the caller) and reports
// whether a repair is armed, and whether the path's budget is exhausted for
// the transcript's current stamp — the re-arming read answers history-failed
// itself, while subsequent reads during the re-armed repair answer
// rebuilding.
func (r *transcriptIndexRepairRegistry) answerRead(path, refused string) (armed, exhausted bool) {
	base := filepath.Base(path)
	r.mu.Lock()
	if e := r.entries[path]; e != nil && e.inFlight {
		r.mu.Unlock()
		return true, false
	}
	if !r.armedLocked() {
		r.mu.Unlock()
		return false, false
	}
	e := r.entries[path]
	if e == nil {
		e = &transcriptIndexRepairEntry{}
		r.entries[path] = e
	}
	e.inFlight = true
	exhausted = r.exhaustedLocked(path, e)
	r.mu.Unlock()
	transcriptIndexRepairLog("transcript index repair started (%s)", base)
	go r.run(path, false, refused)
	return true, exhausted
}

// inFlight reports whether a repair of path is running right now, for the
// lock-busy branch's consult: contention with a repair in flight answers the
// same typed rebuilding error, while contention with a writer or a foreign
// rebuild is an ordinary transient.
func (r *transcriptIndexRepairRegistry) inFlight(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entries[path]
	return e != nil && e.inFlight
}

// askFromRefresher is the message-search refresher's ask for path's repair,
// through the single background lane. A lane already busy retains the ask —
// one per path, the newest refusal's identity — and serves it as the running
// repair retires; a path already in flight just skips, retrying next cycle;
// client reads start their own path's repair immediately, independent of the
// lane. A path whose budget is exhausted is paced: the lane skips it until
// the interval has passed since its last refresher re-arm, without recording
// a failure. refused names the live build the refresher's own refusal stood
// on, the identity the armed repair revalidates against.
func (r *transcriptIndexRepairRegistry) askFromRefresher(path, refused string) {
	base := filepath.Base(path)
	r.mu.Lock()
	if !r.armedLocked() {
		r.mu.Unlock()
		return
	}
	if r.laneBusy {
		r.retainLaneAskLocked(path, refused)
		r.mu.Unlock()
		return
	}
	e := r.entries[path]
	if e != nil && e.inFlight {
		r.mu.Unlock()
		return
	}
	// A path whose budget is exhausted is paced: the lane skips it until the
	// interval has passed since its last refresher re-arm, without recording
	// a failure. The pacing uses the budget's own stamp key — a transcript
	// that changed since the failures is no longer exhausted, so the lane
	// re-arms it now rather than against another state of the file.
	// exhaustedLocked holds the budget and stamp checks itself, before its
	// stat, so nothing repeats here.
	if e != nil && transcriptIndexRepairNow().Sub(e.lastLaneRearm) < transcriptRepairLaneExhaustedInterval && r.exhaustedLocked(path, e) {
		r.mu.Unlock()
		return
	}
	r.armLaneLocked(path)
	r.mu.Unlock()
	transcriptIndexRepairLog("transcript index repair started (%s)", base)
	go r.run(path, true, refused)
}

// retainLaneAskLocked holds a busy lane's ask for the path — one per path —
// so the lane serves it as the running repair retires. A re-ask overwrites
// the retained identity: the newest refusal names the build the next repair
// must revalidate against. Callers hold r.mu.
func (r *transcriptIndexRepairRegistry) retainLaneAskLocked(path, refused string) {
	if e := r.entries[path]; e != nil && e.inFlight {
		return // its own repair is serving it; the next cycle re-asks
	}
	for i := range r.laneQueue {
		if r.laneQueue[i].path == path {
			r.laneQueue[i].refused = refused
			return
		}
	}
	r.laneQueue = append(r.laneQueue, transcriptIndexLaneAsk{path: path, refused: refused})
}

// takeLaneQueuedLocked pops the next retained ask the lane may serve, under
// the ask-time contract: an ask whose path is already in flight — its own
// repair is serving it — and an ask whose budget is exhausted within the
// re-arm interval retire un-taken, exactly as a fresh ask would skip.
// Callers hold r.mu.
func (r *transcriptIndexRepairRegistry) takeLaneQueuedLocked() *transcriptIndexLaneAsk {
	for len(r.laneQueue) > 0 {
		ask := r.laneQueue[0]
		r.laneQueue = r.laneQueue[1:]
		e := r.entries[ask.path]
		if e != nil && e.inFlight {
			continue
		}
		if e != nil && transcriptIndexRepairNow().Sub(e.lastLaneRearm) < transcriptRepairLaneExhaustedInterval && r.exhaustedLocked(ask.path, e) {
			continue
		}
		r.armLaneLocked(ask.path)
		return &ask
	}
	return nil
}

// armLaneLocked arms the lane's next repair for path. Callers hold r.mu.
func (r *transcriptIndexRepairRegistry) armLaneLocked(path string) {
	e := r.entries[path]
	if e == nil {
		e = &transcriptIndexRepairEntry{}
		r.entries[path] = e
	}
	e.inFlight = true
	r.laneBusy = true
	if e.failures >= transcriptRepairBudget && e.stamp != nil {
		e.lastLaneRearm = transcriptIndexRepairNow()
	}
}

// forget drops path's registry entry — the session is gone. A repair
// completion landing on the dropped entry records nothing, so a deleted
// session leaves no registry residue, not even from an in-flight repair
// finishing after the deletion — and not even the ask a busy lane retained
// for it, which a later take must not re-register.
func (r *transcriptIndexRepairRegistry) forget(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, path)
	r.laneQueue = slices.DeleteFunc(r.laneQueue, func(ask transcriptIndexLaneAsk) bool { return ask.path == path })
}

// run completes one armed repair in its detached goroutine: the recipe, then
// the outcome recorded into the path's entry only if it still exists.
// viaLane marks a refresher-fed repair, which holds the lane's single slot
// until it completes — and, as it releases the slot, takes the next retained
// ask the queue holds, under the same pacing a fresh ask would meet.
// refused is the live build the registration's refusal stood on, the
// revalidation key the recipe rebuilds only while the disk still names it.
func (r *transcriptIndexRepairRegistry) run(path string, viaLane bool, refused string) {
	err := runTranscriptIndexRepair(path, refused)
	base := filepath.Base(path)
	var next *transcriptIndexLaneAsk
	r.mu.Lock()
	if viaLane {
		r.laneBusy = false
	}
	e := r.entries[path]
	switch {
	case e == nil:
		// Deleted while the repair ran: the completion records nothing, and
		// no entry is recreated.
	case err == nil:
		delete(r.entries, path)
		transcriptIndexRepairLog("transcript index repair finished (%s)", base)
	default:
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			// The transcript is gone: the deletion's cleanup already ran
			// (forget), and a delayed registration carrying a pre-deletion
			// refusal resurrected this entry. Retire it with its queued
			// asks instead of retaining a failed count for a session that
			// no longer exists. A stat failure that is not a missing file
			// keeps the ordinary failure accounting: the transcript may
			// still be there (#4343 fast-follow low 1).
			delete(r.entries, path)
			r.laneQueue = slices.DeleteFunc(r.laneQueue, func(ask transcriptIndexLaneAsk) bool { return ask.path == path })
			transcriptIndexRepairLog("transcript index repair found the transcript gone (%s)", base)
			break
		}
		e.inFlight = false
		if stamp, ok := r.statLocked(path); ok && (e.stamp == nil || stamp != *e.stamp) {
			// A changed transcript starts the count over: the failures were
			// counted against another state of the file.
			e.stamp = &stamp
			e.failures = 0
		}
		e.failures++
		transcriptIndexRepairLog("transcript index repair failed (%s): %v", base, err)
		if e.failures == transcriptRepairBudget {
			transcriptIndexRepairLog("transcript index repair budget exhausted (%s)", base)
		}
	}
	if viaLane {
		if r.armedLocked() {
			next = r.takeLaneQueuedLocked()
		} else {
			// The hub's background is done: retention retires with it, so a
			// later wiring does not serve asks a dead context received.
			r.laneQueue = nil
		}
	}
	r.mu.Unlock()
	if next != nil {
		transcriptIndexRepairLog("transcript index repair started (%s)", filepath.Base(next.path))
		go r.run(next.path, true, next.refused)
	}
	if transcriptIndexRepairRecorded != nil {
		transcriptIndexRepairRecorded(path)
	}
}
