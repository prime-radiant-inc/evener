package transcript

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// syncCountingFS counts the fsyncs its transcript file makes, so a door's own
// sync is observable without a timing budget: a writer held to a long
// SyncInterval syncs only when a door forces it.
type syncCountingFS struct {
	afero.Fs
	file *syncCountingFile
}

func (fs *syncCountingFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	fs.file = &syncCountingFile{File: file}
	return fs.file, nil
}

type syncCountingFile struct {
	afero.File
	syncs int
}

func (f *syncCountingFile) Sync() error {
	f.syncs++
	return f.File.Sync()
}

func scanMemEntries(t *testing.T, fs afero.Fs) (ScanResult, []Entry) {
	t.Helper()
	data, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	var entries []Entry
	result, err := Scan(context.Background(), bytes.NewReader(data), DefaultMaxLineBytes, func(_ []byte, entry Entry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("scan transcript: %v", err)
	}
	return result, entries
}

// A deferred append records the whole line, readable through the normal read
// path, without an fsync of its own: with the SyncInterval gate an hour away,
// the only sync a deferred append makes is none. Close settles the debt.
func TestDeferredDoorRecordsWithoutForcingASync(t *testing.T) {
	fs := &syncCountingFS{Fs: afero.NewMemMapFs()}
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	w.SyncInterval = time.Hour
	before := fs.file.syncs

	if _, err := w.Record(schema.NewTurn(schema.TurnAssistant, llm.Assistant("deferred")), RecordOptions{Door: DoorDeferred}); err != nil {
		t.Fatalf("deferred record: %v", err)
	}
	if got := fs.file.syncs - before; got != 0 {
		t.Fatalf("deferred append made %d syncs, want none: the interval gate covers it", got)
	}
	w.mu.Lock()
	dirty := w.dirty
	w.mu.Unlock()
	if !dirty {
		t.Fatal("deferred append left the writer clean; Close flushes only what it is told is dirty")
	}
	result, entries := scanMemEntries(t, fs)
	if result.TornTail || len(entries) != 1 || entries[0].Turn.Message.Text() != "deferred" {
		t.Fatalf("unsynced transcript = %+v with %d entries, want one readable deferred entry", result, len(entries))
	}

	before = fs.file.syncs
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := fs.file.syncs - before; got != 1 {
		t.Fatalf("Close made %d syncs, want the one that settles the deferred debt", got)
	}
}

// The deferred door keeps the durable door's recorded-or-error contract: a
// write it cannot record is rolled back, so a power crash can only ever cost a
// suffix and never a half-line. A whole line that landed and then failed is
// taken back out, which is what separates this door from the buffered one.
func TestDeferredDoorRollsBackAFailedWrite(t *testing.T) {
	fs := &partialWriteFS{Fs: afero.NewMemMapFs()}
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	w.SyncInterval = time.Hour
	// The whole line reaches the file and then the write reports failure; the
	// rollback truncate is left working, so the door can take the line back out.
	fs.file.transferBeforeFailure = math.MaxInt32
	fs.file.writeFailure = errors.New("injected transcript write failure")

	if _, err := w.Record(schema.NewTurn(schema.TurnAssistant, llm.Assistant("never recorded")), RecordOptions{Door: DoorDeferred}); err == nil {
		t.Fatal("deferred append reported success over a failed write")
	}
	w.mu.Lock()
	poisoned := w.poisoned
	w.mu.Unlock()
	if poisoned {
		t.Fatal("a clean rollback poisoned the writer")
	}
	result, entries := scanMemEntries(t, fs)
	if result.TornTail || len(entries) != 0 {
		t.Fatalf("rolled-back deferred write left %d entries (torn tail %t): the failed line must be gone", len(entries), result.TornTail)
	}
}

// Every truncation of a transcript a deferred door wrote yields a valid prefix:
// whole records only, never half a line, so a reader recovers at any point a
// power crash could have cut the file. The cut runs over every byte offset
// after the header, where a torn tail can begin.
func TestDeferredDoorTruncatedAtAnyPointYieldsAValidPrefix(t *testing.T) {
	fs := afero.NewMemMapFs()
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	w.SyncInterval = time.Hour
	const written = 4
	for i := range written {
		turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("deferred line"))
		if _, err := w.Record(turn, RecordOptions{Door: DoorDeferred}); err != nil {
			t.Fatalf("deferred record %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	full, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	headerEnd := bytes.IndexByte(full, '\n') + 1
	if headerEnd <= 0 {
		t.Fatal("transcript has no header line")
	}
	for cut := headerEnd; cut <= len(full); cut++ {
		var entries []Entry
		result, scanErr := Scan(context.Background(), bytes.NewReader(full[:cut]), DefaultMaxLineBytes, func(_ []byte, entry Entry) error {
			entries = append(entries, entry)
			return nil
		})
		if scanErr != nil {
			t.Fatalf("truncated at %d/%d bytes: %v", cut, len(full), scanErr)
		}
		for i, entry := range entries {
			if entry.Seq != i || entry.Turn.Kind != schema.TurnAssistant {
				t.Fatalf("truncated at %d/%d bytes: entry %d = (seq %d, %s), want the prefix in order", cut, len(full), i, entry.Seq, entry.Turn.Kind)
			}
		}
		if wantTorn := cut < len(full) && full[cut-1] != '\n'; result.TornTail != wantTorn {
			t.Fatalf("truncated at %d/%d bytes: TornTail = %t, want %t", cut, len(full), result.TornTail, wantTorn)
		}
	}
}
