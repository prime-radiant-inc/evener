package server

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appoverlay"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm/registry"
)

// The test seams below are package-level and read by the projection
// goroutine without synchronization: tests that set them must not run in
// parallel.

// threadHistoryPublishHook, when set, runs just before each history/updated
// publish; an error it returns fails that publish. Test seam only; nil in
// production.
var threadHistoryPublishHook func(threadID string) error

// threadHistoryRebuildHook, when set, runs just before each rebuild attempt,
// after the attempt captured its boundary; an error it returns fails that
// attempt. Test seam only; nil in production.
var threadHistoryRebuildHook func(threadID string) error

// threadHistoryPublishedHook, when set, runs after each projection or
// recovery that advanced what clients hold history through, with the new
// length. Test seam only; unset in production. It is atomic, unlike the
// seams above, because tests install it while histories of servers they
// share a package with may still be projecting.
var threadHistoryPublishedHook atomic.Pointer[func(threadID string, length int64)]

// threadHistoryMaxRebuilds is how many rebuilds in a row may fail, or be
// overrun by a queue overflow before they catch up, before the thread's
// history enters its failed state (spec: "If the rebuild itself fails three
// times in a row").
const threadHistoryMaxRebuilds = 3

// threadHistoryMaxQueuedBytes bounds one thread's projection queue: the
// summed Record.Length of the entries it holds (spec: "bounded to 4 MB of
// queued entries per thread").
const threadHistoryMaxQueuedBytes = 4 << 20

// errIncarnationRotated reports an index another handle rebuilt under a new
// incarnation: the history clients merged no longer describes it.
var errIncarnationRotated = errors.New("transcript index incarnation changed")

// errOverrunByOverflow reports a rebuild the projection queue overflowed
// before it finished: its boundary no longer covers the dropped entries.
var errOverrunByOverflow = errors.New("the projection queue overflowed during the rebuild")

// threadHistoryConfig is what one thread's history projection needs.
type threadHistoryConfig struct {
	threadID, ref, path string
	cache               *transcriptindex.Cache
	overlay             *appoverlay.Overlay
	publish             func(appwire.HistoryUpdatedParams) error // commits one history/updated
	resync              func(epoch uint64)                       // commits one evener/thread/resync
	// cost prices a turn's usage at the model its entries recorded; nil
	// reports usage without cost.
	cost func(model string) *registry.Cost
	// recordedLength seeds recordedLength and published: the length already
	// recorded before any entry reaches this history's hook (a
	// transcript.Writer's in-memory RecordedLength(), never a stat). Zero
	// for a history whose hook is wired from the start, matching the
	// existing behavior where the first hooked entry sets published.
	recordedLength int64
	// epoch is the resync epoch the history starts at.
	epoch uint64
	// bootGeneration is the daemon's boot generation, stamped on every
	// history/updated.
	bootGeneration string
	// maxQueuedBytes bounds the projection queue; 0 is
	// threadHistoryMaxQueuedBytes.
	maxQueuedBytes int64
}

