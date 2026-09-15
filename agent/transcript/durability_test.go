package transcript

import (
	"bytes"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// syncFailsRollbackRefusedFS lets the line land, fails the sync that would make
// it durable, and refuses the truncate that would take it back out — the one
// shape where a durable append leaves a record it can neither make durable nor
// remove.
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

// partialLineFS stops a write midway, leaving bytes at the tail that are the
// remains of a record rather than a record, and refuses the truncate that would
// take them out.
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

func (file *partialLineFile) Truncate(size int64) error {
	if file.fs.armed.Load() {
		return errors.New("injected truncate failure: the half line stays in the file")
	}
	return file.File.Truncate(size)
}

// A durable append whose sync failed and whose rollback could not take the line
// back out leaves a readable record on a file nothing synced. The record IS a
// record — the append returns nil, and a returning reader finds it — but the
// writer stops there: nothing later may run onto an unsynced tail. The sync
// failure is not lost; it is available as a warning the session surfaces once.
func TestDurableRetainedRecordReturnsNilStopsTheWriterAndWarns(t *testing.T) {
	fs := &syncFailsRollbackRefusedFS{Fs: afero.NewMemMapFs()}
	w, err := NewWriterWithFS(fs, "/retained-durable.jsonl", Header{SessionID: "sess-retained-durable"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fs.armed.Store(true)

	turn := schema.NewTurn(schema.TurnUserInput, llm.User("the line no sync made durable"))
	if err := w.AppendDurable(turn); err != nil {
		t.Fatalf("AppendDurable error = %v, want nil: the whole line is a record", err)
	}
	warning, ok := w.TakeWarning()
	if !ok || warning == "" {
		t.Fatalf("TakeWarning() = %q, %v, want the retained sync failure", warning, ok)
	}
	if _, again := w.TakeWarning(); again {
		t.Fatal("TakeWarning() returned a second warning; it must be surfaced once")
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
		t.Fatalf("the next durable append = %v, want ErrWriterPoisoned", err)
	}
	if err := w.Append(next); !errors.Is(err, ErrWriterPoisoned) {
		t.Fatalf("the next buffered append = %v, want ErrWriterPoisoned", err)
	}
}

// A whole line the writer could neither sync nor take back out stops it because
// nothing made that line durable. A barrier that fsyncs the whole file is
// exactly what it was missing: the record becomes authoritative, and the writer
// goes back to work.
func TestDurabilityBarrierRestartsAWriterStoppedByARetainedLine(t *testing.T) {
	fs := &syncFailsRollbackRefusedFS{Fs: afero.NewMemMapFs()}
	w, err := NewWriterWithFS(fs, "/barrier-clears.jsonl", Header{SessionID: "sess-barrier"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fs.armed.Store(true)
	if err := w.AppendDurable(schema.NewTurn(schema.TurnUserInput, llm.User("the retained line"))); err != nil {
		t.Fatalf("AppendDurable error = %v, want nil", err)
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

// Half a line is not a record, and no fsync makes it one: a partial append
// reports its error (nothing was recorded), poisons permanently, and a barrier
// leaves it poisoned.
func TestPartialLinePoisonIsPermanent(t *testing.T) {
	fs := &partialLineFS{Fs: afero.NewMemMapFs()}
	w, err := NewWriterWithFS(fs, "/partial.jsonl", Header{SessionID: "sess-partial"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fs.armed.Store(true)
	if err := w.AppendDurable(schema.NewTurn(schema.TurnUserInput, llm.User("the line that stopped partway"))); err == nil {
		t.Fatal("AppendDurable reported success; half a line is not a record")
	}
	if _, ok := w.TakeWarning(); ok {
		t.Fatal("a partial append queued a retained warning; nothing was recorded")
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

// landThenFailFS writes the whole line and then reports failure: a buffered
// append that leaves a readable record behind.
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

func (file *landThenFailFile) Sync() error {
	if file.fs.armed.Load() {
		return errors.New("injected buffered sync failure with the line landed")
	}
	return file.File.Sync()
}

// The buffered door rolls nothing back, so a write whose whole line landed
// leaves a record every reader finds. It returns nil, like the durable door,
// and reports the sync failure as a warning. It does NOT poison: a buffered
// writer whose whole line is in the file can still take the next one.
func TestBufferedRetainedRecordReturnsNilAndWarns(t *testing.T) {
	fs := &landThenFailFS{Fs: afero.NewMemMapFs()}
	w, err := NewWriterWithFS(fs, "/retained-buffered.jsonl", Header{SessionID: "sess-retained"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fs.armed.Store(true)

	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("the line that landed"))); err != nil {
		t.Fatalf("Append error = %v, want nil: the whole line is a record", err)
	}
	if warning, ok := w.TakeWarning(); !ok || warning == "" {
		t.Fatalf("TakeWarning() = %q, %v, want the buffered sync failure", warning, ok)
	}
	data, readErr := afero.ReadFile(fs, "/retained-buffered.jsonl")
	if readErr != nil {
		t.Fatalf("read transcript: %v", readErr)
	}
	if !bytes.Contains(data, []byte("the line that landed")) {
		t.Fatalf("test setup: the line is not in the file: %q", data)
	}
	if w.Poisoned() {
		t.Fatal("a buffered sync failure poisoned the writer; its whole line is a record and it stays usable")
	}
}

// AppendBatch writes every turn in one write and one fsync, and reports the seq
// the first turn took. The seqs are contiguous, and a reader finds every line.
func TestAppendBatchWritesEveryTurnAtContiguousSeqs(t *testing.T) {
	fs := afero.NewMemMapFs()
	w, err := NewWriterWithFS(fs, "/batch.jsonl", Header{SessionID: "sess-batch"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("one")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("two")),
		schema.NewTurn(schema.TurnUserInput, llm.User("three")),
	}
	firstSeq, err := w.AppendBatch(turns)
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if firstSeq != 0 {
		t.Fatalf("firstSeq = %d, want 0", firstSeq)
	}
	entries := decodeAllEntries(t, fs, "/batch.jsonl")
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	for i, e := range entries {
		if e.Seq != firstSeq+i {
			t.Fatalf("entry %d seq = %d, want %d: batch seqs must be contiguous", i, e.Seq, firstSeq+i)
		}
	}
	// The seq counter advanced past the batch, so the next append follows it.
	next, err := w.AppendBatch([]schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("four"))})
	if err != nil {
		t.Fatalf("AppendBatch (second): %v", err)
	}
	if next != len(turns) {
		t.Fatalf("second batch firstSeq = %d, want %d", next, len(turns))
	}
}

// A batch is all-or-nothing: a write failure that rolls back leaves nothing in
// the file and spends no seq, so a retry lands the whole batch cleanly.
func TestAppendBatchRollsBackWholeBatchOnWriteFailure(t *testing.T) {
	fs := &midBatchWriteFailFS{Fs: afero.NewMemMapFs()}
	w, err := NewWriterWithFS(fs, "/batch-rollback.jsonl", Header{SessionID: "sess-batch-rollback"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	fs.armed.Store(true) // fail partway through the encoded batch

	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("alpha")),
		schema.NewTurn(schema.TurnUserInput, llm.User("bravo")),
	}
	if _, err := w.AppendBatch(turns); err == nil {
		t.Fatal("AppendBatch reported success; the injected write failure never reached it")
	}
	if entries := decodeAllEntries(t, fs, "/batch-rollback.jsonl"); len(entries) != 0 {
		t.Fatalf("entries after a rolled-back batch = %d, want 0: all-or-nothing", len(entries))
	}
	fs.armed.Store(false)
	if _, err := w.AppendBatch(turns); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	entries := decodeAllEntries(t, fs, "/batch-rollback.jsonl")
	if len(entries) != 2 || entries[0].Seq != 0 || entries[1].Seq != 1 {
		t.Fatalf("entries after the retry = %+v, want two at seq 0 and 1: the failed batch spent no seq", entries)
	}
}

// midBatchWriteFailFS writes half of an armed batch's buffer and then reports
// failure, leaving a partial run of records the rollback must take back out.
type midBatchWriteFailFS struct {
	afero.Fs
	armed atomic.Bool
}

func (fs *midBatchWriteFailFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &midBatchWriteFailFile{File: file, fs: fs}, nil
}

func (fs *midBatchWriteFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &midBatchWriteFailFile{File: file, fs: fs}, nil
}

type midBatchWriteFailFile struct {
	afero.File
	fs *midBatchWriteFailFS
}

func (file *midBatchWriteFailFile) Write(p []byte) (int, error) {
	if !file.fs.armed.Load() || len(p) < 4 {
		return file.File.Write(p)
	}
	written, err := file.File.Write(p[:len(p)/2])
	if err != nil {
		return written, err
	}
	return written, errors.New("injected mid-batch write failure")
}

func decodeAllEntries(t *testing.T, fs afero.Fs, path string) []Entry {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var entries []Entry
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if entry, err := DecodeEntry(line); err == nil {
			entries = append(entries, entry)
		}
	}
	return entries
}
