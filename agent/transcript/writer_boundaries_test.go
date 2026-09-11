package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/fuzz/fault"
	"primeradiant.com/evener/llm"
)

type zeroProgressFS struct {
	afero.Fs
	file *zeroProgressFile
}

func (fs *zeroProgressFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	fs.file = &zeroProgressFile{File: file}
	return fs.file, nil
}

type zeroProgressFile struct {
	afero.File
	zeroNextWrite bool
}

func (f *zeroProgressFile) Write(p []byte) (int, error) {
	if f.zeroNextWrite {
		f.zeroNextWrite = false
		return 0, nil
	}
	return f.File.Write(p)
}

func TestWriterZeroProgressReturnsErrShortWrite(t *testing.T) {
	fs := &zeroProgressFS{Fs: afero.NewMemMapFs()}
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	fs.file.zeroNextWrite = true

	err = w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("not persisted")))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Append error = %v, want io.ErrShortWrite", err)
	}
	if w.seq != 0 {
		t.Fatalf("next sequence = %d, want 0 after failed append", w.seq)
	}

	data, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("persisted lines = %d, want only the v2 header", len(lines))
	}
	var header Header
	if err := json.Unmarshal(lines[0], &header); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHeader(header); err != nil {
		t.Fatalf("persisted header: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterAppendRechecksClosedStateAfterLock(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })

	fs := afero.NewMemMapFs()
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	before, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatal(err)
	}

	w.mu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		started <- struct{}{}
		done <- w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("closed")))
	}()
	<-started
	runtime.Gosched()
	w.closed.Store(true)
	w.mu.Unlock()

	if err := <-done; err != nil {
		t.Fatalf("Append after closed-state transition: %v", err)
	}
	after, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("post-lock closed-state append changed transcript:\nbefore=%q\nafter=%q", before, after)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// partialWriteFS fails one Write after letting it transfer a chosen number of
// bytes, and can fail the rollback truncate that answers it. The two together
// are the shape no rollback can undo: bytes at the tail that are not a record.
type partialWriteFS struct {
	afero.Fs
	file *partialWriteFile
}

func (fs *partialWriteFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	fs.file = &partialWriteFile{File: file}
	return fs.file, nil
}

type partialWriteFile struct {
	afero.File
	transferBeforeFailure int
	writeFailure          error
	truncateFailure       error
}

func (f *partialWriteFile) Write(p []byte) (int, error) {
	if f.writeFailure == nil {
		return f.File.Write(p)
	}
	failure := f.writeFailure
	f.writeFailure = nil
	transfer := min(f.transferBeforeFailure, len(p))
	n, err := f.File.Write(p[:transfer])
	if err != nil {
		return n, err
	}
	return n, failure
}

func (f *partialWriteFile) Truncate(size int64) error {
	if f.truncateFailure == nil {
		return f.File.Truncate(size)
	}
	failure := f.truncateFailure
	f.truncateFailure = nil
	return failure
}

// armPartialWriteFailure returns a writer whose next entry write transfers
// transferBytes and then fails, with the rollback truncate failing behind it.
func armPartialWriteFailure(t *testing.T, transferBytes int) (*Writer, *partialWriteFS) {
	t.Helper()
	fs := &partialWriteFS{Fs: afero.NewMemMapFs()}
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	w.TrackFailures(nil, 0)
	fs.file.transferBeforeFailure = transferBytes
	fs.file.writeFailure = errors.New("injected transcript write failure")
	fs.file.truncateFailure = errors.New("injected transcript rollback failure")
	return w, fs
}

