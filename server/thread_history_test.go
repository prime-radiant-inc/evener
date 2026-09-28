package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appoverlay"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// These tests set package-level seams (threadHistoryPublishHook,
// threadHistoryRebuildHook) that the projection goroutine reads without
// synchronization, so none of them may use t.Parallel().

// historyTestWait bounds every wait on the projection goroutine; it only
// fires when the goroutine never delivers what the test awaits.
const historyTestWait = 2 * time.Second

// historyHarness is one thread's history over a real transcript writer, with
// publish and resync recorded on channels the tests synchronize on.
type historyHarness struct {
	path     string
	writer   *transcript.Writer
	hook     func(transcript.Record) // the history's append hook, as installed
	cache    *transcriptindex.Cache
	overlay  *appoverlay.Overlay
	history  *threadHistory
	updates  chan appwire.HistoryUpdatedParams
	resyncs  chan uint64
	onRecord func(transcript.Record) // runs in the append hook before the history's
	// publications wakes awaitPublished.
	publications *historyPublications
	// commits, when set, is the appserver whose projection commits the
	// history's publish and resync go through, as production's do.
	commits *appserver.Server

	// deferredResyncs holds each resync updatesThrough read while it was
	// waiting for an update. The harness delivers history/updated and
	// thread/resync on two independent channels, so a resync can be ready
	// before the update it follows; the reader must not read that channel
	// order as the delivery order. nextResync and expectQuiet consume these
	// first, and a test that leaves one unconsumed fails at cleanup.
	deferredResyncs []uint64
}

// deliver runs send inside a projection commit when the harness has one.
func (hx *historyHarness) deliver(send func()) {
	if hx.commits == nil {
		send()
		return
	}
	hx.commits.CommitProjection(func() []appserver.SequencedNotification {
		send()
		return nil
	})
}

// awaitPublished returns once the history holds clients' history through
// length, by projection or by a recovery's rebuild. An entry recorded after
// it is past any rebuild boundary taken so far.
func (hx *historyHarness) awaitPublished(t *testing.T, length int64) {
	t.Helper()
	hx.publications.await(t, hx.history, length)
}

func newHistoryHarness(t *testing.T) *historyHarness {
	t.Helper()
	return newHistoryHarnessWith(t, 0)
}

// newHistoryHarnessWith is newHistoryHarness with the projection queue
// bounded to maxQueuedBytes (0: the production bound).
func newHistoryHarnessWith(t *testing.T, maxQueuedBytes int64) *historyHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "th_history.transcript.jsonl")
	writer, err := transcript.NewWriterNoSync(path, transcript.Header{SessionID: "th_history"})
	if err != nil {
		t.Fatal(err)
	}
	// Recorded lines are in the file whether or not they are synced; the
	// projection never depends on fsync.
	writer.SyncInterval = time.Hour
	t.Cleanup(func() { _ = writer.Close() })
	cache := transcriptindex.NewCache(transcriptindex.DefaultCacheCapacity)
	t.Cleanup(func() { _ = cache.Close() })
	overlay := appoverlay.New(appoverlay.NewBudget(1 << 20))
	t.Cleanup(overlay.Close)
	hx := &historyHarness{
		publications: watchHistoryPublications(t),
		path:         path,
		writer:       writer,
		cache:        cache,
		overlay:      overlay,
		updates:      make(chan appwire.HistoryUpdatedParams, 256),
		resyncs:      make(chan uint64, 256),
	}
	t.Cleanup(func() {
		if len(hx.deferredResyncs) > 0 {
			t.Errorf("unconsumed resync(s) %v: read past an awaited update but not expected", hx.deferredResyncs)
		}
	})
	hx.history = newThreadHistory(threadHistoryConfig{
		threadID:       "th_history",
		ref:            "local:th_history",
		bootGeneration: "1",
		path:           path,
		cache:          cache,
		overlay:        overlay,
		publish: func(params appwire.HistoryUpdatedParams) error {
			hx.deliver(func() { hx.updates <- params })
			return nil
		},
		resync:         func(epoch uint64) { hx.deliver(func() { hx.resyncs <- epoch }) },
		maxQueuedBytes: maxQueuedBytes,
	})
	t.Cleanup(hx.history.close)
	hx.hook = func(rec transcript.Record) {
		if hx.onRecord != nil {
			hx.onRecord(rec)
		}
		hx.history.recorded(rec)
	}
	writer.OnRecorded(hx.hook)
	return hx
}

