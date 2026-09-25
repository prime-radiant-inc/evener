package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// restoreExecutionSession restores s's session from its state directory into
// a new session, as a restarted daemon would.
func restoreExecutionSession(t *testing.T, stateDir, id string) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	client.Register(&executionAdapter{})
	restored, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
		StateDir:       stateDir,
		LLMRetryPolicy: &llm.RetryPolicy{MaxRetries: 2},
		LLMSleep:       func(context.Context, time.Duration) error { return nil },
		testOnly:       testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	return restored
}

func completionsOf(turns []schema.Turn, turnID string) []schema.TurnCompletionStatus {
	var out []schema.TurnCompletionStatus
	for _, turn := range turns {
		if turn.Kind == schema.TurnCompletion && turn.TurnID == turnID {
			out = append(out, turn.Completion.Status)
		}
	}
	return out
}

// A crash leaves an execution with entries and no completion. Resume records
// it interrupted, once: a second restore finds it complete.
func TestResumeClosesAnExecutionACrashLeftOpen(t *testing.T) {
	s, _ := newExecutionSession(t)
	if _, err := s.ProcessInput(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	stateDir, id, path := s.stateDir, s.ID(), s.TranscriptPath()
	s.Close()
	w, _, err := transcript.OpenWriterForSession(path, id)
	if err != nil {
		t.Fatal(err)
	}
	w.BeginExecution("t_crashed", false)
	if _, err := w.Record(schema.NewTurn(schema.TurnUserInput, llm.User("lost to a crash")), transcript.RecordOptions{Door: transcript.DoorDurable, Place: transcript.PlaceSession}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	restored := restoreExecutionSession(t, stateDir, id)
	turns := transcriptTurnsOf(t, restored)
	if got := completionsOf(turns, "t_crashed"); len(got) != 1 || got[0] != schema.TurnInterrupted {
		t.Fatalf("crashed turn completions = %v, want one interrupted", got)
	}
	restored.Close()
	again := restoreExecutionSession(t, stateDir, id)
	if got := completionsOf(transcriptTurnsOf(t, again), "t_crashed"); len(got) != 1 {
		t.Fatalf("a second restore wrote more completions: %v", got)
	}
	for _, turn := range transcriptTurnsOf(t, again) {
		if turn.Kind == schema.TurnCompletion && turn.TurnID != "t_crashed" && len(completionsOf(turns, turn.TurnID)) != 1 {
			t.Fatalf("turn %q gained a completion on restore", turn.TurnID)
		}
	}
}

// A turn recovery runs again under its old ID reopens it: the new span starts
// with a reopen marker and never restamps the turn's TurnKind.
func TestAnExecutionRunAgainUnderItsIDReopens(t *testing.T) {
	s, _ := newExecutionSession(t)
	s.mu.Lock()
	s.recordedExecutions = map[string]bool{"turn_m7": true}
	s.mu.Unlock()
	s.beginExecution("turn_m7")
	s.attentionMu.Lock()
	_, err := s.recordTranscriptLocked(schema.NewTurn(schema.TurnUserInput, llm.User("again")), transcript.DoorDurable, transcript.PlaceSession)
	s.attentionMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s.completeExecution(schema.TurnCompleted)
	turns := transcriptTurnsOf(t, s)
	span := turns[len(turns)-3:]
	if span[0].Kind != schema.TurnReopen || span[0].TurnID != "turn_m7" {
		t.Fatalf("span opens with %s in %q, want the reopen marker", span[0].Kind, span[0].TurnID)
	}
	for _, turn := range span {
		if turn.TurnID != "turn_m7" || turn.TurnKind != "" {
			t.Fatalf("reopened span entry %s = (%q, %q)", turn.Kind, turn.TurnID, turn.TurnKind)
		}
	}
	if span[2].Kind != schema.TurnCompletion {
		t.Fatalf("span ends with %s", span[2].Kind)
	}
}

// Resume leaves open the turns pending client work will run again, and
// closes every other open execution, in a stable order.
func TestResumeLeavesOpenTheTurnsPendingWorkWillRunAgain(t *testing.T) {
	executions := map[string]bool{"turn_m1": true, "t_b": true, "t_a": true, "turn_m2": false}
	closed := closeCrashedExecutionTargets(executions, map[string]bool{"turn_m1": true})
	if len(closed) != 2 || closed[0] != "t_a" || closed[1] != "t_b" {
		t.Fatalf("resume would close %v, want [t_a t_b]", closed)
	}
}
