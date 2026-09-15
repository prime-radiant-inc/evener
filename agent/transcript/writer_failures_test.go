package transcript

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func newFailureTestWriter(t *testing.T, path string) *Writer {
	t.Helper()
	w, err := NewWriter(path, Header{
		SessionID: "sess-failures",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	return w
}

func TestWriterReportsNoCountUntilItIsAskedToTrack(t *testing.T) {
	// Absent and zero are different claims. A writer nobody installed a counter
	// on has measured nothing, and must say so rather than report a clean 0 —
	// a producer that forgets to track has to fall silent, not vouch.
	w := newFailureTestWriter(t, filepath.Join(t.TempDir(), "transcript.jsonl"))
	defer w.Close()

	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if count, ok := w.FailedToolCalls(); ok {
		t.Fatalf("FailedToolCalls() = (%d, true), want absent on an untracked writer", count)
	}
}

func TestWriterCountsFailuresAsTheyAreWritten(t *testing.T) {
	w := newFailureTestWriter(t, filepath.Join(t.TempDir(), "transcript.jsonl"))
	defer w.Close()
	w.TrackFailures(nil, 0)

	if count, ok := w.FailedToolCalls(); !ok || count != 0 {
		t.Fatalf("FailedToolCalls() = (%d, %t), want a measured 0 before anything is written", count, ok)
	}
	if err := w.Append(toolCallTurn("call_1", "shell")); err != nil {
		t.Fatalf("Append call: %v", err)
	}
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", ToolState: exitState(1)})); err != nil {
		t.Fatalf("Append result: %v", err)
	}
	if count, ok := w.FailedToolCalls(); !ok || count != 1 {
		t.Fatalf("FailedToolCalls() = (%d, %t), want (1, true) right after the failure landed", count, ok)
	}
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_2", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append second result: %v", err)
	}
	if count, _ := w.FailedToolCalls(); count != 2 {
		t.Fatalf("FailedToolCalls() = %d, want 2", count)
	}
}

func TestWriterSeedsFromTheEntriesAlreadyOnDisk(t *testing.T) {
	// A resumed session's earlier failures are on disk and nowhere in memory
	// (compaction rewrites history). Seeding from the entries the resume read
	// is what makes the live figure whole-session rather than since-restart.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w := newFailureTestWriter(t, path)
	w.TrackFailures(nil, 0)
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, entries, err := OpenWriterForSession(path, "sess-failures")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	defer reopened.Close()
	reopened.TrackFailures(entries, 0)

	if count, ok := reopened.FailedToolCalls(); !ok || count != 1 {
		t.Fatalf("FailedToolCalls() = (%d, %t), want (1, true) seeded from the transcript", count, ok)
	}
	if err := reopened.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_2", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append after resume: %v", err)
	}
	if count, _ := reopened.FailedToolCalls(); count != 2 {
		t.Fatalf("FailedToolCalls() = %d, want 2 (seed plus this run)", count)
	}
}

func TestWriterKeepsItsCountAfterClose(t *testing.T) {
	// A session that ends while someone is watching keeps serving its thread
	// from the daemon until the next read reroutes to disk. The final count has
	// to survive Close or the figure blinks out exactly at the moment it settles.
	w := newFailureTestWriter(t, filepath.Join(t.TempDir(), "transcript.jsonl"))
	w.TrackFailures(nil, 0)
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if count, ok := w.FailedToolCalls(); !ok || count != 1 {
		t.Fatalf("FailedToolCalls() after Close = (%d, %t), want (1, true)", count, ok)
	}
}

func TestWriterDoesNotCountAnEntryThatFailedToLand(t *testing.T) {
	// The count is a statement about the transcript, so it may only move for
	// bytes that actually reached it — an append that errored (and rolled back)
	// leaves no glyph to agree with.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w := newFailureTestWriter(t, path)
	w.TrackFailures(nil, 0)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A closed writer drops appends on the floor; nothing lands, nothing counts.
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append after close: %v", err)
	}
	if count, _ := w.FailedToolCalls(); count != 0 {
		t.Fatalf("FailedToolCalls() = %d, want 0: the entry never reached the transcript", count)
	}
}

// landThenFailFS writes everything it is given and then reports failure: the
// one shape where a buffered append leaves a whole, readable entry behind and
// still returns an error.
type landThenFailFS struct {
	afero.Fs
	armed atomic.Bool
}

func (fs *landThenFailFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &landThenFailFile{File: file, fs: fs}, nil
}

func (fs *landThenFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &landThenFailFile{File: file, fs: fs}, nil
}

type landThenFailFile struct {
	afero.File
	fs *landThenFailFS
}

func (file *landThenFailFile) Write(p []byte) (int, error) {
	n, err := file.File.Write(p)
	if err == nil && file.fs.armed.Load() {
		return n, errors.New("injected write failure with the line landed")
	}
	return n, err
}

