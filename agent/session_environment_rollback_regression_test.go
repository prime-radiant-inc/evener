package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type environmentSyncFailureFS struct {
	afero.Fs
	mu                         sync.Mutex
	failure                    error
	rollbackFailure            error
	seekFailure                error
	writeFailure               error
	writesBeforeFailure        int
	transferBeforeWriteFailure int
	onFailure                  func()
}

type environmentSyncFailureFile struct {
	afero.File
	fs *environmentSyncFailureFS
}

func (fs *environmentSyncFailureFS) OpenFile(name string, flag int, mode os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	return &environmentSyncFailureFile{File: file, fs: fs}, nil
}

func (file *environmentSyncFailureFile) Sync() error {
	file.fs.mu.Lock()
	failure := file.fs.failure
	file.fs.failure = nil
	onFailure := file.fs.onFailure
	file.fs.mu.Unlock()
	if failure != nil {
		if onFailure != nil {
			onFailure()
		}
		return failure
	}
	return file.File.Sync()
}

// Truncate is the writer's rollback of a failed durable append. Failing it
// leaves the entry's line in the file with nothing to say whether it landed.
func (file *environmentSyncFailureFile) Truncate(size int64) error {
	file.fs.mu.Lock()
	failure := file.fs.rollbackFailure
	file.fs.rollbackFailure = nil
	file.fs.mu.Unlock()
	if failure != nil {
		return failure
	}
	return file.File.Truncate(size)
}

// Write stops partway through the line it is given and then fails, which is the
// shape no rollback can undo.
func (file *environmentSyncFailureFile) Write(p []byte) (int, error) {
	file.fs.mu.Lock()
	failure := file.fs.writeFailure
	if failure != nil && file.fs.writesBeforeFailure > 0 {
		// Let this one through: the caller is aiming the fault at a later door
		// than the next one.
		file.fs.writesBeforeFailure--
		failure = nil
	} else {
		file.fs.writeFailure = nil
	}
	transfer := min(file.fs.transferBeforeWriteFailure, len(p))
	file.fs.mu.Unlock()
	if failure == nil {
		return file.File.Write(p)
	}
	n, err := file.File.Write(p[:transfer])
	if err != nil {
		return n, err
	}
	return n, failure
}

// Seek closes the writer's rollback after the truncate. Failing it reports the
// same indeterminate append over a transcript the truncate already emptied, so
// it drives the other half of the contract: an entry confirmed absent.
func (file *environmentSyncFailureFile) Seek(offset int64, whence int) (int64, error) {
	file.fs.mu.Lock()
	failure := file.fs.seekFailure
	file.fs.seekFailure = nil
	file.fs.mu.Unlock()
	if failure != nil {
		return 0, failure
	}
	return file.File.Seek(offset, whence)
}

func attachEnvironmentSyncFailure(t *testing.T, sess *Session, failure error, onFailure func()) {
	t.Helper()
	fs := attachEnvironmentFailureFS(t, sess)
	fs.mu.Lock()
	fs.failure = failure
	fs.onFailure = onFailure
	fs.mu.Unlock()
}

// attachEnvironmentAmbiguousWrite makes the next durable transcript write fail
// its sync and then fail the rollback that would take the entry back out.
func attachEnvironmentAmbiguousWrite(t *testing.T, sess *Session, syncFailure, rollbackFailure error) {
	t.Helper()
	fs := attachEnvironmentFailureFS(t, sess)
	fs.mu.Lock()
	fs.failure = syncFailure
	fs.rollbackFailure = rollbackFailure
	fs.mu.Unlock()
}

// attachEnvironmentRolledBackWrite makes the next durable transcript write fail
// its sync, lets the rollback truncate the entry back out, and then fails the
// seek that ends that rollback. The append reports an indeterminate outcome
// over a transcript the entry is genuinely gone from. The writer takes its own
// seek to EOF before each durable append, so arming this only once the sync has
// failed puts the fault on the rollback's seek rather than that one.
func attachEnvironmentRolledBackWrite(t *testing.T, sess *Session, syncFailure, seekFailure error) {
	t.Helper()
	fs := attachEnvironmentFailureFS(t, sess)
	fs.mu.Lock()
	fs.failure = syncFailure
	fs.onFailure = func() {
		fs.mu.Lock()
		fs.seekFailure = seekFailure
		fs.onFailure = nil
		fs.mu.Unlock()
	}
	fs.mu.Unlock()
}

// attachEnvironmentUnverifiableWrite makes the next durable transcript write
// fail its sync, fail the rollback that would take the entry back out, and then
// fail again on the durability barrier the reconciliation raises before reading
// the transcript back, leaving the entry's fate unknowable.
func attachEnvironmentUnverifiableWrite(t *testing.T, sess *Session, syncFailure, rollbackFailure, durabilityFailure error) {
	t.Helper()
	fs := attachEnvironmentFailureFS(t, sess)
	fs.mu.Lock()
	fs.failure = syncFailure
	fs.rollbackFailure = rollbackFailure
	fs.onFailure = func() {
		fs.mu.Lock()
		fs.failure = durabilityFailure
		fs.onFailure = nil
		fs.mu.Unlock()
	}
	fs.mu.Unlock()
}

// attachEnvironmentPoisoningWrite makes the next durable transcript write stop
// partway through its line and fails the rollback that would take those bytes
// back out, which is what leaves the writer refusing every later append.
func attachEnvironmentPoisoningWrite(t *testing.T, sess *Session, writeFailure, rollbackFailure error) {
	t.Helper()
	fs := attachEnvironmentFailureFS(t, sess)
	fs.mu.Lock()
	fs.writeFailure = writeFailure
	fs.transferBeforeWriteFailure = 12
	fs.rollbackFailure = rollbackFailure
	fs.mu.Unlock()
}

// attachEnvironmentFailureFS swaps in a writer over a faultable filesystem and
// returns it unarmed: attaching flushes held turns through the same file, so
// callers arm their faults only once the swap has settled.
func attachEnvironmentFailureFS(t *testing.T, sess *Session) *environmentSyncFailureFS {
	t.Helper()
	if err := sess.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &environmentSyncFailureFS{Fs: afero.NewOsFs()}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, sess.TranscriptPath(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	sess.attachTranscript(writer)
	return fs
}

// assertEnvironmentNotReemitted drives the turn after an append whose entry the
// session could not confirm gone, and requires the transcript to still carry
// only the entry the failed rollback left behind.
func assertEnvironmentNotReemitted(t *testing.T, sess *Session, retained []string) {
	t.Helper()
	assertEnvironmentTrackerMatchesModelHistory(t, sess)
	if err := sess.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if got := durableEnvironmentTurnIDs(t, sess); !reflect.DeepEqual(got, retained) {
		t.Fatalf("durable environment entries after the next turn = %v, want only the entry already in the transcript %v", got, retained)
	}
	assertEnvironmentTrackerMatchesModelHistory(t, sess)
}

// drainPendingEvents takes every event the session has already published,
// without waiting for another.
func drainPendingEvents(sess *Session) []events.SessionEvent {
	var drained []events.SessionEvent
	for {
		select {
		case event := <-sess.Events():
			drained = append(drained, event)
		default:
			return drained
		}
	}
}

// environmentEventTurnIDs lists the turn IDs the live ENVIRONMENT events in
// these carried, which is what a watching client projects.
func environmentEventTurnIDs(t *testing.T, drained []events.SessionEvent) []string {
	t.Helper()
	var ids []string
	for _, event := range drained {
		if event.Kind != events.EventEnvironment {
			continue
		}
		data, ok := event.Data.(events.EnvironmentData)
		if !ok {
			t.Fatalf("environment event data = %#v, want EnvironmentData", event.Data)
		}
		ids = append(ids, data.TurnID)
	}
	return ids
}

// historyEnvironmentTurnIDs lists the stable IDs of the ENVIRONMENT turns in
// the session's live model history.
func historyEnvironmentTurnIDs(sess *Session) []string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	var ids []string
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnEnvironment {
			ids = append(ids, turn.StableTurnID)
		}
	}
	return ids
}

