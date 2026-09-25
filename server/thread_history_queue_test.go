package server

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// smallQueueBytes is the queue bound these tests give a harness, so a few
// entries overflow it.
const smallQueueBytes = 1 << 10

// padded is a user input about a quarter of smallQueueBytes long, so five of
// them overflow the queue.
func padded(text string) string {
	return text + " " + strings.Repeat("x", smallQueueBytes/4)
}

// parkedPublishes parks the projection goroutine in its n'th publish (1-based)
// until release is called; parked is closed once it is there.
type parkedPublishes struct {
	parked  chan struct{}
	release func()
}

func parkPublish(t *testing.T, n int, fail func(call int) error) *parkedPublishes {
	t.Helper()
	p := &parkedPublishes{parked: make(chan struct{})}
	gate := make(chan struct{})
	p.release = sync.OnceFunc(func() { close(gate) })
	var calls int
	threadHistoryPublishHook = func(string) error {
		calls++
		if calls == n {
			close(p.parked)
			<-gate
		}
		if fail != nil {
			return fail(calls)
		}
		return nil
	}
	t.Cleanup(func() {
		p.release()
		threadHistoryPublishHook = nil
	})
	return p
}

func awaitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(historyTestWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// readKeys is every item key a latest-window read of the whole history
// returns.
func (hx *historyHarness) readKeys(t *testing.T) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	turns, cursor, _, _, err := hx.history.latest(hx.history.capture(), "local:th_history", 40)
	for {
		if err != nil {
			t.Fatal(err)
		}
		for _, turn := range turns {
			for _, item := range turn.Items {
				keys[item.TranscriptKey] = true
			}
		}
		if cursor == "" {
			return keys
		}
		turns, cursor, _, err = hx.history.before("local:th_history", cursor, 40)
	}
}

// publishedOnce fails if any item key was published twice across updates.
func publishedOnce(t *testing.T, updates []appwire.HistoryUpdatedParams) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	for _, params := range updates {
		for _, item := range params.Items {
			if seen[item.TranscriptKey] {
				t.Fatalf("item %s published twice", item.TranscriptKey)
			}
			seen[item.TranscriptKey] = true
		}
	}
	return seen
}

// The recorded hook takes the queue mutex and nothing else: it runs to the
// end while every other lock the server, the registry and the history own is
// held elsewhere.
func TestRecordedHookTakesOnlyTheQueueMutex(t *testing.T) {
	srv := NewServer(ServerConfig{})
	st := newServedTranscript(t, srv, "root")
	history := srv.appHistoryForID("root")
	if history == nil {
		t.Fatal("no history")
	}
	srv.mu.Lock()
	srv.appHistories.mu.Lock()
	history.applyMu.Lock()
	recorded := make(chan struct{})
	go func() {
		defer close(recorded)
		st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("under every other lock")))
	}()
	select {
	case <-recorded:
	case <-time.After(historyTestWait):
		t.Error("the recorded hook blocked on a lock other than the queue mutex")
	}
	history.applyMu.Unlock()
	srv.appHistories.mu.Unlock()
	srv.mu.Unlock()
	<-recorded
	st.settle(t)
}

// A descendant record that finds no history is dropped: the hook never
// creates one (that happens on the event path).
func TestDescendantHookDropsARecordWithNoHistory(t *testing.T) {
	root := newServedTranscript(t, NewServer(ServerConfig{}), "root")
	srv := root.srv
	childPath := writeDelegateTranscript(t, "child", "child history")
	srv.SetDescendantTranscriptPathFunc(func(string) string { return childPath })
	srv.recordDescendantTranscriptEntry("root", "child", transcript.Record{Recorded: true, Ordinal: 0, Offset: 1, Length: 1})
	if srv.appHistories.get("child") != nil {
		t.Fatal("the descendant hook created a history")
	}
}

