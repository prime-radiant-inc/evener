package agent

import (
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type environmentSyncFailureFS struct {
	afero.Fs
	mu              sync.Mutex
	failure         error
	rollbackFailure error
	seekFailure     error
	onFailure       func()
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
	if err := sess.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if got := durableEnvironmentTurnIDs(t, sess); !reflect.DeepEqual(got, retained) {
		t.Fatalf("durable environment entries after the next turn = %v, want only the entry already in the transcript %v", got, retained)
	}
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
}

// TestEnvironmentAmbiguousWriteKeepsEntryWhenTranscriptUnreadable: an
// indeterminate append the session cannot read back is still indeterminate.
// Absence has to be confirmed, never inferred from a failed reconciliation, or
// a transcript that cannot be read becomes a licence to write the environment
// a second time.
func TestEnvironmentAmbiguousWriteKeepsEntryWhenTranscriptUnreadable(t *testing.T) {
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
	ambiguous := durableEnvironmentTurnIDs(t, sess)
	if len(ambiguous) != 1 || ambiguous[0] == "" {
		t.Fatalf("durable environment entries after the failed rollback = %v, want the one entry rollback could not remove", ambiguous)
	}

	assertEnvironmentNotReemitted(t, sess, ambiguous)
}

// TestEnvironmentAmbiguousWriteKeepsEntryWhenDurabilityUnestablished: the
// reconciliation reads the transcript back only once it has raised a durability
// barrier over it. A barrier that cannot be raised leaves the entry's fate
// unknowable, which is not the same as knowing it is gone.
func TestEnvironmentAmbiguousWriteKeepsEntryWhenDurabilityUnestablished(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	syncFailure := errors.New("environment transcript durability failure")
	rollbackFailure := errors.New("environment transcript rollback failure")
	durabilityFailure := errors.New("environment transcript barrier failure")
	attachEnvironmentUnverifiableWrite(t, sess, syncFailure, rollbackFailure, durabilityFailure)

	err := sess.maybeAppendEnvironmentContext()
	if !errors.Is(err, syncFailure) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("ambiguous append error = %v, want both the sync and the rollback failure", err)
	}
	ambiguous := durableEnvironmentTurnIDs(t, sess)
	if len(ambiguous) != 1 || ambiguous[0] == "" {
		t.Fatalf("durable environment entries after the failed rollback = %v, want the one entry rollback could not remove", ambiguous)
	}

	assertEnvironmentNotReemitted(t, sess, ambiguous)
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

// TestEnvironmentEntryOutcomeZeroValueIsConservative: reconciliation's outcome
// decides whether the tracker rewinds, and a rewind against an entry that is
// really there duplicates the environment. A future path that returns the
// type's zero value — a bare declaration, a struct field, a failed decode —
// must therefore land on the outcome that does nothing, not on the one that
// re-renders.
func TestEnvironmentEntryOutcomeZeroValueIsConservative(t *testing.T) {
	var outcome environmentEntryOutcome
	if outcome != environmentEntryUnknown {
		t.Fatalf("zero-valued outcome = %d, want environmentEntryUnknown (%d) so an unset outcome cannot rewind the tracker", outcome, environmentEntryUnknown)
	}
}
