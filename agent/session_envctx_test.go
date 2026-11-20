package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/gittest"
	"primeradiant.com/evener/llm"
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
		steps[i] = func(llm.Request) llm.Response { return agenttest.FinalResponse(message) }
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

func TestEnvironmentContextCrossesLiveEventBoundaryBeforeUserInput(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	sendOneUserInput(t, s, "hello")
	var kinds []events.EventKind
	for {
		select {
		case event := <-s.Events():
			kinds = append(kinds, event.Kind)
		default:
			goto drained
		}
	}

drained:
	environment, user := -1, -1
	for i, kind := range kinds {
		if kind == events.EventEnvironment && environment == -1 {
			environment = i
		}
		if kind == events.EventUserInput && user == -1 {
			user = i
		}
	}
	if environment == -1 || user == -1 || environment >= user {
		t.Fatalf("live events must expose environment before user input: environment=%d user=%d kinds=%v", environment, user, kinds)
	}
}

func TestEnvironmentContextTranscriptFailureDoesNotPublishOrAdvanceTracker(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	initialEnvironmentTurns := countEnvironmentTurns(s)
	for {
		select {
		case <-s.Events():
		default:
			goto drainedBeforeFailure
		}
	}
drainedBeforeFailure:
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	writer, err := transcript.NewWriterWithFS(fs, "/session.jsonl", transcript.Header{SessionID: s.id})
	if err != nil {
		t.Fatalf("create failing transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	fs.fail = true
	s.mu.Lock()
	original := s.transcript
	if original != nil {
		t.Cleanup(func() { _ = original.Close() })
	}
	s.transcript = writer
	s.transcriptReady = true
	s.mu.Unlock()

	if err := s.maybeAppendEnvironmentContext(); err == nil {
		t.Fatal("environment append reported success over a failing transcript")
	}

	if got := countEnvironmentTurns(s); got != initialEnvironmentTurns {
		t.Fatalf("environment history turns = %d, want unchanged at %d after transcript failure", got, initialEnvironmentTurns)
	}
	if got := len(environmentTurnsInBytes(t, fs, "/session.jsonl")); got != 0 {
		t.Fatalf("failed environment append left %d durable environment turns, want 0", got)
	}
	for {
		select {
		case event := <-s.Events():
			if event.Kind == events.EventEnvironment {
				t.Fatal("transcript failure published an environment event")
			}
		default:
			goto drained
		}
	}
drained:
	meta := loadMetaForTest(t, s)
	if meta.EnvContext != nil && meta.EnvContext.HasSent {
		t.Fatal("transcript failure advanced the persisted environment tracker")
	}

	fs.fail = false
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatalf("retry after a recovered transcript: %v", err)
	}
	if got := countEnvironmentTurns(s); got != initialEnvironmentTurns+1 {
		t.Fatalf("retry environment history turns = %d, want one emitted turn after failure", got)
	}
	var environmentEvents []events.SessionEvent
	for {
		select {
		case event := <-s.Events():
			if event.Kind == events.EventEnvironment {
				environmentEvents = append(environmentEvents, event)
			}
		default:
			goto drainedAfterRetry
		}
	}
drainedAfterRetry:
	if len(environmentEvents) != 1 {
		t.Fatalf("retry published %d environment events, want 1", len(environmentEvents))
	}
	var retried schema.Turn
	for _, turn := range s.history {
		if turn.Kind == schema.TurnEnvironment {
			retried = turn
		}
	}
	if environmentEvents[0].Data.(events.EnvironmentData).TurnID != retried.StableTurnID {
		t.Fatalf("retry event stable ID = %q, durable turn ID = %q", environmentEvents[0].Data.(events.EnvironmentData).TurnID, retried.StableTurnID)
	}
	durable := environmentTurnsInBytes(t, fs, "/session.jsonl")
	if len(durable) != 1 {
		t.Fatalf("retry durable environment turns = %d, want exactly 1", len(durable))
	}
	if durable[0].StableTurnID != environmentEvents[0].Data.(events.EnvironmentData).TurnID {
		t.Fatalf("retry durable stable ID = %q, event stable ID = %q", durable[0].StableTurnID, environmentEvents[0].Data.(events.EnvironmentData).TurnID)
	}
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatalf("turn over an unchanged environment: %v", err)
	}
	if got := countEnvironmentTurns(s); got != initialEnvironmentTurns+1 {
		t.Fatalf("unchanged retry environment history turns = %d, want suppressed after success", got)
	}
}