// pairLogEnvironmentTurnIDs lists the ENVIRONMENT turns recorded in the
// append/write pair log, the forms a fold publication re-appends after its
// compaction markers.
func pairLogEnvironmentTurnIDs(sess *Session) []string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	var ids []string
	for _, turn := range sess.persistedAppendLog {
		if turn.Kind == schema.TurnEnvironment {
			ids = append(ids, turn.StableTurnID)
		}
	}
	return ids
}

// assertEnvironmentTrackerMatchesModelHistory requires the environment tracker
// to describe exactly what the model has been shown: replaying the ENVIRONMENT
// blocks in history has to land on the tracker's own state. A tracker advanced
// past history renders every later observation as a diff against a baseline the
// model never received, which reads as a complete environment and is not one.
func assertEnvironmentTrackerMatchesModelHistory(t *testing.T, sess *Session) {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	replayed := envctx.NewTracker(envctx.State{})
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnEnvironment {
			replayed.ReplayBlock(turn.Message.Text())
		}
	}
	if got, want := sess.envTracker.State(), replayed.State(); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment tracker state = %+v, want the %+v the model's history accounts for", got, want)
	}
}

// assertDurableSequenceStrictlyIncreases requires every entry a reader of the
// session's transcript sees to carry its own sequence number. A retained entry
// from a rollback that could not remove it still spends its seq, so the turns
// the session goes on to write must not land on top of it.
func assertDurableSequenceStrictlyIncreases(t *testing.T, sess *Session) {
	t.Helper()
	data, err := readTranscriptFull(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(data.Entries); i++ {
		if data.Entries[i].Seq <= data.Entries[i-1].Seq {
			t.Fatalf("transcript entry %d carries seq %d after seq %d, want a strictly greater sequence", i, data.Entries[i].Seq, data.Entries[i-1].Seq)
		}
	}
}

// durableEnvironmentTurnIDs lists the stable IDs of every ENVIRONMENT entry a
// reader of the session's transcript would see, in file order.
func durableEnvironmentTurnIDs(t *testing.T, sess *Session) []string {
	t.Helper()
	data, err := readTranscriptFull(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnEnvironment {
			ids = append(ids, entry.Turn.StableTurnID)
		}
	}
	return ids
}

func TestRestoreDeferredHookWaitsForEnvironmentDurability(t *testing.T) {
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("ok") },
	}}
	sess := restoredSessionWithResumeHook(t, adapter)
	failure := errors.New("environment transcript durability failure")
	attachEnvironmentSyncFailure(t, sess, failure, nil)
	before := sessionHistoryText(sess)
	if _, err := sess.ProcessInput(t.Context(), "first", nil); !errors.Is(err, failure) {
		t.Fatalf("first input error = %v, want environment durability failure", err)
	}
	if after := sessionHistoryText(sess); after != before {
		t.Fatal("failed environment append changed model history before input acceptance")
	}
	sess.mu.Lock()
	pending := sess.pendingSessionStartKind != nil || sess.pendingSessionStartResult != nil
	sess.mu.Unlock()
	if !pending {
		t.Fatal("environment failure consumed the deferred resume hook")
	}
	if _, err := sess.ProcessInput(t.Context(), "retry", nil); err != nil {
		t.Fatal(err)
	}
	// These are opaque fixture payloads from the external hook, not prompt copy.
	if count := strings.Count(sessionHistoryText(sess), "RESUME_HOOK_CONTEXT"); count != 1 {
		t.Fatalf("resume hook payload deliveries = %d, want one", count)
	}
	if len(adapter.Requests()) != 1 {
		t.Fatalf("provider requests = %d, want one accepted retry", len(adapter.Requests()))
	}
	retained, err := readTranscriptFull(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	payloads := 0
	for _, entry := range retained.Entries {
		payloads += strings.Count(entry.Turn.Message.Text(), "RESUME_HOOK_CONTEXT")
	}
	if payloads != 1 {
		t.Fatalf("durable hook payload deliveries = %d, want one", payloads)
	}
}

func TestQueuedEnvironmentFailureReportsRollbackFailure(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	mutationID := "rollback-error"
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: mutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "retry-input"}},
	}); err != nil {
		t.Fatal(err)
	}
	environmentFailure := errors.New("environment transcript durability failure")
	rollbackFailure := errors.New("queue rollback durability failure")
	var failRollback atomic.Bool
	sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		if failRollback.Swap(false) {
			return rollbackFailure
		}
		return nil
	}
	attachEnvironmentSyncFailure(t, sess, environmentFailure, func() { failRollback.Store(true) })
	_, ran, err := sess.ProcessPendingUserInput(t.Context(), nil)
	if !ran || !errors.Is(err, environmentFailure) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("queued attempt ran=%v error=%v; want both environment and rollback failures", ran, err)
	}
	snapshot := sess.clientMutations.snapshot()
	if state := snapshot.Journal[mutationID].ExecutionState; state != "accepted" {
		t.Fatalf("unincorporated queued attempt execution state = %q, want accepted", state)
	}
	if _, pending := snapshot.PendingExecutions[mutationID]; pending {
		t.Fatal("failed queued attempt left a stranded pending execution")
	}
	// Turn completion returns an unincorporated claim even when the first
	// rollback failed. The original input remains available for a later retry.
	if sess.QueueDepth() != 1 || len(snapshot.InputQueue) != 1 {
		t.Fatal("turn completion did not restore the claimed queue entry")
	}
	entry := snapshot.InputQueue[0]
	if entry.ClientMutationID != mutationID || len(entry.Input) != 1 || entry.Input[0].Text != "retry-input" {
		t.Fatalf("restored queue entry = %+v, want original mutation and input", entry)
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatal("returned queue claim still owns the active turn")
	}
}

func TestQueuedEnvironmentFailureReturnsRunnableClaimAndWakesRetry(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	var wakes atomic.Int32
	sess.SetPendingUserInputWakeFunc(func() { wakes.Add(1) })
	mutationID := "environment-queue-retry"
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: mutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "queued retry payload"}},
	}); err != nil {
		t.Fatal(err)
	}
	wakes.Store(0)
	failure := errors.New("environment transcript durability failure")
	attachEnvironmentSyncFailure(t, sess, failure, nil)
	_, ran, err := sess.ProcessPendingUserInput(t.Context(), nil)
	if !ran || !errors.Is(err, failure) {
		t.Fatalf("queued attempt ran=%v error=%v, want environment durability failure", ran, err)
	}
	snapshot := sess.clientMutations.snapshot()
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("returned claim still owns active turn %q", snapshot.ActiveTurnID)
	}
	if _, pending := snapshot.PendingExecutions[mutationID]; pending {
		t.Fatal("returned claim still has a pending execution")
	}
	if sess.QueueDepth() != 1 || len(snapshot.InputQueue) != 1 || snapshot.InputQueue[0].ClientMutationID != mutationID {
		t.Fatalf("returned queue=%+v, want original runnable mutation", snapshot.InputQueue)
	}
	if wakes.Load() == 0 {
		t.Fatal("restored queue did not wake its runner")
	}
	stableTurnID := snapshot.Journal[mutationID].StableTurnID
	_, ran, err = sess.ProcessPendingUserInput(t.Context(), nil)
	if !ran || err != nil {
		t.Fatalf("queued retry ran=%v error=%v, want accepted retry", ran, err)
	}
	snapshot = sess.clientMutations.snapshot()
	if sess.QueueDepth() != 0 || snapshot.ActiveTurnID != "" || snapshot.Journal[mutationID].StableTurnID != stableTurnID {
		t.Fatal("retry did not settle the original queued identity")
	}
	if count := countEnvironmentTurns(sess); count != 1 {
		t.Fatalf("environment turns=%d, want one durable retry", count)
	}
}

