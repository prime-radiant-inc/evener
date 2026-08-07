package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/envctx"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// envctxFixedTime is the deterministic clock the injected envProbes seam
// reports, so a Snapshot's LocalDateHour never changes between calls within a
// test (the unchanged-environment fast path depends on that).
var envctxFixedTime = time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)

// repeatFinalResponse builds n identical scripted "turn ends via communicate"
// steps, since fakeAdapter falls back to a bare-text (non-communicate)
// response once its steps list is exhausted, which the round loop rejects.
func repeatFinalResponse(n int, message string) []func(req llm.Request) llm.Response {
	steps := make([]func(req llm.Request) llm.Response, n)
	for i := range steps {
		steps[i] = func(llm.Request) llm.Response { return finalResponse(message) }
	}
	return steps
}

// newTestSessionForEnvctx builds a *Session with StateDir set (so meta.json
// persistence is exercised) and a deterministic envctx probe set injected via
// the testOnly.envProbes seam, mirroring newTestSession's pattern
// (subagents_test.go) plus a per-test StateDir. The default scripted
// adapter answers up to 4 model rounds (enough for every test in this file);
// pass withSteps(...) to override.
func newTestSessionForEnvctx(t *testing.T, opts ...sessionOpt) *Session {
	t.Helper()
	dir := t.TempDir()
	base := []sessionOpt{
		withDir(dir),
		withSteps(repeatFinalResponse(4, "ok")...),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			StateDir:         dir,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
				envProbes:           &envctx.Probes{Now: func() time.Time { return envctxFixedTime }},
			},
		}),
	}
	return newSession(t, append(base, opts...)...)
}

// sendOneUserInput drives one ProcessInput round to completion.
func sendOneUserInput(t *testing.T, s *Session, text string) {
	t.Helper()
	if _, err := s.ProcessInput(context.Background(), text, nil); err != nil {
		t.Fatalf("ProcessInput(%q): %v", text, err)
	}
}

