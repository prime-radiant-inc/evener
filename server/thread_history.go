package server

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appoverlay"
	"primeradiant.com/evener/internal/transcriptindex"
)

// The test seams below are package-level and read by the projection
// goroutine without synchronization: tests that set them must not run in
// parallel.

// threadHistoryPublishHook, when set, runs just before each history/updated
// publish; an error it returns fails that publish. Test seam only; nil in
// production.
var threadHistoryPublishHook func(threadID string) error

// threadHistoryRebuildHook, when set, runs just before each rebuild attempt.
// Test seam only; nil in production.
var threadHistoryRebuildHook func(threadID string)

// threadHistoryMaxRebuilds is how many rebuilds in a row may fail before the
// thread's history enters its failed state (spec: "If the rebuild itself
// fails three times in a row").
const threadHistoryMaxRebuilds = 3

// errIncarnationRotated reports an index another handle rebuilt under a new
// incarnation: the history clients merged no longer describes it.
var errIncarnationRotated = errors.New("transcript index incarnation changed")

// threadHistoryConfig is what one thread's history projection needs.
type threadHistoryConfig struct {
	threadID, ref, path string
	cache               *transcriptindex.Cache
	overlay             *appoverlay.Overlay
	publish             func(appwire.HistoryUpdatedParams) error // commits one history/updated
	resync              func(epoch uint64)                       // commits one evener/thread/resync
}