// TestEnvironmentAmbiguousWriteDoesNotDuplicateEntry: a durable environment
// append whose sync fails and whose rollback also fails leaves the entry's
// line in the transcript. Rewinding the tracker there — the plain
// durability-failure response — makes the next turn render the same
// observation again under a fresh identity, so the transcript carries the
// environment twice and every reader projecting it shows duplicate context.
// The append's outcome is unknown, so it has to be reconciled against the
// transcript by the entry's stable ID before any retry.
func TestEnvironmentAmbiguousWriteDoesNotDuplicateEntry(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	syncFailure := errors.New("environment transcript durability failure")
	rollbackFailure := errors.New("environment transcript rollback failure")
	attachEnvironmentAmbiguousWrite(t, sess, syncFailure, rollbackFailure)

	err := sess.maybeAppendEnvironmentContext()
	if !errors.Is(err, syncFailure) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("ambiguous append error = %v, want both the sync and the rollback failure", err)
	}
	ambiguous := durableEnvironmentTurnIDs(t, sess)
	if len(ambiguous) != 1 || ambiguous[0] == "" {
		t.Fatalf("durable environment entries after the failed rollback = %v, want the one entry rollback could not remove", ambiguous)
	}
	// Confirming the entry commits it late, so model history carries it once —
	// TestEnvironmentAmbiguousWriteCommitsConfirmedEntry owns that contract in
	// full. Here it only has to stay at one across the next turn.
	if got := countEnvironmentTurns(sess); got != 1 {
		t.Fatalf("model history environment turns after the confirmed append = %d, want the committed entry", got)
	}

	assertEnvironmentNotReemitted(t, sess, ambiguous)
	if got := countEnvironmentTurns(sess); got != 1 {
		t.Fatalf("model history environment turns after the next turn = %d, want no duplicate of the committed entry", got)
	}
}

// TestEnvironmentRolledBackWriteReemitsEntry is the other half of the
// reconciliation contract. A rollback that truncates the entry away and then
// fails its closing seek reports the same indeterminate outcome as one that
// left the entry behind, over a transcript the entry is genuinely absent from.
// Reconciling by the stable ID has to find that absence and rewind the tracker,
// so the next turn emits the environment the session still owes the model. An
// implementation that never reconciles — treating every rollback failure as a
// surviving entry — keeps the environment silent forever and fails here.
func TestEnvironmentRolledBackWriteReemitsEntry(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	syncFailure := errors.New("environment transcript durability failure")
	seekFailure := errors.New("environment transcript rollback seek failure")
	attachEnvironmentRolledBackWrite(t, sess, syncFailure, seekFailure)

	err := sess.maybeAppendEnvironmentContext()
	if !errors.Is(err, syncFailure) || !errors.Is(err, seekFailure) {
		t.Fatalf("rolled-back append error = %v, want both the sync and the rollback failure", err)
	}
	if !errors.Is(err, transcript.ErrRollbackFailed) {
		t.Fatalf("rolled-back append error = %v, want the indeterminate-outcome marker the reconciliation keys on", err)
	}
	if got := durableEnvironmentTurnIDs(t, sess); len(got) != 0 {
		t.Fatalf("durable environment entries after the rollback truncated the entry = %v, want none", got)
	}

	if err := sess.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	reemitted := durableEnvironmentTurnIDs(t, sess)
	if len(reemitted) != 1 || reemitted[0] == "" {
		t.Fatalf("durable environment entries after the next turn = %v, want the re-emitted entry the transcript was confirmed to be missing", reemitted)
	}
	if got := countEnvironmentTurns(sess); got != 1 {
		t.Fatalf("model-visible environment turns after the retry = %d, want the re-emitted block", got)
	}
	assertEnvironmentTrackerMatchesModelHistory(t, sess)
}

// TestEnvironmentUnreadableTranscriptReemitsForTheModel: an append the session
// cannot read back leaves the entry's fate unknown, and an unknown entry is one
// the model was never shown. The tracker has to come back to the last state the
// model did see so the next turn renders the whole of the observation, even
// though the entry may be in the transcript already — a redundant environment
// entry is readable, an environment diff against a baseline the model never
// received is not.
func TestEnvironmentUnreadableTranscriptReemitsForTheModel(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	syncFailure := errors.New("environment transcript durability failure")
	rollbackFailure := errors.New("environment transcript rollback failure")
	attachEnvironmentAmbiguousWrite(t, sess, syncFailure, rollbackFailure)

	var unreadable atomic.Bool
	previousOpen := openTranscriptFile
	openTranscriptFile = func(path string) (io.ReadCloser, error) {
		if unreadable.Load() {
			return nil, errors.New("transcript unreadable during reconciliation")
		}
		return previousOpen(path)
	}
	t.Cleanup(func() { openTranscriptFile = previousOpen })

	unreadable.Store(true)
	err := sess.maybeAppendEnvironmentContext()
	unreadable.Store(false)
	if !errors.Is(err, syncFailure) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("ambiguous append error = %v, want both the sync and the rollback failure", err)
	}
	assertEnvironmentReemittedForModel(t, sess)
}

// TestEnvironmentUnestablishedDurabilityReemitsForTheModel: reconciliation
// reads the transcript back only once it has raised a durability barrier over
// it, and a barrier that cannot be raised leaves the same unknown. The model is
// owed the observation either way.
func TestEnvironmentUnestablishedDurabilityReemitsForTheModel(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	syncFailure := errors.New("environment transcript durability failure")
	rollbackFailure := errors.New("environment transcript rollback failure")
	durabilityFailure := errors.New("environment transcript barrier failure")
	attachEnvironmentUnverifiableWrite(t, sess, syncFailure, rollbackFailure, durabilityFailure)

	err := sess.maybeAppendEnvironmentContext()
	if !errors.Is(err, syncFailure) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("ambiguous append error = %v, want both the sync and the rollback failure", err)
	}
	assertEnvironmentReemittedForModel(t, sess)
}

// assertEnvironmentReemittedForModel requires an unresolved append to leave the
// tracker where the model's history is, and the next turn to put the
// observation into that history.
func assertEnvironmentReemittedForModel(t *testing.T, sess *Session) {
	t.Helper()
	assertEnvironmentTrackerMatchesModelHistory(t, sess)
	if got := countEnvironmentTurns(sess); got != 0 {
		t.Fatalf("model history environment turns after the unresolved append = %d, want none claimed", got)
	}
	if err := sess.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if got := countEnvironmentTurns(sess); got != 1 {
		t.Fatalf("model history environment turns after the next turn = %d, want the observation the unresolved append owes the model", got)
	}
	assertEnvironmentTrackerMatchesModelHistory(t, sess)
}

