package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func newDurableAdmissionAskSessionWithAdapter(t *testing.T, cfg SessionConfig) (*Session, *fakeAdapter) {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(askUserCall("ask1", askUserArgsValid())) },
		},
	}
	c.Register(adapter)
	cfg.StateDir = dir
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess, adapter
}

func newDurableAdmissionAskSession(t *testing.T, cfg SessionConfig) *Session {
	sess, _ := newDurableAdmissionAskSessionWithAdapter(t, cfg)
	return sess
}

func seedDurableAdmissionAsk(t *testing.T, sess *Session) context.Context {
	t.Helper()
	// TRIPWIRE: scripted in-process provider and local temporary transcript
	// files, with no network; 30s is a generous guard for a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("initial ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("initial ask pending count = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("initial state = %q, want %q", got, SessionAwaiting)
	}
	return ctx
}

func assertDurableAdmissionAskRestoresAwaiting(t *testing.T, sess *Session) {
	t.Helper()
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live ask pending count = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state = %q, want %q", got, SessionAwaiting)
	}
	meta := sess.Meta()
	dir := sess.stateDir
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	t.Cleanup(restored.Close)
	if got := restored.askPendingCount(); got != 1 {
		t.Fatalf("restored ask pending count = %d, want 1", got)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q", got, SessionAwaiting)
	}
}

func TestAskUser_DurableAdmissionMaxTurnsRefusalMatchesRestore(t *testing.T) {
	t.Parallel()
	sess := newDurableAdmissionAskSession(t, SessionConfig{MaxTurns: 1})
	ctx := seedDurableAdmissionAsk(t, sess)
	_, err := sess.ProcessInput(ctx, "answer the question", nil)
	var exhausted *budgetExhaustionError
	if !errors.As(err, &exhausted) || exhausted.Budget != exhaustedBudgetTurns {
		t.Fatalf("refused resolving input error = %v, want MaxTurns exhaustion", err)
	}
	assertDurableAdmissionAskRestoresAwaiting(t, sess)
}

func TestAskUser_DurableAdmissionEnvironmentPersistFailureMatchesRestore(t *testing.T) {
	t.Parallel()
	var branch atomic.Value
	branch.Store("before")
	sess := newDurableAdmissionAskSession(t, SessionConfig{
		testOnly: testConfig{envProbes: &envctx.Probes{
			Now:       func() time.Time { return time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC) },
			GitBranch: func(string) string { return branch.Load().(string) },
		}},
	})
	ctx := seedDurableAdmissionAsk(t, sess)
	branch.Store("after")
	failure := errors.New("environment transcript durability failure")
	attachEnvironmentSyncFailure(t, sess, failure, nil)
	if _, err := sess.ProcessInput(ctx, "answer the question", nil); !errors.Is(err, failure) {
		t.Fatalf("environment-refused resolving input error = %v, want %v", err, failure)
	}
	assertDurableAdmissionAskRestoresAwaiting(t, sess)
}

func TestAskUser_DurableAdmissionTranscriptAppendFailureMatchesRestore(t *testing.T) {
	t.Parallel()
	sess := newDurableAdmissionAskSession(t, SessionConfig{})
	ctx := seedDurableAdmissionAsk(t, sess)
	failure := errors.New("user transcript append zero-byte failure")
	fs := attachEnvironmentFailureFS(t, sess)
	fs.mu.Lock()
	fs.writeFailure = failure
	fs.transferBeforeWriteFailure = 0
	fs.mu.Unlock()
	if _, err := sess.ProcessInput(ctx, "answer the question", nil); !errors.Is(err, failure) {
		t.Fatalf("transcript-refused resolving input error = %v, want %v", err, failure)
	}
	assertDurableAdmissionAskRestoresAwaiting(t, sess)
}

func TestAskUser_DurableAdmissionClosedTranscriptMatchesRestore(t *testing.T) {
	t.Parallel()
	sess, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
	ctx := seedDurableAdmissionAsk(t, sess)
	if err := sess.closeAttachedTranscript(); err != nil {
		t.Fatalf("close attached transcript: %v", err)
	}
	if _, err := sess.ProcessInput(ctx, "answer the question", nil); !errors.Is(err, transcript.ErrWriterClosed) {
		t.Fatalf("closed-transcript admission error = %v, want ErrWriterClosed", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live ask pending count after closed transcript = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after closed transcript = %q, want %q", got, SessionAwaiting)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests after closed transcript = %d, want initial ask only", got)
	}

	meta := sess.Meta()
	dir := sess.stateDir
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 1 {
		t.Fatalf("restored ask pending count after closed transcript = %d, want 1", got)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state after closed transcript = %q, want %q", got, SessionAwaiting)
	}
}

