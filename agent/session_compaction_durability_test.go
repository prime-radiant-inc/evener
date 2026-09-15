package agent

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type compactionAppendFailFile struct {
	afero.File
	fail      *atomic.Bool
	attempted *atomic.Bool
}

// Only the layer's own CONTEXT_COMPACTION record fails: its marker has to land,
// or the fold withholds the record with the anchor and never attempts the write
// this test is about.
func (f *compactionAppendFailFile) Write(p []byte) (int, error) {
	if f.fail.Load() {
		if entry, err := transcript.DecodeEntry(bytes.TrimSpace(p)); err == nil && entry.Turn.Kind == schema.TurnContextCompaction {
			f.attempted.Store(true)
			return 0, errors.New("injected compaction transcript write failure")
		}
	}
	return f.File.Write(p)
}

type compactionAppendFailFS struct {
	afero.Fs
	fail      atomic.Bool
	attempted atomic.Bool
}

func (fs *compactionAppendFailFS) Create(name string) (afero.File, error) {
	f, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &compactionAppendFailFile{File: f, fail: &fs.fail, attempted: &fs.attempted}, nil
}

func TestCompactSuppressesContextEventWhenTranscriptAppendFails(t *testing.T) {
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}})
	s := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-test")), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12)

	fs := &compactionAppendFailFS{Fs: afero.NewMemMapFs()}
	w, err := transcript.NewWriterWithFS(fs, "/compact.jsonl", transcript.Header{SessionID: "compact"})
	if err != nil {
		t.Fatal(err)
	}
	fs.fail.Store(true)
	s.attachTranscript(w)
	eventsSeen, eventsMu, done := collectEvents(s)

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	s.Close()
	<-done
	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, ev := range *eventsSeen {
		if ev.Kind == events.EventContextCompaction {
			t.Fatal("context compaction event emitted after its transcript append failed")
		}
	}
	foundWarning := false
	for _, ev := range *eventsSeen {
		if ev.Kind == events.EventWarning {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatal("transcript append failure emitted no warning")
	}
	if !fs.attempted.Load() {
		t.Fatal("filesystem failure did not receive a decodable CONTEXT_COMPACTION transcript record")
	}
}

// The steering a fold injects is one of the fold's records in both places it
// exists: the entry it writes and the turn it leaves in live history. A tag on
// only one of them leaves a live reader and a returning one disagreeing about
// which fold produced the same turn.
func TestFoldPublication_InjectedSteeringCarriesTheFoldIDInHistoryToo(t *testing.T) {
	t.Parallel()
	s := newScriptedSummaryCompactSession(t, "fold-id-history-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}))
	seedNumberedSessionHistory(t, s, 12)
	s.setPinnedNote("REMEMBER: the API signature")

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	durable := map[string]string{}
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnSteering {
			durable[entry.Turn.Message.Text()] = entry.Turn.CompactionFoldID
		}
	}
	if len(durable) == 0 {
		t.Fatal("test setup: the fold injected no steering to compare")
	}
	seen := 0
	for _, turn := range currentHistory(t, s) {
		if turn.Kind != schema.TurnSteering {
			continue
		}
		fold, ok := durable[turn.Message.Text()]
		if !ok {
			continue
		}
		seen++
		if turn.CompactionFoldID != fold {
			t.Fatalf("live history has the fold's steering under fold %q, the transcript under %q", turn.CompactionFoldID, fold)
		}
	}
	if seen == 0 {
		t.Fatal("test setup: the fold's steering is in the transcript but not in live history")
	}
}