// TestEnvironmentAmbiguousWriteCommitsConfirmedEntry: reconciling an
// indeterminate append by reading the entry back out of the transcript
// establishes that the write committed, late. Everything the clean path does
// with a committed environment entry has to happen too — model history, the
// pair log a fold publication replays after its markers, the persisted tracker
// state, and the live ENVIRONMENT event — or the model and every watching
// client omit a block that cold restore reads straight out of the transcript.
func TestEnvironmentAmbiguousWriteCommitsConfirmedEntry(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	syncFailure := errors.New("environment transcript durability failure")
	rollbackFailure := errors.New("environment transcript rollback failure")
	attachEnvironmentAmbiguousWrite(t, sess, syncFailure, rollbackFailure)
	drainPendingEvents(sess)

	err := sess.maybeAppendEnvironmentContext()
	if !errors.Is(err, syncFailure) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("ambiguous append error = %v, want both the sync and the rollback failure", err)
	}
	confirmed := durableEnvironmentTurnIDs(t, sess)
	if len(confirmed) != 1 || confirmed[0] == "" {
		t.Fatalf("durable environment entries after the failed rollback = %v, want the one entry rollback could not remove", confirmed)
	}

	if got := historyEnvironmentTurnIDs(sess); !reflect.DeepEqual(got, confirmed) {
		t.Fatalf("model history environment turns = %v, want the confirmed durable entry %v", got, confirmed)
	}
	if got := pairLogEnvironmentTurnIDs(sess); !reflect.DeepEqual(got, confirmed) {
		t.Fatalf("pair-log environment turns = %v, want the confirmed durable entry %v", got, confirmed)
	}
	if got := environmentEventTurnIDs(t, drainPendingEvents(sess)); !reflect.DeepEqual(got, confirmed) {
		t.Fatalf("live environment events = %v, want the confirmed durable entry %v", got, confirmed)
	}
	sess.mu.Lock()
	state := sess.envContextState
	sess.mu.Unlock()
	if state == nil || !state.HasSent {
		t.Fatalf("persisted environment tracker state = %+v, want the confirmed entry recorded", state)
	}

	assertEnvironmentNotReemitted(t, sess, confirmed)
	if got := historyEnvironmentTurnIDs(sess); !reflect.DeepEqual(got, confirmed) {
		t.Fatalf("model history environment turns after the next turn = %v, want only the committed entry %v", got, confirmed)
	}

	// The committed entry spent its sequence number, so the turns that follow
	// it have to take later ones.
	sendOneUserInput(t, sess, "hello")
	assertDurableSequenceStrictlyIncreases(t, sess)
}

// TestEnvironmentEntryOutcomeZeroValueIsConservative: one outcome commits a
// turn into model history and the pair log on the strength of a confirmation
// that the entry is in the transcript. A future path that returns the type's
// zero value — a bare declaration, a struct field, a failed decode — carries no
// such confirmation, so the zero value must be the outcome that claims nothing.
func TestEnvironmentEntryOutcomeZeroValueIsConservative(t *testing.T) {
	var outcome environmentEntryOutcome
	if outcome != environmentEntryUnknown {
		t.Fatalf("zero-valued outcome = %d, want environmentEntryUnknown (%d) so an unset outcome cannot commit a turn nobody confirmed", outcome, environmentEntryUnknown)
	}
}

// TestEnvironmentPoisonedWriterFailsEveryTurnLoudly: a transcript nothing
// further can safely be added to must say so on every turn. The session's
// answer to a poisoned writer is its ordinary transcript-failure path — the
// error its caller aborts on and the warning a client sees — never a turn that
// quietly proceeds without the environment it owes the model.
func TestEnvironmentPoisonedWriterFailsEveryTurnLoudly(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	writeFailure := errors.New("environment transcript write failure")
	rollbackFailure := errors.New("environment transcript rollback failure")
	attachEnvironmentPoisoningWrite(t, sess, writeFailure, rollbackFailure)
	drainPendingEvents(sess)

	if err := sess.maybeAppendEnvironmentContext(); !errors.Is(err, writeFailure) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("partial-write append error = %v, want both the write and the rollback failure", err)
	}
	if err := sess.maybeAppendEnvironmentContext(); !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("turn after the poisoning = %v, want transcript.ErrWriterPoisoned", err)
	}
	if got := durableEnvironmentTurnIDs(t, sess); len(got) != 0 {
		t.Fatalf("durable environment entries = %v, want none: neither append produced a record", got)
	}
	if got := countEnvironmentTurns(sess); got != 0 {
		t.Fatalf("failed and refused environment appends entered model history %d times", got)
	}
	assertEnvironmentTrackerMatchesModelHistory(t, sess)

	warnings := 0
	for _, event := range drainPendingEvents(sess) {
		if event.Kind == events.EventWarning {
			warnings++
		}
	}
	if warnings != 2 {
		t.Fatalf("transcript-failure warnings = %d, want one for the write that poisoned the writer and one for the turn it then refused", warnings)
	}
}

// countingFinalResponses scripts n turn-ending responses that count the model
// requests they answer, so a test can say how many turns actually ran.
func countingFinalResponses(requests *atomic.Int32, n int) []func(llm.Request) llm.Response {
	steps := make([]func(llm.Request) llm.Response, n)
	for i := range steps {
		steps[i] = func(llm.Request) llm.Response {
			requests.Add(1)
			return finalResponse("ok")
		}
	}
	return steps
}

// armEnvironmentPartialWrite makes the next transcript write stop partway
// through its line, which is what poisons the writer through the buffered door
// recordTurn uses.
func armEnvironmentPartialWrite(fs *environmentSyncFailureFS) {
	armEnvironmentPartialWriteAfter(fs, 0)
}

// armEnvironmentPartialWriteAfter aims the partial write at a door further down
// the turn: skip lets that many writes through first.
func armEnvironmentPartialWriteAfter(fs *environmentSyncFailureFS, skip int) {
	fs.mu.Lock()
	fs.writeFailure = errors.New("injected transcript write failure")
	fs.writesBeforeFailure = skip
	fs.transferBeforeWriteFailure = 12
	fs.mu.Unlock()
}

// assertRefusalEndedTheInput requires the refused input to have ended the way a
// failed turn ends one: the session settled idle, and one SESSION_END said so.
// Without the emission the session looks perpetually mid-input to every client
// on the event stream, because admission cleared the emit-once gate on its way
// in; without the settle the emission claims an idle the session is not in.
func assertRefusalEndedTheInput(t *testing.T, sess *Session, drained []events.SessionEvent) events.SessionEndData {
	t.Helper()
	var ends []events.SessionEndData
	for _, event := range drained {
		if event.Kind != events.EventSessionEnd {
			continue
		}
		data, ok := event.Data.(events.SessionEndData)
		if !ok {
			t.Fatalf("session-end event data = %#v, want SessionEndData", event.Data)
		}
		ends = append(ends, data)
	}
	if len(ends) != 1 || ends[0].Reason != "turn_failed" {
		t.Fatalf("session-end events after the refusal = %+v, want exactly one turn_failed", ends)
	}
	// The state a client reads off this event has to be the state it would read
	// off the thread. A refusal ends the input from wherever the session
	// actually is, which is not always idle.
	if got := sess.WireState(); ends[0].State != got {
		t.Fatalf("session-end state = %q, want the %q the session is in", ends[0].State, got)
	}
	return ends[0]
}