// The buffered door rolls nothing back, so a write that reported failure with
// the whole line written leaves a record every reader of this file will find.
// The durable door says so with ErrEntryRetained; this one has to say the same
// thing, or the same failure means two different things depending on which
// door the caller used.
func TestBufferedAppendReportsAnEntryItCouldNotTakeBack(t *testing.T) {
	fs := &landThenFailFS{Fs: afero.NewMemMapFs()}
	w, err := NewWriterWithFS(fs, "/retained-buffered.jsonl", Header{SessionID: "sess-retained"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fs.armed.Store(true)

	turn := schema.NewTurn(schema.TurnUserInput, llm.User("the line that landed"))
	appendErr := w.Append(turn)
	if appendErr == nil {
		t.Fatal("Append reported success; the injected write failure never reached it")
	}
	if !errors.Is(appendErr, ErrEntryRetained) {
		t.Fatalf("Append error = %v, want it to carry ErrEntryRetained: the whole line is in the file", appendErr)
	}
	data, readErr := afero.ReadFile(fs, "/retained-buffered.jsonl")
	if readErr != nil {
		t.Fatalf("read transcript: %v", readErr)
	}
	if !bytes.Contains(data, []byte("the line that landed")) {
		t.Fatalf("test setup: the line is not in the file, so nothing was retained: %q", data)
	}
}

// A fold's replay copy is a second record of a turn this file already holds,
// so the failing tool result inside it is the same failure the original
// already settled. Counting it charges the session twice for one failure — the
// same double count the item readers already refuse (countsTowardTotals). The
// names a copy announces are still learned: a call the copy carries can be the
// one a later result answers by id.
func TestWriterDoesNotCountAFailureTwiceBecauseAFoldCopiedIt(t *testing.T) {
	counter := NewFailureCounter(0)
	call := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind:     llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{ID: "call_copy", Name: "read_file"},
	}}})
	result := toolResultTurn(llm.ToolResultData{ToolCallID: "call_copy", IsError: true})
	counter.Observe(call)
	counter.Observe(result)
	if got := counter.Count(); got != 1 {
		t.Fatalf("failures after the original round = %d, want 1", got)
	}

	copyOf := func(turn schema.Turn) schema.Turn {
		turn.ContextReplay = true
		turn.CompactionFoldID = "fold_failures"
		return turn
	}
	counter.Observe(copyOf(call))
	counter.Observe(copyOf(result))
	if got := counter.Count(); got != 1 {
		t.Fatalf("failures after the fold copied the round = %d, want the same 1: a copy is the same failure written down again", got)
	}

	// The copy's call is still a name the counter knows, so a result that
	// arrives later and names only the call id is still judged by tool.
	late := toolResultTurn(llm.ToolResultData{ToolCallID: "call_copy", IsError: true})
	counter.Observe(late)
	if got := counter.Count(); got != 2 {
		t.Fatalf("failures after a genuinely new result = %d, want 2", got)
	}
}

// syncFailsRollbackRefusedFS lets the line land, fails the sync that would make
// it durable, and then refuses the truncate that would take it back out — the
// one shape where the durable door reports an entry it can neither make durable
// nor remove.
type syncFailsRollbackRefusedFS struct {
	afero.Fs
	armed atomic.Bool
}

func (fs *syncFailsRollbackRefusedFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &syncFailsRollbackRefusedFile{File: file, fs: fs}, nil
}

func (fs *syncFailsRollbackRefusedFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &syncFailsRollbackRefusedFile{File: file, fs: fs}, nil
}

type syncFailsRollbackRefusedFile struct {
	afero.File
	fs *syncFailsRollbackRefusedFS
}

func (file *syncFailsRollbackRefusedFile) Sync() error {
	if file.fs.armed.Load() {
		return errors.New("injected sync failure after the record landed")
	}
	return file.File.Sync()
}

func (file *syncFailsRollbackRefusedFile) Truncate(size int64) error {
	if file.fs.armed.Load() {
		return errors.New("injected truncate failure: the line stays in the file")
	}
	return file.File.Truncate(size)
}

