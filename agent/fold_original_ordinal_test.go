package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A fold re-appends, after its markers, the entries recorded while it ran.
// Each copy records the entry ordinal of the line it copies; the original
// carries none.
func TestFoldCopiesRecordTheOrdinalOfTheirOriginal(t *testing.T) {
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "fold-ordinal-cheap", func(llm.Request) llm.Response {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}))
	seedNumberedSessionHistory(t, s, 12)

	compactErr := make(chan error, 1)
	go func() { compactErr <- s.Compact(context.Background()) }()
	<-entered
	const text = "recorded while the fold ran"
	turn := schema.NewTurn(schema.TurnUserInput, llm.User(text))
	s.recordTurn(turn, turn)
	// A failure recorded before the fold publishes is copied too; the copy
	// must not fail an execution that runs when the copy is written.
	failure := schema.NewTurn(schema.TurnFailure, llm.System("an earlier failure"))
	failure.Error = &schema.TurnFailureInfo{Message: "an earlier failure"}
	s.recordTurn(failure, failure)
	// The model changes before the copies are written: each copy keeps the
	// model its original was recorded with.
	if err := s.SetModel("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	s.beginExecution("")
	close(proceed)
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatal(err)
	}
	original, copied := -1, -1
	for i, entry := range data.Entries {
		if entry.Turn.Message.Text() != text {
			continue
		}
		if entry.Turn.OriginalOrdinal == nil {
			original = i
			continue
		}
		copied = i
		if got := *entry.Turn.OriginalOrdinal; got != uint64(original) {
			t.Fatalf("copy at ordinal %d records original %d, want %d", i, got, original)
		}
	}
	if original < 0 || copied <= original {
		t.Fatalf("original at %d, copy at %d: want a copy after the original", original, copied)
	}
	if got, want := data.Entries[copied].Turn.Model, data.Entries[original].Turn.Model; got != want {
		t.Fatalf("copy model = %q, want its original's %q", got, want)
	}
	s.mu.Lock()
	failed := s.execution.failed
	s.mu.Unlock()
	if failed {
		t.Fatal("a copied TURN_FAILURE failed the execution running when the fold wrote it")
	}
}
