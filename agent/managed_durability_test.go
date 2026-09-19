package agent

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// The wrapper faults actual file operations; all retained bytes and reopens use
// the real filesystem and transcript writer, not an in-memory journal replica.
type managedFaultFS struct {
	afero.Fs
	mu                     sync.Mutex
	failSync, failTruncate bool
	syncFailures           int
}
type managedFaultFile struct {
	afero.File
	fs *managedFaultFS
}

func (f *managedFaultFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &managedFaultFile{file, f}, nil
}
func (f *managedFaultFile) Sync() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.fs.failSync {
		f.fs.syncFailures++
		return errors.New("injected transcript sync")
	}
	return f.File.Sync()
}
func (f *managedFaultFile) Truncate(n int64) error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.fs.failTruncate {
		return errors.New("injected truncate")
	}
	return f.File.Truncate(n)
}
func (f *managedFaultFS) set(syncFail, truncateFail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failSync, f.failTruncate = syncFail, truncateFail
}
func installManagedTranscriptFault(t *testing.T, s *Session) *managedFaultFS {
	t.Helper()
	if err := s.transcript.Close(); err != nil {
		t.Fatal(err)
	}
	fs := &managedFaultFS{Fs: afero.NewOsFs()}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.id)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.transcript = writer
	s.mu.Unlock()
	return fs
}
func TestManagedAssistantRetainedUnsyncedPreventsDispatch(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	fs := installManagedTranscriptFault(t, s)
	fs.set(true, true)
	err := s.appendAssistantTurn(managedCallStep(llm.Request{}), ModelAttemptMetadata{AttemptGroupID: "unsynced-round"})
	if !errors.Is(err, transcript.ErrRetainedUnsynced) {
		t.Fatalf("assistant append=%v", err)
	}
	if len(f.requests) != 0 || len(s.managedJournal.pending()) != 0 {
		t.Fatal("unsynced assistant admitted mutation")
	}
	fs.set(false, false)
}
func TestManagedJournalBarriersPreventDispatchAndKeepSameInvocation(t *testing.T) {
	for _, point := range []string{"file_sync", "directory_sync"} {
		t.Run(point, func(t *testing.T) {
			f := &managedFixture{}
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
			response := managedCallStep(llm.Request{})
			if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "barrier-round"}); err != nil {
				t.Fatal(err)
			}
			reached := false
			s.managedJournal.fault = func(p string) error {
				if p == point {
					reached = true
					return errors.New("journal barrier")
				}
				return nil
			}
			call := response.ToolCalls()[0]
			result := s.execTool(s.managedCallContext(t.Context(), 0), call, "")
			pending := s.managedJournal.pending()
			if !reached || len(f.requests) != 0 || !result.IsError || len(pending) != 1 {
				t.Fatalf("result=%+v pending=%+v", result, pending)
			}
			s.managedJournal.fault = nil
			if err := s.reconcileManagedInvocations(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(f.requests) != 1 || f.requests[0].InvocationID != pending[0].Request.InvocationID {
				t.Fatalf("recovery reminted: %+v", f.requests)
			}
		})
	}
}
func TestManagedRetainedToolResultIsSyncedWithoutDuplicate(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	fs := installManagedTranscriptFault(t, s)
	response := managedCallStep(llm.Request{})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "result-round"}); err != nil {
		t.Fatal(err)
	}
	results, err := s.execToolBatch(t.Context(), response.ToolCalls(), s.currentProfile(), "")
	if err != nil {
		t.Fatal(err)
	}
	fs.set(true, true)
	if err := s.persistToolResults(t.Context(), response.ToolCalls(), results); !errors.Is(err, transcript.ErrRetainedUnsynced) {
		t.Fatalf("result append=%v", err)
	}
	if len(s.managedJournal.pending()) != 1 {
		t.Fatal("unsynced result evicted journal")
	}
	fs.set(false, false)
	if err := s.reconcileManagedInvocations(t.Context()); err != nil {
		t.Fatal(err)
	}
	full, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range full.Entries {
		for _, part := range entry.Turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.Name == "managed_write" {
				count++
			}
		}
	}
	if count != 1 || len(f.requests) != 1 || len(s.managedJournal.pending()) != 0 {
		t.Fatalf("results=%d backend=%d pending=%d", count, len(f.requests), len(s.managedJournal.pending()))
	}
}

