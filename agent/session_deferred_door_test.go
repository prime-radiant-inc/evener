package agent

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// syncCountingFS wraps a session's real transcript filesystem and counts the
// fsyncs made through it, recording which entry kinds the last write carried.
// A sync that settles the per-round assistant turn can then be told apart from
// one that settles a turn completion by what it covered, with no clock.
type syncCountingFS struct {
	afero.Fs
	mu     sync.Mutex
	last   schema.TurnKind
	syncs  int
	covers []schema.TurnKind
}

func (fs *syncCountingFS) OpenFile(name string, flags int, mode os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flags, mode)
	if err != nil {
		return nil, err
	}
	return &syncCountingFile{File: f, fs: fs}, nil
}

type syncCountingFile struct {
	afero.File
	fs *syncCountingFS
}

func (f *syncCountingFile) Write(p []byte) (int, error) {
	var entry transcript.Entry
	if err := json.Unmarshal(bytesTrimNewline(p), &entry); err == nil && entry.Turn.Kind != "" {
		f.fs.mu.Lock()
		f.fs.last = entry.Turn.Kind
		f.fs.mu.Unlock()
	}
	return f.File.Write(p)
}

func (f *syncCountingFile) Sync() error {
	f.fs.mu.Lock()
	f.fs.syncs++
	f.fs.covers = append(f.fs.covers, f.fs.last)
	f.fs.mu.Unlock()
	return f.File.Sync()
}

// counts returns the number of fsyncs and, for each, the kind of the last
// entry written before it.
func (fs *syncCountingFS) counts() (int, []schema.TurnKind) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.syncs, append([]schema.TurnKind(nil), fs.covers...)
}

func bytesTrimNewline(p []byte) []byte {
	for len(p) > 0 && (p[len(p)-1] == '\n' || p[len(p)-1] == '\r') {
		p = p[:len(p)-1]
	}
	return p
}

// A round's assistant turn is recorded without forcing an fsync of its own.
// The append lands and is readable through the normal read path while the
// writer is still dirty; the sync is left to the interval gate.
func TestAnAssistantTurnIsRecordedWithoutForcingASync(t *testing.T) {
	s, _ := newExecutionSession(t)
	fs := attachSyncCountingTranscript(t, s, time.Hour)
	if err := s.appendAssistantTurn(llm.Response{Message: llm.Assistant("a round's answer")}, ModelAttemptMetadata{}); err != nil {
		t.Fatalf("appendAssistantTurn: %v", err)
	}
	if syncs, covers := fs.counts(); syncs != 0 {
		t.Fatalf("the assistant turn forced %d fsyncs, want none: covered kinds %v", syncs, covers)
	}
	// Readable now, through the same reader production uses, before any sync.
	entries := transcriptEntries(t, s)
	found := false
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnAssistant {
			found = true
		}
	}
	if !found {
		t.Fatalf("the recorded assistant turn is not readable before its sync; entries = %d", len(entries))
	}
}

// A turn completion still syncs through its real call site. A deferred
// assistant turn ahead of it leaves the writer dirty, and the completion's
// synced door settles that debt: exactly one sync follows, and it covers the
// completion, so the terminal status never waits on an interval.
func TestATurnCompletionSyncsOnceAfterADeferredAssistantTurn(t *testing.T) {
	s, _ := newExecutionSession(t)
	fs := attachSyncCountingTranscript(t, s, time.Hour)
	if err := s.appendAssistantTurn(llm.Response{Message: llm.Assistant("a round's answer")}, ModelAttemptMetadata{}); err != nil {
		t.Fatalf("appendAssistantTurn: %v", err)
	}
	// completeTurn is the completion write a turn that no running process owns
	// takes; completeExecution's own write is the same door. Driving the door
	// helper here would pass even if that call site changed doors.
	if _, err := s.completeTurn("turn-1", schema.TurnCompleted); err != nil {
		t.Fatalf("completeTurn: %v", err)
	}
	syncs, covers := fs.counts()
	if syncs != 1 {
		t.Fatalf("made %d fsyncs, want exactly the completion's: covered kinds %v", syncs, covers)
	}
	if covers[0] != schema.TurnCompletion {
		t.Fatalf("the sync covered %q, want the completion", covers[0])
	}
}

// attachSyncCountingTranscript reopens the session's transcript on an FS that
// counts fsyncs and arms the writer's interval an hour out, so any sync a test
// observes came from a door rather than the interval gate.
func attachSyncCountingTranscript(t *testing.T, s *Session, interval time.Duration) *syncCountingFS {
	t.Helper()
	fs := &syncCountingFS{Fs: afero.NewOsFs()}
	w, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatalf("OpenWriterForSessionWithFS: %v", err)
	}
	w.SyncInterval = interval
	s.mu.Lock()
	s.setTranscriptLocked(w)
	s.mu.Unlock()
	return fs
}