// TestPoisonedWriterRefusesTheNextInput: a poisoned writer has stopped
// accepting records for the rest of the session, so a turn that runs against it
// cannot be persisted at all — every record it makes is lost on the next
// restart, while the session reports only warnings and carries on. The
// environment path already fails loudly under this condition, but only when an
// environment block is due; with an unchanged environment there is nothing to
// append and the turn used to proceed. Admission has to refuse instead: the
// session stops accepting input until it is restarted against the records the
// file still holds, which is the failure that can be seen.
func TestPoisonedWriterRefusesTheNextInput(t *testing.T) {
	var requests atomic.Int32
	// Two scripted responses: the turn that emits the environment, and the one
	// the refused input must never make.
	sess := newTestSessionForEnvctx(t, withSteps(countingFinalResponses(&requests, 2)...))
	sendOneUserInput(t, sess, "first")
	if requests.Load() != 1 {
		t.Fatalf("model requests after the first turn = %d, want 1", requests.Load())
	}

	// Poison the writer through the buffered door recordTurn itself uses: a
	// write that stops partway with no rollback behind it.
	fs := attachEnvironmentFailureFS(t, sess)
	armEnvironmentPartialWrite(fs)
	if err := sess.writeTranscript(schema.NewTurn(schema.TurnAssistant, llm.Assistant("stops partway"))); err == nil {
		t.Fatal("partial transcript write reported success")
	}

	before := len(sessionHistoryText(sess))
	drainPendingEvents(sess)
	_, err := sess.ProcessInput(t.Context(), "second", nil)
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("input against a poisoned transcript = %v, want an error wrapping transcript.ErrWriterPoisoned", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("model requests after the refused input = %d, want the turn never to have run", got)
	}
	if after := len(sessionHistoryText(sess)); after != before {
		t.Fatal("the refused input changed model history")
	}
	// No turn ran in this call, so the session is still in whatever the previous
	// one left it — awaiting, for this fixture — and the emission has to say so
	// rather than report the idle a settled turn would have reached.
	if end := assertRefusalEndedTheInput(t, sess, drainPendingEvents(sess)); end.State != string(SessionAwaiting) {
		t.Fatalf("session-end state = %q, want the %q an untouched pending question leaves", end.State, SessionAwaiting)
	}
}

// TestPoisonedWriterRefusesTheTurnBehindAPoisoningTurn: admission is not the
// only door a turn comes through. A turn's own records can poison the writer
// while it runs — the buffered door warns and lets the turn finish — and the
// drain loop then runs whatever stands behind it as a full turn, model request
// and all, with every record it makes already lost. The refusal has to stand in
// front of every turn, not only the first.
//
// A follow-up is the shape that reaches the model: its user turn is recorded
// through the buffered door (session_lifecycle.go's appendTurn), which warns and
// continues. A drained queue message is claimed durably first ("append claimed
// user input"), so that path already refuses ahead of the model on its own.
func TestPoisonedWriterRefusesTheTurnBehindAPoisoningTurn(t *testing.T) {
	var requests atomic.Int32
	// Three: the turn that emits the environment, the turn whose own record
	// poisons the writer, and the follow-up that must never run.
	steps := countingFinalResponses(&requests, 3)
	sess := newTestSessionForEnvctx(t, withSteps(steps...))
	sendOneUserInput(t, sess, "first")

	fs := attachEnvironmentFailureFS(t, sess)
	steps[1] = func(llm.Request) llm.Response {
		requests.Add(1)
		// The assistant record this response produces is durable and would abort
		// the turn; aim past it at the buffered tool-results record, whose
		// failure warns and lets the turn finish — which is how a writer ends up
		// poisoned with the drain loop still running.
		armEnvironmentPartialWriteAfter(fs, 1)
		return finalResponse("ok")
	}
	sess.FollowUp("runs behind the poisoning")
	drainPendingEvents(sess)

	_, err := sess.ProcessInput(t.Context(), "poisons mid-turn", nil)
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("turn behind the poisoning = %v, want an error wrapping transcript.ErrWriterPoisoned", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("model requests = %d, want the follow-up never to have run", got)
	}
	// A turn ran and nothing is left waiting, so this is the shape where the
	// session really is idle and the emission says idle.
	if end := assertRefusalEndedTheInput(t, sess, drainPendingEvents(sess)); end.State != string(SessionIdle) {
		t.Fatalf("session-end state = %q, want the %q a settled turn with nothing pending leaves", end.State, SessionIdle)
	}
}

