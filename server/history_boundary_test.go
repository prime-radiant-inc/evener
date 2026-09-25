package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// The transcript read model's boundary tests (plan Task 18): each pauses at
// a synchronization boundary -- a channel, a recorded hook, an ordering of
// direct writer calls -- never a sleep or a timing assertion. Tests that
// drive a real session through a scripted provider reuse the parity
// harness's helpers (parityProvider, bridgeParitySession, newScriptedSession);
// tests that only need control over which entries land when drive a
// servedTranscript or a historyHarness directly.

// TestReadBetweenAssistantAndCommunicate holds the COMMUNICATE entry back --
// simply by not writing it yet -- after an ASSISTANT round is recorded and
// its streaming preview is live in the overlay. A read in between sees the
// ASSISTANT items and the preview; once COMMUNICATE is written and
// published, the reduced client holds the message exactly once (not also as
// a leftover preview).
func TestReadBetweenAssistantAndCommunicate(t *testing.T) {
	hx := newHistoryHarness(t)
	question := hx.record(t, "a question")
	hx.updatesThrough(t, question.Offset+question.Length)

	// Round 1: text and reasoning, recorded as the ASSISTANT entry.
	assistant := hx.recordAssistantEntry(t, "r_1", "", "reasoning, then text")
	hx.awaitPublished(t, assistant.Offset+assistant.Length)

	// Round 2 starts streaming toward COMMUNICATE; its preview is live in the
	// overlay while COMMUNICATE itself is held back (simply not recorded
	// yet).
	hx.overlay.Event(events.New(events.RoundStartedData{RoundID: "r_2"}))
	hx.overlay.Event(events.New(events.AssistantTextDeltaData{Delta: "drafting the reply"}))
	turns, _, _, overlay, err := hx.history.latest(hx.history.capture(), "local:th_history", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || len(turns[0].Items) != 2 || turns[0].Items[1].Text != "reasoning, then text" {
		t.Fatalf("read before COMMUNICATE = %+v, want the question and the ASSISTANT entry", turns)
	}
	if len(overlay) != 1 || overlay[0].Kind != appwire.OverlayStream {
		t.Fatalf("overlay = %+v, want round 2's streaming preview", overlay)
	}

	client := newHistoryClient(appwire.ThreadReadResponse{Thread: appwire.Thread{Turns: turns}})
	comm := hx.recordTurn(t, schema.Turn{
		Kind: schema.TurnCommunicate, Format: schema.TurnFormatIdentity, TurnID: "turn_comm_1",
		Communicate: &schema.CommunicateInfo{Message: "the whole answer", EndTurn: true},
	})
	updates := hx.updatesThrough(t, comm.Offset+comm.Length)
	for _, u := range updates {
		client.apply(t, u)
	}
	final := client.turnsInOrder()
	count := 0
	for _, turn := range final {
		for _, item := range turn.Items {
			if item.Text == "the whole answer" {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("the reduced client holds %d copies of the answer, want 1: %+v", count, final)
	}
}

// recordAssistantEntry records an ASSISTANT entry under roundID, without the
// pricing hx.recordAssistant folds in.
func (hx *historyHarness) recordAssistantEntry(t *testing.T, roundID, model, text string) transcript.Record {
	t.Helper()
	turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
	turn.RoundID, turn.Model = roundID, model
	return hx.recordTurn(t, turn)
}

// TestReclaimedTurn writes a client-mutation execution's opening entry with
// no completion, then reopens it (TURN_REOPEN, the way a real reclaim does:
// the same TurnID, TurnKind still execution) before writing its completion.
// While it is open the served thread's status is inProgress with
// ActiveTurnID naming it; once it completes, ActiveTurnID is empty.
func TestReclaimedTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	st := newServedTranscript(t, srv, "reclaim-thread")
	turnID := "turn_m1"
	st.record(t, inExecution(turnID, true, schema.NewTurn(schema.TurnUserInput, llm.User("before the crash"))))
	st.settle(t)

	startExecution(srv, "reclaim-thread", turnID)
	st.record(t, inExecution(turnID, false, schema.Turn{Kind: schema.TurnReopen}))
	st.settle(t)
	read := st.read(t)
	if read.Thread.Evener.ActiveTurnID != turnID {
		t.Fatalf("evener.activeTurnId = %+v, want %q while the reclaimed turn runs", read.Thread.Evener, turnID)
	}
	reopened := findTurn(t, read, turnID)
	if reopened.Status != appwire.TurnStatusInProgress {
		t.Fatalf("reclaimed turn status = %q, want inProgress while running", reopened.Status)
	}

	now := time.Now().UTC()
	st.record(t, inExecution(turnID, false, schema.Turn{
		Kind:       schema.TurnCompletion,
		Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted, CompletedAt: now},
	}))
	srv.SetProcessingTurn("")
	st.settle(t)
	read = st.read(t)
	if read.Thread.Evener.ActiveTurnID != "" {
		t.Fatalf("evener.activeTurnId = %q after completion, want empty", read.Thread.Evener.ActiveTurnID)
	}
	completed := findTurn(t, read, turnID)
	if completed.Status != appwire.TurnStatusCompleted {
		t.Fatalf("reclaimed turn status = %q after completion, want completed", completed.Status)
	}
}

func findTurn(t *testing.T, read appwire.ThreadReadResponse, turnID string) appwire.Turn {
	t.Helper()
	for _, turn := range read.Thread.Turns {
		if turn.ID == turnID {
			return turn
		}
	}
	t.Fatalf("no turn %q in the read: %+v", turnID, read.Thread.Turns)
	return appwire.Turn{}
}

// TestPaginatedReadOverlappingCrossProcessRollback simulates a second
// process's write and its rollback: a second writer sharing the file's
// append tail (transcript.OpenWriterForSession, real filesystem) extends the
// file with one entry, another handle's index catches up to that longer
// file the way another process's own index would, and then the write is
// rolled back -- the file truncated to what it covered before. The index
// sees the file stop extending (transcriptindex's extend(): grownByAppends
// fails once info.Size() < the covered length) and rebuilds under a new
// incarnation. A backfill cursor minted before the rollback is stale; the
// next latest read carries the new incarnation, so a client replaces its
// history rather than merging into it.
func TestPaginatedReadOverlappingCrossProcessRollback(t *testing.T) {
	hx := newHistoryHarness(t)
	hx.record(t, "one")
	two := hx.record(t, "two")
	hx.updatesThrough(t, two.Offset+two.Length)

	_, older, snapshotBefore, _, err := hx.history.latest(hx.history.capture(), "local:th_history", 1)
	if err != nil || older == "" {
		t.Fatalf("latest = cursor %q, %v; want an older cursor", older, err)
	}

	w2, _, err := transcript.OpenWriterForSession(hx.path, "th_history")
	if err != nil {
		t.Fatal(err)
	}
	extended, err := w2.Record(schema.NewTurn(schema.TurnUserInput, llm.User("from another process")), transcript.RecordOptions{})
	if err != nil || !extended.Recorded {
		t.Fatalf("second writer's record = %+v, %v", extended, err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
	idx, err := hx.cache.Acquire(hx.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.CatchUpTo(extended.Offset + extended.Length); err != nil {
		t.Fatal(err)
	}
	hx.cache.Release(idx)
	if err := os.Truncate(hx.path, extended.Offset); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := hx.history.before("local:th_history", older, 1); !isTranscriptItemCursorStale(err) {
		t.Fatalf("before after the rollback = %v, want stale", err)
	}

	_, _, snapshotAfter, _, err := hx.history.latest(hx.history.capture(), "local:th_history", 10)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotAfter.Incarnation == snapshotBefore.Incarnation {
		t.Fatalf("incarnation after the rollback = %q, want a new one (was %q)", snapshotAfter.Incarnation, snapshotBefore.Incarnation)
	}
}

// TestReadOverlappingRetryResetAndNextAttempt starts a round's first
// attempt's stream, resets it (a retried round: AssistantTextReset ends
// attempt 1 and moves the overlay to attempt 2), and starts attempt 2's
// stream -- pausing right there, with the reset and attempt 2's first delta
// both committed. A read at that point holds only attempt 2's stream:
// attempt 1's was discarded by the reset. Once ASSISTANT is recorded for
// attempt 2, the reduced client applies its later deltas once, not twice
// alongside the recorded text.
func TestReadOverlappingRetryResetAndNextAttempt(t *testing.T) {
	hx := newHistoryHarness(t)
	question := hx.record(t, "a question")
	hx.updatesThrough(t, question.Offset+question.Length)

	hx.overlay.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	hx.overlay.Event(events.New(events.AssistantTextDeltaData{Delta: "attempt one, cut short"}))
	hx.overlay.Event(events.New(events.AssistantTextResetData{}))
	hx.overlay.Event(events.New(events.AssistantTextDeltaData{Delta: "attempt two, "}))

	captured := hx.history.capture()
	if len(captured.overlay) != 1 || captured.overlay[0].Kind != appwire.OverlayStream || captured.overlay[0].Item.Text != "attempt two, " {
		t.Fatalf("captured overlay = %+v, want only attempt two's stream", captured.overlay)
	}
	turns, _, _, overlay, err := hx.history.latest(captured, "local:th_history", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlay) != 1 || overlay[0].Item.Text != "attempt two, " {
		t.Fatalf("read overlay = %+v, want only attempt two's stream", overlay)
	}

	client := newHistoryClient(appwire.ThreadReadResponse{Thread: appwire.Thread{Turns: turns}})
	hx.overlay.Event(events.New(events.AssistantTextDeltaData{Delta: "the rest"}))
	final := hx.recordAssistantEntry(t, "r_1", "", "attempt two, the rest")
	updates := hx.updatesThrough(t, final.Offset+final.Length)
	for _, u := range updates {
		client.apply(t, u)
	}
	result := client.turnsInOrder()
	count := 0
	for _, turn := range result {
		for _, item := range turn.Items {
			if item.Text == "attempt two, the rest" {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("client holds %d copies of the final text, want 1: %+v", count, result)
	}
}

// TestFailedHistoryPublication fails one publish; the thread resyncs at a
// new epoch and a client that re-reads on the resync (the reducer's rule for
// any resync) holds every item once the rebuild catches back up, none
// missing.
func TestFailedHistoryPublication(t *testing.T) {
	var failNext atomic.Bool
	threadHistoryPublishHook = func(string) error {
		if failNext.CompareAndSwap(true, false) {
			return errors.New("injected publish failure")
		}
		return nil
	}
	t.Cleanup(func() { threadHistoryPublishHook = nil })
	hx := newHistoryHarness(t)
	one := hx.record(t, "one")
	hx.updatesThrough(t, one.Offset+one.Length)

	failNext.Store(true)
	hx.record(t, "two")
	epoch := hx.nextResync(t)
	if epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	three := hx.record(t, "three")
	hx.awaitPublished(t, three.Offset+three.Length)

	// The client re-reads on the resync instead of merging deltas across it.
	turns, older, _, _, err := hx.history.latest(hx.history.capture(), "local:th_history", 10)
	if err != nil {
		t.Fatal(err)
	}
	if older != "" {
		t.Fatalf("older cursor %q, want the whole history in one page", older)
	}
	texts := map[string]bool{}
	for _, turn := range turns {
		for _, item := range turn.Items {
			texts[item.Text] = true
		}
	}
	for _, want := range []string{"one", "two", "three"} {
		if !texts[want] {
			t.Fatalf("re-read after the resync = %v, missing %q", texts, want)
		}
	}
}

// TestFailedResyncDelivery is a narrower proxy for the plan's scenario (a
// connection whose full outbound buffer gets it evicted, then reconnects):
// it does not drive internal/appserver's eviction path at all, only checks
// the contract a reconnect's fresh read depends on -- that the thread's
// epoch after a resync is the resync's epoch, not a stale one -- since a
// reconnect is always a fresh latest-window read at the thread's current
// epoch, never a replay of what the evicted connection missed. Driving a
// real eviction (internal/appserver.Server.evictSlowConsumer, a full
// Connection.send channel) end to end through a served session is not done
// here.
func TestFailedResyncDelivery(t *testing.T) {
	hx := newHistoryHarness(t)
	one := hx.record(t, "one")
	hx.updatesThrough(t, one.Offset+one.Length)

	threadHistoryPublishHook = func(string) error { return errBrokenProjection }
	threadHistoryRebuildHook = nil
	t.Cleanup(func() { threadHistoryPublishHook = nil })
	hx.record(t, "two")
	epoch := hx.nextResync(t)
	if epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	threadHistoryPublishHook = nil
	three := hx.record(t, "three")
	hx.awaitPublished(t, three.Offset+three.Length)

	// A reconnect after the eviction is a fresh subscribe: it reads the
	// thread's live epoch, not whatever it held before the drop.
	if got := hx.history.Epoch(); got != epoch {
		t.Fatalf("history epoch = %d, want the resync's epoch %d for the reconnect read to carry", got, epoch)
	}
}

// TestAsyncAttentionWriteAfterCompletion writes a delegate's attention entry
// through PlaceAsync (agent/transcript/placement.go: the door for a write
// from another goroutine, not the session's own turn loop) after the running
// execution's completion is already recorded. PlaceAsync "never joins a turn
// whose completion is already recorded" -- it gets a delivery turn of its
// own -- so the delivery must appear in history as its own turn, never
// reopening the turn that already completed.
func TestAsyncAttentionWriteAfterCompletion(t *testing.T) {
	hx := newHistoryHarness(t)
	turnID := "turn_exec_1"
	hx.writer.BeginExecution(turnID, false)
	opened, err := hx.writer.Record(schema.Turn{
		Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution,
		Kind: schema.TurnUserInput, Message: llm.User("running"),
	}, transcript.RecordOptions{Place: transcript.PlaceSession})
	if err != nil || !opened.Recorded {
		t.Fatalf("open the execution: %+v, %v", opened, err)
	}
	completed, err := hx.writer.Record(schema.Turn{
		Kind: schema.TurnCompletion,
		Completion: &schema.TurnCompletionInfo{
			Status: schema.TurnCompleted, CompletedAt: time.Now().UTC(),
		},
	}, transcript.RecordOptions{Place: transcript.PlaceCompletion})
	if err != nil || !completed.Recorded {
		t.Fatalf("complete the execution: %+v, %v", completed, err)
	}
	hx.awaitPublished(t, completed.Offset+completed.Length)

	// The attention delivery, released only now that the completion is
	// already on disk: released after completion, not before.
	delivered, err := hx.writer.Record(schema.Turn{
		Kind: schema.TurnSteering, AttentionID: "attn-1", Message: llm.User("delegate report"),
	}, transcript.RecordOptions{Place: transcript.PlaceAsync})
	if err != nil || !delivered.Recorded {
		t.Fatalf("deliver attention: %+v, %v", delivered, err)
	}
	hx.awaitPublished(t, delivered.Offset+delivered.Length)

	turns := hxReadWhole(t, hx)
	var execTurn, deliveryTurn *appwire.Turn
	for i := range turns {
		switch turns[i].ID {
		case turnID:
			execTurn = &turns[i]
		default:
			for _, item := range turns[i].Items {
				if item.Text == "delegate report" {
					deliveryTurn = &turns[i]
				}
			}
		}
	}
	if execTurn == nil || execTurn.Status != appwire.TurnStatusCompleted {
		t.Fatalf("execution turn = %+v, want it present and completed", execTurn)
	}
	if deliveryTurn == nil {
		t.Fatalf("no turn carries the delivered attention message: %+v", turns)
	}
	if deliveryTurn.ID == execTurn.ID {
		t.Fatal("the attention delivery reopened the completed execution turn")
	}
}

// TestConcurrentAppendsProjectInOrdinalOrder appends through the same
// session writer from two goroutines at once (transcript.Writer.NewWriterNoSync
// takes an exclusive lock on the file, so a genuinely independent "cold"
// writer on the same path cannot be opened from this process; two goroutines
// sharing the one open writer still exercise the writer's append lock as the
// serialization point the hook depends on, which is the finding this test
// pins). The published history/updated stream still covers every ordinal
// exactly once, in order.

// hxReadWhole is the latest window of hx.history with every older page
// prepended, item counting only (turn grouping does not matter here).
func hxReadWhole(t *testing.T, hx *historyHarness) []appwire.Turn {
	t.Helper()
	turns, older, _, _, err := hx.history.latest(hx.history.capture(), "local:th_history", appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	for older != "" {
		var page []appwire.Turn
		page, older, _, err = hx.history.before("local:th_history", older, appwire.TranscriptItemPageLimit)
		if err != nil {
			t.Fatal(err)
		}
		turns = append(page, turns...)
	}
	return turns
}

func TestConcurrentAppendsProjectInOrdinalOrder(t *testing.T) {
	hx := newHistoryHarness(t)
	// Every goroutine appends through hx.writer, the one open handle on the
	// file: the append lock inside transcript.Writer.Record, not the caller,
	// is what serializes the hook's view, exactly as
	// TestThreadHistoryPublishesConcurrentAppendsInOrdinalOrder (Task 7)
	// establishes at the history level; this test pins the same finding
	// through a served read.
	const n = 40
	var wg sync.WaitGroup
	for g := range 2 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for range n {
				hx.record(t, "concurrent")
			}
		}(g)
	}
	wg.Wait()
	last := hx.writer.RecordedLength()
	hx.awaitPublished(t, last)

	turns := hxReadWhole(t, hx)
	count := 0
	for _, turn := range turns {
		count += len(turn.Items)
	}
	if count != 2*n {
		t.Fatalf("projected %d items from %d concurrent appends, want %d, none lost or duplicated", count, 2*n, 2*n)
	}
}

// TestStaleIncarnationResponseOrdering is the server half of plan item 10
// (the TS half is Task 15's reducer test): a read served before a rebuild
// and one served after each carry the request generation the caller gave
// them (ThreadReadParams.RequestGeneration is a pure echo -- the server
// keeps no ordering of its own) and their own accurate epoch and
// incarnation, so a client that fires the earlier read but receives its
// response after the later one's can tell which is fresher from the
// generation alone, without the two ever needing to arrive in order.
func TestStaleIncarnationResponseOrdering(t *testing.T) {
	srv := NewServer(ServerConfig{})
	// The seed turn is folded into the served identity's starting recorded
	// length (newServedTranscriptAt records it before ReplaceAppIdentity), so
	// it is never itself projected -- project()'s first real call, whichever
	// entry that is, has no earlier publication to compare with and just
	// adopts the index's incarnation (thread_history.go's project() doc
	// comment). A second entry establishes a real incarnation baseline
	// before the rebuild below, so the rebuild has something to diverge from.
	st := newServedTranscript(t, srv, "stale-gen-thread", schema.NewTurn(schema.TurnUserInput, llm.User("one")))
	st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("two")))
	st.settle(t)

	before, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: "stale-gen-thread", IncludeTurns: true, RequestGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if before.RequestGeneration != 1 {
		t.Fatalf("RequestGeneration = %d, want the caller's 1", before.RequestGeneration)
	}

	// Another handle rebuilds the index (the same "another handle rotated
	// the incarnation" seam TestThreadHistoryRotatedIncarnationResyncs uses):
	// the next entry the projection sees under the old incarnation resyncs.
	history := srv.appHistoryForID("stale-gen-thread")
	if history == nil {
		t.Fatal("the served thread has no history")
	}
	idx, err := history.cache.Acquire(history.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Rebuild(before.Snapshot.Length); err != nil {
		t.Fatal(err)
	}
	history.cache.Release(idx)
	st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("three")))
	st.settle(t)

	after, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: "stale-gen-thread", IncludeTurns: true, RequestGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	if after.RequestGeneration != 2 {
		t.Fatalf("RequestGeneration = %d, want the caller's 2", after.RequestGeneration)
	}
	if before.Epoch == after.Epoch {
		t.Fatalf("epoch %d unchanged across the rebuild", before.Epoch)
	}
	if before.Snapshot.Incarnation == after.Snapshot.Incarnation {
		t.Fatalf("incarnation %q unchanged across the rebuild", before.Snapshot.Incarnation)
	}
	if before.RequestGeneration >= after.RequestGeneration {
		t.Fatalf("generations %d, %d do not order the reads a client fired in that order", before.RequestGeneration, after.RequestGeneration)
	}
}

// TestCrashLostBufferedEntriesAreReplacedByBootGeneration simulates a crash
// that lost entries a client had already been told about (buffered, not
// fsynced): the transcript on disk is shorter than what the client holds.
// A restart at a higher boot generation replaces the client's whole history
// per CompareBootGeneration, so the lost entries never survive a merge.
func TestCrashLostBufferedEntriesAreReplacedByBootGeneration(t *testing.T) {
	srv := NewServer(ServerConfig{})
	st := newServedTranscriptAt(t, srv, "crash-thread", "1")
	first := st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("kept")))
	st.settle(t)
	lost := st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("lost to the crash")))
	st.settle(t)
	_ = lost

	read := st.read(t)
	held := read.BootGeneration
	if action := appwire.CompareBootGeneration(held, held); action != appwire.BootGenerationApply {
		t.Fatalf("same generation = %v, want apply", action)
	}

	// The crash: the file is truncated back to just the first entry, and the
	// daemon restarts at a higher boot generation over the same path.
	if err := os.Truncate(st.path, first.Offset+first.Length); err != nil {
		t.Fatal(err)
	}
	srv2 := NewServer(ServerConfig{})
	t.Cleanup(srv2.Close)
	prepared, err := PrepareAppIdentity("local", "crash-thread", st.path)
	if err != nil {
		t.Fatal(err)
	}
	srv2.ReplaceAppIdentity(prepared.WithRecordedLength(first.Offset+first.Length).WithBootGeneration("2"), nil)
	restarted := st.readFrom(t, srv2)
	if action := appwire.CompareBootGeneration(held, restarted.BootGeneration); action != appwire.BootGenerationReplace {
		t.Fatalf("higher boot generation compares as %v, want replace", action)
	}
	if len(restarted.Thread.Turns) != 1 || restarted.Thread.Turns[0].Items[0].Text != "kept" {
		t.Fatalf("restarted read = %+v, want only the entry that survived the crash", restarted.Thread.Turns)
	}
}