func environmentTurnsInBytes(t *testing.T, fs afero.Fs, path string) []schema.Turn {
	t.Helper()
	var result []schema.Turn
	for _, turn := range transcriptTurnsInBytes(t, fs, path) {
		if turn.Kind == schema.TurnEnvironment {
			result = append(result, turn)
		}
	}
	return result
}

func transcriptTurnsInBytes(t *testing.T, fs afero.Fs, path string) []schema.Turn {
	t.Helper()
	contents, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read transcript bytes: %v", err)
	}
	var turns []schema.Turn
	for line := range bytes.SplitSeq(contents, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var record struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode record boundary: %v", err)
		}
		if record.Kind == "header" {
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatalf("decode transcript entry: %v", err)
		}
		turns = append(turns, entry.Turn)
	}
	return turns
}

func TestEnvironmentContextWriteFailureAbortsUserAcceptance(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	writer, err := transcript.NewWriterWithFS(fs, "/session.jsonl", transcript.Header{SessionID: s.id})
	if err != nil {
		t.Fatalf("create failing transcript: %v", err)
	}
	fs.fail = true
	t.Cleanup(func() { _ = writer.Close() })
	s.mu.Lock()
	original := s.transcript
	if original != nil {
		t.Cleanup(func() { _ = original.Close() })
	}
	s.transcript = writer
	s.transcriptReady = true
	before := len(s.history)
	s.mu.Unlock()

	if err := s.acceptUserInput(context.Background(), "must not reach model", nil, nil, false); err == nil {
		t.Fatal("acceptUserInput unexpectedly succeeded after environment transcript failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot := s.clientMutations.snapshot(); snapshot.AcceptedTurns != uint64(s.turns) {
		t.Fatalf("accepted turns after environment write failure = %d, want %d", snapshot.AcceptedTurns, s.turns)
	}
	if len(s.history) != before {
		t.Fatalf("history length after environment write failure = %d, want %d", len(s.history), before)
	}
	for _, turn := range s.history {
		if turn.Kind == schema.TurnUserInput {
			t.Fatal("user input was appended despite environment durability failure")
		}
	}
}

func TestEnvironmentContextFailureRestoresPublicMutationClaimsForRetry(t *testing.T) {
	for _, kind := range []string{"direct", "start", "queue"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestSessionForEnvctx(t, withSteps(repeatFinalResponse(2, "ok")...))
			s.cfg.MaxTurns = 1
			fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
			writer, err := transcript.NewWriterWithFS(fs, "/session.jsonl", transcript.Header{SessionID: s.id})
			if err != nil {
				t.Fatalf("create transcript: %v", err)
			}
			t.Cleanup(func() { _ = writer.Close() })
			s.mu.Lock()
			original := s.transcript
			if original != nil {
				t.Cleanup(func() { _ = original.Close() })
			}
			s.transcript = writer
			s.transcriptReady = true
			s.mu.Unlock()
			fs.fail = true

			id := "retry-" + kind
			var run func() error
			switch kind {
			case "direct":
				run = func() error { _, err := s.ProcessInput(context.Background(), "retry direct", nil); return err }
			case "start":
				if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id, Input: []appwire.InputItem{{Type: "text", Text: "retry start"}}}); err != nil {
					t.Fatalf("AcceptClientMutationStart: %v", err)
				}
				run = func() error { _, _, err := s.ProcessClientMutationStart(context.Background(), nil); return err }
			case "queue":
				if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: id, Input: []appwire.InputItem{{Type: "text", Text: "retry queue"}}}); err != nil {
					t.Fatalf("AcceptClientMutationQueue: %v", err)
				}
				run = func() error { _, _, err := s.ProcessPendingUserInput(context.Background(), nil); return err }
			}
			if err := run(); err == nil {
				t.Fatal("first attempt unexpectedly succeeded")
			}
			failed := s.clientMutations.snapshot()
			if failed.AcceptedTurns != uint64(s.turns) {
				t.Fatalf("accepted turns after failed %s = %d, want %d", kind, failed.AcceptedTurns, s.turns)
			}
			for _, turn := range transcriptTurnsInBytes(t, fs, "/session.jsonl") {
				if turn.Kind == schema.TurnEnvironment || turn.Kind == schema.TurnUserInput {
					t.Fatalf("failed %s persisted %s", kind, turn.Kind)
				}
			}
			fs.fail = false
			if err := run(); err != nil {
				t.Fatalf("retry %s: %v", kind, err)
			}
			if got := countEnvironmentTurns(s); got != 1 {
				t.Fatalf("retry %s environment turns = %d, want 1", kind, got)
			}
			inputCount := 0
			for _, turn := range s.history {
				if turn.Kind == schema.TurnUserInput {
					inputCount++
				}
			}
			if inputCount != 1 {
				t.Fatalf("retry %s user input turns = %d, want 1", kind, inputCount)
			}
			var durableKinds []schema.TurnKind
			for _, turn := range transcriptTurnsInBytes(t, fs, "/session.jsonl") {
				if turn.Kind == schema.TurnEnvironment || turn.Kind == schema.TurnUserInput {
					durableKinds = append(durableKinds, turn.Kind)
				}
			}
			if len(durableKinds) != 2 || durableKinds[0] != schema.TurnEnvironment || durableKinds[1] != schema.TurnUserInput {
				t.Fatalf("retry %s durable input sequence = %v, want environment then exactly one user input", kind, durableKinds)
			}
		})
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
	okStep := func(llm.Request) llm.Response { return agenttest.FinalResponse("ok") }
	steps := []func(req llm.Request) llm.Response{
		okStep, // ProcessInput "hello"
		func(llm.Request) llm.Response { return agenttest.FinalResponse("forced summary") }, // Compact's L4 summarize call
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
// on one goroutine concurrently with resetEnvContextTrackerLocked (reassigns
// envTracker) on another — the exact pair the reviewer flagged: Session.Compact
// can reach the reset on a caller's own goroutine with no idle gate, racing the
// turn goroutine's maybeAppendEnvironmentContext. RED under -race before both
// sides took the locks around the tracker touch; GREEN after.
//
// The reset side is driven through the production sequence rather than a
// helper of its own: attentionMu then mu around resetEnvContextTrackerLocked,
// then the autosave outside both, which is exactly what handleCompactionTurn's
// CHECKPOINT/SUMMARY branch (session_namer.go) and publishFoldTransaction's
// foldCommit.resetEnvContextTrackerLocked do. Calling those two callers
// outright would drag in a transcript write, the compaction-turn effects and
// the async namer — unbounded work this race test has no use for.
//
// The append side calls maybeAppendEnvironmentContext directly rather than
// going through ProcessInput/Compact's full machinery: routing through
// Compact() (which unconditionally writes s.contextMgr.Meta, same as every
// per-round ManageContext call inside ProcessInput) surfaces a SEPARATE,
// pre-existing data race on contextMgr.Meta that predates this task and is out
// of its scope — see task-5-report.md's fix-round addendum. Calling the two
// sides directly isolates exactly the envTracker race under test without also
// tripping that unrelated one.
func TestSession_MaybeAppendEnvironmentContext_NoRaceWithCompact(t *testing.T) {
	if !raceDetectorEnabled {
		t.Skip("race-detector stress test; run with -race")
	}
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var hammer sync.WaitGroup
	hammer.Go(func() {
		for range 5000 {
			// The hammer races the append against the fold reset to drive the
			// tracker's locking; which attempts win and which report a failure
			// is the race under test, so the results are deliberately dropped.
			_ = sess.maybeAppendEnvironmentContext()
		}
	})
	hammer.Go(func() {
		for range 5000 {
			sess.attentionMu.Lock()
			sess.mu.Lock()
			changed := sess.resetEnvContextTrackerLocked()
			sess.mu.Unlock()
			sess.attentionMu.Unlock()
			if changed {
				sess.maybeAutoSave()
			}
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
	c.Register(newFakeAdapter("openai", repeatFinalResponse(2, "ok")...))
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	c2.Register(newFakeAdapter("openai", repeatFinalResponse(2, "ok")...))
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

// TestRestoredSessionReconcilesDurableEnvironmentAfterStaleMeta exercises the
// crash window after the ENVIRONMENT transcript append but before meta.json's
// EnvContext checkpoint. Restore must seed the tracker from the durable entry
// so the next unchanged turn does not append a duplicate block.
func TestRestoredSessionReconcilesDurableEnvironmentAfterStaleMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	probes := &envctx.Probes{Now: func() time.Time { return envctxFixedTime }}
	testCfg := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, envProbes: probes}

	c := llm.NewClient()
	c.Register(newFakeAdapter("openai", repeatFinalResponse(1, "ok")...))
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
		t.Fatalf("setup: want one durable environment turn, got %d", countEnvironmentTurns(sess))
	}
	meta := loadMetaForTest(t, sess)
	meta.EnvContext = &envctx.State{}
	if err := schema.SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("save stale meta: %v", err)
	}
	sess.Close()

	c2 := llm.NewClient()
	c2.Register(newFakeAdapter("openai", repeatFinalResponse(1, "ok")...))
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
	if count := countEnvironmentTurns(restored); count != 1 {
		t.Fatalf("stale EnvContext duplicated durable environment: got %d turns, want original 1", count)
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
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	c2.Register(newFakeAdapter("openai", repeatFinalResponse(1, "ok")...))
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

func TestEnvironmentContextResetIsAtomicWithFoldPublication(t *testing.T) {
	t.Parallel()
	s := newScriptedSummaryCompactSession(t, "env-fold-provider", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nSaved work summary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: t.TempDir(), testOnly: testConfig{
		envProbes: &envctx.Probes{Now: func() time.Time { return envctxFixedTime }},
	}}))
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if got := countEnvironmentTurns(s); got != 1 {
		t.Fatalf("initial environment turns = %d, want 1", got)
	}
	seedNumberedSessionHistory(t, s, 12)
	s.contextMgr.PreserveRecentTurns = 1

	parked := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldSideEffectsFlush = func() {
			close(parked)
			<-release
		}
	})
	done := make(chan struct{})
	var compactErr error
	go func() {
		defer close(done)
		compactErr = s.Compact(context.Background())
	}()
	t.Cleanup(func() { unblock(); <-done })
	select {
	case <-parked:
	case <-done:
		t.Fatalf("Compact returned before publication barrier: %v", compactErr)
	}
	if got := countEnvironmentTurns(s); got != 0 {
		t.Fatalf("fold retained %d environment turns, want folded prefix removed", got)
	}
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if got := countEnvironmentTurns(s); got != 1 {
		t.Fatalf("published fold must permit fresh environment before deferred flush: got %d turns", got)
	}
	var environmentID string
	for _, turn := range currentHistory(t, s) {
		if turn.Kind == schema.TurnEnvironment {
			environmentID = turn.StableTurnID
		}
	}
	unblock()
	<-done
	if compactErr != nil {
		t.Fatal(compactErr)
	}
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if got := countEnvironmentTurns(s); got != 1 {
		t.Fatalf("deferred fold flush reset the fresh tracker: got %d environment turns", got)
	}
	data, err := readTranscriptFull(context.Background(), s.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	var durableEnvironmentIDs []string
	for _, turn := range ResumeHistory(data.Entries) {
		if turn.Kind == schema.TurnEnvironment {
			durableEnvironmentIDs = append(durableEnvironmentIDs, turn.StableTurnID)
		}
	}
	if len(durableEnvironmentIDs) != 1 || durableEnvironmentIDs[0] != environmentID {
		t.Fatalf("reloaded environment IDs = %v, want exactly %s", durableEnvironmentIDs, environmentID)
	}
	meta := loadMetaForTest(t, s)
	if meta.EnvContext == nil || !meta.EnvContext.HasSent {
		t.Fatalf("fresh environment tracker not persisted: %+v", meta.EnvContext)
	}
}