func TestManagedRejectedJournalAdmissionHasNoPendingResultIdentity(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	response := managedCallStep(llm.Request{})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "capacity-round"}); err != nil {
		t.Fatal(err)
	}
	// An actual invalid request from the application must fail before admission.
	f.prepareErr = nil
	broken := &invalidManagedPrepare{managedFixture: f}
	s.managedBinding = broken
	result := s.execTool(s.managedCallContext(t.Context(), 0), response.ToolCalls()[0], "")
	if !result.IsError || result.ManagedInvocationID != "" || len(s.managedJournal.pending()) != 0 || len(f.requests) != 0 {
		t.Fatalf("invalid admission=%+v", result)
	}
}

type invalidManagedPrepare struct{ *managedFixture }

func (f *invalidManagedPrepare) Prepare(ctx context.Context, call ManagedCall) (ManagedRequest, error) {
	request, err := f.managedFixture.Prepare(ctx, call)
	request.Identity.NamespaceID = ""
	return request, err
}

func TestManagedLateResolutionRetainedThenReopenedDoesNotDuplicate(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}), withSteps(managedCallStep, managedDoneStep))
	if _, err := s.ProcessInput(t.Context(), "run", nil); err != nil {
		t.Fatal(err)
	}
	fs := installManagedTranscriptFault(t, s)
	f.executeErr = nil
	f.afterExecute = func() { fs.set(true, true) }
	err := s.reconcileManagedInvocations(t.Context())
	if !errors.Is(err, transcript.ErrRetainedUnsynced) || len(s.managedJournal.pending()) != 1 {
		t.Fatalf("late append=%v pending=%+v", err, s.managedJournal.pending())
	}
	fs.set(false, false)
	f.afterExecute = nil
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	full, err := readTranscriptFull(restored.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range full.Entries {
		if entry.Turn.ManagedResolution != nil {
			count++
		}
	}
	if count != 1 || len(f.requests) != 2 || len(restored.managedJournal.pending()) != 0 {
		t.Fatalf("resolutions=%d backend=%d pending=%d", count, len(f.requests), len(restored.managedJournal.pending()))
	}
}

func TestManagedNilWriterCannotSettleOriginalReceipt(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	response := managedCallStep(llm.Request{})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "nil-result-writer"}); err != nil {
		t.Fatal(err)
	}
	results, err := s.execToolBatch(t.Context(), response.ToolCalls(), s.currentProfile(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.transcript.Close(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.transcript = nil
	s.mu.Unlock()
	if err = s.persistToolResults(t.Context(), response.ToolCalls(), results); !errors.Is(err, transcript.ErrWriterClosed) {
		t.Fatalf("nil result writer: %v", err)
	}
	pending := s.managedJournal.pending()
	if len(pending) != 1 || pending[0].Result == nil || pending[0].Result.Host == nil {
		t.Fatal("nil writer discarded original receipt")
	}
}
func TestManagedCancellationPreservesReturnedReceipt(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &managedFixture{afterExecute: cancel}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	response := managedCallStep(llm.Request{})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "cancel-result"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.execToolBatch(ctx, response.ToolCalls(), s.currentProfile(), "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	pending := s.managedJournal.pending()
	if len(pending) != 1 || pending[0].Result == nil || pending[0].Result.Host == nil {
		t.Fatal("cancellation discarded returned receipt")
	}
	f.afterExecute = nil
	if err = s.reconcileManagedInvocations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 || len(s.managedJournal.pending()) != 0 {
		t.Fatal("cancellation forced duplicate backend call")
	}
}