func (st *servedTranscript) readFrom(t *testing.T, srv *Server) appwire.ThreadReadResponse {
	t.Helper()
	response, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: st.threadID, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// isTranscriptHistoryFailed reports whether err is the wire error a read of
// a failed thread's history gets (appwire.TranscriptHistoryFailed).
func isTranscriptHistoryFailed(err error) bool {
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		return false
	}
	data, ok := wireErr.Data.(appwire.HistoryReadErrorData)
	return ok && data.EvenerErrorInfo == appwire.ErrorTranscriptHistoryFailed
}

// TestThreeRebuildFailuresEnterTheFailedState is the server-level companion
// to TestThreadHistoryThirdFailedRebuildFailsTheThread
// (thread_history_test.go, which pins the projection-level mechanics): after
// three consecutive rebuild failures, a server-level read of the thread
// returns ErrorTranscriptHistoryFailed while the writer keeps recording
// underneath it, and a restart -- a fresh server over the same transcript, at
// a higher boot generation -- recovers cleanly, holding everything recorded
// both before and after the failure.
func TestThreeRebuildFailuresEnterTheFailedState(t *testing.T) {
	srv := NewServer(ServerConfig{})
	st := newServedTranscript(t, srv, "fail-thread", schema.NewTurn(schema.TurnUserInput, llm.User("one")))
	st.settle(t)
	history := srv.appHistoryForID("fail-thread")
	if history == nil {
		t.Fatal("the served thread has no history")
	}

	// attemptStarted fires (unsynchronized on purpose, like every other
	// package-level test seam here) once the projection goroutine holds
	// history.serial, mid-recovery; that lock is then held across all
	// threadHistoryMaxRebuilds attempts and the failure finalization, so
	// once we can take it ourselves, the failed state is already set.
	attemptStarted := make(chan struct{}, threadHistoryMaxRebuilds)
	repair := breakProjection(t, func() {
		select {
		case attemptStarted <- struct{}{}:
		default:
		}
	})
	bad := st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("bad")))
	<-attemptStarted
	history.serial.Lock()
	history.serial.Unlock()

	var entryErr *transcriptindex.EntryError
	if err := history.Failed(); !errors.As(err, &entryErr) || entryErr.Ordinal != bad.Ordinal {
		t.Fatalf("Failed() = %v, want an entry error naming ordinal %d", err, bad.Ordinal)
	}

	// The server-level read fails with ErrorTranscriptHistoryFailed while
	// recording continues underneath it.
	if _, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: "fail-thread", IncludeTurns: true}); !isTranscriptHistoryFailed(err) {
		t.Fatalf("read of a failed thread = %v, want transcriptHistoryFailed", err)
	}
	st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("after the failure"))) // fatals itself if not recorded

	// A restart recovers: a fresh server over the same transcript, at a
	// higher boot generation, reads cleanly and holds everything recorded
	// both before and after the failure.
	repair()
	srv2 := NewServer(ServerConfig{})
	t.Cleanup(srv2.Close)
	prepared, err := PrepareAppIdentity("local", "fail-thread", st.path)
	if err != nil {
		t.Fatal(err)
	}
	srv2.ReplaceAppIdentity(prepared.WithRecordedLength(st.writer.RecordedLength()).WithBootGeneration("2"), nil)
	restarted := st.readFrom(t, srv2)
	texts := readTexts(restarted)
	for _, want := range []string{"one", "bad", "after the failure"} {
		found := false
		for _, text := range texts {
			if text == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("restarted read = %v, missing %q", texts, want)
		}
	}
}

