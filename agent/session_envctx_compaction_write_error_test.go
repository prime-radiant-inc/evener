package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type failAfterCreateFile struct {
	afero.File
	writes int
}

func (f *failAfterCreateFile) Write(data []byte) (int, error) {
	if f.writes > 0 {
		return 0, errors.New("fixture-compaction-append-failure")
	}
	f.writes++
	return f.File.Write(data)
}

type failAfterCreateFS struct{ afero.Fs }

func (fs failAfterCreateFS) Create(name string) (afero.File, error) {
	f, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &failAfterCreateFile{File: f}, nil
}

func TestFallbackCompactionWriteFailureStillResetsEnvironmentTracker(t *testing.T) {
	s := newScriptedSummaryCompactSession(t, "env-compaction-write-error", func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("unused")} }, withDir(t.TempDir()))
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriterWithFS(failAfterCreateFS{Fs: afero.NewMemMapFs()}, "/session.jsonl", transcript.Header{SessionID: s.id})
	if err != nil {
		t.Fatal(err)
	}
	s.attentionMu.Lock()
	s.mu.Lock()
	old := s.transcript
	s.transcript = writer
	s.transcriptReady = true
	s.mu.Unlock()
	s.attentionMu.Unlock()
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	eventsSeen, eventsMu, done := collectEvents(s)
	s.handleCompactionTurn(schema.NewTurn(schema.TurnSummary, llm.Assistant("summary")))
	if s.envTracker.State().HasSent {
		t.Fatal("environment tracker retained a snapshot after compaction replaced history")
	}
	s.Close()
	<-done
	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, event := range *eventsSeen {
		warning, ok := event.Data.(events.WarningData)
		if event.Kind == events.EventWarning && ok && strings.Contains(warning.Message, "fixture-compaction-append-failure") {
			return
		}
	}
	t.Fatal("compaction transcript write failure was not reported")
}