func TestAskUser_DurableAdmissionRetainedRecordIsAdopted(t *testing.T) {
	t.Parallel()
	sess := newDurableAdmissionAskSession(t, SessionConfig{})
	seedDurableAdmissionAsk(t, sess)
	fs := attachEnvironmentFailureFS(t, sess)
	syncFailure := errors.New("retained user transcript sync failure")
	rollbackFailure := errors.New("retained user transcript rollback failure")
	durabilityFailure := errors.New("retained user transcript barrier failure")
	remainingFailures := 2
	fs.mu.Lock()
	fs.failure = syncFailure
	fs.rollbackFailure = rollbackFailure
	fs.onFailure = func() {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if remainingFailures == 0 {
			fs.failure = nil
			fs.onFailure = nil
			return
		}
		fs.failure = durabilityFailure
		remainingFailures--
	}
	fs.mu.Unlock()

	turn := schema.NewTurn(schema.TurnUserInput, llm.User("retained resolving input"))
	if err := sess.appendUserInputTurnRefusingPoison(turn); err != nil {
		t.Fatalf("retained direct admission error = %v, want the recorded line adopted", err)
	}
	sess.Close()
	kinds := transcriptTurnKinds(t, sess.TranscriptPath())
	userInputs := 0
	for _, kind := range kinds {
		if kind == schema.TurnUserInput {
			userInputs++
		}
	}
	if userInputs != 2 {
		t.Fatalf("durable user-input records = %d, want initial ask plus adopted retained input", userInputs)
	}
}

func prepareDurableAdmissionFailedStart(t *testing.T) (*Session, queuedInput) {
	sess, _, claimed := prepareDurableAdmissionFailedStartWithAdapter(t)
	return sess, claimed
}

func prepareDurableAdmissionFailedStartWithAdapter(t *testing.T) (*Session, *fakeAdapter, queuedInput) {
	t.Helper()
	sess, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
	seedDurableAdmissionAsk(t, sess)
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "failed-start-ask",
		Input:            []appwire.InputItem{{Type: "text", Text: "answer the question"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	return sess, adapter, claimed
}

func TestAskUser_RecoveredStartAdmissionClearsPendingAskLikeRestore(t *testing.T) {
	t.Parallel()
	sess, claimed := prepareDurableAdmissionFailedStart(t)
	failure := errors.New("deterministic pre-append failure")
	sess.clientMutationPreAppendFailure = func(schema.Turn) error { return failure }

	_, err := sess.ProcessInputKind(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		EntryUserInput,
	)
	if !errors.Is(err, failure) {
		t.Fatalf("recovered start error = %v, want %v", err, failure)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live askPendingCount after recovered user record = %d, want 0", got)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("live state after recovered user record = %q, want %q", got, SessionIdle)
	}

	meta := sess.Meta()
	dir := sess.stateDir
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored askPendingCount after recovered user record = %d, want 0", got)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state after recovered user record = %q, want %q", got, SessionIdle)
	}
}

func TestAskUser_PartialRecoveredStartAfterUserMatchesRestore(t *testing.T) {
	t.Parallel()
	sess, claimed := prepareDurableAdmissionFailedStart(t)
	failure := errors.New("deterministic pre-append failure")
	crash := errors.New("simulated recovery interruption after user")
	sess.clientMutationPreAppendFailure = func(schema.Turn) error { return failure }
	sess.clientMutationFailureRecoveryFault = func(boundary string) error {
		if boundary == "after_user" {
			return crash
		}
		return nil
	}

	_, err := sess.ProcessInputKind(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		EntryUserInput,
	)
	if !errors.Is(err, failure) || !errors.Is(err, crash) {
		t.Fatalf("partial recovered start error = %v, want failure and crash", err)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live askPendingCount after recovered user record with later failure = %d, want 0", got)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("live state after recovered user record with later failure = %q, want %q", got, SessionIdle)
	}

	meta := sess.Meta()
	dir := sess.stateDir
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored askPendingCount after partial recovery = %d, want 0", got)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state after partial recovery = %q, want %q", got, SessionIdle)
	}
}

func TestAskUser_RecoveredStartWithoutUserRecordKeepsPendingAsk(t *testing.T) {
	t.Parallel()
	sess, adapter, claimed := prepareDurableAdmissionFailedStartWithAdapter(t)
	failure := errors.New("deterministic pre-append failure")
	recovery := errors.New("simulated recovery interruption before user")
	sess.clientMutationPreAppendFailure = func(schema.Turn) error { return failure }
	sess.clientMutationFailureRecoveryFault = func(boundary string) error {
		if boundary == "before_user" {
			return recovery
		}
		return nil
	}

	_, err := sess.ProcessInputKind(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		EntryUserInput,
	)
	if !errors.Is(err, failure) || !errors.Is(err, recovery) {
		t.Fatalf("unrecorded recovered start error = %v, want failure and recovery", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("ask pending count after unrecorded recovery = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after unrecorded recovery = %q, want %q", got, SessionAwaiting)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests after unrecorded recovery = %d, want initial ask only", got)
	}
}