// A write that stopped mid-line and could not be rolled back leaves bytes that
// are not a record. Appending after them would weld the next entry onto the
// remains of this one, so the writer has to refuse rather than hand a reader a
// file it will reject whole.
func TestAppendDurable_PartialLineRollbackFailurePoisonsWriter(t *testing.T) {
	w, fs := armPartialWriteFailure(t, 12)

	err := w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("interrupted")))
	if !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("append error = %v, want a rollback failure over the partial line", err)
	}
	if w.seq != 0 {
		t.Fatalf("next sequence = %d, want 0: a partial line is no entry", w.seq)
	}

	before, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("would weld onto the partial line"))); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("append after an unresolved partial write = %v, want ErrWriterPoisoned", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("the buffered door too"))); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("buffered append after an unresolved partial write = %v, want ErrWriterPoisoned", err)
	}
	after, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back after refusals: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("refused appends changed the file: %q then %q", before, after)
	}
}

// A write that transferred the whole line before failing leaves a record a
// reader will see. Its sequence number is spent and the failure it settles is
// counted, exactly as the retained-entry path does — and the writer still stops,
// because a write that reported failure says nothing dependable about the tail.
func TestAppendDurable_WholeLineWriteFailureSpendsSequence(t *testing.T) {
	w, fs := armPartialWriteFailure(t, math.MaxInt32)

	retained := toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})
	if err := w.AppendDurable(retained); !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("append error = %v, want a rollback failure over the retained line", err)
	}
	if w.seq != 1 {
		t.Fatalf("next sequence = %d, want 1: a whole line a reader sees spends its sequence", w.seq)
	}
	if count, ok := w.FailedToolCalls(); !ok || count != 1 {
		t.Fatalf("failure count = %d (counted=%v), want the 1 a reader of the transcript counts", count, ok)
	}
	if err := w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("after"))); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("append after an unresolved write = %v, want ErrWriterPoisoned", err)
	}

	data, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want the header and the retained entry", len(lines))
	}
	entry, err := DecodeEntry(lines[1])
	if err != nil {
		t.Fatalf("decode retained entry: %v", err)
	}
	if entry.Seq != 0 {
		t.Fatalf("retained entry seq = %d, want the 0 the next append must not reuse", entry.Seq)
	}
}

// The buffered door attempts no rollback, so whatever a failed write left at
// the tail simply stays. That is harmless only while nothing follows it: the
// next append would run its record onto the remains of this one and make the
// file unreadable whole, so the writer has to stop here too.
func TestAppend_PartialLineFailurePoisonsWriter(t *testing.T) {
	w, fs := armPartialWriteFailure(t, 12)

	err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("interrupted")))
	if err == nil || errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("buffered append error = %v, want the write failure itself", err)
	}
	if w.seq != 0 {
		t.Fatalf("next sequence = %d, want 0: a partial line is no entry", w.seq)
	}

	before, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("would weld onto the partial line"))); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("buffered append after an unresolved partial write = %v, want ErrWriterPoisoned", err)
	}
	if err := w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("the durable door too"))); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("durable append after an unresolved partial write = %v, want ErrWriterPoisoned", err)
	}
	after, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back after refusals: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("refused appends changed the file: %q then %q", before, after)
	}
}

// A buffered write that transferred the whole line before failing left a record
// a reader will see, so it spends its sequence number and counts the failures it
// settles — the same accounting the durable door does for a retained line.
func TestAppend_WholeLineFailureSpendsSequence(t *testing.T) {
	w, _ := armPartialWriteFailure(t, math.MaxInt32)

	retained := toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})
	if err := w.Append(retained); err == nil {
		t.Fatal("buffered append reported success over an injected write failure")
	}
	if w.seq != 1 {
		t.Fatalf("next sequence = %d, want 1: a whole line a reader sees spends its sequence", w.seq)
	}
	if count, ok := w.FailedToolCalls(); !ok || count != 1 {
		t.Fatalf("failure count = %d (counted=%v), want the 1 a reader of the transcript counts", count, ok)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("after"))); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("append after an unresolved write = %v, want ErrWriterPoisoned", err)
	}
}