// TestPoisonedWriterLeavesAQueuedMessageQueued: the drain pops the queue head
// durably at the bottom of an iteration and the gate refuses at the top of the
// next one, so a message taken off the queue for a turn that never runs is a
// message nobody has any more — it is not in the transcript, not in the queue,
// and not in the session. A turn the gate will refuse must not consume one:
// the message stays queued for the restart that recovers the transcript.
func TestPoisonedWriterLeavesAQueuedMessageQueued(t *testing.T) {
	var requests atomic.Int32
	steps := countingFinalResponses(&requests, 3)
	sess := newTestSessionForEnvctx(t, withSteps(steps...))
	sendOneUserInput(t, sess, "first")

	fs := attachEnvironmentFailureFS(t, sess)
	steps[1] = func(llm.Request) llm.Response {
		requests.Add(1)
		// Past the durable assistant record, onto the buffered tool-results
		// one, so this turn finishes with the writer already poisoned.
		armEnvironmentPartialWriteAfter(fs, 1)
		return finalResponse("ok")
	}
	if err := sess.Enqueue(t.Context(), "waits for the restart"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	drainPendingEvents(sess)

	_, err := sess.ProcessInput(t.Context(), "poisons mid-turn", nil)
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("turn behind the poisoning = %v, want an error wrapping transcript.ErrWriterPoisoned", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("model requests = %d, want the queued message never to have run", got)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("durable queue depth after the refusal = %d, want the message still waiting", got)
	}
	// The message this refusal kept is work still pending, so the session is not
	// idle and the emission must not claim it is: a client told idle with a
	// message still queued would show the thread as finished with it.
	if end := assertRefusalEndedTheInput(t, sess, drainPendingEvents(sess)); end.State != string(SessionProcessing) {
		t.Fatalf("session-end state = %q, want the %q a still-queued message leaves", end.State, SessionProcessing)
	}
}

// TestReturnedDirectTurnClaimReleasesItsOwnUnit: a claim raises the accepted-turn
// count by one, and returning it has to give back that one whoever else claimed
// in between. Releasing against the returning caller's own floor instead makes
// the outcome depend on the order two failed inputs unwind in: the first release
// takes the count below the second's floor, the second then sees nothing to
// return, and the session carries a turn nobody is using toward its max-turn
// limit for the rest of its life.
func TestReturnedDirectTurnClaimReleasesItsOwnUnit(t *testing.T) {
	orders := []struct {
		name    string
		release []int
	}{
		{"first claim returns first", []int{0, 1}},
		{"second claim returns first", []int{1, 0}},
	}
	for _, order := range orders {
		t.Run(order.name, func(t *testing.T) {
			sess := newQueuePersistTestSession(t, t.TempDir())
			defer sess.Close()
			start := sess.clientMutations.snapshot().AcceptedTurns

			// Two direct inputs whose claims interleave: each read the turn
			// counter before the other's turn was counted, so their floors
			// differ by one while both claims stand.
			floors := []uint64{start, start + 1}
			for _, floor := range floors {
				if err := sess.claimDirectClientMutationTurn(floor); err != nil {
					t.Fatalf("claim at floor %d: %v", floor, err)
				}
			}
			if got := sess.clientMutations.snapshot().AcceptedTurns; got != start+2 {
				t.Fatalf("accepted turns while both claims stand = %d, want %d", got, start+2)
			}

			// Post-fix these two calls are indistinguishable, which is the
			// whole of the fix: a release belongs to no particular claim. The
			// table stays because pre-fix they were not, and one order lost.
			for _, claim := range order.release {
				if err := sess.returnClaimedDirectClientMutationTurn(); err != nil {
					t.Fatalf("return the claim taken at floor %d: %v", floors[claim], err)
				}
			}
			if got := sess.clientMutations.snapshot().AcceptedTurns; got != start {
				t.Fatalf("accepted turns after both claims were returned = %d, want the %d they started from", got, start)
			}
		})
	}
}

// TestFailedDirectInputReturnsItsTurnClaim covers the call site the release has:
// a direct input claims a turn, its durable environment append fails, and the
// claim goes back. One failed input, one claim, one return.
func TestFailedDirectInputReturnsItsTurnClaim(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	start := sess.clientMutations.snapshot().AcceptedTurns
	failure := errors.New("environment transcript durability failure")
	attachEnvironmentSyncFailure(t, sess, failure, nil)

	if _, err := sess.ProcessInput(t.Context(), "fails its environment append", nil); !errors.Is(err, failure) {
		t.Fatalf("direct input error = %v, want the environment durability failure", err)
	}
	if got := sess.clientMutations.snapshot().AcceptedTurns; got != start {
		t.Fatalf("accepted turns after the failed input = %d, want the %d it started from", got, start)
	}
}

// poisonSessionTranscript poisons the session's writer through the buffered door
// recordTurn uses, leaving it refusing every later append.
func poisonSessionTranscript(t *testing.T, sess *Session) {
	t.Helper()
	fs := attachEnvironmentFailureFS(t, sess)
	armEnvironmentPartialWrite(fs)
	if err := sess.writeTranscript(schema.NewTurn(schema.TurnAssistant, llm.Assistant("stops partway"))); err == nil {
		t.Fatal("partial transcript write reported success")
	}
	if !sess.attachedTranscript().Poisoned() {
		t.Fatal("the partial write did not poison the writer")
	}
}

// TestPoisonedWriterDoesNotClaimAStartMutation: the turn gate refuses inside the
// drain loop, but ProcessClientMutationStart claims its mutation — and spends a
// turn of the budget — before it gets there. A refusal after that leaves the
// start claimed and the budget gone, with nothing to run it and nothing to give
// it back until a restart recovers the session.
func TestPoisonedWriterDoesNotClaimAStartMutation(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-behind-a-dead-transcript",
		Input:            []appwire.InputItem{{Type: "text", Text: "waits for the restart"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	poisonSessionTranscript(t, sess)
	start := sess.clientMutations.snapshot().AcceptedTurns

	_, _, err := sess.ProcessClientMutationStart(t.Context(), nil)
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("start against a poisoned transcript = %v, want an error wrapping transcript.ErrWriterPoisoned", err)
	}
	if got := sess.clientMutations.snapshot().AcceptedTurns; got != start {
		t.Fatalf("accepted turns after the refusal = %d, want the %d the refused start never spent", got, start)
	}
	if _, runnable := sess.runnableClientMutationStartTurnID(); !runnable {
		t.Fatal("the refused start is no longer runnable: its claim was consumed by a turn that never ran")
	}
}

// TestPoisonedWriterDoesNotPopAQueuedMessage: same door, the other caller.
// ProcessPendingUserInput takes the queue head durably before the gate can
// refuse, so a refusal leaves the message in no queue and no transcript.
func TestPoisonedWriterDoesNotPopAQueuedMessage(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "queued-behind-a-dead-transcript",
		Input:            []appwire.InputItem{{Type: "text", Text: "waits for the restart"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	poisonSessionTranscript(t, sess)

	_, _, err := sess.ProcessPendingUserInput(t.Context(), nil)
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("queued message against a poisoned transcript = %v, want an error wrapping transcript.ErrWriterPoisoned", err)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("durable queue depth after the refusal = %d, want the message still waiting", got)
	}
}

// TestEnvironmentEventPublishesInsideTheTranscriptOrderingBoundary: the entry
// and the live event that announces it are one publication. Emitted after the
// transcript door is released, a concurrent fold can take that door in between,
// commit its compaction markers and flush its own events, so a live reader sees
// the compaction ahead of the environment block while the transcript holds them
// the other way round. The projector reads an environment event as a turn
// boundary, so live then splits or closes a turn cold replay does not.
//
// The door is the statement: while it is held, no fold can publish, so live
// order is transcript order. A publication that can take the door is a
// publication that happens outside it.
func TestEnvironmentEventPublishesInsideTheTranscriptOrderingBoundary(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	publications := 0
	updateSessionTestConfig(sess, func(cfg *testConfig) {
		cfg.beforeEnvironmentEventPublish = func() {
			publications++
			if sess.attentionMu.TryLock() {
				sess.attentionMu.Unlock()
				t.Error("environment event published with the transcript door open: a fold can commit and flush its own events between the entry and this event")
			}
		}
	})

	if err := sess.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if publications != 1 {
		t.Fatalf("environment event publications = %d, want the one the appended entry owes", publications)
	}
	ids := environmentEventTurnIDs(t, drainPendingEvents(sess))
	if durable := durableEnvironmentTurnIDs(t, sess); !reflect.DeepEqual(ids, durable) {
		t.Fatalf("live environment events = %v, want the durable entries %v they announce", ids, durable)
	}
}

// TestPoisonedWriterStandsDownOnAnIdleWake: the refusal is for work the session
// would otherwise claim and lose. An idle wake has nothing to claim, so a dead
// transcript is not its business — answering it with an error turns every poll
// into a logged failure while the session waits for its restart.
func TestPoisonedWriterStandsDownOnAnIdleWake(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	poisonSessionTranscript(t, sess)
	if got := sess.QueueDepth(); got != 0 {
		t.Fatalf("setup: queue depth = %d, want nothing queued", got)
	}

	result, ran, err := sess.ProcessPendingUserInput(t.Context(), nil)
	if result != "" || ran || err != nil {
		t.Fatalf("idle wake against a poisoned transcript = (%q, %v, %v), want a quiet stand-down", result, ran, err)
	}
}

// TestPoisonedWriterRefusesAWakeCarryingSteering: pending user steering is work
// the wake would carry into a turn, so a dead transcript has to refuse it the
// way it refuses a queued message — and leave the steering where it is, for the
// turn that runs after the restart.
func TestPoisonedWriterRefusesAWakeCarryingSteering(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	poisonSessionTranscript(t, sess)
	sess.SteerFromUser("waits for the restart")
	if got := sess.QueueDepth(); got != 0 {
		t.Fatalf("setup: queue depth = %d, want the steering to be the only work", got)
	}

	var announced []string
	result, ran, err := sess.ProcessPendingUserInput(t.Context(), func(turnID string) {
		announced = append(announced, turnID)
	})
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("wake carrying steering = (%q, %v, %v), want an error wrapping transcript.ErrWriterPoisoned", result, ran, err)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("the refused wake consumed the steering it never delivered")
	}
	// Refusing before the steering carrier is claimed is the point: a wake that
	// claims first tells the daemon a turn is running and then fails it, so a
	// client sees a turn begin and die for every poll until the restart.
	if len(announced) != 0 {
		t.Fatalf("the refused wake announced turns %v, want a refusal ahead of any claim", announced)
	}
}

// TestPoisonedWriterStandsDownOnParkedQueuedWork: a Stop parks the queue, and
// parked work is not work this wake can take — popQueueHead refuses it at the
// same flag. A refusal here would answer every wake with an error for a message
// the session was never going to claim, which is the idle case in a different
// coat.
func TestPoisonedWriterStandsDownOnParkedQueuedWork(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "parked-behind-a-stop", "parked")
	if _, err := sess.InterruptClientMutation(t.Context(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-the-parked-queue",
	}, func() {}); err != nil {
		t.Fatalf("stop over the queue: %v", err)
	}
	if !sess.clientMutations.queueHeld() {
		t.Fatal("setup: the Stop did not park the queue")
	}
	poisonSessionTranscript(t, sess)

	result, ran, err := sess.ProcessPendingUserInput(t.Context(), nil)
	if result != "" || ran || err != nil {
		t.Fatalf("wake over parked queued work = (%q, %v, %v), want a quiet stand-down", result, ran, err)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth after the stand-down = %d, want the parked message untouched", got)
	}
	if !sess.clientMutations.queueHeld() {
		t.Fatal("the stand-down released the Stop's hold on the queue")
	}
}

// TestPoisonedWriterStandsDownOnParkedSteering: the same, through the steering
// rail. A parked steer stays pending — it never moves — so the raw count says
// work while the claim gate says none.
func TestPoisonedWriterStandsDownOnParkedSteering(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-parked-by-the-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	if _, err := sess.InterruptClientMutation(t.Context(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-the-pending-steer",
	}, func() {}); err != nil {
		t.Fatalf("stop over the steer: %v", err)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("setup: the Stop did not park the steering rail")
	}
	poisonSessionTranscript(t, sess)

	result, ran, err := sess.ProcessPendingUserInput(t.Context(), nil)
	if result != "" || ran || err != nil {
		t.Fatalf("wake over parked steering = (%q, %v, %v), want a quiet stand-down", result, ran, err)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("the stand-down consumed the parked steer")
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("the stand-down released the Stop's hold on the steering rail")
	}
}

// interruptDrainTurnContext returns the context a turn runs under when the host
// has wired the interrupt drain (cmd/evener serve's shape), together with the
// cancel that interrupts that turn while the root stays live -- the state in
// which interruptDrainConfig agrees the interrupted turn may take the queue
// head.
func interruptDrainTurnContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	root, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	turnCtx, cancelTurn := context.WithCancel(root)
	t.Cleanup(cancelTurn)
	var nextCtx func(context.Context) (context.Context, context.CancelFunc)
	nextCtx = func(parent context.Context) (context.Context, context.CancelFunc) {
		next, cancel := context.WithCancel(parent)
		t.Cleanup(cancel)
		return WithQueuedInputDrainOnInterruptHandler(next, root, nextCtx), cancel
	}
	return WithQueuedInputDrainOnInterruptHandler(turnCtx, root, nextCtx), cancelTurn
}