// threadHistory is one thread's history projection: the recorded entries the
// append hook hands it, projected by extending the transcript index in order,
// and published as history/updated. It holds no history.
type threadHistory struct {
	threadID, ref, path string
	cache               *transcriptindex.Cache
	overlay             *appoverlay.Overlay
	publish             func(appwire.HistoryUpdatedParams) error
	resync              func(epoch uint64)

	mu sync.Mutex // leaf: taken by the append hook
	// recordedLength is the latest recorded length the hook saw, 0 before
	// any, and ordinal the entry ordinal that ended there. The goroutine
	// projects up to recordedLength.
	recordedLength int64
	ordinal        uint64
	// published is the length clients hold history through: set to the
	// first hooked entry's offset (reads cover everything before it), then
	// advanced by each projection.
	published int64
	epoch     uint64
	failed    *transcriptindex.EntryError

	// incarnation is the index incarnation published history belongs to;
	// only the projection goroutine touches it.
	incarnation string

	wake      chan struct{} // 1-buffered
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newThreadHistory(cfg threadHistoryConfig) *threadHistory {
	h := &threadHistory{
		threadID: cfg.threadID,
		ref:      cfg.ref,
		path:     cfg.path,
		cache:    cfg.cache,
		overlay:  cfg.overlay,
		publish:  cfg.publish,
		resync:   cfg.resync,
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go h.run()
	return h
}

// recorded is the append hook: it runs under the transcript append lock, so
// it takes only leaf locks, never blocks and does no I/O.
func (h *threadHistory) recorded(rec transcript.Record) {
	if !rec.Recorded {
		return
	}
	h.mu.Lock()
	if h.recordedLength == 0 {
		h.published = rec.Offset
	}
	h.recordedLength, h.ordinal = rec.Offset+rec.Length, rec.Ordinal
	h.mu.Unlock()
	// Still under the append lock, so the overlay sees records in ordinal
	// order; outside h.mu, so h.mu stays a leaf.
	h.overlay.Recorded(rec)
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// RecordedLength is the length reads project to: the hook's latest, or the
// file's size when no entry was recorded in this process.
func (h *threadHistory) RecordedLength() (int64, error) {
	h.mu.Lock()
	recorded := h.recordedLength
	h.mu.Unlock()
	if recorded > 0 {
		return recorded, nil
	}
	info, err := os.Stat(h.path)
	if err != nil {
		return 0, fmt.Errorf("stat transcript: %w", err)
	}
	return info.Size(), nil
}

// Epoch is the thread's resync epoch.
func (h *threadHistory) Epoch() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.epoch
}

// Failed is non-nil once the thread's history has failed: it names the entry
// ordinal that fails to project.
func (h *threadHistory) Failed() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failed == nil {
		return nil
	}
	return h.failed
}

// close stops the projection goroutine and waits for it. Idempotent.
func (h *threadHistory) close() {
	h.closeOnce.Do(func() { close(h.stop) })
	<-h.done
}

// run projects each wake's recorded entries until close, or until the
// history fails.
func (h *threadHistory) run() {
	defer close(h.done)
	for {
		select {
		case <-h.stop:
			return
		case <-h.wake:
		}
		// A wake and a close may both be ready; close wins.
		if h.stopping() {
			return
		}
		h.mu.Lock()
		target, published, epoch := h.recordedLength, h.published, h.epoch
		h.mu.Unlock()
		if target <= published {
			continue
		}
		if err := h.project(target, published, epoch); err != nil && !h.recover() {
			return
		}
	}
}

// stopping reports whether close has been called.
func (h *threadHistory) stopping() bool {
	select {
	case <-h.stop:
		return true
	default:
		return false
	}
}

// project extends the index to target and publishes what changed past
// published.
func (h *threadHistory) project(target, published int64, epoch uint64) error {
	idx, err := h.cache.Acquire(h.path)
	if err != nil {
		return err
	}
	defer h.cache.Release(idx)
	if err := idx.CatchUpTo(target); err != nil {
		return err
	}
	changes, err := idx.ChangedSince(published)
	if err != nil {
		return err
	}
	// The first projection has no earlier publication to compare with and
	// adopts the index's incarnation. A client whose read came from an
	// incarnation rotated since sees the new one in the update's snapshot
	// identity and re-reads, as it does after a rebuild.
	if h.incarnation != "" && changes.Incarnation != h.incarnation {
		return errIncarnationRotated
	}
	h.incarnation = changes.Incarnation
	if len(changes.Items) > 0 || len(changes.Turns) > 0 {
		params := appwire.HistoryUpdatedParams{
			ThreadID: h.threadID,
			Ref:      h.ref,
			Epoch:    epoch,
			Snapshot: appwire.SnapshotIdentity{Incarnation: changes.Incarnation, Length: changes.Length},
			Turns:    changes.Turns,
			Items:    make([]appwire.ThreadItem, 0, len(changes.Items)),
		}
		for _, candidate := range changes.Items {
			params.Items = append(params.Items, candidate.Item)
		}
		if threadHistoryPublishHook != nil {
			if err := threadHistoryPublishHook(h.threadID); err != nil {
				return err
			}
		}
		if err := h.publish(params); err != nil {
			return err
		}
	}
	h.mu.Lock()
	h.published = changes.Length
	h.mu.Unlock()
	return nil
}

// recover handles a failed projection or publish: it bumps the epoch, pushes
// a resync carrying it, and rebuilds the index from the file up to the
// recorded length captured with the new epoch, so a client that re-reads for
// the resync reads at least that far and entries recorded afterwards are
// projected after the rebuild. The rebuild mints a new incarnation, so a
// client that re-read before it finished receives updates under an
// incarnation it does not hold, and re-reads on that change. A failed
// rebuild is retried at once unless the history is closing. It reports
// false when projection stops: on close, or once threadHistoryMaxRebuilds
// have failed in a row, when the history is failed and one more resync has
// gone out.
func (h *threadHistory) recover() bool {
	h.mu.Lock()
	h.epoch++
	epoch, target, ordinal := h.epoch, h.recordedLength, h.ordinal
	h.mu.Unlock()
	h.resync(epoch)
	var err error
	for attempt := range threadHistoryMaxRebuilds {
		if attempt > 0 && h.stopping() {
			return false
		}
		if err = h.rebuild(target); err == nil {
			h.mu.Lock()
			h.published = target
			h.mu.Unlock()
			return true
		}
	}
	var entryErr *transcriptindex.EntryError
	if !errors.As(err, &entryErr) {
		entryErr = &transcriptindex.EntryError{Ordinal: ordinal, Err: err}
	}
	h.mu.Lock()
	h.failed = entryErr
	h.epoch++
	epoch = h.epoch
	h.mu.Unlock()
	h.resync(epoch)
	return false
}

// rebuild rebuilds the index up to target under a new incarnation, which
// published history then belongs to.
func (h *threadHistory) rebuild(target int64) error {
	if threadHistoryRebuildHook != nil {
		threadHistoryRebuildHook(h.threadID)
	}
	idx, err := h.cache.Acquire(h.path)
	if err != nil {
		return err
	}
	defer h.cache.Release(idx)
	if err := idx.Rebuild(target); err != nil {
		return err
	}
	changes, err := idx.ChangedSince(target)
	if err != nil {
		return err
	}
	h.incarnation = changes.Incarnation
	return nil
}