func (hx *historyHarness) record(t *testing.T, text string) transcript.Record {
	t.Helper()
	return hx.recordTurn(t, schema.NewTurn(schema.TurnUserInput, llm.User(text)))
}

func (hx *historyHarness) recordTurn(t *testing.T, turn schema.Turn) transcript.Record {
	t.Helper()
	rec, err := hx.writer.Record(turn, transcript.RecordOptions{})
	if err != nil || !rec.Recorded {
		t.Errorf("record %s = %+v, %v", turn.Kind, rec, err)
	}
	return rec
}

// errBrokenProjection is the failure breakProjection injects.
var errBrokenProjection = errors.New("injected projection failure")

// breakProjection fails every projection publish and every rebuild attempt
// until repair is called; onRebuild, when set, runs first at each rebuild
// attempt. It stands for an infrastructure failure (I/O, a corrupt index): an
// entry that does not decode is quarantined instead.
func breakProjection(t *testing.T, onRebuild func()) (repair func()) {
	t.Helper()
	var broken atomic.Bool
	broken.Store(true)
	threadHistoryPublishHook = func(string) error {
		if broken.Load() {
			return errBrokenProjection
		}
		return nil
	}
	threadHistoryRebuildHook = func(string) error {
		if onRebuild != nil {
			onRebuild()
		}
		if broken.Load() {
			return errBrokenProjection
		}
		return nil
	}
	t.Cleanup(func() {
		threadHistoryPublishHook = nil
		threadHistoryRebuildHook = nil
	})
	return func() { broken.Store(false) }
}

// corruptNext corrupts the next recorded entry's line in place, same length,
// before the history sees it: a line that does not decode. repair writes the
// original line back.
func (hx *historyHarness) corruptNext(t *testing.T) (repair func()) {
	t.Helper()
	var at int64
	var original []byte
	hx.onRecord = func(rec transcript.Record) {
		hx.onRecord = nil
		data, err := os.ReadFile(hx.path)
		if err != nil {
			t.Error(err)
			return
		}
		at, original = rec.Offset, data[rec.Offset:rec.Offset+rec.Length]
		corrupt := bytes.Replace(original, []byte(`"kind":"entry"`), []byte(`"kind":"entrx"`), 1)
		if bytes.Equal(corrupt, original) {
			t.Errorf("entry line %s has no record kind to corrupt", original)
			return
		}
		hx.writeAt(t, corrupt, at)
	}
	return func() { hx.writeAt(t, original, at) }
}

func (hx *historyHarness) writeAt(t *testing.T, data []byte, offset int64) {
	t.Helper()
	f, err := os.OpenFile(hx.path, os.O_WRONLY, 0)
	if err != nil {
		t.Error(err)
		return
	}
	defer f.Close() //nolint:errcheck // fixture
	if _, err := f.WriteAt(data, offset); err != nil {
		t.Error(err)
	}
}

// updatesThrough collects history/updated notifications until one covers
// length, keeping any resync it reads meanwhile for nextResync.
func (hx *historyHarness) updatesThrough(t *testing.T, length int64) []appwire.HistoryUpdatedParams {
	t.Helper()
	var got []appwire.HistoryUpdatedParams
	for {
		select {
		case params := <-hx.updates:
			got = append(got, params)
			if params.Snapshot.Length == length {
				return got
			}
		case epoch := <-hx.resyncs:
			hx.deferredResyncs = append(hx.deferredResyncs, epoch)
		case <-time.After(historyTestWait):
			t.Fatalf("no history/updated covering length %d; got %d updates", length, len(got))
		}
	}
}