// boundaryTearingFs is the real filesystem whose files write only half of an
// entry carrying marker and then fail, poisoning the writer: the same
// mechanism agent/session_fail_closed_test.go's toolResultsTearingFs pins,
// generalized to any marker so this package can reuse it on COMMUNICATE.
type boundaryTearingFs struct {
	afero.Fs
	marker []byte
}

func (fs boundaryTearingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return boundaryTearingFile{File: f, marker: fs.marker}, nil
}

type boundaryTearingFile struct {
	afero.File
	marker []byte
}

func (f boundaryTearingFile) Write(p []byte) (int, error) {
	if strings.Contains(string(p), string(f.marker)) {
		n, _ := f.File.Write(p[:len(p)/2])
		return n, errors.New("injected torn write")
	}
	return f.File.Write(p)
}

// seenSnapshot is every event ps has seen so far, safe to read after close.
func (ps *paritySession) seenSnapshot() []events.SessionEvent {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]events.SessionEvent(nil), ps.seen...)
}

// visibleFailClosedDiagnostics counts the served events that are the one
// diagnostic a session failing closed shows (agent/session_fail_closed.go's
// announceFailClosed): an EventError whose Cause.Kind is
// "transcript_failed_closed". That constant is unexported in package agent,
// so this mirrors agent/session_fail_closed_test.go's failClosedDiagnostics
// by the same literal rather than importing it.
func visibleFailClosedDiagnostics(evs []events.SessionEvent) int {
	count := 0
	for _, ev := range evs {
		if data, ok := ev.Data.(events.ErrorData); ok && ev.Kind == events.EventError && data.Cause != nil && data.Cause.Kind == "transcript_failed_closed" {
			count++
		}
	}
	return count
}