func TestEnvironmentContextPreservedRecentTailStaysSilentAfterCompact(t *testing.T) {
	t.Parallel()
	s := newScriptedSummaryCompactSession(t, "env-preserved-tail", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{StateDir: t.TempDir(), testOnly: testConfig{envProbes: &envctx.Probes{Now: func() time.Time { return envctxFixedTime }}}}))
	seedNumberedSessionHistory(t, s, 6)
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	s.contextMgr.PreserveRecentTurns = 2
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := countEnvironmentTurns(s)
	if before != 1 {
		t.Fatalf("compaction retained %d environment turns, want 1", before)
	}
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if got := countEnvironmentTurns(s); got != before {
		t.Fatalf("preserved environment tracker emitted duplicate: before=%d after=%d", before, got)
	}
}

func TestEnvironmentContextRemovedFullBlockResetsAgainstRetainedChangedBlock(t *testing.T) {
	t.Parallel()
	now := envctxFixedTime
	s := newTestSessionForEnvctx(t, withConfig(SessionConfig{testOnly: testConfig{envProbes: &envctx.Probes{Now: func() time.Time { return now }}}}))
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	oldID := ""
	for _, turn := range s.history {
		if turn.Kind == schema.TurnEnvironment {
			oldID = turn.StableTurnID
		}
	}
	seedNumberedSessionHistory(t, s, 8)
	now = now.Add(time.Hour)
	setProfile := func(model string) { s.mu.Lock(); s.profile = NewOpenAIProfile(model); s.mu.Unlock() }
	setProfile("gpt-5.2-b") // a runtime fact newer than the prompt
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	changedID := s.history[len(s.history)-1].StableTurnID
	if countEnvironmentTurns(s) != 2 || changedID == "" || changedID == oldID {
		t.Fatal("changed observation did not append a distinct environment turn")
	}
	s.contextMgr.PreserveRecentTurns = 2
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if oldID == "" {
		t.Fatal("missing original environment identity")
	}
	var retainedIDs []string
	for _, turn := range s.history {
		if turn.Kind == schema.TurnEnvironment {
			retainedIDs = append(retainedIDs, turn.StableTurnID)
		}
	}
	if len(retainedIDs) != 1 || retainedIDs[0] != changedID {
		t.Fatalf("retained environment IDs = %v, want only changed turn %s (old %s)", retainedIDs, changedID, oldID)
	}
	before := countEnvironmentTurns(s)
	setProfile("gpt-5.2") // switching back is a change the prompt never stated
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if b := lastTurnOfKind(t, s, schema.TurnEnvironment).Message.Text(); countEnvironmentTurns(s) != before+1 || !strings.Contains(b, "model: gpt-5.2\n") {
		t.Fatalf("retained runtime diff did not report the switch back: before=%d after=%d\n%s", before, countEnvironmentTurns(s), b)
	}
}