// threadHistory is one thread's history projection. The transcript's
// recorded-entry hook hands it each recorded entry, in ordinal order, while
// the append lock is held; the history queues it and wakes its projection
// goroutine, which projects by extending the transcript index to the recorded
// length and publishes what changed as history/updated. It holds no history.
//
// Lock order: the projection goroutine (the thread's projection
// serialization) is outermost, then the transcript's append lock, then mu,
// the queue's mutex, a leaf. The hook takes only mu. The goroutine captures a
// rebuild's boundary by taking the append lock and then mu
// (transcript.AtRecordedBoundary), so the boundary and the queue are one
// snapshot. Nothing takes the append lock while holding mu, and neither mu
// nor applyMu is held across I/O.
//
// The queue holds the entries the overlay has not yet applied (overlay
// coverage never runs under the append lock): the goroutine, every read
// (after taking its recorded length) and the event path (before
// overlay.Event) first apply what is queued, in ordinal order, under applyMu.
// Projection itself needs only lengths, so a queue that overflows its bound
// is dropped: the thread is marked for resync, and the goroutine rebuilds
// through a new boundary and replays the dropped entries into the overlay
// from the file (the overlay gap).
type threadHistory struct {
	threadID, ref, path string
	cache               *transcriptindex.Cache
	overlay             *appoverlay.Overlay
	publish             func(appwire.HistoryUpdatedParams) error
	resync              func(epoch uint64)
	cost                func(model string) *registry.Cost
	bootGeneration      string
	maxQueuedBytes      int64

	// mu is the projection queue's mutex, a leaf.
	mu sync.Mutex
	// queue is the recorded entries the overlay has not applied, in ordinal
	// order, and queuedBytes their summed length.
	queue       []transcript.Record
	queuedBytes int64
	// recordedLength is the latest recorded length the hook saw, 0 before
	// any, and ordinal the entry ordinal that ended there (hooked reports
	// whether there is one). The goroutine projects up to recordedLength.
	recordedLength int64
	ordinal        uint64
	hooked         bool
	// published is the length clients hold history through: set to the
	// first hooked entry's offset (reads cover everything before it), then
	// advanced by each projection.
	published int64
	epoch     uint64
	failed    *transcriptindex.EntryError
	// overflowed reports a queue dropped since the goroutine last captured a
	// boundary: the thread resyncs.
	overflowed bool
	// gap is the first entry the overlay has not applied that the queue no
	// longer holds: the overlay misses every entry from it up to the queue's
	// head (or the recorded length). Only the goroutine closes it.
	gap *overlayGap
	// retry asks a failed history's goroutine to try recovering again.
	retry  bool
	closed bool

	// applyMu serializes applying queued entries to the overlay, so they
	// apply in ordinal order. It is never taken under the append lock.
	applyMu sync.Mutex

	// incarnation is the index incarnation published history belongs to;
	// only the projection goroutine touches it.
	incarnation string

	wake      chan struct{} // 1-buffered
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// overlayGap names an entry by its ordinal and offset.
type overlayGap struct {
	ordinal uint64
	offset  int64
}

func newThreadHistory(cfg threadHistoryConfig) *threadHistory {
	maxQueuedBytes := cfg.maxQueuedBytes
	if maxQueuedBytes == 0 {
		maxQueuedBytes = threadHistoryMaxQueuedBytes
	}
	h := &threadHistory{
		threadID:       cfg.threadID,
		ref:            cfg.ref,
		path:           cfg.path,
		cache:          cfg.cache,
		overlay:        cfg.overlay,
		publish:        cfg.publish,
		resync:         cfg.resync,
		cost:           cfg.cost,
		bootGeneration: cfg.bootGeneration,
		maxQueuedBytes: maxQueuedBytes,
		recordedLength: cfg.recordedLength,
		published:      cfg.recordedLength,
		epoch:          cfg.epoch,
		wake:           make(chan struct{}, 1),
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}
	go h.run()
	return h
}

// recorded is the append hook: it runs under the transcript append lock, so
// it takes only the queue's mutex, never blocks and does no I/O. A failed or
// closed history takes nothing.
func (h *threadHistory) recorded(rec transcript.Record) {
	if !rec.Recorded {
		return
	}
	h.mu.Lock()
	if h.failed != nil || h.closed {
		h.mu.Unlock()
		return
	}
	if h.recordedLength == 0 {
		h.published = rec.Offset
	}
	if len(h.queue) > 0 && h.queuedBytes+rec.Length > h.maxQueuedBytes {
		h.dropQueueLocked()
		h.overflowed = true
	} else {
		h.queue = append(h.queue, rec)
		h.queuedBytes += rec.Length
	}
	h.recordedLength, h.ordinal, h.hooked = rec.Offset+rec.Length, rec.Ordinal, true
	h.mu.Unlock()
	h.wakeUp()
}

func (h *threadHistory) wakeUp() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// dropQueueLocked empties the queue, opening the overlay gap at its head (or
// at the next entry) unless one is already open. Callers hold mu.
func (h *threadHistory) dropQueueLocked() {
	if h.gap == nil {
		switch {
		case len(h.queue) > 0:
			h.gap = &overlayGap{ordinal: h.queue[0].Ordinal, offset: h.queue[0].Offset}
		case h.hooked:
			h.gap = &overlayGap{ordinal: h.ordinal + 1, offset: h.recordedLength}
		}
	}
	h.queue, h.queuedBytes = nil, 0
}

// gapEndLocked is where the overlay gap ends: the queue's head, or the
// recorded length. Callers hold mu.
func (h *threadHistory) gapEndLocked() int64 {
	if len(h.queue) > 0 {
		return h.queue[0].Offset
	}
	return h.recordedLength
}

// applyQueued applies to the overlay, in ordinal order, every queued entry
// that ends at or before through. While the overlay gap is open nothing
// applies: the entries in it come first, and only the goroutine replays them.
func (h *threadHistory) applyQueued(through int64) {
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	h.mu.Lock()
	if h.gap != nil {
		h.mu.Unlock()
		return
	}
	n := 0
	for n < len(h.queue) && h.queue[n].Offset+h.queue[n].Length <= through {
		h.queuedBytes -= h.queue[n].Length
		n++
	}
	batch := slices.Clone(h.queue[:n])
	h.queue = slices.Delete(h.queue, 0, n)
	h.mu.Unlock()
	for _, rec := range batch {
		h.overlay.Recorded(rec)
	}
}

// overlayEvent applies one session event to the overlay, after every entry
// recorded before it.
func (h *threadHistory) overlayEvent(ev events.SessionEvent) []appoverlay.Change {
	h.applyQueued(math.MaxInt64)
	return h.overlay.Event(ev)
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

// requestRecovery asks a failed history's goroutine to try recovering: a
// read found the transcript readable again.
func (h *threadHistory) requestRecovery() {
	h.mu.Lock()
	h.retry = true
	h.mu.Unlock()
	h.wakeUp()
}

// retire stops the history taking entries and starting a recovery, and
// returns its epoch, which is then final. A retired history still publishes
// what it has when it closes.
func (h *threadHistory) retire() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return h.epoch
}

// close publishes every entry recorded so far, then stops the projection
// goroutine and waits for it. Idempotent.
func (h *threadHistory) close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		h.mu.Unlock()
		close(h.stop)
	})
	<-h.done
}

