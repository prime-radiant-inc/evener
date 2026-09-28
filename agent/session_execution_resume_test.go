package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
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
	turnsAfterSecondRestore := transcriptTurnsOf(t, again)
	if got := completionsOf(turnsAfterSecondRestore, "t_crashed"); len(got) != 1 {
		t.Fatalf("a second restore wrote more completions: %v", got)
	}
	for _, turn := range turnsAfterSecondRestore {
		if turn.Kind == schema.TurnCompletion && turn.TurnID != "t_crashed" && len(completionsOf(turnsAfterSecondRestore, turn.TurnID)) != 1 {
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
// before its execution's completion is (the own-branch record's own snapshot
// commit is what the crash cuts off; the record itself gates the completion
// now, so the process dies with no completion in the file, exactly as it
// really would). Recovery at restore then has nothing to append, but the
// execution it owns is still open: it completes it, failed, rather than
// leaving it for a later restart to call interrupted.
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
	if last := transcriptTurnsOf(t, sess); last[len(last)-1].Kind != schema.TurnFailure || last[len(last)-1].TurnID != started.Turn.ID {
		t.Fatalf("setup: the transcript ends with %s in %q, want its own uncompleted FAILURE entry", last[len(last)-1].Kind, last[len(last)-1].TurnID)
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

// recoverClientMutationFailures's wasOpen branch must not lose the
// open-pending marker when recordClientMutationFailure itself succeeds but
// the completion entry it then writes fails: the marker is the only thing
// that ever lets this turn close, and taking it before the completion
// actually lands leaves nothing to retry -- a later restart would then close
// the turn interrupted instead of failed.
func TestRecoverClientMutationFailuresKeepsTheMarkerWhenTheCompletionWriteFails(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	started, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-marker-loss-completion",
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

	// A real restart reopens the transcript through a brand-new process, so
	// its writer starts with no running execution recorded on it (nothing has
	// called BeginExecution on that fresh handle yet). This process's writer
	// still carries the crashed "own" pass's BeginExecution(turn_m1) on its
	// shared append tail (append_tail.go pins the tail by the file's real
	// identity across writer handles), so it has to be closed and reopened
	// -- releasing that tail -- to reach the same idle state a restart would.
	if err := sess.attachedTranscript().Close(); err != nil {
		t.Fatal(err)
	}
	// This time the client-mutation store commit succeeds -- both entries are
	// already recorded, so recordClientMutationFailure has nothing left to do
	// and returns nil, reaching the wasOpen && err == nil case -- but the
	// completion entry's own write fails.
	refuseEntries(t, sess, schema.TurnCompletion)
	if err := sess.recoverClientMutationFailures(false); err == nil {
		t.Fatal("recovery reported success despite the completion write failing")
	}

	sess.mu.Lock()
	stillOpen := sess.openPendingExecutions[started.Turn.ID]
	sess.mu.Unlock()
	if !stillOpen {
		t.Fatal("the open-pending marker was consumed although the completion was never recorded; closeAbandonedExecutions can never close this turn now")
	}
}

// recoverClientMutationFailures's wasOpen branch must chase durability for a
// RETAINED (recorded-but-unsynced) recovered completion the same way
// completeExecution does, not just adopt it and never retry: without that,
// a served session's recovered-turn completion could stay undurable forever
// with nothing ever failing it closed.
func TestRecoverClientMutationFailuresRetriesADurabilityDebtOnTheRecoveredCompletion(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	started, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-retained-completion",
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

	sess.mu.Lock()
	sess.openPendingExecutions = map[string]bool{started.Turn.ID: true}
	sess.mu.Unlock()

	// Fresh idle tail, as a real restart would have (see the comment on the
	// sibling test above).
	if err := sess.attachedTranscript().Close(); err != nil {
		t.Fatal(err)
	}
	clk := agenttest.NewFakeClock()
	sess.clock = clk
	served := serveFailClosedSession(sess)
	// The completion's own fsync and rollback fail (retained), and the
	// barrier never succeeds either: durability can never be established.
	attachRetainedBarrierWrite(t, sess, schema.TurnCompletion, durabilityRetryAttempts+1)
	baseline := clk.BlockedCount()
	if err := sess.recoverClientMutationFailures(false); err != nil {
		t.Fatalf("recoverClientMutationFailures = %v, want nil: a retained record is adopted, not an error", err)
	}
	driveDurabilityRetries(clk, baseline, durabilityRetryAttempts)
	sess.sendersWG.Wait() // the retry goroutine fails closed (or not) before this returns
	if refusal := sess.failedClosedRefusal(); refusal == nil {
		t.Fatal("the recovered turn's undurable completion never failed the served session closed")
	}
	served.settle(sess)
}

// A crash can land after a failed client start's USER_INPUT entry is recorded
// and before its TURN_FAILURE entry is: recordClientMutationFailure's error
// gates the own branch's completion, so the process dies with the file ending
// at the USER_INPUT entry, no completion, exactly as it really would.
// Recovery at restart then owns the execution itself (own == true:
// items.Failure is still false) and completes it failed -- but
// closeCrashedExecutions already left the crash-left-open marker behind for
// it (open, and pending client work at that point). The "own" branch must
// consume that marker too, or closeAbandonedExecutions, which runs right
// after, adds a second, contradictory completion to a turn recovery already
// completed failed.
func TestRecoveredFailedStartAfterUserEntryGetsExactlyOneCompletion(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	started, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-crashed-before-failure-entry",
		Input:            []appwire.InputItem{{Type: "text", Text: "fails before its failure entry, then the process dies"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	failure := errors.New("deterministic pre-append failure")
	crash := errors.New("simulated crash before the failure entry")
	sess.clientMutationPreAppendFailure = func(schema.Turn) error { return failure }
	sess.clientMutationFailureRecoveryFault = func(point string) error {
		if point == "before_failure" {
			return crash
		}
		return nil
	}
	if err := sess.acceptUserInput(withQueuedClientMutation(context.Background(), claimed), claimed.Text, claimed.Images, nil, false); !errors.Is(err, crash) {
		t.Fatalf("acceptUserInput = %v, want the simulated crash", err)
	}
	sess.Close()
	if last := transcriptTurnsOf(t, sess); last[len(last)-1].Kind != schema.TurnUserInput || last[len(last)-1].TurnID != started.Turn.ID {
		t.Fatalf("setup: the transcript ends with %s in %q, want its own uncompleted USER_INPUT entry", last[len(last)-1].Kind, last[len(last)-1].TurnID)
	}

	restored := restoreQueuePersistTestSessionWith(t, dir, id, RestoreSessionConfig{})
	defer restored.Close()
	if got := completionsOf(transcriptTurnsOf(t, restored), started.Turn.ID); len(got) != 1 || got[0] != schema.TurnFailed {
		t.Fatalf("completions of the recovered turn = %v, want exactly one failed", got)
	}
}

// The "own" branch of recoverClientMutationFailures must not complete the
// execution when recordClientMutationFailure itself fails before it records
// the FAILURE entry: a first recovery attempt that dies between the USER_INPUT
// entry and the FAILURE entry must leave the execution open, so a second
// recovery attempt -- the next restore, reading the same still-pending
// journal state -- is the only one that ever records a completion. Gating
// completeExecution on the recording actually having succeeded is what makes
// that true; completing unconditionally records a completion the first attempt
// never earned, and the second attempt then adds a second, contradictory one.
func TestRecoverClientMutationFailuresOwnBranchCompletesOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	clientMutationID := "start-fails-before-failure-entry"
	started, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: clientMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "recovery itself dies before its own FAILURE entry"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := sess.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := sess.beginClientMutationFailure(clientMutationID, errors.New("deterministic failure")); err != nil {
		t.Fatal(err)
	}

	recordFailure := errors.New("recovery dies before its own FAILURE entry")
	sess.clientMutationFailureRecoveryFault = func(point string) error {
		if point == "before_failure" {
			return recordFailure
		}
		return nil
	}
	if err := sess.recoverClientMutationFailures(false); !errors.Is(err, recordFailure) {
		t.Fatalf("first recovery attempt = %v, want the injected record failure", err)
	}
	if got := completionsOf(transcriptTurnsOf(t, sess), started.Turn.ID); len(got) != 0 {
		t.Fatalf("completions after the failed recovery attempt = %v, want none: nothing yet earned a completion", got)
	}

	// The process dies right there: close as a crash would leave it, with
	// nothing more written. The next restore reads the same on-disk state --
	// still pending, still missing its FAILURE entry -- through the real
	// resume path, which runs recoverClientMutationFailures itself with no
	// fault armed, so this time recording succeeds.
	sess.Close()
	restored := restoreQueuePersistTestSessionWith(t, dir, id, RestoreSessionConfig{})
	defer restored.Close()
	if got := completionsOf(transcriptTurnsOf(t, restored), started.Turn.ID); len(got) != 1 || got[0] != schema.TurnFailed {
		t.Fatalf("completions after the restore = %v, want exactly one failed", got)
	}
}