// loadMetaForTest reloads the session's persisted meta.json from its state dir.
func loadMetaForTest(t *testing.T, s *Session) schema.SessionMeta {
	t.Helper()
	meta, err := schema.LoadSessionMeta(s.stateDir, s.id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	return meta
}

func countEnvironmentTurns(s *Session) int {
	n := 0
	for _, tn := range s.history {
		if tn.Kind == schema.TurnEnvironment {
			n++
		}
	}
	return n
}

func TestFirstUserTurnIsPrecededByEnvironmentContext(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	sendOneUserInput(t, s, "hello")

	turns := s.history
	var envIdx, userIdx = -1, -1
	for i, tn := range turns {
		switch tn.Kind {
		case schema.TurnEnvironment:
			envIdx = i
		case schema.TurnUserInput:
			userIdx = i
		}
	}
	if envIdx == -1 || userIdx == -1 || envIdx != userIdx-1 {
		t.Fatalf("want ENVIRONMENT immediately before USER_INPUT, got env=%d user=%d turns=%+v", envIdx, userIdx, turns)
	}
	if !strings.Contains(turns[envIdx].Message.Text(), "<environment_context>") {
		t.Fatalf("environment turn content: %q", turns[envIdx].Message.Text())
	}
}

func TestSecondUserTurnEmitsNoEnvironmentContextWhenUnchanged(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	sendOneUserInput(t, s, "hello")
	sendOneUserInput(t, s, "again")

	if count := countEnvironmentTurns(s); count != 1 {
		t.Fatalf("unchanged environment must emit exactly once, got %d", count)
	}
}

func TestEnvContextStatePersistsToMeta(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	sendOneUserInput(t, s, "hello")
	meta := loadMetaForTest(t, s)
	if meta.EnvContext == nil || !meta.EnvContext.HasSent {
		t.Fatalf("EnvContext not persisted: %+v", meta.EnvContext)
	}
}

// TestCompactionResetsEnvironmentContextTracker: a CHECKPOINT/SUMMARY turn
// folds the earlier ENVIRONMENT turn out of the model-visible history, so the
// tracker must forget what it last reported. Without the reset, the next user
// turn would stay silent (the collector observes the same unchanged
// environment) even though the model can no longer see any environment_context
// block at all — the controller ruling this test pins.
func TestCompactionResetsEnvironmentContextTracker(t *testing.T) {
	t.Parallel()
	okStep := func(llm.Request) llm.Response { return finalResponse("ok") }
	steps := []func(req llm.Request) llm.Response{
		okStep, // ProcessInput "hello"
		func(llm.Request) llm.Response { return finalResponse("forced summary") }, // Compact's L4 summarize call
		okStep, // ProcessInput "again"
	}
	s := newTestSessionForEnvctx(t, withSteps(steps...))
	sendOneUserInput(t, s, "hello")
	if countEnvironmentTurns(s) != 1 {
		t.Fatalf("setup: want exactly one ENVIRONMENT turn before compaction, got %d: %+v", countEnvironmentTurns(s), s.history)
	}

	// Fold everything but the last turn away, guaranteeing the ENVIRONMENT
	// turn (near the front of history) gets compacted out.
	s.contextMgr.PreserveRecentTurns = 1
	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if countEnvironmentTurns(s) != 0 {
		t.Fatalf("compaction should have folded away the ENVIRONMENT turn, still have %d: %+v", countEnvironmentTurns(s), s.history)
	}

	sendOneUserInput(t, s, "again")

	if count := countEnvironmentTurns(s); count != 1 {
		t.Fatalf("want a fresh full ENVIRONMENT turn after compaction, got %d: %+v", count, s.history)
	}
	meta := loadMetaForTest(t, s)
	if meta.EnvContext == nil || !meta.EnvContext.HasSent {
		t.Fatalf("post-compaction EnvContext not re-persisted: %+v", meta.EnvContext)
	}
}

// TestSession_MaybeAppendEnvironmentContext_NoRaceWithCompact hammers
// maybeAppendEnvironmentContext (reads envTracker, mutates it via RenderDiff)
// on one goroutine concurrently with resetEnvContextTrackerAfterCompaction
// (reassigns envTracker) on another — the exact pair the reviewer flagged:
// Session.Compact can run resetEnvContextTrackerAfterCompaction on a caller's
// own goroutine with no idle gate, racing the turn goroutine's
// maybeAppendEnvironmentContext. RED under -race before both methods took mu
// around the tracker touch; GREEN after.
//
// This calls the two methods directly rather than going through
// ProcessInput/Compact's full machinery: routing through Compact() (which
// unconditionally writes s.contextMgr.Meta, same as every per-round
// ManageContext call inside ProcessInput) surfaces a SEPARATE, pre-existing
// data race on contextMgr.Meta that predates this task and is out of its
// scope — see task-5-report.md's fix-round addendum. Calling
// maybeAppendEnvironmentContext/resetEnvContextTrackerAfterCompaction
// directly isolates exactly the envTracker race under test without also
// tripping that unrelated one.
func TestSession_MaybeAppendEnvironmentContext_NoRaceWithCompact(t *testing.T) {
	if !raceDetectorEnabled {
		t.Skip("race-detector stress test; run with -race")
	}
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var hammer sync.WaitGroup
	hammer.Go(func() {
		for range 5000 {
			sess.maybeAppendEnvironmentContext()
		}
	})
	hammer.Go(func() {
		for range 5000 {
			sess.resetEnvContextTrackerAfterCompaction()
		}
	})
	hammer.Wait()
}

// TestRestoredSessionWithMatchingEnvContextStaysSilent: a session persists
// EnvContext after its first turn, closes, and is restored against the SAME
// execution environment (same cwd, sandbox, and — via the fixed envProbes
// clock — the same observed date). The restored session's next turn must
// observe an unchanged environment and emit no ENVIRONMENT turn at all.
func TestRestoredSessionWithMatchingEnvContextStaysSilent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	probes := &envctx.Probes{Now: func() time.Time { return envctxFixedTime }}
	testCfg := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, envProbes: probes}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: repeatFinalResponse(2, "ok")})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
		testOnly: testCfg,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if countEnvironmentTurns(sess) != 1 {
		t.Fatalf("setup: want exactly one ENVIRONMENT turn, got %d: %+v", countEnvironmentTurns(sess), sess.history)
	}
	sess.Close()

	meta := loadMetaForTest(t, sess)
	if meta.EnvContext == nil || !meta.EnvContext.HasSent {
		t.Fatalf("setup: EnvContext not persisted: %+v", meta.EnvContext)
	}

	c2 := llm.NewClient()
	c2.Register(&fakeAdapter{name: "openai", steps: repeatFinalResponse(2, "ok")})
	restored, err := RestoreSessionFromMetaWithConfig(c2, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir: dir,
		testOnly: testCfg,
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if _, err := restored.ProcessInput(context.Background(), "again", nil); err != nil {
		t.Fatalf("ProcessInput after restore: %v", err)
	}
	// The restored history already carries the ONE ENVIRONMENT turn from
	// before Close (restored verbatim from the transcript): a matching
	// environment on the "again" turn must not add a second one.
	if count := countEnvironmentTurns(restored); count != 1 {
		t.Fatalf("restored session with a matching environment must stay silent (want the original 1, no new one), got %d ENVIRONMENT turns: %+v", count, restored.history)
	}
}

// TestRestoredSessionWithNilEnvContextReemitsFullBlock: a session closed
// before ever processing a turn persists a nil EnvContext (predating the
// feature is the same shape: no prior report to be silent about). The
// restored session's first turn must re-emit a full ENVIRONMENT block, the
// same as a brand-new session's first turn.
func TestRestoredSessionWithNilEnvContextReemitsFullBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	probes := &envctx.Probes{Now: func() time.Time { return envctxFixedTime }}
	testCfg := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, envProbes: probes}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
		testOnly: testCfg,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.Close() // Closed without ever processing a turn: meta.EnvContext stays nil.

	meta := loadMetaForTest(t, sess)
	if meta.EnvContext != nil {
		t.Fatalf("setup: expected nil EnvContext before any turn, got %+v", meta.EnvContext)
	}

	c2 := llm.NewClient()
	c2.Register(&fakeAdapter{name: "openai", steps: repeatFinalResponse(1, "ok")})
	restored, err := RestoreSessionFromMetaWithConfig(c2, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir: dir,
		testOnly: testCfg,
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if _, err := restored.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatalf("ProcessInput after restore: %v", err)
	}
	if count := countEnvironmentTurns(restored); count != 1 {
		t.Fatalf("restored session with nil EnvContext must re-emit a full block, got %d: %+v", count, restored.history)
	}
	restoredMeta := loadMetaForTest(t, restored)
	if restoredMeta.EnvContext == nil || !restoredMeta.EnvContext.HasSent {
		t.Fatalf("EnvContext not persisted after restore re-emit: %+v", restoredMeta.EnvContext)
	}
}