func (hx *historyHarness) nextResync(t *testing.T) uint64 {
	t.Helper()
	if len(hx.deferredResyncs) > 0 {
		epoch := hx.deferredResyncs[0]
		hx.deferredResyncs = hx.deferredResyncs[1:]
		return epoch
	}
	select {
	case epoch := <-hx.resyncs:
		return epoch
	case params := <-hx.updates:
		t.Fatalf("history/updated %+v where a resync was expected", params)
	case <-time.After(historyTestWait):
		t.Fatal("no resync")
	}
	return 0
}

func (hx *historyHarness) expectQuiet(t *testing.T) {
	t.Helper()
	if len(hx.deferredResyncs) > 0 {
		t.Fatalf("unexpected resync to epoch %d", hx.deferredResyncs[0])
	}
	select {
	case params := <-hx.updates:
		t.Fatalf("unexpected history/updated %+v", params)
	case epoch := <-hx.resyncs:
		t.Fatalf("unexpected resync to epoch %d", epoch)
	default:
	}
}

func itemVersions(params appwire.HistoryUpdatedParams) []uint64 {
	var versions []uint64
	for _, item := range params.Items {
		versions = append(versions, item.Version)
	}
	return versions
}

// The spec's two-goroutine boundary test, through projection: entries
// appended from two goroutines at once are published in ordinal order, each
// entry's item exactly once at its final version, with its turn.
func TestThreadHistoryPublishesConcurrentAppendsInOrdinalOrder(t *testing.T) {
	hx := newHistoryHarness(t)
	var wg sync.WaitGroup
	for g := range 2 {
		wg.Go(func() {
			for i := range 10 {
				hx.record(t, fmt.Sprintf("goroutine %d message %d", g, i))
			}
		})
	}
	wg.Wait()
	updates := hx.updatesThrough(t, hx.writer.RecordedLength())

	seen := map[string]bool{}
	var versions, turnVersions []uint64
	for _, params := range updates {
		if params.ThreadID != "th_history" || params.Ref != "local:th_history" || params.Epoch != 0 {
			t.Fatalf("envelope = %q %q epoch %d", params.ThreadID, params.Ref, params.Epoch)
		}
		if params.Snapshot.Incarnation == "" {
			t.Fatal("history/updated carries no incarnation")
		}
		for _, item := range params.Items {
			if item.TurnID == "" || item.Position == nil || item.TranscriptKey == "" {
				t.Fatalf("item %+v lacks its turn, position or key", item)
			}
			if seen[item.TranscriptKey] {
				t.Fatalf("item %s published twice", item.TranscriptKey)
			}
			seen[item.TranscriptKey] = true
		}
		versions = append(versions, itemVersions(params)...)
		for _, turn := range params.Turns {
			if len(turn.Items) != 0 {
				t.Fatalf("turn %s carries %d items", turn.ID, len(turn.Items))
			}
			turnVersions = append(turnVersions, turn.Version)
		}
	}
	if len(versions) != 20 {
		t.Fatalf("published item versions %v, want one per entry", versions)
	}
	for i, version := range versions {
		if version != uint64(i+1) {
			t.Fatalf("published item versions %v, want ordinals 0..19 in order", versions)
		}
	}
	for i := 1; i < len(turnVersions); i++ {
		if turnVersions[i] <= turnVersions[i-1] {
			t.Fatalf("published turn versions %v go backwards", turnVersions)
		}
	}
	if len(turnVersions) == 0 || turnVersions[len(turnVersions)-1] != 20 {
		t.Fatalf("published turn versions %v do not end at the last entry", turnVersions)
	}

	// A tool call's item gains a second contributor when its TOOL_RESULTS
	// lands in a later update: it is published again at the higher version,
	// and the reduced state (by key, highest version wins) holds it once at
	// its final form.
	call := llm.ToolCallData{ID: "call_read", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)}
	assistant := hx.recordTurn(t, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}})
	updates = append(updates, hx.updatesThrough(t, assistant.Offset+assistant.Length)...)
	results := hx.recordTurn(t, schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_read", "read_file", "line 1", false)))
	updates = append(updates, hx.updatesThrough(t, results.Offset+results.Length)...)
	reduced := map[string]appwire.ThreadItem{}
	publications := 0
	for _, params := range updates {
		for _, item := range params.Items {
			if item.CallID == "call_read" {
				publications++
			}
			if held, ok := reduced[item.TranscriptKey]; !ok || item.Version > held.Version {
				reduced[item.TranscriptKey] = item
			}
		}
	}
	var tool []appwire.ThreadItem
	for _, item := range reduced {
		if item.CallID == "call_read" {
			tool = append(tool, item)
		}
	}
	if len(tool) != 1 || tool[0].Version != results.Ordinal+1 || tool[0].Output != "line 1" {
		t.Fatalf("reduced tool items %+v, want one at version %d with its output", tool, results.Ordinal+1)
	}
	if publications != 2 {
		t.Fatalf("tool item published %d times, want at the call and again at its result", publications)
	}
	if len(reduced) != 21 {
		t.Fatalf("reduced state holds %d items, want 21", len(reduced))
	}
	if err := hx.history.Failed(); err != nil {
		t.Fatalf("Failed() = %v", err)
	}
	hx.expectQuiet(t)
}