// TestPoisonedWriterLeavesTheInterruptDrainedMessageQueued: the interrupt
// recovery is the drain loop's other claim site. A turn whose own record
// poisoned the writer and that then ends by interrupt hands the queue head to
// the next iteration, which the gate at the top of the loop refuses -- so the
// message is in no transcript, no queue and no session, recoverable only by
// restarting. The claim has to be refused ahead of the pop, the same way the
// completed-turn drain below it refuses.
func TestPoisonedWriterLeavesTheInterruptDrainedMessageQueued(t *testing.T) {
	var requests atomic.Int32
	steps := countingFinalResponses(&requests, 3)
	sess := newTestSessionForEnvctx(t, withSteps(steps...))
	sendOneUserInput(t, sess, "first")

	turnCtx, interrupt := interruptDrainTurnContext(t)
	fs := attachEnvironmentFailureFS(t, sess)
	// The buffered user-input record poisons the writer before the model is
	// asked, and the interrupt then ends the turn with the bare cancellation
	// the drain keys on.
	armEnvironmentPartialWrite(fs)
	steps[1] = func(llm.Request) llm.Response {
		requests.Add(1)
		interrupt()
		return finalResponse("ok")
	}
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "queued-behind-an-interrupt",
		Input:            []appwire.InputItem{{Type: "text", Text: "waits for the restart"}},
	}); err != nil {
		t.Fatal(err)
	}
	drainPendingEvents(sess)

	_, err := sess.ProcessInput(turnCtx, "poisons mid-turn", nil)
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("interrupted turn behind the poisoning = %v, want an error wrapping transcript.ErrWriterPoisoned", err)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("durable queue depth after the refusal = %d, want the message still waiting", got)
	}
}

// TestRejectedInputDoesNotPersistItsProvisionalTurn: acceptUserInput counts the
// turn before it appends the environment context, and takes the count back when
// that append fails. An append whose entry reconciliation proves durable
// checkpoints metadata on its way out, while that provisional count is still
// standing, and then reports the failure that rejects the input -- so the
// rollback behind it owes a checkpoint of its own. processOneInput's deferred
// save is that checkpoint, and this pins it: without one, the restart loads a
// turn no user ever spent and charges it against MaxTurns for the life of the
// session.
func TestRejectedInputDoesNotPersistItsProvisionalTurn(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	syncFailure := errors.New("environment transcript durability failure")
	rollbackFailure := errors.New("environment transcript rollback failure")
	attachEnvironmentAmbiguousWrite(t, sess, syncFailure, rollbackFailure)

	if _, err := sess.ProcessInput(t.Context(), "rejected by its environment append", nil); !errors.Is(err, syncFailure) {
		t.Fatalf("rejected input error = %v, want the environment durability failure", err)
	}
	if got := loadMetaForTest(t, sess).AcceptedInputTurns; got != 0 {
		t.Fatalf("persisted accepted input turns after the rejected input = %d, want the none it accepted", got)
	}
	restored := restoreQueuePersistTestSession(t, sess.stateDir, sess.ID())
	defer restored.Close()
	if got := restored.turns; got != 0 {
		t.Fatalf("restarted session accepted input turns = %d, want the none the rejected input accepted", got)
	}
}