// isFailClosedRefusal reports whether err is the refusal a session that
// failed closed gives every later input. agent.errTranscriptFailedClosed is
// unexported, so this matches its stable message instead of errors.Is.
func isFailClosedRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cannot record what it shows")
}

// TestUnrecordedAppendFailsClosed drives a real served session (a scripted
// provider, wired into its own server the way bridgeParitySession does)
// through the two ways the spec's "Writer failure" section makes a served
// session fail closed: a poisoned writer, and one that was never created.
// agent/session_fail_closed_test.go's TestAServedSessionWithNoTranscriptFailsClosed
// and TestAnUnrecordedCommunicateFailsAServedSessionClosed pin the same
// seams from inside package agent, where a test can swap the session's
// writer directly; this package cannot reach that unexported field, so a
// poisoned writer is reached the same way OpenWriterForSessionWithFS's own
// doc comment describes: a second *transcript.Writer opened on the real
// filesystem shares the file's append tail with the session's own writer,
// so poisoning it through a torn write poisons what the session sees too.
func TestUnrecordedAppendFailsClosed(t *testing.T) {
	t.Run("poisoned writer", func(t *testing.T) {
		sess, script, _, stateDir := newScriptedSession(t)
		ps := bridgeParitySession(t, sess, stateDir)
		t.Cleanup(func() { ps.close(t) })

		// The provider's step runs inside the round, before the session
		// records anything for it: poisoning the shared tail here lands
		// before the COMMUNICATE append that would otherwise announce
		// "never delivered".
		script.script(func(llm.Request) (llm.Response, error) {
			w, _, err := transcript.OpenWriterForSessionWithFS(boundaryTearingFs{Fs: afero.NewOsFs(), marker: []byte(`"kind":"COMMUNICATE"`)}, sess.TranscriptPath(), sess.ID())
			if err != nil {
				return llm.Response{}, err
			}
			defer w.Close() //nolint:errcheck // fixture
			if _, err := w.Record(schema.NewTurn(schema.TurnCommunicate, llm.Assistant("poison")), transcript.RecordOptions{}); err == nil {
				return llm.Response{}, errors.New("setup: the torn write did not fail")
			}
			if !w.Poisoned() {
				return llm.Response{}, errors.New("setup: the torn write did not poison the shared tail")
			}
			return parityCommunicate("poison-1", "never delivered", true), nil
		})
		if _, err := sess.ProcessInput(context.Background(), "talk", nil); err == nil {
			t.Fatal("the input completed after its writer was poisoned")
		}

		// The running execution is interrupted: it does not end as completed
		// once its terminal entries cannot be recorded.
		ended := ps.await(t, events.EventExecutionEnded, nil)
		if data, ok := ended.Data.(events.ExecutionEndedData); !ok || data.Status == string(schema.TurnCompleted) {
			t.Fatalf("execution ended = %+v, want a status other than completed once its writer was poisoned", ended.Data)
		}

		// Further input is refused.
		if _, err := sess.ProcessInput(context.Background(), "again", nil); !isFailClosedRefusal(err) {
			t.Fatalf("input after failing closed = %v, want the fail-closed refusal", err)
		}

		// The overlay ended its round: no live stream or tool state is left
		// for the client to hold once the execution stopped.
		history := ps.srv.appHistoryForID(sess.ID())
		if history == nil {
			t.Fatal("the served session has no history")
		}
		for _, item := range history.overlay.Snapshot() {
			if item.Kind == appwire.OverlayStream || item.Kind == appwire.OverlayTool {
				t.Fatalf("overlay still holds %+v after the execution ended", item)
			}
		}

		ps.close(t)
		if got := visibleFailClosedDiagnostics(ps.seenSnapshot()); got != 1 {
			t.Fatalf("%d visible fail-closed diagnostics, want 1", got)
		}
	})

	// "missing writer": a transcript that was never created. package agent's
	// own test reaches this with a construction-time fault hook
	// (sessionInitFault) this package cannot call; the same effect is
	// reachable here through public API: AcquireSessionOwnership hands this
	// package the freshly generated session ID before any ID-specific state
	// is persisted (agent/session_config.go), early enough to occupy the
	// transcript's path with a directory before the writer tries to create
	// it there -- the writer's create then fails at the real OS boundary,
	// the same way it would on a full disk, and every other per-ID state
	// (which lives elsewhere under "sessions") is unaffected.
	t.Run("missing writer", func(t *testing.T) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "state")
		workDir := filepath.Join(root, "work")
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// "sessions": agent.sessionsSubdir, unexported; the literal names a
		// stable on-disk path other tools resolve the same way.
		if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		script := &parityProvider{childRelease: make(chan struct{})}
		client := llm.NewClient()
		client.Register(script)
		sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{
			StateDir:    stateDir,
			VisionModel: "off",
			LLMSleep:    noSleep,
			AcquireSessionOwnership: func(sessionID string) error {
				return os.MkdirAll(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), 0o755)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		// The writer's create already failed against the occupying
		// directory; remove it so a fresh read of the path (PrepareAppIdentity,
		// below) sees a plain absence rather than a directory where a file
		// belongs.
		if err := os.RemoveAll(sess.TranscriptPath()); err != nil {
			t.Fatal(err)
		}
		sess.SetClientMutationStartWakeFunc(func() {})
		ps := bridgeParitySession(t, sess, stateDir)
		t.Cleanup(func() { ps.close(t) })

		if h := ps.srv.appHistoryForID(sess.ID()); h != nil {
			t.Fatal("a session whose transcript could not be created has a history")
		}
		// No execution ever starts: the refusal is at the claim, before any
		// round begins (agent/session_lifecycle.go's
		// refuseTurnOnUnhealthyTranscript runs first).
		if _, err := sess.ProcessInput(context.Background(), "hello", nil); !isFailClosedRefusal(err) {
			t.Fatalf("input on a session with no transcript = %v, want the fail-closed refusal", err)
		}
		if _, err := sess.ProcessInput(context.Background(), "again", nil); !isFailClosedRefusal(err) {
			t.Fatalf("second input = %v, want the fail-closed refusal", err)
		}

		ps.close(t)
		if got := visibleFailClosedDiagnostics(ps.seenSnapshot()); got != 1 {
			t.Fatalf("%d visible fail-closed diagnostics, want 1", got)
		}
	})
}

