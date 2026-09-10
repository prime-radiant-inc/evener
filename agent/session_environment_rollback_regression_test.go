package agent

import (
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type environmentSyncFailureFS struct {
	afero.Fs
	mu        sync.Mutex
	failure   error
	onFailure func()
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

func attachEnvironmentSyncFailure(t *testing.T, sess *Session, failure error, onFailure func()) {
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
	fs.mu.Lock()
	fs.failure = failure
	fs.onFailure = onFailure
	fs.mu.Unlock()
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
