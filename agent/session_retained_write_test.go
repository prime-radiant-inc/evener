package agent

import (
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// retainDurableWriteFS lets the record's whole line land, fails the fsync that
// would make it durable, and refuses the truncate that would take it back out —
// the shape where a durable append leaves a record it can neither sync nor
// remove. The writer treats it as recorded (returns nil), stops itself, and
// hands the sync failure to the session as a warning.
type retainDurableWriteFS struct {
	afero.Fs
	armed atomic.Bool
}

func (fs *retainDurableWriteFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &retainDurableWriteFile{File: file, fs: fs}, nil
}

func (fs *retainDurableWriteFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &retainDurableWriteFile{File: file, fs: fs}, nil
}

type retainDurableWriteFile struct {
	afero.File
	fs *retainDurableWriteFS
}

func (file *retainDurableWriteFile) Sync() error {
	if file.fs.armed.Load() {
		return errors.New("injected sync failure after the record landed")
	}
	return file.File.Sync()
}

func (file *retainDurableWriteFile) Truncate(size int64) error {
	if file.fs.armed.Load() {
		return errors.New("injected truncate failure: the line stays in the file")
	}
	return file.File.Truncate(size)
}

func attachRetainingTranscript(t *testing.T, s *Session, match string) *retainDurableWriteFS {
	t.Helper()
	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatalf("close default transcript: %v", err)
	}
	fs := &retainDurableWriteFS{Fs: afero.NewOsFs()}
	writer, err := transcript.NewWriterWithFS(fs, transcriptPath(s.stateDir, s.id), transcript.Header{SessionID: s.id})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	writer.SyncInterval = 0 // every durable append syncs, so the matched record's own sync fails
	s.attachTranscript(writer)
	return fs
}

func countTurnsWithText(t *testing.T, s *Session, text string) int {
	t.Helper()
	n := 0
	for _, turn := range currentHistory(t, s) {
		if turn.Message.Text() == text {
			n++
		}
	}
	return n
}

func countTranscriptTurnsWithText(t *testing.T, s *Session, text string) int {
	t.Helper()
	data, err := readStrictChildTranscript(transcriptPath(s.stateDir, s.id), s.id, s.strictTranscriptMaxLineBytes)
	if err != nil {
		t.Fatalf("read transcript back: %v", err)
	}
	n := 0
	for _, entry := range data.Entries {
		if entry.Turn.Message.Text() == text {
			n++
		}
	}
	return n
}

// The writer owns durability: a durable append whose whole line landed but did
// not sync is recorded — it appears once in history and once in the transcript,
// exactly as a clean write would — and the writer surfaces the sync failure to
// the session as one warning, then refuses everything after it. The old
// behaviour returned the sync error, which left the same record in the file but
// absent from history and re-written by the retry.
func TestRetainedDurableWriteIsRecordedOnceAndWarnsOnce(t *testing.T) {
	t.Parallel()
	const body = "the turn a retained write kept"
	s := newSession(t,
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}),
		withoutGitSnapshot(),
	)
	fs := attachRetainingTranscript(t, s, body)
	seen, mu, done := collectEvents(s)

	fs.armed.Store(true)
	msg := llm.User(body)
	if err := s.appendTurnWithDurableTranscriptMessage(schema.TurnUserInput, msg, msg); err != nil {
		t.Fatalf("durable append error = %v, want nil: the transcript holds the record", err)
	}
	if !s.attachedTranscript().Poisoned() {
		t.Fatal("the retained write left the writer accepting appends over an unsynced tail")
	}
	if got := countTurnsWithText(t, s, body); got != 1 {
		t.Fatalf("the turn appears %d times in history, want once", got)
	}

	// The writer stopped, so the next durable write is refused rather than
	// silently dropped.
	next := llm.User("the turn after it")
	if err := s.appendTurnWithDurableTranscriptMessage(schema.TurnUserInput, next, next); !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("append after the retained write = %v, want ErrWriterPoisoned", err)
	}

	s.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	retainedWarnings := 0
	for _, ev := range *seen {
		if ev.Kind != events.EventWarning {
			continue
		}
		if warning, ok := ev.Data.(events.WarningData); ok && strings.Contains(warning.Message, "sync transcript entry") {
			retainedWarnings++
		}
	}
	if retainedWarnings != 1 {
		t.Fatalf("retained-write warnings = %d, want exactly one surfaced by the session", retainedWarnings)
	}
	if got := countTranscriptTurnsWithText(t, s, body); got != 1 {
		t.Fatalf("the turn appears %d times in the transcript, want once", got)
	}
}