// Entries recorded while a rebuild is parked come after its boundary, which
// was captured when the rebuild started: each is published exactly once, after
// the rebuild.
func TestThreadHistoryEntriesDuringARebuildPublishOnce(t *testing.T) {
	var publishes atomic.Int32
	threadHistoryPublishHook = func(string) error {
		if publishes.Add(1) == 2 {
			return errors.New("injected publish failure")
		}
		return nil
	}
	t.Cleanup(func() { threadHistoryPublishHook = nil })
	parked, release := make(chan struct{}), make(chan struct{})
	var rebuilds int
	threadHistoryRebuildHook = func(string) error {
		rebuilds++
		if rebuilds == 1 {
			close(parked)
			<-release
		}
		return nil
	}
	t.Cleanup(func() { threadHistoryRebuildHook = nil })
	hx := newHistoryHarness(t)
	first := hx.record(t, "first")
	updates := hx.updatesThrough(t, first.Offset+first.Length)
	hx.record(t, "second") // its publish fails: resync, then the parked rebuild
	if epoch := hx.nextResync(t); epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	awaitClosed(t, parked, "the rebuild to park")
	var during []transcript.Record
	for i := range 3 {
		during = append(during, hx.record(t, fmt.Sprintf("during %d", i)))
	}
	close(release)
	last := during[len(during)-1]
	updates = append(updates, hx.updatesThrough(t, last.Offset+last.Length)...)
	seen := publishedOnce(t, updates)
	for _, rec := range during {
		found := false
		for key := range seen {
			found = found || strings.Contains(key, fmt.Sprintf(":%d:", rec.Ordinal))
		}
		if !found {
			t.Fatalf("entry %d recorded during the rebuild was not published", rec.Ordinal)
		}
	}
	hx.expectQuiet(t)
}

// A failed thread drops its queue and stops enqueueing: later appends leave
// it empty.
func TestThreadHistoryFailedThreadQueuesNothing(t *testing.T) {
	hx := newHistoryHarness(t)
	first := hx.record(t, "first")
	hx.updatesThrough(t, first.Offset+first.Length)
	breakProjection(t, nil)
	hx.record(t, "fails to project")
	hx.nextResync(t)
	hx.nextResync(t)
	if hx.history.Failed() == nil {
		t.Fatal("history not failed")
	}
	for i := range 3 {
		hx.record(t, fmt.Sprintf("after the failure %d", i))
	}
	hx.history.mu.Lock()
	queued, bytes := len(hx.history.queue), hx.history.queuedBytes
	hx.history.mu.Unlock()
	if queued != 0 || bytes != 0 {
		t.Fatalf("failed thread queued %d entries (%d bytes)", queued, bytes)
	}
}

// A queue that overflows while the projection goroutine is parked is dropped
// without blocking the append lock; after release exactly one resync goes
// out, the rebuild covers every entry (no item is published twice, and a
// read holds them all), the overlay has seen every entry, and later entries
// publish under the new epoch.
func TestThreadHistoryQueueOverflowResyncsOnce(t *testing.T) {
	park := parkPublish(t, 1, nil)
	hx := newHistoryHarnessWith(t, smallQueueBytes)
	first := hx.record(t, "first")
	awaitClosed(t, park.parked, "the first publish to park")
	var last transcript.Record
	for i := range 6 {
		last = hx.record(t, padded(fmt.Sprintf("overflowing %d", i)))
	}
	hx.history.mu.Lock()
	queued := hx.history.queuedBytes
	hx.history.mu.Unlock()
	if queued > smallQueueBytes {
		t.Fatalf("queue holds %d bytes past its %d bound", queued, smallQueueBytes)
	}
	park.release()
	updates := hx.updatesThrough(t, first.Offset+first.Length)
	if epoch := hx.nextResync(t); epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	hx.awaitPublished(t, last.Offset+last.Length)
	after := hx.record(t, "after the resync")
	later := hx.updatesThrough(t, after.Offset+after.Length)
	if later[len(later)-1].Epoch != 1 {
		t.Fatalf("update after the resync at epoch %d, want 1", later[len(later)-1].Epoch)
	}
	publishedOnce(t, append(updates, later...))
	if keys := hx.readKeys(t); len(keys) != int(after.Ordinal)+1 {
		t.Fatalf("read after the overflow holds %d items, want %d", len(keys), after.Ordinal+1)
	}
	hx.history.overlayEvent(events.New(events.LoopDetectionData{Message: "loop"}))
	snapshot := hx.overlay.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Anchor == nil || snapshot[0].Anchor.Entry != after.Ordinal+1 {
		t.Fatalf("overlay anchored %+v, want past every entry (%d); last overflowing entry %d", snapshot, after.Ordinal+1, last.Ordinal)
	}
	hx.expectQuiet(t)
}