// A durable append whose sync failed and whose rollback could not take the line
// back out leaves a readable entry on a file nothing synced. The entry is a
// record — ErrEntryRetained says so, and every returning reader finds it — but
// the writer must stop there: a later append running onto that tail would
// announce work over a file whose last record may not survive the crash this
// sync failure is warning about.
func TestDurableAppendStopsTheWriterWhenItCanNeitherSyncNorRollBack(t *testing.T) {
	fs := &syncFailsRollbackRefusedFS{Fs: afero.NewMemMapFs()}
	w, err := NewWriterWithFS(fs, "/retained-durable.jsonl", Header{SessionID: "sess-retained-durable"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fs.armed.Store(true)

	turn := schema.NewTurn(schema.TurnUserInput, llm.User("the line no sync made durable"))
	appendErr := w.AppendDurable(turn)
	if !errors.Is(appendErr, ErrEntryRetained) {
		t.Fatalf("AppendDurable error = %v, want it to carry ErrEntryRetained", appendErr)
	}
	data, readErr := afero.ReadFile(fs, "/retained-durable.jsonl")
	if readErr != nil {
		t.Fatalf("read transcript: %v", readErr)
	}
	if !bytes.Contains(data, []byte("the line no sync made durable")) {
		t.Fatalf("test setup: the line is not in the file, so nothing was retained: %q", data)
	}
	if !w.Poisoned() {
		t.Fatal("the writer still accepts appends over an unsynced tail it could not roll back")
	}
	next := schema.NewTurn(schema.TurnUserInput, llm.User("the record after it"))
	if err := w.AppendDurable(next); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("the next durable append = %v, want ErrWriterPoisoned: nothing further may be written or announced", err)
	}
	if err := w.Append(next); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("the next buffered append = %v, want ErrWriterPoisoned", err)
	}
}

// partialLineFS stops a write midway, leaving bytes at the tail that are the
// remains of a record rather than a record.
type partialLineFS struct {
	afero.Fs
	armed atomic.Bool
}

func (fs *partialLineFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &partialLineFile{File: file, fs: fs}, nil
}

func (fs *partialLineFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &partialLineFile{File: file, fs: fs}, nil
}

type partialLineFile struct {
	afero.File
	fs *partialLineFS
}

func (file *partialLineFile) Truncate(size int64) error {
	if file.fs.armed.Load() {
		return errors.New("injected truncate failure: the half line stays in the file")
	}
	return file.File.Truncate(size)
}

func (file *partialLineFile) Write(p []byte) (int, error) {
	if !file.fs.armed.Load() || len(p) < 4 {
		return file.File.Write(p)
	}
	written, err := file.File.Write(p[:len(p)/2])
	if err != nil {
		return written, err
	}
	return written, errors.New("injected write failure partway through the line")
}

// A whole line the writer could neither sync nor take back out stops it because
// nothing made that line durable. A barrier that fsyncs the whole file is
// exactly the thing it was missing: the record at the tail becomes
// authoritative, and the writer goes back to work.
func TestDurabilityBarrierRestartsAWriterStoppedByARetainedLine(t *testing.T) {
	fs := &syncFailsRollbackRefusedFS{Fs: afero.NewMemMapFs()}
	w, err := NewWriterWithFS(fs, "/barrier-clears.jsonl", Header{SessionID: "sess-barrier"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fs.armed.Store(true)
	retained := schema.NewTurn(schema.TurnUserInput, llm.User("the retained line"))
	if err := w.AppendDurable(retained); !errors.Is(err, ErrEntryRetained) {
		t.Fatalf("AppendDurable error = %v, want it to carry ErrEntryRetained", err)
	}
	if !w.Poisoned() {
		t.Fatal("test setup: the retained line did not stop the writer")
	}

	fs.armed.Store(false) // the barrier's own fsync succeeds
	if err := w.EstablishDurability(); err != nil {
		t.Fatalf("EstablishDurability: %v", err)
	}
	if w.Poisoned() {
		t.Fatal("a barrier that made the whole file durable left the writer stopped over a record that is now durable")
	}
	if err := w.AppendDurable(schema.NewTurn(schema.TurnUserInput, llm.User("the record after it"))); err != nil {
		t.Fatalf("append after the barrier: %v", err)
	}
}

// Half a line is not a record, and no fsync makes it one: a barrier must leave
// a writer stopped by a partial append exactly where it is, or the next append
// lands on bytes no reader can parse.
func TestDurabilityBarrierLeavesAWriterStoppedByAPartialLineStopped(t *testing.T) {
	fs := &partialLineFS{Fs: afero.NewMemMapFs()}
	w, err := NewWriterWithFS(fs, "/barrier-keeps.jsonl", Header{SessionID: "sess-barrier-partial"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fs.armed.Store(true)
	if err := w.AppendDurable(schema.NewTurn(schema.TurnUserInput, llm.User("the line that stopped partway"))); err == nil {
		t.Fatal("AppendDurable reported success; the injected partial write never reached it")
	} else if errors.Is(err, ErrEntryRetained) {
		t.Fatalf("AppendDurable error = %v, want no ErrEntryRetained: half a line is not a record", err)
	}
	if !w.Poisoned() {
		t.Fatal("test setup: the partial write did not stop the writer")
	}

	fs.armed.Store(false)
	if err := w.EstablishDurability(); err != nil {
		t.Fatalf("EstablishDurability: %v", err)
	}
	if !w.Poisoned() {
		t.Fatal("a barrier restarted a writer whose file ends in half a record")
	}
	if err := w.AppendDurable(schema.NewTurn(schema.TurnUserInput, llm.User("refused"))); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("append after the barrier = %v, want ErrWriterPoisoned", err)
	}
}
