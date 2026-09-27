package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
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

// A crash can land after a failed client start's entries are recorded and
// before its execution's completion is. Recovery at restore then has nothing
// to append, but the execution it owns is still open: it completes it, failed,
// rather than leaving it for a later restart to call interrupted.
func TestRecoveredFailedStartCompletesItsOpenExecution(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	started, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-crashed-before-completion",
		Input:            []appwire.InputItem{{Type: "text", Text: "fails, then the process dies"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	failure := errors.New("deterministic pre-append failure")
	crash := errors.New("simulated crash after the failure entry")
	sess.clientMutationPreAppendFailure = func(schema.Turn) error { return failure }
	sess.clientMutationFailureRecoveryFault = func(point string) error {
		if point == "after_failure" {
			return crash
		}
		return nil
	}
	if err := sess.acceptUserInput(withQueuedClientMutation(context.Background(), claimed), claimed.Text, claimed.Images, nil, false); !errors.Is(err, crash) {
		t.Fatalf("acceptUserInput = %v, want the simulated crash", err)
	}
	sess.Close()
	// The process died before the completion entry: cut it off the file.
	path := transcriptPath(dir, id)
	_, entries, _, err := readTranscript(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if last := entries[len(entries)-1].Turn; last.Kind != schema.TurnCompletion || last.TurnID != started.Turn.ID {
		t.Fatalf("setup: the transcript ends with %s in %q", last.Kind, last.TurnID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.LastIndexByte(bytes.TrimSuffix(data, []byte{'\n'}), '\n')
	if err := os.WriteFile(path, data[:cut+1], 0o600); err != nil {
		t.Fatal(err)
	}

	restored := restoreQueuePersistTestSessionWith(t, dir, id, RestoreSessionConfig{})
	defer restored.Close()
	if got := completionsOf(transcriptTurnsOf(t, restored), started.Turn.ID); len(got) != 1 || got[0] != schema.TurnFailed {
		t.Fatalf("completions of the recovered turn = %v, want one failed", got)
	}
}

// Restore leaves an open execution to the pending work that owns it. When
// recovery then retires that work without running it (an accepted Stop
// finalizes the turn interrupted), the execution is closed interrupted.
func TestRestoreClosesAnOpenExecutionItsPendingWorkAbandoned(t *testing.T) {
	s, _ := newExecutionSession(t)
	s.mu.Lock()
	s.openPendingExecutions = map[string]bool{"turn_m9": true}
	s.mu.Unlock()
	if !s.closeAbandonedExecutions() {
		t.Fatal("nothing was recorded")
	}
	if got := completionsOf(transcriptTurnsOf(t, s), "turn_m9"); len(got) != 1 || got[0] != schema.TurnInterrupted {
		t.Fatalf("completions = %v, want one interrupted", got)
	}
	if s.closeAbandonedExecutions() {
		t.Fatal("a second pass closed the turn again")
	}
}

// recoverClientMutationFailures must not lose the open-pending marker when
// the failure it tries to record for an already-recorded (not "own")
// execution fails to record: takeOpenPendingExecution consumes the marker
// before the record runs, and a record failure here leaves nothing else that
// will ever complete the turn -- closeAbandonedExecutions can only close what
// is still in openPendingExecutions.
func TestRecoverClientMutationFailuresKeepsTheMarkerWhenTheRecordFails(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	started, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-marker-loss",
		Input:            []appwire.InputItem{{Type: "text", Text: "fails, then the process dies"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	failure := errors.New("deterministic pre-append failure")
	crash := errors.New("simulated crash after both entries are recorded")
	sess.clientMutationPreAppendFailure = func(schema.Turn) error { return failure }
	sess.clientMutationFailureRecoveryFault = func(point string) error {
		if point == "after_failure" {
			return crash
		}
		return nil
	}
	if err := sess.acceptUserInput(withQueuedClientMutation(context.Background(), claimed), claimed.Text, claimed.Images, nil, false); !errors.Is(err, crash) {
		t.Fatalf("acceptUserInput = %v, want the simulated crash", err)
	}
	sess.clientMutationFailureRecoveryFault = nil

	// A restart's closeCrashedExecutions would find the turn open (both
	// entries recorded, no completion) and pending client work, and mark it
	// exactly this way.
	sess.mu.Lock()
	sess.openPendingExecutions = map[string]bool{started.Turn.ID: true}
	sess.mu.Unlock()

	// The next attempt to record the failure (recoverClientMutationFailures,
	// as restore runs it) fails at the store's own commit. Both items are
	// already recorded, so "own" is false, and wasOpen && err == nil is the
	// only outcome that ever completes the turn.
	sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error { return errors.New("store commit failure") }
	if err := sess.recoverClientMutationFailures(false); err == nil {
		t.Fatal("recovery reported success despite the store commit failing")
	}
	sess.clientMutations.faults.BeforeEffectSnapshotRename = nil

	sess.mu.Lock()
	stillOpen := sess.openPendingExecutions[started.Turn.ID]
	sess.mu.Unlock()
	if !stillOpen {
		t.Fatal("the open-pending marker was consumed although the failure was never recorded; closeAbandonedExecutions can never close this turn now")
	}
}