// run projects each wake's recorded entries until close. A closing history
// first projects what is recorded (finish).
func (h *threadHistory) run() {
	defer close(h.done)
	healthy := true
	for {
		select {
		case <-h.stop:
			h.finish(healthy)
			return
		case <-h.wake:
		}
		// A wake and a close may both be ready; close wins.
		if h.stopping() {
			h.finish(healthy)
			return
		}
		healthy = h.step()
	}
}

// step applies the queue to the overlay and projects what was recorded,
// recovering when that fails or the queue overflowed. It reports whether the
// projection is healthy: caught up to a boundary, not failed, not aborted.
func (h *threadHistory) step() bool {
	h.applyQueued(math.MaxInt64)
	h.mu.Lock()
	failed, retry, overflowed := h.failed != nil, h.retry, h.overflowed
	h.retry = false
	target, published, epoch := h.recordedLength, h.published, h.epoch
	h.mu.Unlock()
	switch {
	case failed && !retry:
		return false
	case failed || overflowed:
		return h.recover()
	case target <= published:
		return true
	}
	if err := h.project(target, published, epoch); err != nil {
		return h.recover()
	}
	return true
}

// finish is a closing history's last projection: every entry recorded up to
// the recorded length is published before the goroutine stops. A history
// that is failed, resyncing or was stopped mid-recovery publishes nothing
// more; its clients re-read.
func (h *threadHistory) finish(healthy bool) {
	h.mu.Lock()
	target, published, epoch := h.recordedLength, h.published, h.epoch
	healthy = healthy && h.failed == nil && !h.overflowed
	h.mu.Unlock()
	if healthy && target > published {
		_ = h.project(target, published, epoch)
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
			ThreadID:       h.threadID,
			Ref:            h.ref,
			BootGeneration: h.bootGeneration,
			Epoch:          epoch,
			Snapshot:       appwire.SnapshotIdentity{Incarnation: changes.Incarnation, Length: changes.Length},
			Turns:          changes.Turns,
			Items:          make([]appwire.ThreadItem, 0, len(changes.Items)),
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
	if hook := threadHistoryPublishedHook.Load(); hook != nil {
		(*hook)(h.threadID, changes.Length)
	}
	return nil
}

// recover handles a failed projection or publish, an overflowed queue, or a
// failed thread a read found readable: it bumps the epoch, pushes a resync
// carrying it, and rebuilds. Each attempt captures a boundary under the
// append lock (every entry up to it was handed to the hook, every later one
// will be), rebuilds the index through exactly that boundary under a new
// incarnation, and replays into the overlay the entries a dropped queue took
// from it. Entries after the boundary project on later wakes, so none is
// published twice and none is skipped; a client that re-read before the
// rebuild finished receives updates under an incarnation it does not hold,
// and re-reads on that change.
//
// An attempt that fails, or that the queue overflows before it finishes
// (the boundary no longer covers what was dropped), is retried through a new
// boundary unless the history is closing. After threadHistoryMaxRebuilds in
// a row the history is failed: its queue is dropped, the hook stops
// queueing, and one more resync goes out. recover reports whether the
// projection caught up.
func (h *threadHistory) recover() bool {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return false
	}
	h.epoch++
	epoch, ordinal := h.epoch, h.ordinal
	h.mu.Unlock()
	h.resync(epoch)
	var err error
	for attempt := range threadHistoryMaxRebuilds {
		if attempt > 0 && h.stopping() {
			return false
		}
		if err = h.rebuildThroughBoundary(); err == nil {
			return true
		}
	}
	var entryErr *transcriptindex.EntryError
	if !errors.As(err, &entryErr) {
		entryErr = &transcriptindex.EntryError{Ordinal: ordinal, Err: err}
	}
	h.mu.Lock()
	h.failed = entryErr
	h.dropQueueLocked()
	h.overflowed = false
	h.epoch++
	epoch = h.epoch
	h.mu.Unlock()
	h.resync(epoch)
	return false
}