// A queue that overflows while a rebuild is parked restarts the rebuild
// through a new boundary: one resync, and every entry reaches the client
// once.
func TestThreadHistoryOverflowDuringARebuildRestartsIt(t *testing.T) {
	var publishes atomic.Int32
	threadHistoryPublishHook = func(string) error {
		if publishes.Add(1) == 2 {
			return errors.New("injected publish failure")
		}
		return nil
	}
	t.Cleanup(func() { threadHistoryPublishHook = nil })
	parked, release := make(chan struct{}), make(chan struct{})
	var rebuilds int
	threadHistoryRebuildHook = func(string) error {
		rebuilds++
		if rebuilds == 1 {
			close(parked)
			<-release
		}
		return nil
	}
	t.Cleanup(func() { threadHistoryRebuildHook = nil })
	hx := newHistoryHarnessWith(t, smallQueueBytes)
	first := hx.record(t, "first")
	updates := hx.updatesThrough(t, first.Offset+first.Length)
	hx.record(t, "second")
	if epoch := hx.nextResync(t); epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	awaitClosed(t, parked, "the rebuild to park")
	var last transcript.Record
	for i := range 6 {
		last = hx.record(t, padded(fmt.Sprintf("overflowing %d", i)))
	}
	close(release)
	hx.awaitPublished(t, last.Offset+last.Length)
	after := hx.record(t, "after")
	updates = append(updates, hx.updatesThrough(t, after.Offset+after.Length)...)
	if rebuilds != 2 {
		t.Fatalf("rebuilds = %d, want the parked one and one restart", rebuilds)
	}
	publishedOnce(t, updates)
	if keys := hx.readKeys(t); len(keys) != int(after.Ordinal)+1 {
		t.Fatalf("read holds %d items, want %d", len(keys), after.Ordinal+1)
	}
	hx.expectQuiet(t)
}

// Rebuilds that the queue overflows every time fail the thread after the
// third, instead of rebuilding forever; a later read that succeeds recovers
// it, and live updates resume.
func TestThreadHistoryOverflowingEveryRebuildFailsThenAReadRecovers(t *testing.T) {
	var publishes atomic.Int32
	threadHistoryPublishHook = func(string) error {
		if publishes.Add(1) == 2 {
			return errors.New("injected publish failure")
		}
		return nil
	}
	t.Cleanup(func() { threadHistoryPublishHook = nil })
	var hx *historyHarness
	var rebuilds int
	overflowing := true
	threadHistoryRebuildHook = func(string) error {
		rebuilds++
		if overflowing {
			for i := range 6 {
				hx.record(t, padded(fmt.Sprintf("rebuild %d overflow %d", rebuilds, i)))
			}
		}
		return nil
	}
	t.Cleanup(func() { threadHistoryRebuildHook = nil })
	hx = newHistoryHarnessWith(t, smallQueueBytes)
	first := hx.record(t, "first")
	hx.updatesThrough(t, first.Offset+first.Length)
	hx.record(t, "second")
	if epoch := hx.nextResync(t); epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	if epoch := hx.nextResync(t); epoch != 2 {
		t.Fatalf("failed-state resync epoch = %d, want 2", epoch)
	}
	if rebuilds != threadHistoryMaxRebuilds || hx.history.Failed() == nil {
		t.Fatalf("rebuilds = %d, failed = %v; want the thread failed after %d", rebuilds, hx.history.Failed(), threadHistoryMaxRebuilds)
	}

	overflowing = false
	keys := hx.readKeys(t)
	if epoch := hx.nextResync(t); epoch != 3 {
		t.Fatalf("recovery resync epoch = %d, want 3", epoch)
	}
	if len(keys) == 0 {
		t.Fatal("the recovering read returned nothing")
	}
	hx.awaitPublished(t, hx.writer.RecordedLength())
	after := hx.record(t, "after recovery")
	updates := hx.updatesThrough(t, after.Offset+after.Length)
	if updates[len(updates)-1].Epoch != 3 || hx.history.Failed() != nil {
		t.Fatalf("after recovery: epoch %d, failed %v", updates[len(updates)-1].Epoch, hx.history.Failed())
	}
}