// TestPoisonedWriterRefusesToPublishAFold: a fold is the other durable write
// the session makes, and it is one the session reports as done. The publication
// transaction swaps model history, resets the environment tracker and tells
// every client the context was compacted, and only then writes the checkpoint
// and summary entries that make the fold survive a restart. A poisoned writer
// refuses every one of those entries, so a published fold there is a compaction
// the session announces and acts on and the transcript never holds -- the
// restart brings back the pre-compaction history. The turn loop already fails
// closed on this writer before admitting a turn; the fold has to fail closed
// before publishing, for the same reason and at the same door.
func TestPoisonedWriterRefusesToPublishAFold(t *testing.T) {
	dir := t.TempDir()
	sess := newScriptedSummaryCompactSession(t, "poisoned-fold-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withDir(dir), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))
	if err := sess.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if !sess.envTracker.State().HasSent {
		t.Fatal("the environment tracker has nothing to lose; this test is not in the state it means to be")
	}
	seedNumberedSessionHistory(t, sess, 12) // > PreserveRecentTurns(6): forces a real fold
	poisonSessionTranscript(t, sess)

	sess.mu.Lock()
	before := append([]schema.Turn(nil), sess.history...)
	beforeRevision := sess.historyRevision
	sess.mu.Unlock()
	drainPendingEvents(sess)

	if err := sess.Compact(t.Context()); err == nil {
		t.Fatal("compaction against a poisoned transcript reported success")
	}

	sess.mu.Lock()
	after := append([]schema.Turn(nil), sess.history...)
	afterRevision := sess.historyRevision
	sess.mu.Unlock()
	if afterRevision != beforeRevision || !reflect.DeepEqual(after, before) {
		t.Fatalf("history was folded on a poisoned transcript: %d turns at revision %d became %d turns at revision %d, and no transcript entry records it",
			len(before), beforeRevision, len(after), afterRevision)
	}
	if !sess.envTracker.State().HasSent {
		t.Fatal("environment tracking was reset for a fold the transcript never took: the next turn renders a full block for a compaction that did not durably happen")
	}
	for _, event := range drainPendingEvents(sess) {
		if event.Kind == events.EventContextCompaction {
			t.Fatal("a compaction the transcript refused was announced to clients")
		}
	}
}

// TestClosingSessionRefusesEnvironmentPublication: shutdown claims the session
// before it publishes the terminal boundary, and an environment append landing
// in that window would put a live EventEnvironment and a durable environment
// entry behind SESSION_END -- context for a session that is over, in a
// transcript whose writer is about to close. The append has to observe the
// shutdown state, and it has to observe it under the same lock that orders the
// publication: attentionMu is the transcript door the environment event is
// already published under for exactly that ordering reason.
//
// The observation point is Close's own dispose/sweep seam, which runs with
// `closing` already set and SESSION_END still ahead of it, and with no session
// lock held -- so the append runs there the way a racing turn's would, without
// a second goroutine or a sleep to make the interleaving happen.
func TestClosingSessionRefusesEnvironmentPublication(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	var appendErr error
	var appended bool
	updateSessionTestConfig(sess, func(cfg *testConfig) {
		cfg.closeAfterDisposeSweepJoin = func() {
			appended = true
			appendErr = sess.maybeAppendEnvironmentContext()
		}
	})
	collected, eventsMu, done := collectEvents(sess)

	sess.Close()
	<-done

	if !appended {
		t.Fatal("the close seam never ran; this test is not in the state it means to be")
	}
	if appendErr != nil {
		t.Fatalf("environment append during shutdown = %v, want a silent no-op", appendErr)
	}
	if got := countEnvironmentTurns(sess); got != 0 {
		t.Fatalf("model history environment turns after the shutdown append = %d, want none: the session is closed and nothing will read them", got)
	}
	if got := durableEnvironmentTurnIDs(t, sess); len(got) != 0 {
		t.Fatalf("durable environment entries written during shutdown = %v, want none behind the terminal boundary", got)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	end := -1
	for i, event := range *collected {
		switch event.Kind {
		case events.EventSessionEnd:
			end = i
		case events.EventEnvironment:
			if end >= 0 {
				t.Fatalf("EventEnvironment published at index %d, after the SESSION_END at %d", i, end)
			}
			t.Fatalf("EventEnvironment published at index %d by a session already shutting down", i)
		}
	}
	if end < 0 {
		t.Fatal("no SESSION_END was published; this test is not in the state it means to be")
	}
}

// TestPoisonedCompactionReportsDurabilityNotARace: publishFoldTransaction
// refuses two very different ways -- a competing fold won the publication race,
// and the transcript has stopped accepting records -- and the fold loop
// collapses both into "try again". A race is worth retrying and a poisoned
// writer never will be, so the operator retyping /compact against a dead
// transcript has to be told what actually stopped it.
func TestPoisonedCompactionReportsDurabilityNotARace(t *testing.T) {
	dir := t.TempDir()
	sess := newScriptedSummaryCompactSession(t, "poisoned-compaction-message", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withDir(dir), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))
	seedNumberedSessionHistory(t, sess, 12) // > PreserveRecentTurns(6): forces a real fold
	poisonSessionTranscript(t, sess)

	err := sess.Compact(t.Context())
	if err == nil {
		t.Fatal("compaction against a poisoned transcript reported success")
	}
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("compaction error = %v, want one wrapping transcript.ErrWriterPoisoned", err)
	}
	if strings.Contains(err.Error(), "publication race") {
		t.Fatalf("compaction error = %v, want a durability failure rather than a race the operator can never win", err)
	}
}

// TestTurnWhoseOwnInputPoisonedTheTranscriptNeverRuns: the drain loop refuses a
// turn on a poisoned writer at the TOP of each iteration, which is one turn too
// late for the turn that did the poisoning. When the USER_INPUT record is
// itself the write that lands partially and stops the writer, this turn's every
// later record -- the assistant answer, its tool calls, their results -- is
// already lost, and the input it would run is in no transcript either. The turn
// has to be refused where the poisoning happened: before the input is announced
// and before the model is asked.
func TestTurnWhoseOwnInputPoisonedTheTranscriptNeverRuns(t *testing.T) {
	var requests atomic.Int32
	steps := countingFinalResponses(&requests, 3)
	sess := newTestSessionForEnvctx(t, withSteps(steps...))
	sendOneUserInput(t, sess, "first") // settles the environment block
	before := sess.clientMutations.snapshot().AcceptedTurns
	sess.mu.Lock()
	turnsBefore, historyBefore := sess.turns, len(sess.history)
	sess.mu.Unlock()

	fs := attachEnvironmentFailureFS(t, sess)
	// The USER_INPUT record is the next write, and it stops partway.
	armEnvironmentPartialWrite(fs)
	drainPendingEvents(sess)

	_, err := sess.ProcessInput(t.Context(), "poisons its own input record", nil)
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("turn whose input poisoned the writer = %v, want an error wrapping transcript.ErrWriterPoisoned", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("model requests = %d, want only the first turn's: the poisoned turn must not reach the model", got)
	}
	for _, event := range drainPendingEvents(sess) {
		if event.Kind == events.EventUserInput {
			t.Fatal("the refused input was announced to clients")
		}
	}
	sess.mu.Lock()
	turnsAfter, historyAfter := sess.turns, len(sess.history)
	sess.mu.Unlock()
	if historyAfter != historyBefore {
		t.Fatalf("model history grew from %d to %d turns for an input no transcript holds", historyBefore, historyAfter)
	}
	if turnsAfter != turnsBefore {
		t.Fatalf("accepted input turns = %d, want the %d the refused input never spent", turnsAfter, turnsBefore)
	}
	if got := sess.clientMutations.snapshot().AcceptedTurns; got != before {
		t.Fatalf("durable accepted turns = %d, want the %d the returned claim restores", got, before)
	}
}

// TestPoisonedToolResultStopsTheInputBeforeTheNextRound: the poisoned-writer
// rule refuses a turn at admission, but an input does not stop being admitted
// after its first round. A tool-result record that lands partially stops the
// writer mid-input, and every round behind it -- another model request, another
// batch of tool executions, their results -- is then work whose every record is
// lost, run against a transcript that already refuses them. The rule has to
// hold before each subsequent round of the same input, not only before the
// first.
func TestPoisonedToolResultStopsTheInputBeforeTheNextRound(t *testing.T) {
	var requests atomic.Int32
	steps := make([]func(llm.Request) llm.Response, 3)
	steps[0] = func(llm.Request) llm.Response { requests.Add(1); return finalResponse("ok") }
	steps[2] = func(llm.Request) llm.Response { requests.Add(1); return finalResponse("must never run") }
	sess := newTestSessionForEnvctx(t, withSteps(steps...))
	sendOneUserInput(t, sess, "first") // settles the environment block

	fs := attachEnvironmentFailureFS(t, sess)
	steps[1] = func(llm.Request) llm.Response {
		requests.Add(1)
		// Past the durable assistant record, onto the buffered tool-results
		// one: that write stops partway and poisons the writer.
		armEnvironmentPartialWriteAfter(fs, 1)
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "running a tool"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_1",
				Name:      "glob",
				Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`),
				Type:      "function",
			}},
		}}}
	}
	drainPendingEvents(sess)

	_, err := sess.ProcessInput(t.Context(), "its tool result poisons the writer", nil)
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("input whose tool result poisoned the writer = %v, want an error wrapping transcript.ErrWriterPoisoned", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("model requests = %d, want 2 (the first turn and the poisoning round): no round may run behind a transcript that refuses its records", got)
	}
}