// rebuildThroughBoundary is one recovery attempt.
func (h *threadHistory) rebuildThroughBoundary() error {
	boundary, err := h.captureBoundary()
	if err != nil {
		return err
	}
	if threadHistoryRebuildHook != nil {
		if err := threadHistoryRebuildHook(h.threadID); err != nil {
			return err
		}
	}
	if err := h.rebuild(boundary); err != nil {
		return err
	}
	if err := h.fillOverlayGap(); err != nil {
		return err
	}
	h.mu.Lock()
	overrun := h.overflowed
	if !overrun {
		h.published = boundary
	}
	h.mu.Unlock()
	if overrun {
		return errOverrunByOverflow
	}
	if hook := threadHistoryPublishedHook.Load(); hook != nil {
		(*hook)(h.threadID, boundary)
	}
	return nil
}

// captureBoundary takes the rebuild's boundary, the recorded length, under
// the transcript's append lock, so it and the queue are one snapshot. It
// clears the overflow it covers and a failed state (the hook queues again),
// and adopts the length: entries a failed history's hook passed over lie in
// the overlay gap up to it. A transcript no writer in this process has open
// cannot grow, and its size is the boundary.
func (h *threadHistory) captureBoundary() (int64, error) {
	var boundary int64
	adopt := func(recordedLength int64) {
		h.mu.Lock()
		defer h.mu.Unlock()
		boundary = recordedLength
		h.overflowed = false
		h.failed = nil
		h.recordedLength = max(h.recordedLength, recordedLength)
	}
	found, err := transcript.AtRecordedBoundary(h.path, adopt)
	if err != nil {
		return 0, err
	}
	if !found {
		info, err := os.Stat(h.path)
		if err != nil {
			return 0, fmt.Errorf("stat transcript: %w", err)
		}
		adopt(info.Size())
	}
	return boundary, nil
}

// rebuild rebuilds the index up to target under a new incarnation, which
// published history then belongs to.
func (h *threadHistory) rebuild(target int64) error {
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

// fillOverlayGap replays into the overlay, from the file, the entries a
// dropped queue took from it, then closes the gap so queued entries apply
// again. The file is read with no lock held; the gap can only grow at its
// end meanwhile (another overflow), and the loop reads on to its new end.
func (h *threadHistory) fillOverlayGap() error {
	for {
		h.mu.Lock()
		gap, end := h.gap, h.gapEndLocked()
		h.mu.Unlock()
		if gap == nil {
			return nil
		}
		records, err := readRecordedEntries(h.path, *gap, end)
		if err != nil {
			return err
		}
		h.applyMu.Lock()
		for _, rec := range records {
			h.overlay.Recorded(rec)
		}
		h.mu.Lock()
		closed := h.gapEndLocked() == end
		if closed {
			h.gap = nil
		} else {
			h.gap = &overlayGap{ordinal: gap.ordinal + uint64(len(records)), offset: end}
		}
		h.mu.Unlock()
		h.applyMu.Unlock()
		if closed {
			return nil
		}
	}
}

// readRecordedEntries reads the recorded entry lines from from up to end as
// records, numbering them from from's ordinal. A line that does not decode
// takes its ordinal and nothing else: the overlay has nothing to learn from
// it, and the index reports it.
func readRecordedEntries(path string, from overlayGap, end int64) ([]transcript.Record, error) {
	if end <= from.offset {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only
	reader := bufio.NewReaderSize(io.NewSectionReader(f, from.offset, end-from.offset), 64<<10)
	var records []transcript.Record
	offset, ordinal := from.offset, from.ordinal
	for {
		line, complete, n, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if err != nil {
			return nil, fmt.Errorf("read transcript: %w", err)
		}
		if !complete {
			return records, nil
		}
		start := offset
		offset += n
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if entry, err := transcript.DecodeEntry(trimmed); err == nil {
			records = append(records, transcript.Record{Recorded: true, Ordinal: ordinal, Seq: entry.Seq, Offset: start, Length: n, Turn: entry.Turn})
		}
		ordinal++
	}
}