// A publish that fails bumps the epoch, pushes a resync carrying it and
// rebuilds; the next entry publishes under the new epoch and a new
// incarnation, with only its own item.
func TestThreadHistoryFailedPublishResyncsAndRebuilds(t *testing.T) {
	var calls []string
	threadHistoryPublishHook = func(threadID string) error {
		calls = append(calls, threadID)
		if len(calls) == 2 {
			return errors.New("injected publish failure")
		}
		return nil
	}
	t.Cleanup(func() { threadHistoryPublishHook = nil })
	hx := newHistoryHarness(t)
	first := hx.record(t, "first")
	hx.updatesThrough(t, first.Offset+first.Length)
	second := hx.record(t, "second")

	if epoch := hx.nextResync(t); epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	hx.awaitPublished(t, second.Offset+second.Length)
	third := hx.record(t, "third")
	updates := hx.updatesThrough(t, third.Offset+third.Length)
	if len(updates) != 1 || updates[0].Epoch != 1 {
		t.Fatalf("after the resync got %+v, want one update at epoch 1", updates)
	}
	if got := itemVersions(updates[0]); len(got) != 1 || got[0] != third.Ordinal+1 {
		t.Fatalf("update item versions %v, want only the third entry's (%d); the rebuild covered the second (%d)", got, third.Ordinal+1, second.Ordinal+1)
	}
	if hx.history.Epoch() != 1 {
		t.Fatalf("Epoch() = %d, want 1", hx.history.Epoch())
	}
	if len(calls) != 3 || calls[0] != "th_history" {
		t.Fatalf("publish hook calls = %q", calls)
	}
	hx.expectQuiet(t)
}