// Appends from several goroutines while the projection rebuilds over and over
// never deadlock, and the thread ends up holding every entry.
func TestThreadHistoryAppendsDuringRepeatedRebuilds(t *testing.T) {
	var publishes atomic.Int32
	var failing atomic.Bool
	failing.Store(true)
	threadHistoryPublishHook = func(string) error {
		if publishes.Add(1)%2 == 0 && failing.Load() {
			return errors.New("injected publish failure")
		}
		return nil
	}
	t.Cleanup(func() { threadHistoryPublishHook = nil })
	hx := newHistoryHarnessWith(t, smallQueueBytes)
	// Consume what the history publishes, so a full channel never holds the
	// projection goroutine.
	consumed := make(chan struct{})
	t.Cleanup(func() { close(consumed) })
	go func() {
		for {
			select {
			case <-hx.updates:
			case <-hx.resyncs:
			case <-consumed:
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for g := range 4 {
			wg.Go(func() {
				for i := range 25 {
					text := fmt.Sprintf("goroutine %d entry %d", g, i)
					if i%5 == 0 {
						text = padded(text)
					}
					hx.record(t, text)
				}
			})
		}
		wg.Wait()
	}()
	// A tripwire, not a wait: a deadlock between the append lock, the queue
	// mutex and the projection would hold the appenders forever.
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("appends deadlocked against the rebuilding projection")
	}
	failing.Store(false)
	final := hx.record(t, "final")
	hx.awaitPublished(t, final.Offset+final.Length)
	if err := hx.history.Failed(); err != nil {
		t.Fatalf("history failed: %v", err)
	}
	if keys := hx.readKeys(t); len(keys) != int(final.Ordinal)+1 {
		t.Fatalf("read holds %d items, want %d", len(keys), final.Ordinal+1)
	}
}

// Closing a history first projects and publishes every entry recorded up to
// its recorded length, then stops.
func TestThreadHistoryClosePublishesWhatItHas(t *testing.T) {
	park := parkPublish(t, 1, nil)
	hx := newHistoryHarness(t)
	first := hx.record(t, "first")
	awaitClosed(t, park.parked, "the first publish to park")
	hx.record(t, "second")
	last := hx.record(t, "last before close")
	closed := make(chan struct{})
	go func() {
		hx.history.close()
		close(closed)
	}()
	<-hx.history.stop
	park.release()
	awaitClosed(t, closed, "close")
	var got []appwire.HistoryUpdatedParams
	for len(hx.updates) > 0 {
		got = append(got, <-hx.updates)
	}
	if len(got) == 0 || got[0].Snapshot.Length != first.Offset+first.Length || got[len(got)-1].Snapshot.Length != last.Offset+last.Length {
		t.Fatalf("updates before close covered %v, want through %d", snapshotLengths(got), last.Offset+last.Length)
	}
}

func snapshotLengths(updates []appwire.HistoryUpdatedParams) []int64 {
	var lengths []int64
	for _, params := range updates {
		lengths = append(lengths, params.Snapshot.Length)
	}
	return lengths
}

// A delegate's entries recorded just before its closing SESSION_END reach
// clients as history/updated: the release publishes them before it stops.
func TestAClosingDelegatePublishesItsLastEntries(t *testing.T) {
	root := newServedTranscript(t, NewServer(ServerConfig{}), "root")
	srv := root.srv
	childPath := writeDelegateTranscript(t, "child", "child history")
	srv.SetDescendantTranscriptPathFunc(func(string) string { return childPath })
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.SessionStartData{}))
	child, _, err := transcript.OpenWriterForSession(childPath, "child")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Close() })
	child.OnRecorded(func(rec transcript.Record) { srv.recordDescendantTranscriptEntry("root", "child", rec) })
	park := parkPublish(t, 1, nil)
	cursor := srv.appNotifier.CurrentSequence()
	if _, err := child.Record(schema.NewTurn(schema.TurnUserInput, llm.User("parked")), transcript.RecordOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitClosed(t, park.parked, "the delegate's publish to park")
	if _, err := child.Record(schema.NewTurn(schema.TurnUserInput, llm.User("last words")), transcript.RecordOptions{}); err != nil {
		t.Fatal(err)
	}
	history := srv.appHistories.get("child")
	go func() {
		<-history.stop
		park.release()
	}()
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.SessionEndData{Reason: "shutdown", State: appwire.ThreadStatusClosed}))
	published := false
	for _, n := range srv.AppNotificationsAfter(cursor, "child") {
		if n.Notification.Method == appwire.NotifyHistoryUpdated {
			for _, item := range notificationParams[appwire.HistoryUpdatedParams](t, n).Items {
				published = published || item.Text == "last words"
			}
		}
	}
	if !published {
		t.Fatal("the delegate's last entry did not reach clients before its history closed")
	}
}