// TestProjectionQueueOverflowResyncs overflows the projection queue (bound
// to a few bytes here) with entries the goroutine never gets a chance to
// drain, all recorded before the first wake is serviced: the thread resyncs
// once and, once it catches up, every entry is published -- none lost to the
// dropped queue.
func TestProjectionQueueOverflowResyncs(t *testing.T) {
	hx := newHistoryHarnessWith(t, 8)
	// Hold the goroutine's serial lock (the same lock its own step() takes,
	// see threadHistory's doc comment) for every append: the recorded hook
	// only takes the queue mutex, so this does not block the appends, but it
	// keeps the goroutine from draining the queue between them, so every one
	// of the 50 appends' drops is the SAME overflow the goroutine resolves
	// in its one recover() call once released -- deterministically one
	// resync, not a race between the appends and however fast the goroutine
	// happens to drain.
	hx.history.serial.Lock()
	var recorded []transcript.Record
	for range 50 {
		recorded = append(recorded, hx.record(t, "x"))
	}
	hx.history.serial.Unlock()
	last := recorded[len(recorded)-1]

	// Exactly one resync for the whole overflow: recover() bumps the epoch
	// once up front and retries through it, so every drop the 50 appends
	// caused resolves under that same epoch.
	epoch := hx.nextResync(t)
	if epoch != 1 {
		t.Fatalf("resync epoch = %d, want 1", epoch)
	}
	hx.awaitPublished(t, last.Offset+last.Length)
	hx.expectQuiet(t)
	if got := hx.history.Epoch(); got != 1 {
		t.Fatalf("Epoch() = %d, want 1 after the one resync", got)
	}

	turns := hxReadWhole(t, hx)
	count := 0
	for _, turn := range turns {
		count += len(turn.Items)
	}
	if count != len(recorded) {
		t.Fatalf("projected %d of %d entries after the overflow, want all of them", count, len(recorded))
	}
}