// An index another handle rebuilt under a new incarnation holds no history
// the thread's clients merged: the next entry resyncs instead of publishing
// against it.
func TestThreadHistoryRotatedIncarnationResyncs(t *testing.T) {
	hx := newHistoryHarness(t)
	first := hx.record(t, "first")
	before := hx.updatesThrough(t, first.Offset+first.Length)

	idx, err := hx.cache.Acquire(hx.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Rebuild(first.Offset + first.Length); err != nil {
		t.Fatal(err)
	}
	hx.cache.Release(idx)

	second := hx.record(t, "second")
	if epoch := hx.nextResync(t); epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	hx.awaitPublished(t, second.Offset+second.Length)
	third := hx.record(t, "third")
	after := hx.updatesThrough(t, third.Offset+third.Length)
	if after[0].Epoch != 1 || after[0].Snapshot.Incarnation == before[0].Snapshot.Incarnation {
		t.Fatalf("after the rotation got epoch %d incarnation %q (was %q)", after[0].Epoch, after[0].Snapshot.Incarnation, before[0].Snapshot.Incarnation)
	}
	hx.expectQuiet(t)
}

// A projection that fails, and every rebuild after it: after the third failed
// rebuild the thread's history is failed, naming the last entry, a last
// resync goes out, and projection stops (nothing is queued)
// while the writer keeps recording.
func TestThreadHistoryThirdFailedRebuildFailsTheThread(t *testing.T) {
	hx := newHistoryHarness(t)
	first := hx.record(t, "first")
	hx.updatesThrough(t, first.Offset+first.Length)

	var rebuilds int
	breakProjection(t, func() { rebuilds++ })
	bad := hx.record(t, "second")

	if epoch := hx.nextResync(t); epoch != 1 {
		t.Fatalf("first resync epoch = %d, want 1", epoch)
	}
	if epoch := hx.nextResync(t); epoch != 2 {
		t.Fatalf("failed-state resync epoch = %d, want 2", epoch)
	}
	var entryErr *transcriptindex.EntryError
	if err := hx.history.Failed(); !errors.As(err, &entryErr) || entryErr.Ordinal != bad.Ordinal {
		t.Fatalf("Failed() = %v, want an entry error naming ordinal %d", err, bad.Ordinal)
	}
	if hx.history.Epoch() != 2 {
		t.Fatalf("Epoch() = %d, want 2", hx.history.Epoch())
	}
	if rebuilds != threadHistoryMaxRebuilds {
		t.Fatalf("rebuild attempts = %d, want %d", rebuilds, threadHistoryMaxRebuilds)
	}

	later := hx.record(t, "third")
	if later.Ordinal != bad.Ordinal+1 || hx.writer.RecordedLength() != later.Offset+later.Length {
		t.Fatalf("writer stopped recording: %+v, recorded length %d", later, hx.writer.RecordedLength())
	}
	hx.history.mu.Lock()
	queued := len(hx.history.queue)
	hx.history.mu.Unlock()
	if queued != 0 {
		t.Fatalf("the failed thread queued %d entries", queued)
	}
	hx.expectQuiet(t)
}

// A rebuild that fails for a reason other than an entry (here, the index
// cache is gone) fails the thread naming the last entry it attempted.
func TestThreadHistoryFailedStateNamesTheLastAttemptedEntry(t *testing.T) {
	hx := newHistoryHarness(t)
	first := hx.record(t, "first")
	hx.updatesThrough(t, first.Offset+first.Length)
	if err := hx.cache.Close(); err != nil {
		t.Fatal(err)
	}
	last := hx.record(t, "second")
	if epoch := hx.nextResync(t); epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	if epoch := hx.nextResync(t); epoch != 2 {
		t.Fatalf("failed-state resync epoch = %d, want 2", epoch)
	}
	var entryErr *transcriptindex.EntryError
	if err := hx.history.Failed(); !errors.As(err, &entryErr) || entryErr.Ordinal != last.Ordinal || entryErr.Err == nil {
		t.Fatalf("Failed() = %v, want an error naming ordinal %d with its cause", err, last.Ordinal)
	}
}

// Rebuilds that fail count only in a row: two failed rebuilds and a third
// that succeeds recover the thread, and the next failure starts counting
// again.
func TestThreadHistoryRebuildFailuresCountInARow(t *testing.T) {
	hx := newHistoryHarness(t)
	var repair func()
	rebuilds := 0
	onRebuild := func() {
		rebuilds++
		if rebuilds%threadHistoryMaxRebuilds == 0 {
			repair()
		}
	}

	for round := uint64(1); round <= 2; round++ {
		repair = breakProjection(t, onRebuild)
		corrupt := hx.record(t, fmt.Sprintf("corrupt %d", round))
		if epoch := hx.nextResync(t); epoch != round {
			t.Fatalf("resync epoch = %d, want %d", epoch, round)
		}
		hx.awaitPublished(t, corrupt.Offset+corrupt.Length)
		next := hx.record(t, fmt.Sprintf("after %d", round))
		updates := hx.updatesThrough(t, next.Offset+next.Length)
		if updates[len(updates)-1].Epoch != round {
			t.Fatalf("update after recovery %d at epoch %d", round, updates[len(updates)-1].Epoch)
		}
	}
	if err := hx.history.Failed(); err != nil {
		t.Fatalf("Failed() = %v after rebuilds that recovered", err)
	}
	if rebuilds != 2*threadHistoryMaxRebuilds {
		t.Fatalf("rebuild attempts = %d, want %d", rebuilds, 2*threadHistoryMaxRebuilds)
	}
}

// Each recorded entry reaches the thread's overlay before a later event: a
// notice raised after it anchors past it.
func TestThreadHistoryHandsRecordedEntriesToTheOverlay(t *testing.T) {
	hx := newHistoryHarness(t)
	rec := hx.record(t, "first")
	hx.history.overlayEvent(events.New(events.LoopDetectionData{Message: "loop"}))
	snapshot := hx.overlay.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Anchor == nil || snapshot[0].Anchor.Entry != rec.Ordinal+1 {
		t.Fatalf("overlay snapshot %+v, want one notice anchored at entry %d", snapshot, rec.Ordinal+1)
	}
}

// Before anything is recorded in this process, reads project to the file's
// size; afterwards, to the hook's recorded length.
func TestThreadHistoryRecordedLength(t *testing.T) {
	hx := newHistoryHarness(t)
	info, err := os.Stat(hx.path)
	if err != nil {
		t.Fatal(err)
	}
	if length, err := hx.history.RecordedLength(); err != nil || length != info.Size() {
		t.Fatalf("RecordedLength() before any append = %d, %v; want the file size %d", length, err, info.Size())
	}
	rec := hx.record(t, "first")
	if length, err := hx.history.RecordedLength(); err != nil || length != rec.Offset+rec.Length {
		t.Fatalf("RecordedLength() = %d, %v; want %d", length, err, rec.Offset+rec.Length)
	}
}

// History recorded before the first entry this process hands the thread is
// what reads cover: the first history/updated carries only entries from the
// first hooked one on.
func TestThreadHistoryPublishesFromTheFirstHookedEntry(t *testing.T) {
	hx := newHistoryHarness(t)
	hx.writer.OnRecorded(nil)
	hx.record(t, "before the hook")
	hx.record(t, "also before the hook")
	hx.writer.OnRecorded(hx.hook)
	hooked := hx.record(t, "hooked")
	updates := hx.updatesThrough(t, hooked.Offset+hooked.Length)
	if len(updates) != 1 {
		t.Fatalf("got %d updates, want 1", len(updates))
	}
	if got := itemVersions(updates[0]); len(got) != 1 || got[0] != hooked.Ordinal+1 {
		t.Fatalf("item versions %v, want only the hooked entry's %d", got, hooked.Ordinal+1)
	}
}

// Closing while a failing rebuild is being retried stops the retries: close
// does not wait through every remaining re-parse.
func TestThreadHistoryCloseDuringRecoveryStopsRetrying(t *testing.T) {
	hx := newHistoryHarness(t)
	parked, release := make(chan struct{}), make(chan struct{})
	var rebuilds int
	breakProjection(t, func() {
		rebuilds++
		if rebuilds == 1 {
			close(parked)
			<-release
		}
	})
	hx.record(t, "fails to project")
	select {
	case <-parked:
	case <-time.After(historyTestWait):
		t.Fatal("no rebuild attempt")
	}
	closed := make(chan struct{})
	go func() {
		hx.history.close()
		close(closed)
	}()
	<-hx.history.stop // close has signalled before the parked rebuild resumes
	close(release)
	select {
	case <-closed:
	case <-time.After(historyTestWait):
		t.Fatal("close did not return")
	}
	if rebuilds != 1 {
		t.Fatalf("rebuild attempts = %d after close, want 1", rebuilds)
	}
	if err := hx.history.Failed(); err != nil {
		t.Fatalf("Failed() = %v; a closed history is not a failed one", err)
	}
}

// Closing stops the projection goroutine; a record after close neither
// blocks nor publishes.
func TestThreadHistoryCloseStopsTheGoroutine(t *testing.T) {
	hx := newHistoryHarness(t)
	first := hx.record(t, "first")
	hx.updatesThrough(t, first.Offset+first.Length)
	hx.history.close()
	select {
	case <-hx.history.done:
	default:
		t.Fatal("close returned with the projection goroutine still running")
	}
	hx.record(t, "after close")
	hx.expectQuiet(t)
	hx.history.close()
}

// An entry that does not decode is quarantined, not a failure: it reaches
// clients as one unreadable-entry item naming its ordinal, with no resync,
// and the entries after it publish as usual.
func TestThreadHistoryQuarantinesAnUnreadableEntry(t *testing.T) {
	hx := newHistoryHarness(t)
	first := hx.record(t, "first")
	hx.updatesThrough(t, first.Offset+first.Length)
	hx.corruptNext(t)
	bad := hx.record(t, "unreadable")
	after := hx.record(t, "after")
	updates := hx.updatesThrough(t, after.Offset+after.Length)
	var unreadable, afterItem bool
	for _, params := range updates {
		for _, item := range params.Items {
			unreadable = unreadable || (item.EventKind == appwire.ThreadItemEventKindError && strings.Contains(item.Text, fmt.Sprintf("entry %d", bad.Ordinal)))
			afterItem = afterItem || item.Text == "after"
		}
	}
	if !unreadable || !afterItem {
		t.Fatalf("updates %+v, want the unreadable entry's notice and the entry after it", updates)
	}
	if err := hx.history.Failed(); err != nil || hx.history.Epoch() != 0 {
		t.Fatalf("Failed() = %v at epoch %d; want a healthy history", err, hx.history.Epoch())
	}
	hx.expectQuiet(t)
}

// A gap that reopens after a range containing an undecodable line must
// resume numbering from the lines actually consumed, not from
// gap.ordinal + len(decoded records): the undecodable line took an ordinal
// too, so counting only decoded records undercounts every entry replayed
// after it. fillOverlayGap's own loop only reopens a gap when another
// overflow lands mid-replay (rare and timing-dependent in production); the
// threadHistoryFillGapHook test seam lands it deterministically instead of
// racing a real one.
func TestFillOverlayGapAfterAnUndecodableLineResumesAtTheRightOrdinal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gap_ordinal.transcript.jsonl")
	writer, err := transcript.NewWriterNoSync(path, transcript.Header{SessionID: "th_gap"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	first := recordLine(t, writer, schema.NewTurn(schema.TurnUserInput, llm.User("first")))
	garbage := recordLine(t, writer, schema.NewTurn(schema.TurnUserInput, llm.User("garbage")))
	corruptLine(t, path, garbage)
	filler := recordLine(t, writer, schema.NewTurn(schema.TurnUserInput, llm.User("filler")))
	call := llm.ToolCallData{ID: "call1", Name: "shell", Arguments: json.RawMessage(`{}`)}
	assistant := recordLine(t, writer, schema.Turn{
		Kind:    schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}},
	})
	// results is never itself replayed: it stays past what the gap ever
	// covers, so the test controls exactly when the overlay learns of it
	// (below), after the live tool-call-start below, matching production's
	// order (the call starts live before its own TOOL_RESULTS is recorded).
	results := recordLine(t, writer, schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call1", "shell", "done", false)))

	// The lines, in ordinal order: first(0), garbage(1, undecodable),
	// filler(2), assistant(3, the tool call), results(4). The true ordinal
	// of the assistant entry is 3.
	const assistantTrueOrdinal = 3

	cache := transcriptindex.NewCache(transcriptindex.DefaultCacheCapacity)
	t.Cleanup(func() { _ = cache.Close() })
	overlay := appoverlay.New(appoverlay.NewBudget(1 << 20))
	t.Cleanup(overlay.Close)
	h := newThreadHistory(threadHistoryConfig{
		threadID:       "th_gap",
		ref:            "local:th_gap",
		bootGeneration: "1",
		path:           path,
		cache:          cache,
		overlay:        overlay,
		publish:        func(appwire.HistoryUpdatedParams) error { return nil },
		resync:         func(uint64) {},
	})
	t.Cleanup(h.close)

	// The gap opens right after "first" and is set to close after "filler"
	// (a range containing the undecodable "garbage" line); the hook then
	// extends it to cover "assistant" too, simulating a second overflow
	// landing while this replay was in flight. "results" stays outside
	// every replay range; the test feeds it explicitly, later.
	h.mu.Lock()
	h.gap = &overlayGap{ordinal: 1, offset: first.Offset + first.Length}
	h.recordedLength = filler.Offset + filler.Length
	h.mu.Unlock()
	bumped := false
	threadHistoryFillGapHook = func(threadID string) {
		if bumped || threadID != "th_gap" {
			return
		}
		bumped = true
		h.mu.Lock()
		h.recordedLength = assistant.Offset + assistant.Length
		h.mu.Unlock()
	}
	t.Cleanup(func() { threadHistoryFillGapHook = nil })

	if err := h.fillOverlayGap(); err != nil {
		t.Fatalf("fillOverlayGap: %v", err)
	}
	if !bumped {
		t.Fatal("setup: the hook never fired, so this never exercised a reopened gap")
	}
	h.mu.Lock()
	closedGap := h.gap
	h.mu.Unlock()
	if closedGap != nil {
		t.Fatalf("gap = %+v after replay, want closed", closedGap)
	}

	// The assistant entry's tool call was learned at replay time (from the
	// wrong ordinal, if the bug is present); a live TOOL_CALL_START arriving
	// after replay picks up whatever historyKey learnCalls computed.
	changes := overlay.Event(events.SessionEvent{
		Kind: events.EventToolCallStart,
		Data: events.ToolCallStartData{ToolName: "shell", CallID: "call1"},
	})
	if len(changes) != 1 {
		t.Fatalf("tool call start produced %d changes, want 1", len(changes))
	}
	upserted, ok := changes[0].Params.(appwire.OverlayUpsertedParams)
	if !ok {
		t.Fatalf("change params = %T, want OverlayUpsertedParams", changes[0].Params)
	}
	want := transcriptindex.ItemKey("", appwire.ThreadItemPosition{Entry: assistantTrueOrdinal + 1, Item: 0})
	if got := upserted.Item.HistoryKey; got != want {
		t.Fatalf("tool call historyKey = %q, want %q (the assistant entry's true ordinal %d)", got, want, assistantTrueOrdinal)
	}

	// The later TOOL_RESULTS entry prunes the running slot regardless (it
	// matches by call id, not by the possibly-wrong historyKey), but a
	// stale slot is still worth pinning: it must be gone.
	overlay.Recorded(transcript.Record{
		Recorded: true,
		Ordinal:  assistantTrueOrdinal + 1,
		Offset:   results.Offset,
		Length:   results.Length,
		Turn:     schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call1", "shell", "done", false)),
	})
	if got := overlay.Snapshot(); slotForCall(got, "call1") {
		t.Fatalf("the tool call's overlay slot is still running after its TOOL_RESULTS: %+v", got)
	}
}

// recordLine appends turn and returns its record, failing the test if it did
// not land.
func recordLine(t *testing.T, w *transcript.Writer, turn schema.Turn) transcript.Record {
	t.Helper()
	rec, err := w.Record(turn, transcript.RecordOptions{})
	if err != nil || !rec.Recorded {
		t.Fatalf("record %s = %+v, %v", turn.Kind, rec, err)
	}
	return rec
}

// corruptLine rewrites rec's line in place to one that fails to decode, same
// length as corruptNext's technique.
func corruptLine(t *testing.T, path string, rec transcript.Record) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	original := data[rec.Offset : rec.Offset+rec.Length]
	corrupt := bytes.Replace(original, []byte(`"kind":"entry"`), []byte(`"kind":"entrx"`), 1)
	if bytes.Equal(corrupt, original) {
		t.Fatalf("entry line %s has no record kind to corrupt", original)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // fixture
	if _, err := f.WriteAt(corrupt, rec.Offset); err != nil {
		t.Fatal(err)
	}
}

// slotForCall reports whether snapshot still carries a running tool slot for
// callID.
func slotForCall(snapshot []appwire.OverlayItem, callID string) bool {
	for _, item := range snapshot {
		if item.CallID == callID && item.Kind == appwire.OverlayTool {
			return true
		}
	}
	return false
}
