package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"runtime"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
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