func TestEnvironmentContextFirstAppendBetweenFoldSnapshotAndPublicationStaysSilent(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	s := newScriptedSummaryCompactSession(t, "env-merge-tail", func(llm.Request) llm.Response {
		once.Do(func() { close(started) })
		<-release
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{StateDir: t.TempDir(), testOnly: testConfig{envProbes: &envctx.Probes{Now: func() time.Time { return envctxFixedTime }}}}))
	t.Cleanup(releaseProvider)
	seedNumberedSessionHistory(t, s, 12)
	s.contextMgr.PreserveRecentTurns = 1
	done := make(chan error, 1)
	go func() { done <- s.Compact(context.Background()) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("Compact returned before provider barrier: %v", err)
	}
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	releaseProvider()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	before := countEnvironmentTurns(s)
	if before != 1 {
		t.Fatalf("compaction retained %d merged environment turns, want 1", before)
	}
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if countEnvironmentTurns(s) != before {
		t.Fatalf("merged environment duplicated after fold: before=%d after=%d", before, countEnvironmentTurns(s))
	}
	data, err := readTranscriptFull(context.Background(), s.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, turn := range ResumeHistory(data.Entries) {
		if turn.Kind == schema.TurnEnvironment {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("reloaded environment turns = %d, want 1", got)
	}
}

func launchFactSession(t *testing.T, dir, provenance string) *Session {
	return newSession(t, withDir(dir), withSteps(repeatFinalResponse(8, "ok")...), withConfig(SessionConfig{
		StateDir: dir, CommitProvenance: provenance, AgentsDocPath: filepath.Join(dir, "AGENTS.md"),
		testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true, envProbes: &envctx.Probes{Now: func() time.Time { return envctxFixedTime }}},
	}))
}
func TestEnvironmentBlockStatesRuntimeFactsOnce(t *testing.T) {
	t.Parallel()
	s := launchFactSession(t, t.TempDir(), "coauthor")
	sendOneUserInput(t, s, "hello")
	if b := lastTurnOfKind(t, s, schema.TurnEnvironment).Message.Text(); !strings.Contains(b, "cwd: ") || strings.Contains(b, "model: ") || strings.Contains(b, "cutoff: ") || strings.Contains(b, "trailer: ") || strings.Contains(b, "dir: ") || strings.Contains(b, "tools: ") || strings.Contains(b, "skills: ") {
		t.Fatalf("first block restated the prompt or lost the environment: %q", b)
	}
	s.Close()
	restore := func(id string) *Session {
		r := restoreQueuePersistTestSessionWith(t, s.stateDir, id, RestoreSessionConfig{CommitProvenance: "off", testOnly: s.cfg.testOnly})
		r.client.Register(newFakeAdapter("openai", repeatFinalResponse(4, "resumed")...))
		t.Cleanup(r.Close)
		return r
	}
	first := restore(s.ID())
	sendOneUserInput(t, first, "resume")
	if b := lastTurnOfKind(t, first, schema.TurnEnvironment).Message.Text(); !strings.Contains(b, "commit trailer: (disabled)") {
		t.Fatalf("opting out recorded no disable:\n%s", b)
	}
	second := restore(first.ID())
	settled := countEnvironmentTurns(second)
	sendOneUserInput(t, second, "resume again")
	if countEnvironmentTurns(second) != settled {
		t.Fatal("second restore appended an environment turn")
	}
	second.attentionMu.Lock()
	second.mu.Lock()
	second.profile, second.cfg.CommitProvenance = NewOpenAIProfile("gpt-5.2-changed"), "off"
	second.resetEnvContextTrackerLocked()
	second.mu.Unlock()
	second.attentionMu.Unlock()
	if err := second.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if b := lastTurnOfKind(t, second, schema.TurnEnvironment).Message.Text(); !strings.Contains(b, "model: gpt-5.2-changed") || strings.Contains(b, "knowledge cutoff: ") {
		t.Fatalf("post-compaction block is not a launch-relative diff:\n%s", b)
	}
	fresh := launchFactSession(t, t.TempDir(), "")
	fresh.mu.Lock()
	fresh.cachedToolDefs = append(append([]llm.ToolDefinition{}, fresh.cachedToolDefs...), llm.ToolDefinition{Name: "update_goal"})
	fresh.mu.Unlock()
	fresh.acceptContinuationInput(context.Background(), "keep going", "")
	if b, n := lastTurnOfKind(t, fresh, schema.TurnEnvironment).Message.Text(), len(fresh.history); !strings.Contains(b, "added tools: update_goal") ||
		fresh.history[n-2].Kind != schema.TurnEnvironment || fresh.history[n-1].Kind != schema.TurnSteering {
		t.Fatalf("a tool that became callable crossed no turn, or the driving prompt is not last:\n%s", b)
	}
	steady := countEnvironmentTurns(fresh)
	fresh.acceptContinuationInput(context.Background(), "keep going", "")
	if countEnvironmentTurns(fresh) != steady {
		t.Fatal("an unchanged continuation appended an environment turn")
	}
	fresh.Close()
	forkID, err := AsideSession(fresh.stateDir, fresh.ID())
	if err != nil {
		t.Fatalf("AsideSession: %v", err)
	}
	aside := restoreQueuePersistTestSessionWith(t, fresh.stateDir, forkID, RestoreSessionConfig{testOnly: fresh.cfg.testOnly})
	aside.client.Register(newFakeAdapter("openai", repeatFinalResponse(4, "aside")...))
	t.Cleanup(aside.Close)
	before := len(aside.history)
	if err := aside.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if n := len(aside.history); n > before && strings.Contains(aside.history[n-1].Message.Text(), "added tools: ") {
		t.Fatalf("the aside re-announced launch tools as added:\n%s", aside.history[n-1].Message.Text())
	}
}

func TestOneShotResumeFromPersistentSessionEmitsRunLifetimeDiff(t *testing.T) {
	t.Parallel()
	s := launchFactSession(t, t.TempDir(), "")
	sendOneUserInput(t, s, "hello")
	if b := lastTurnOfKind(t, s, schema.TurnEnvironment).Message.Text(); strings.Contains(b, "run lifetime:") {
		t.Fatalf("fresh persistent session restated run lifetime:\n%s", b)
	}
	launchPrompt := s.cachedSystemPrompt
	s.Close()

	// Resuming with TurnEndsProcess=true emits run lifetime diff once while
	// keeping the launch prompt byte-identical.
	oneShot := restoreQueuePersistTestSessionWith(t, s.stateDir, s.ID(), RestoreSessionConfig{
		TurnEndsProcess: true,
		testOnly:        s.cfg.testOnly,
	})
	oneShot.client.Register(newFakeAdapter("openai", repeatFinalResponse(4, "resumed")...))
	t.Cleanup(oneShot.Close)
	sendOneUserInput(t, oneShot, "one shot input")
	if oneShot.cachedSystemPrompt != launchPrompt {
		t.Fatal("one-shot resume mutated launch system prompt")
	}
	b := lastTurnOfKind(t, oneShot, schema.TurnEnvironment).Message.Text()
	if !strings.Contains(b, "run lifetime: one-shot") {
		t.Fatalf("one-shot resume did not emit run lifetime diff:\n%s", b)
	}
	countAfterFirst := countEnvironmentTurns(oneShot)
	sendOneUserInput(t, oneShot, "second one shot input")
	if countEnvironmentTurns(oneShot) != countAfterFirst {
		t.Fatalf("second turn re-emitted run lifetime: got %d, want %d", countEnvironmentTurns(oneShot), countAfterFirst)
	}
	oneShot.Close()

	// Legacy baselineless persistent session resumed via one-shot also receives
	// the run lifetime correction once.
	legacy := launchFactSession(t, t.TempDir(), "")
	legacy.Close()
	legacyMeta, err := schema.LoadSessionMeta(legacy.stateDir, legacy.ID())
	if err != nil {
		t.Fatalf("LoadSessionMeta(legacy): %v", err)
	}
	legacyMeta.LaunchEnvBaseline = envctx.Snapshot{}
	legacyMeta.EnvContext = nil
	if err := schema.SaveSessionMeta(legacy.stateDir, legacyMeta); err != nil {
		t.Fatalf("SaveSessionMeta(legacy): %v", err)
	}
	legacyOneShot := restoreQueuePersistTestSessionWith(t, legacy.stateDir, legacy.ID(), RestoreSessionConfig{
		TurnEndsProcess: true,
		testOnly:        legacy.cfg.testOnly,
	})
	legacyOneShot.client.Register(newFakeAdapter("openai", repeatFinalResponse(4, "legacy")...))
	t.Cleanup(legacyOneShot.Close)
	sendOneUserInput(t, legacyOneShot, "legacy one shot")
	lb := lastTurnOfKind(t, legacyOneShot, schema.TurnEnvironment).Message.Text()
	if !strings.Contains(lb, "run lifetime: one-shot") {
		t.Fatalf("legacy baselineless one-shot resume did not emit run lifetime:\n%s", lb)
	}
	beforeSecond := countEnvironmentTurns(legacyOneShot)
	sendOneUserInput(t, legacyOneShot, "legacy second")
	if countEnvironmentTurns(legacyOneShot) != beforeSecond {
		t.Fatalf("legacy second turn re-emitted environment: got %d, want %d", countEnvironmentTurns(legacyOneShot), beforeSecond)
	}
}

func TestCompactedGitSessionResumedOutsideGitReportsNoRepo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		mutateMeta func(m *schema.SessionMeta)
	}{
		{name: "current-baseline"},
		{
			name: "6968-nonempty-baseline-empty-git-branch",
			mutateMeta: func(m *schema.SessionMeta) {
				m.LaunchEnvBaseline.GitBranch = ""
				m.EnvInfo.GitBranch = "main"
			},
		},
		{
			name: "legacy-saved-once-outside-git",
			mutateMeta: func(m *schema.SessionMeta) {
				m.LaunchEnvBaseline.GitBranch = ""
				m.EnvInfo.GitBranch = ""
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gitDir := t.TempDir()
			gittest.Run(t, gitDir, "init")
			if err := os.WriteFile(filepath.Join(gitDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
				t.Fatalf("write README.md: %v", err)
			}
			gittest.Run(t, gitDir, "add", "README.md")
			gittest.Run(t, gitDir, "commit", "-m", "initial")

			stateDir := t.TempDir()
			client := llm.NewClient()
			client.Register(newFakeAdapter("openai", repeatFinalResponse(4, "ok")...))
			s, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(gitDir), SessionConfig{
				StateDir: stateDir,
				testOnly: testConfig{noSyncJobStore: true},
			})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			sendOneUserInput(t, s, "in git")
			muteNoteElicitation(s)
			s.contextMgr.SummarizeThreshold = 2.0
			s.contextMgr.PreserveRecentTurns = 0
			if err := s.Compact(context.Background()); err != nil {
				t.Fatalf("Compact: %v", err)
			}
			id := s.ID()
			s.Close()

			meta, err := schema.LoadSessionMeta(stateDir, id)
			if err != nil {
				t.Fatalf("LoadSessionMeta: %v", err)
			}
			if tc.mutateMeta != nil {
				tc.mutateMeta(&meta)
			}
			nonGitDir := t.TempDir()
			rClient := llm.NewClient()
			rClient.Register(newFakeAdapter("openai", repeatFinalResponse(16, "resumed")...))
			r, err := RestoreSessionFromMetaWithConfig(rClient, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(nonGitDir), meta, RestoreSessionConfig{
				StateDir: stateDir,
				testOnly: testConfig{noSyncJobStore: true},
			})
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			defer r.Close()
			sendOneUserInput(t, r, "outside git")
			if b := lastTurnOfKind(t, r, schema.TurnEnvironment).Message.Text(); !strings.Contains(b, "git branch: (not in a git repository)") {
				t.Fatalf("compacted git session resumed outside git missing git branch placeholder:\n%s", b)
			}
			settled := countEnvironmentTurns(r)
			sendOneUserInput(t, r, "still outside git")
			if countEnvironmentTurns(r) != settled {
				t.Fatalf("second turn outside git re-emitted environment block: got %d, want %d", countEnvironmentTurns(r), settled)
			}

			// Compact again after resuming outside git: resetEnvContextTrackerLocked
			// must still preserve the launch GitBranch on s.launchEnvBaseline.
			muteNoteElicitation(r)
			r.contextMgr.SummarizeThreshold = 2.0
			r.contextMgr.PreserveRecentTurns = 0
			if err := r.Compact(context.Background()); err != nil {
				t.Fatalf("Compact after resume: %v", err)
			}
			if err := r.maybeAppendEnvironmentContext(); err != nil {
				t.Fatalf("maybeAppendEnvironmentContext after resume compact: %v", err)
			}
			if b := lastTurnOfKind(t, r, schema.TurnEnvironment).Message.Text(); !strings.Contains(b, "git branch: (not in a git repository)") {
				t.Fatalf("post-compaction replacement outside git missing git branch placeholder:\n%s", b)
			}
			afterCompact := countEnvironmentTurns(r)
			sendOneUserInput(t, r, "after post-resume compact")
			if countEnvironmentTurns(r) != afterCompact {
				t.Fatalf("unchanged turn after post-resume compact re-emitted environment block: got %d, want %d", countEnvironmentTurns(r), afterCompact)
			}
		})
	}
}
