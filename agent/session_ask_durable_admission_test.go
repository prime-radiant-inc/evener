package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func newDurableAdmissionAskSession(t *testing.T, cfg SessionConfig) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(askUserCall("ask1", askUserArgsValid())) },
		},
	})
	cfg.StateDir = dir
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

func seedDurableAdmissionAsk(t *testing.T, sess *Session) context.Context {
	t.Helper()
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
	failure := errors.New("user transcript append durability failure")
	fs := attachEnvironmentFailureFS(t, sess)
	fs.mu.Lock()
	fs.writeFailure = failure
	fs.transferBeforeWriteFailure = 12
	fs.mu.Unlock()
	if _, err := sess.ProcessInput(ctx, "answer the question", nil); !errors.Is(err, failure) {
		t.Fatalf("transcript-refused resolving input error = %v, want %v", err, failure)
	}
	assertDurableAdmissionAskRestoresAwaiting(t, sess)
}