// A write that transferred nothing is the shape every pre-existing
// write-failure fixture produces, and it leaves the file and the writer's
// position exactly as they were. There is nothing at the tail to guard, so the
// writer stays usable and a retry still lands.
func TestAppend_NoBytesWrittenLeavesWriterUsable(t *testing.T) {
	w, fs := armPartialWriteFailure(t, 0)

	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("never left the caller"))); err == nil {
		t.Fatal("buffered append reported success over an injected write failure")
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("retry"))); err != nil {
		t.Fatalf("retry after a write that transferred nothing: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want the header and the landed retry", len(lines))
	}
	entry, err := DecodeEntry(lines[1])
	if err != nil {
		t.Fatalf("decode retry entry: %v", err)
	}
	if entry.Seq != 0 {
		t.Fatalf("retry entry seq = %d, want the 0 the failed append never spent", entry.Seq)
	}
}

// faultedOsWriter builds a writer over a real filesystem under a fault plan, so
// a write past the file's end behaves the way a filesystem really behaves
// rather than the way a memory map chooses to.
func faultedOsWriter(t *testing.T, plan []byte) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session", "transcript.jsonl")
	w, err := newWriterFS(fault.FS(afero.NewOsFs(), fault.FromBytes(plan)), path, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	return w, path
}

// rollbackLostPositionPlan faults the entry sync (6) and the seek that closes
// the rollback (8), letting the truncate between them succeed: the entry is
// removed from the file while the writer's own position stays where the entry
// ended, past the file's new end.
func rollbackLostPositionPlan(extraFaults ...int) []byte {
	plan := bytes.Repeat([]byte{0x01}, 128)
	plan[6] = 0x00
	plan[8] = 0x00
	for _, op := range extraFaults {
		plan[op] = 0x00
	}
	return plan
}

// assertSingleLandedEntry requires the file to parse as the header plus exactly
// one entry carrying text, with no zero-filled gap anywhere in it.
func assertSingleLandedEntry(t *testing.T, path, text string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		t.Fatalf("transcript holds a zero-filled gap at byte %d: a write landed past the file's end\n%q", i, data)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want the header and one entry\n%q", len(lines), data)
	}
	entry, err := DecodeEntry(lines[1])
	if err != nil {
		t.Fatalf("decode entry: %v\n%q", err, lines[1])
	}
	if entry.Turn.Message.Text() != text {
		t.Fatalf("entry text = %q, want %q", entry.Turn.Message.Text(), text)
	}
	if entry.Seq != 0 {
		t.Fatalf("entry seq = %d, want the 0 the rolled-back entry never spent", entry.Seq)
	}
}

// A rollback whose truncate removed the entry but whose seek could not restore
// the file position leaves the writer pointing past the file's end. The durable
// door seeks for itself before every append, but the buffered door does not, so
// without repositioning its record lands past the end behind a gap the
// filesystem zero-fills — a line no reader can decode, in a file every reader
// then rejects whole. The writer is right to stay usable here (the file is
// consistent and the re-emit path needs it), so it has to re-establish the end
// instead.
func TestAppend_RepositionsAfterRollbackLostTheFileEnd(t *testing.T) {
	w, path := faultedOsWriter(t, rollbackLostPositionPlan())

	if err := w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("rolled back"))); !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("durable append error = %v, want the rollback failure that lost the position", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("lands at the end"))); err != nil {
		t.Fatalf("buffered append after the rollback: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertSingleLandedEntry(t, path, "lands at the end")
}

// Re-establishing the end can itself fail. That append must write nothing and
// say so, and the writer must still know its end is unestablished so the next
// attempt tries again rather than writing into the gap.
func TestAppend_RetriesAfterAFailedReposition(t *testing.T) {
	w, path := faultedOsWriter(t, rollbackLostPositionPlan(9))

	if err := w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("rolled back"))); !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("durable append error = %v, want the rollback failure that lost the position", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("never left the caller"))); err == nil {
		t.Fatal("buffered append reported success over a failed reposition")
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("lands at the end"))); err != nil {
		t.Fatalf("buffered append after the reposition recovered: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertSingleLandedEntry(t, path, "lands at the end")
}
