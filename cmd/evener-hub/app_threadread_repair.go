package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// transcriptIndexRepairLog is the repair registry's advisory log line, in the
// established [hub] style, naming the session's transcript. A package
// variable so tests can capture the lines.
var transcriptIndexRepairLog = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[hub] "+format+"\n", args...)
}

// runTranscriptIndexRepair is the repair recipe the registry runs: rebuild
// the sidecar from the transcript, off the request path, through the
// exported Rebuild — the one place a full rebuild legitimately runs on the
// hub. A package variable so tests can hold a repair open, fail it, or
// observe it.
var runTranscriptIndexRepair = repairTranscriptIndex

// repairTranscriptIndex rebuilds path's transcript index sidecar from the
// transcript — the authority — through the exported Rebuild, under the
// blocking exclusive lock, in the caller's goroutine. It stats the transcript
// first and aborts without creating anything when it is gone: the open's
// MkdirAll and lock-file creation would otherwise resurrect a deleted path's
// sidecar directory. It opens an ordinary handle without catching up, so
// this rebuild is the one that runs — an open with a built-in catch-up would
// either duplicate the rebuild for identity triggers or no-op for
// corruption. It must not use the hub's cached handles: they carry the
// refusing policy, which would refuse this rebuild too.
func repairTranscriptIndex(transcriptPath string) error {
	info, err := os.Stat(transcriptPath)
	if err != nil {
		return err
	}
	index, err := transcriptindex.OpenWithoutCatchUp(transcriptPath, transcriptindex.DirFor(transcriptPath))
	if err != nil {
		return err
	}
	defer index.Close() //nolint:errcheck // the rebuild has committed or not; close is hygiene
	return index.Rebuild(info.Size())
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
}

var transcriptIndexRepairs = newTranscriptIndexRepairRegistry()

func newTranscriptIndexRepairRegistry() *transcriptIndexRepairRegistry {
	return &transcriptIndexRepairRegistry{entries: map[string]*transcriptIndexRepairEntry{}}
}

// wire arms the registry with the hub's background context: repairs start
// only while it is live, so shutdown abandons an in-flight repair exactly as
// a crash would, which the sidecar's commit rule makes harmless.
func (r *transcriptIndexRepairRegistry) wire(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctx = ctx
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
// path's sidecar: it arms the path's single repair when none is in flight
// (never blocking the caller) and reports whether a repair is armed, and
// whether the path's budget is exhausted for the transcript's current
// stamp — the re-arming read answers history-failed itself, while
// subsequent reads during the re-armed repair answer rebuilding.
func (r *transcriptIndexRepairRegistry) answerRead(path string) (armed, exhausted bool) {
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
	go r.run(path, false)
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
// through the single background lane. A lane already busy or a path already
// in flight just skips, retrying next cycle; client reads start their own
// path's repair immediately, independent of the lane. A path whose budget is
// exhausted is paced: the lane skips it until the interval has passed since
// its last refresher re-arm, without recording a failure.
func (r *transcriptIndexRepairRegistry) askFromRefresher(path string) {
	base := filepath.Base(path)
	r.mu.Lock()
	if !r.armedLocked() || r.laneBusy {
		r.mu.Unlock()
		return
	}
	e := r.entries[path]
	if e != nil && e.inFlight {
		r.mu.Unlock()
		return
	}
	if e != nil && e.failures >= transcriptRepairBudget && e.stamp != nil &&
		time.Since(e.lastLaneRearm) < transcriptRepairLaneExhaustedInterval {
		r.mu.Unlock()
		return
	}
	if e == nil {
		e = &transcriptIndexRepairEntry{}
		r.entries[path] = e
	}
	e.inFlight = true
	r.laneBusy = true
	if e.failures >= transcriptRepairBudget && e.stamp != nil {
		e.lastLaneRearm = time.Now()
	}
	r.mu.Unlock()
	transcriptIndexRepairLog("transcript index repair started (%s)", base)
	go r.run(path, true)
}

// forget drops path's registry entry — the session is gone. A repair
// completion landing on the dropped entry records nothing, so a deleted
// session leaves no registry residue, not even from an in-flight repair
// finishing after the deletion.
func (r *transcriptIndexRepairRegistry) forget(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, path)
}

// run completes one armed repair in its detached goroutine: the recipe, then
// the outcome recorded into the path's entry only if it still exists.
// viaLane marks a refresher-fed repair, which holds the lane's single slot
// until it completes.
func (r *transcriptIndexRepairRegistry) run(path string, viaLane bool) {
	err := runTranscriptIndexRepair(path)
	base := filepath.Base(path)
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
	r.mu.Unlock()
}
