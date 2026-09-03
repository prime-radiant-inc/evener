package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestSetPinnedNote_AndClear(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.setPinnedNote("remember the API signature")
	if got := s.PinnedNote(); got != "remember the API signature" {
		t.Fatalf("note not stored: %q", got)
	}
	s.setPinnedNote("")
	if got := s.PinnedNote(); got != "" {
		t.Fatalf("empty note should clear: %q", got)
	}
}

func TestRequestForceCompact_OnePerRound(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	if err := s.requestForceCompact("drop logs"); err != nil {
		t.Fatalf("first request should succeed: %v", err)
	}
	if err := s.requestForceCompact("drop more"); err == nil {
		t.Fatal("second request in the same round must error")
	}
	instr, ok := s.takeForceRequest()
	if !ok || instr != "drop logs" {
		t.Fatalf("takeForceRequest = %q,%v", instr, ok)
	}
	if err := s.requestForceCompact("next round"); err != nil {
		t.Fatalf("after consume, a new request should succeed: %v", err)
	}
}

// makeSteeringSeed builds a slice of n ordinary (non-steering) turns for use as
// the history seed in runPreCompactHook tests.
func makeSteeringSeed(n int) []schema.Turn {
	turns := make([]schema.Turn, n)
	for i := range turns {
		turns[i] = schema.NewTurn(schema.TurnUserInput, llm.User("turn"))
	}
	return turns
}

// indexOfSteering returns the index of the first TurnSteering turn whose text
// contains substr, or -1 if not found.
func indexOfSteering(history []schema.Turn, substr string) int {
	for i, t := range history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), substr) {
			return i
		}
	}
	return -1
}

// countSteering counts TurnSteering turns whose text contains substr.
func countSteering(history []schema.Turn, substr string) int {
	n := 0
	for _, t := range history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), substr) {
			n++
		}
	}
	return n
}

// kindOfSteeringRecord returns the kind of the first record whose text
// contains substr, or "" if none match.
func kindOfSteeringRecord(records []steeringTurnRecord, substr string) string {
	for _, r := range records {
		if strings.Contains(r.text, substr) {
			return r.kind
		}
	}
	return ""
}

// TestRunPreCompactHook_StampsNoteBeforeObjective verifies that when both a
// pinned note and an active goal are set, runPreCompactHook appends a note
// steering turn that (a) is present, and (b) precedes the goal objective turn
// so the objective stays in the trailing/strongest-recency position. The
// note's actual clearing is staged into the returned commit (a losing fold
// must not have already consumed it) rather than happening eagerly inside
// the hook call — this asserts BOTH that the note survives until commit
// runs, and that it's gone once commit does.
func TestRunPreCompactHook_HandsOffNoteBeforeObjective(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: do X")
	s.getOrCreateGoalStore().Set("Ship the feature", time.Now())

	hist := makeSteeringSeed(4)
	_, commit := s.runPreCompactHook(context.Background(), &hist)

	noteIdx := indexOfSteering(hist, noteHandoffPrefix)
	goalIdx := indexOfSteering(hist, "Ship the feature")
	if noteIdx < 0 {
		t.Fatal("note not handed off")
	}
	if goalIdx < 0 {
		t.Fatal("goal objective not injected by runPreCompactHook")
	}
	if noteIdx > goalIdx {
		t.Fatal("note must precede the goal objective (objective stays trailing)")
	}
	if s.PinnedNote() == "" {
		t.Fatal("note must NOT be cleared before commit runs — a losing fold must be able to leave it intact")
	}
	if commit == nil {
		t.Fatal("expected a non-nil commit: the hook consumed a pinned note, so there's a deferred side effect to commit")
	}
	commit()
	if s.PinnedNote() != "" {
		t.Fatalf("note must be cleared once commit runs (the one-shot handoff), still have %q", s.PinnedNote())
	}
}

// TestRunPreCompactHook_HandoffIsOneShot verifies the note is consumed by the
// compaction it rides on: once a hook pass's commit runs (simulating that
// fold winning publication), a second hook pass (no new note set) injects
// nothing, leaving exactly one handoff turn.
func TestRunPreCompactHook_HandoffIsOneShot(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: do X")
	hist := makeSteeringSeed(4)
	_, commit := s.runPreCompactHook(context.Background(), &hist)
	if commit == nil {
		t.Fatal("expected a non-nil commit from the first pass")
	}
	commit()
	_, commit = s.runPreCompactHook(context.Background(), &hist)
	if commit != nil {
		t.Fatal("second pass found no pinned note (already committed-cleared) — expected a nil commit, nothing to defer")
	}
	if n := countSteering(hist, noteHandoffPrefix); n != 1 {
		t.Fatalf("expected exactly one handoff turn, got %d", n)
	}
}

// TestRunPreCompactHook_StampsEachSourceItsOwnKind is the plugin-less case
// (no hookRunner) review round 1 flagged as mattering most: runPreCompactHook
// merges the pinned-note handoff and the goal objective into one messages
// slice, and flushSteeringTurnRecords used to stamp every record in that
// slice SteeringKindPrecompactHook regardless of which of the three sources
// produced it. With no plugin loaded at all, that made a plugin-less session
// emit ONLY mislabelled records — a goal objective confidently labeled as a
// hook. Each source must carry its own kind instead.
func TestRunPreCompactHook_StampsEachSourceItsOwnKind(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: do X")
	s.getOrCreateGoalStore().Set("Ship the feature", time.Now())

	hist := makeSteeringSeed(4)
	records, _ := s.runPreCompactHook(context.Background(), &hist)

	if got := kindOfSteeringRecord(records, noteHandoffPrefix); got != events.SteeringKindNoteHandoff {
		t.Errorf("note handoff kind = %q, want %q", got, events.SteeringKindNoteHandoff)
	}
	if got := kindOfSteeringRecord(records, "Ship the feature"); got != events.SteeringKindGoalObjective {
		t.Errorf("goal objective kind = %q, want %q", got, events.SteeringKindGoalObjective)
	}
	for _, r := range records {
		if r.kind == events.SteeringKindPrecompactHook {
			t.Errorf("no plugin ran, but record %q was stamped %q", r.text, events.SteeringKindPrecompactHook)
		}
	}
}

// TestRunPreCompactHook_PersistsKindOnTheTurn verifies runPreCompactHook sets
// schema.Turn.SteeringKind on the turns it appends directly to hist — not
// only on the steeringTurnRecord.kind round 1 added for the live emit — so a
// reload labels a pre-compact steering turn the same way the live transcript
// did (review round 2: this direct-append site bypasses the
// SteerKind/consumeSteeringMessage queue path that already persisted its
// kind).
func TestRunPreCompactHook_PersistsKindOnTheTurn(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: do X")
	s.getOrCreateGoalStore().Set("Ship the feature", time.Now())

	hist := makeSteeringSeed(4)
	s.runPreCompactHook(context.Background(), &hist)

	noteIdx := indexOfSteering(hist, noteHandoffPrefix)
	goalIdx := indexOfSteering(hist, "Ship the feature")
	if noteIdx < 0 || goalIdx < 0 {
		t.Fatal("expected both note and goal steering turns in history")
	}
	if got := hist[noteIdx].SteeringKind; got != events.SteeringKindNoteHandoff {
		t.Errorf("note turn SteeringKind = %q, want %q", got, events.SteeringKindNoteHandoff)
	}
	if got := hist[goalIdx].SteeringKind; got != events.SteeringKindGoalObjective {
		t.Errorf("goal turn SteeringKind = %q, want %q", got, events.SteeringKindGoalObjective)
	}
}

// TestRunPreCompactHook_PluginModelContextKeepsPrecompactHookKind verifies
// the one source of the three that legitimately keeps the precompact-hook
// kind: a plugin PreCompact hook's ModelContext output.
func TestRunPreCompactHook_PluginModelContextKeepsPrecompactHookKind(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	hookClient := llm.NewClient()
	hookClient.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant(`{"hookSpecificOutput":{"additionalContext":"plugin context"}}`)}
		},
	}})
	runner := hooks.NewRunner(hookClient, "gpt-5.2")
	runner.Add(plugin.HookPreCompact, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "compact"})
	s.hookRunner = runner

	hist := makeSteeringSeed(2)
	records, _ := s.runPreCompactHook(context.Background(), &hist)

	if got := kindOfSteeringRecord(records, "plugin context"); got != events.SteeringKindPrecompactHook {
		t.Errorf("plugin ModelContext kind = %q, want %q", got, events.SteeringKindPrecompactHook)
	}
}

// TestFlushSteeringTurnRecords_EmitsPerRecordKind pins the exact mechanism
// review round 1 flagged: flushSteeringTurnRecords must emit each record's
// OWN kind, not one constant shared across the whole batch. Uses
// collectEvents (session_parity_test.go) rather than reading s.Events()
// directly since NewSession itself emits onto the channel (SESSION_START)
// before this test's own sends, so a fixed read-order assumption would pick
// up the wrong event.
func TestFlushSteeringTurnRecords_EmitsPerRecordKind(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	evs, mu, doneCh := collectEvents(s)

	hist := []schema.Turn{}
	records := appendSteeringMessagesToHistory(&hist, []preCompactMessage{
		{text: "hook context", kind: events.SteeringKindPrecompactHook},
		{text: "note handoff", kind: events.SteeringKindNoteHandoff},
		{text: "goal objective", kind: events.SteeringKindGoalObjective},
	})
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	s.flushSteeringTurnRecords(records)
	s.Close()
	<-doneCh

	mu.Lock()
	defer mu.Unlock()
	got := map[string]string{}
	for _, ev := range *evs {
		if data, ok := ev.Data.(events.SteeringInjectedData); ok {
			got[data.Text] = data.Kind
		}
	}
	want := map[string]string{
		"hook context":   events.SteeringKindPrecompactHook,
		"note handoff":   events.SteeringKindNoteHandoff,
		"goal objective": events.SteeringKindGoalObjective,
	}
	for text, wantKind := range want {
		if got[text] != wantKind {
			t.Errorf("emitted kind for %q = %q, want %q", text, got[text], wantKind)
		}
	}
}

// seedSessionHistory appends n ordinary TurnUserInput turns to s.history under
// s.mu. This gives the compaction layers enough history to exercise the
// checkpoint path (checkpoint preserves only the recent PreserveRecentTurns).
func seedSessionHistory(t *testing.T, s *Session, n int) {
	t.Helper()
	s.mu.Lock()
	for range n {
		s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User("turn")))
	}
	s.mu.Unlock()
}

// seedNumberedSessionHistory appends n distinctly-numbered TurnUserInput turns
// (text "turn 0".."turn N-1") to s.history under s.mu. Unlike
// seedSessionHistory's identical placeholder turns, the unique text lets a
// test locate one specific pre-fold turn's actual post-fold position by
// content — ground truth — instead of trusting a hand-derived index formula
// for it (issue #634's baseline math has a checkpoint/summarize interaction
// subtle enough that a formula is easy to get wrong).
func seedNumberedSessionHistory(t *testing.T, s *Session, n int) {
	t.Helper()
	s.mu.Lock()
	for i := range n {
		s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("turn %d", i))))
	}
	s.mu.Unlock()
}

// indexOfTurnText returns the index of the first turn whose message text
// equals want, or -1 if none matches.
func indexOfTurnText(history []schema.Turn, want string) int {
	for i, t := range history {
		if t.Message.Text() == want {
			return i
		}
	}
	return -1
}

// currentHistory returns a snapshot of s.history under s.mu.
func currentHistory(t *testing.T, s *Session) []schema.Turn {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.Turn{}, s.history...)
}

func TestApplyPendingForceCompact_CompactsWithNote(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	seedSessionHistory(t, s, 14) // >PreserveRecentTurns ordinary turns
	s.setPinnedNote("REMEMBER: API is Foo(ctx, id)")
	if err := s.requestForceCompact("drop the file dumps"); err != nil {
		t.Fatal(err)
	}

	s.applyPendingForceCompact(context.Background())

	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("force request should be consumed by applyPendingForceCompact")
	}
	h := currentHistory(t, s)
	if len(h) >= 14 {
		t.Fatalf("history not compacted: %d turns remain", len(h))
	}
	if countSteering(h, noteHandoffPrefix) != 1 || indexOfSteering(h, "REMEMBER: API is Foo(ctx, id)") < 0 {
		t.Fatal("pinned note not handed off exactly once after force compaction")
	}
	if s.PinnedNote() != "" {
		t.Fatalf("note must be cleared after the compaction consumed it, still have %q", s.PinnedNote())
	}
}

func TestApplyPendingForceCompact_NoRequest_NoOp(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	seedSessionHistory(t, s, 14)
	before := len(currentHistory(t, s))
	s.applyPendingForceCompact(context.Background()) // no pending request
	if len(currentHistory(t, s)) != before {
		t.Fatal("with no pending request, applyPendingForceCompact must be a no-op")
	}
}

// TestApplyPendingForceCompact_AdjustsTurnHistoryBaselineOnFold pins issue
// #798 (folded into #634): applyPendingForceCompact — the agent's own
// compact_context tool, run at every round tail (session_lifecycle.go) — runs
// the identical ForceCompact fold as the content-filter retry path
// (handleModelError's modelErrorContentFilterRetry, already fixed) but was
// never wired to shrinkTurnHistoryBaseline, so turnHistoryBaseline silently
// drifted right every time an agent self-compacted mid-turn via
// compact_context — the exact symptom issue #634 was filed to eliminate,
// reachable through a third path. Ground-truthed the same way as the
// content-filter and ManageContext pins: find the in-flight turn's actual
// post-fold position by content, not a hand-derived formula.
// testApplyPendingForceCompactAdjustsBaseline parameterizes
// TestApplyPendingForceCompact_AdjustsTurnHistoryBaselineOnFold with an
// injected-steering variant, mirroring testContentFilterRecoveryAdjustsBaseline
// and testManageContextShrinksBaselineOnFold: withGoalSteering activates a
// goal before the fold so goalCompactionSteering injects one turn through
// runPreCompactHook, verifying the injected count is correctly threaded
// through this entry point too, not just the two already covered.
func testApplyPendingForceCompactAdjustsBaseline(t *testing.T, withGoalSteering bool) {
	t.Helper()
	s := newTestSession(t)
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold
	if withGoalSteering {
		s.getOrCreateGoalStore().Set("Ship the feature", time.Now()) // goalCompactionSteering injects 1 turn, unconsumed, every fold
	}

	preHistory := currentHistory(t, s)
	baselineIdx := len(preHistory) - 3 // last 3 turns simulate the in-flight turn
	baselineText := preHistory[baselineIdx].Message.Text()
	s.mu.Lock()
	s.turnHistoryBaseline = baselineIdx
	s.mu.Unlock()

	if err := s.requestForceCompact("drop the file dumps"); err != nil {
		t.Fatal(err)
	}
	s.applyPendingForceCompact(context.Background())

	postHistory := currentHistory(t, s)
	if len(postHistory) >= len(preHistory) {
		t.Fatalf("test setup didn't force an actual fold: history len %d -> %d", len(preHistory), len(postHistory))
	}
	wantBaseline := indexOfTurnText(postHistory, baselineText)
	if wantBaseline < 0 {
		t.Fatalf("in-flight turn %q did not survive the fold — test setup invalid", baselineText)
	}

	s.mu.Lock()
	gotBaseline := s.turnHistoryBaseline
	s.mu.Unlock()

	if gotBaseline != wantBaseline {
		t.Errorf("turnHistoryBaseline = %d after a %d-turn fold (history %d -> %d), want %d (the in-flight turn's actual post-fold index)",
			gotBaseline, len(preHistory)-len(postHistory), len(preHistory), len(postHistory), wantBaseline)
	}
}

func TestApplyPendingForceCompact_AdjustsTurnHistoryBaselineOnFold(t *testing.T) {
	t.Parallel()
	testApplyPendingForceCompactAdjustsBaseline(t, false)
}

func TestApplyPendingForceCompact_AdjustsTurnHistoryBaselineOnFold_WithInjectedSteering(t *testing.T) {
	t.Parallel()
	testApplyPendingForceCompactAdjustsBaseline(t, true)
}

// TestApplyPendingForceCompact_PreservesConcurrentAppendDuringSlowFold pins
// merge-back for applyPendingForceCompact (the agent's own compact_context
// tool, run at every round tail): it snapshots s.history and folds UNLOCKED
// (ForceCompact's Layer 2 summarization can be slow), so republishing the
// snapshot unconditionally would silently drop a turn appended to s.history
// by another goroutine while the fold ran (e.g. a queued tool result), which
// sits past the snapshot's length — the same data-loss class Compact()
// guards against.
//
// No sleep-based synchronization: a scripted cheap-model adapter blocks on a
// channel inside Layer 2's LLM call, mirroring
// TestSessionCompact_PreservesConcurrentAppendDuringSlowFold's approach for
// this different entry point.
func TestApplyPendingForceCompact_PreservesConcurrentAppendDuringSlowFold(t *testing.T) {
	t.Parallel()
	const blockingProvider = "apfc-blocking-cheap"
	entered := make(chan struct{})
	proceed := make(chan struct{})
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(&agenttest.ScriptedAdapter{
		Provider: blockingProvider,
		Responder: func(req llm.Request) llm.Response {
			close(entered)
			<-proceed
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
		},
	})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), blockingProvider+"/model")

	s := newSession(t, withClient(client), withProfile(profile), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold, and ForceCompact always attempts Layer 2 given a real client

	if err := s.requestForceCompact("drop the file dumps"); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		s.applyPendingForceCompact(context.Background())
		close(done)
	}()

	<-entered // the fold is now blocked inside Layer 2's LLM call, past its unlocked snapshot

	const concurrentText = "concurrent append during self-compact fold"
	s.mu.Lock()
	s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(concurrentText)))
	s.mu.Unlock()

	close(proceed) // let the fold finish
	<-done

	if indexOfTurnText(currentHistory(t, s), concurrentText) < 0 {
		t.Fatal("turn appended to s.history while applyPendingForceCompact's fold ran unlocked did not survive publication")
	}
}

// TestSessionCompact_AdjustsTurnHistoryBaselineOnFold pins Session.Compact()
// (the /compact command) as a mid-turn publisher: the server exposes
// /compact while a thread is active, and askPendingCount()>0 — Compact's
// only guard — does not check whether a round loop is active, so a mid-turn
// Compact() is reachable the same way applyPendingForceCompact and the
// content-filter retry are. It runs the identical ForceCompact fold as those
// two and must apply shrinkTurnHistoryBaseline the same way; ground-truthed
// the same way as the other three entry points.
func TestSessionCompact_AdjustsTurnHistoryBaselineOnFold(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold

	preHistory := currentHistory(t, s)
	baselineIdx := len(preHistory) - 3 // last 3 turns simulate the in-flight turn (mid-turn Compact())
	baselineText := preHistory[baselineIdx].Message.Text()
	s.mu.Lock()
	s.turnHistoryBaseline = baselineIdx
	s.mu.Unlock()

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	postHistory := currentHistory(t, s)
	if len(postHistory) >= len(preHistory) {
		t.Fatalf("test setup didn't force an actual fold: history len %d -> %d", len(preHistory), len(postHistory))
	}
	wantBaseline := indexOfTurnText(postHistory, baselineText)
	if wantBaseline < 0 {
		t.Fatalf("in-flight turn %q did not survive the fold — test setup invalid", baselineText)
	}

	s.mu.Lock()
	gotBaseline := s.turnHistoryBaseline
	s.mu.Unlock()

	if gotBaseline != wantBaseline {
		t.Errorf("turnHistoryBaseline = %d after a %d-turn fold (history %d -> %d), want %d (the in-flight turn's actual post-fold index)",
			gotBaseline, len(preHistory)-len(postHistory), len(preHistory), len(postHistory), wantBaseline)
	}
}

// TestSessionCompact_PreservesConcurrentAppendDuringSlowFold pins merge-back
// for Compact(): it snapshots s.history and folds UNLOCKED (ForceCompact's
// Layer 2 summarization can be slow -- a real LLM call), so republishing the
// snapshot unconditionally would drop a turn appended to s.history by
// another goroutine while the fold is in flight (e.g. a concurrent tool
// result), which sits past the snapshot's length. The baseline wiring makes
// every ForceCompact/ManageContext caller load-bearing, so this is a real
// defect class, not a theoretical one.
//
// No sleep-based synchronization: a scripted cheap-model adapter blocks on a
// channel inside Layer 2's LLM call (a real seam ForceCompact always reaches
// given a real client and >PreserveRecentTurns history -- see
// seedNumberedSessionHistory's callers elsewhere in this file), so the test
// deterministically controls exactly when the concurrent append happens
// relative to the fold's unlocked window.
func TestSessionCompact_PreservesConcurrentAppendDuringSlowFold(t *testing.T) {
	t.Parallel()
	const blockingProvider = "compact-blocking-cheap"
	entered := make(chan struct{})
	proceed := make(chan struct{})
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(&agenttest.ScriptedAdapter{
		Provider: blockingProvider,
		Responder: func(req llm.Request) llm.Response {
			close(entered)
			<-proceed
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
		},
	})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), blockingProvider+"/model")

	s := newSession(t, withClient(client), withProfile(profile), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold, and ForceCompact always attempts Layer 2 given a real client

	compactErr := make(chan error, 1)
	go func() {
		compactErr <- s.Compact(context.Background())
	}()

	<-entered // the fold is now blocked inside Layer 2's LLM call, past its unlocked snapshot

	const concurrentText = "concurrent append during fold"
	s.mu.Lock()
	s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(concurrentText)))
	s.mu.Unlock()

	close(proceed) // let the fold finish

	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if indexOfTurnText(currentHistory(t, s), concurrentText) < 0 {
		t.Fatal("turn appended to s.history while Compact's fold ran unlocked did not survive publication")
	}
}

// TestSessionCompact_DoesNotClobberConcurrentCompaction pins conflict
// detection against a COMPETING fold, not just an ordinary append: a
// length-based merge that only checks whether s.history GREW past the
// snapshot length is insufficient, since a competing fold can leave s.history
// the same length (or shorter) while replacing its content entirely -- a
// stale snapshot would then unconditionally win, silently discarding the
// competing fold's work.
//
// Fold A snapshots and then blocks inside its Layer 2 LLM call. While
// blocked, a competing fold's publish is simulated directly via
// publishFoldedHistory -- the same primitive any real competing publisher
// (applyPendingForceCompact, the content-filter retry, or another Compact()
// call landing first) goes through, so this exercises exactly the conflict
// this fix protects against without needing a second concurrent LLM call.
// (A second real ForceCompact racing the same cheap-model route would itself
// serialize behind A's in-flight call via cheapmodel.Caller's single-flight
// probe -- a real, separate mechanism this test must not fight to stay
// focused on the history-publication race.) A is then unblocked, finds its
// publish conflicts with the competing one (s.historyRevision moved), and --
// via foldWithForceCompact's built-in retry -- re-folds against the
// now-current (competitor-published) history and publishes that instead. No
// sleep-based synchronization: the adapter blocks only until the test
// signals it.
func TestSessionCompact_DoesNotClobberConcurrentCompaction(t *testing.T) {
	t.Parallel()
	const blockingProvider = "compact-race-blocking-cheap"
	entered := make(chan struct{})
	proceed := make(chan struct{})
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(&agenttest.ScriptedAdapter{
		Provider: blockingProvider,
		Responder: func(req llm.Request) llm.Response {
			close(entered)
			<-proceed
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nA's stale first-attempt summary\n[END SUMMARY]")}
		},
	})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), blockingProvider+"/model")

	s := newSession(t, withClient(client), withProfile(profile), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold

	errA := make(chan error, 1)
	go func() {
		errA <- s.Compact(context.Background()) // fold A: blocks inside its Layer 2 call
	}()

	<-entered // A is now blocked inside its Layer 2 call, past its unlocked snapshot

	// Simulate a competing fold's publish landing first, directly through
	// publishFoldedHistory -- the same seam every real ForceCompact/
	// ManageContext caller now shares.
	const competingMarker = "competing fold's published summary"
	competingResult := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\n"+competingMarker+"\n[END SUMMARY]")),
	}
	s.mu.Lock()
	snapLen := len(s.history)
	snapRevision := s.historyRevision
	_, publishedCompeting := s.publishFoldedHistory(snapLen, snapRevision, competingResult)
	revisionAfterCompeting := s.historyRevision
	s.mu.Unlock()
	if !publishedCompeting {
		t.Fatal("test setup: the simulated competing publish itself unexpectedly conflicted")
	}

	close(proceed) // let A's blocked call return its (now-stale) result

	if err := <-errA; err != nil {
		t.Fatalf("Compact (A): %v", err)
	}

	final := currentHistory(t, s)
	foundCompeting := false
	for _, turn := range final {
		text := turn.Message.Text()
		if strings.Contains(text, "A's stale first-attempt summary") {
			t.Fatal("A's stale first-attempt summary reached s.history — it should have conflicted and retried instead of clobbering the competing publish")
		}
		if strings.Contains(text, competingMarker) {
			foundCompeting = true
		}
	}
	if !foundCompeting {
		t.Fatal("the competing fold's published content is gone — A's retry should have carried it forward (it's a single turn, well within PreserveRecentTurns, so A's retry fold cannot legitimately absorb it into a new checkpoint/summary either)")
	}

	s.mu.Lock()
	finalRevision := s.historyRevision
	s.mu.Unlock()
	if finalRevision < revisionAfterCompeting+1 {
		t.Fatalf("historyRevision = %d after the competing publish (%d) and A's retry; want at least %d — A's conflict should have produced a retried publish, not a silent no-op",
			finalRevision, revisionAfterCompeting, revisionAfterCompeting+1)
	}
}

// TestSessionCompact_DoesNotResurrectConcurrentlyRemovedAttentionTurn pins
// historyRevision's coverage of non-append mutations:
// removeUnverifiedDelegateAttentionTurn deleting a turn must bump it, because
// a fold snapshotted BEFORE such a removal still has the removed turn in its
// (stale) working copy -- with an unmoved revision, publishFoldedHistory's
// equality check would pass and the publish would resurrect the turn the
// removal deliberately discarded.
//
// Drives Compact() blocked mid-fold (past its unlocked snapshot, which still
// contains the turn), removes the turn via
// removeUnverifiedDelegateAttentionTurn while blocked (bumping
// historyRevision, per this round's fix), then unblocks: the fold must
// detect the conflict, retry against the now-current (turn already removed)
// history, and publish a result that does NOT resurrect it.
func TestSessionCompact_DoesNotResurrectConcurrentlyRemovedAttentionTurn(t *testing.T) {
	t.Parallel()
	const blockingProvider = "compact-attention-blocking-cheap"
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var callCount atomic.Int32
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(&agenttest.ScriptedAdapter{
		Provider: blockingProvider,
		Responder: func(req llm.Request) llm.Response {
			if callCount.Add(1) == 1 {
				close(entered)
				<-proceed
			}
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
		},
	})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), blockingProvider+"/model")

	s := newSession(t, withClient(client), withProfile(profile), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold; the removed turn below (appended last) survives the fold as a distinct, preserved turn

	unverified := schema.NewTurn(schema.TurnSteering, llm.User("unverified delegate attention turn"))
	unverified.AttentionID = "att-1"
	s.mu.Lock()
	s.history = append(s.history, unverified)
	s.mu.Unlock()

	errA := make(chan error, 1)
	go func() {
		errA <- s.Compact(context.Background()) // blocks inside its first Layer 2 call, past its unlocked snapshot
	}()

	<-entered

	s.removeUnverifiedDelegateAttentionTurn(unverified)

	// Ground truth: the removal itself is synchronous and unconditional --
	// verify it actually took effect before trusting the rest of the test.
	for _, turn := range currentHistory(t, s) {
		if turn.AttentionID == "att-1" {
			t.Fatal("test setup: removeUnverifiedDelegateAttentionTurn did not remove the turn")
		}
	}

	close(proceed) // let the blocked fold's first attempt finish (its retry, if any, answers immediately per callCount above)

	if err := <-errA; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	for _, turn := range currentHistory(t, s) {
		if turn.AttentionID == "att-1" {
			t.Fatal("the concurrently-removed attention turn was resurrected by a fold that snapshotted before the removal")
		}
	}
}

// TestFoldConflict_LosingAttemptDoesNotConsumeSideEffects pins side-effect
// staging: a fold attempt must not commit its side effects (pinned note
// consumption, transcript writes for its own steering turn) before the
// publish decision, or a losing attempt would clear the pinned note and write
// a transcript entry for a compaction that never took effect, and the retry
// that eventually wins would find the note already gone.
//
// Drives Compact() blocked mid-fold on its first attempt (which has already
// run runPreCompactHook, handing the note off into its own soon-to-be-
// discarded fold content) -- verifies the note and the transcript are
// untouched while that attempt is still in flight and hasn't published.
// Forces it to lose via a simulated competing publish, then unblocks:
// Compact()'s built-in retry re-folds against the now-current history and
// wins, and ONLY THEN must the note be consumed and the transcript entry
// written -- exactly once.
func TestFoldConflict_LosingAttemptDoesNotConsumeSideEffects(t *testing.T) {
	t.Parallel()
	const blockingProvider = "fold-side-effects-blocking-cheap"
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var callCount atomic.Int32
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(&agenttest.ScriptedAdapter{
		Provider: blockingProvider,
		Responder: func(req llm.Request) llm.Response {
			if callCount.Add(1) == 1 {
				close(entered)
				<-proceed
			}
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
		},
	})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), blockingProvider+"/model")

	s := newSession(t, withClient(client), withProfile(profile), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold for the first (losing) attempt

	s.setPinnedNote("REMEMBER: the API signature")

	var transcriptWrites atomic.Int32
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.appendCompactionTurn = func(schema.Turn) error {
			transcriptWrites.Add(1)
			return nil
		}
	})

	errA := make(chan error, 1)
	go func() {
		errA <- s.Compact(context.Background()) // blocks inside its first attempt's Layer 2 call, AFTER runPreCompactHook already handed the note off into that attempt's own (soon-to-be-discarded) fold content
	}()

	<-entered

	// Nothing has published yet -- this attempt's side effects must still be
	// staged, not committed.
	if s.PinnedNote() == "" {
		t.Fatal("the pinned note was cleared before any fold won publication -- a losing attempt's side effects must not commit eagerly")
	}
	if n := transcriptWrites.Load(); n != 0 {
		t.Fatalf("expected no transcript writes before any fold has published, got %d", n)
	}

	// Force the in-flight attempt to lose: a competing fold publishes first,
	// directly through publishFoldedHistory -- the same seam any real
	// competing publisher goes through.
	s.mu.Lock()
	snapLen := len(s.history)
	snapRevision := s.historyRevision
	_, competingOK := s.publishFoldedHistory(snapLen, snapRevision, []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("competing"))})
	s.mu.Unlock()
	if !competingOK {
		t.Fatal("test setup: competing publish itself conflicted")
	}

	close(proceed) // let the blocked (losing) attempt's LLM call return; Compact()'s built-in retry then re-folds against the competing publish's result and wins

	if err := <-errA; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// The winning retry -- not the losing first attempt -- must be what
	// consumed the note, exactly once: gone from s.PinnedNote(), handed off
	// exactly once into the published history, and written to the
	// transcript exactly once.
	if s.PinnedNote() != "" {
		t.Fatalf("note must be consumed by the winning retry, still have %q", s.PinnedNote())
	}
	if n := countSteering(currentHistory(t, s), noteHandoffPrefix); n != 1 {
		t.Fatalf("expected exactly one note handoff turn in the published history, got %d", n)
	}
	if n := transcriptWrites.Load(); n != 1 {
		t.Fatalf("expected exactly one transcript write (the winning retry's), got %d", n)
	}
}

// forcePressureAbove drives the context manager's reported pressure to at least
// frac by recording a large exact input-token count for the current history
// length. The compaction layers reset lastInputTokens to 0, so this must be
// re-applied after each compaction to re-arm pressure.
func forcePressureAbove(t *testing.T, s *Session, frac float64) {
	t.Helper()
	cw := s.contextMgr.EstimateUsage(nil, 0).Window
	if cw <= 0 {
		t.Fatalf("context window size is %d; cannot force pressure", cw)
	}
	s.mu.Lock()
	histLen := len(s.history)
	s.mu.Unlock()
	tokens := int(frac*float64(cw)) + 1
	s.contextMgr.RecordInputTokens(tokens, histLen)
	if got := s.contextMgr.Pressure(currentHistory(t, s), 0); got < frac {
		t.Fatalf("forcePressureAbove: pressure %.3f < target %.3f", got, frac)
	}
}

func TestNudge_FiresOnceUntilCompaction(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	forcePressureAbove(t, s, s.contextMgr.WarnThreshold)

	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire when pressure crosses WarnThreshold and latch is clear")
	}
	// The nudge must reach the model: it is queued as steering, which the round
	// loop drains into history before the next model call.
	if got := s.SteeringQueueSnapshot(); len(got) != 1 || !strings.Contains(got[0].Text, "compact_context") {
		t.Fatalf("nudge did not queue a steering message naming the compact_context tool: %+v", got)
	}
	if s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge must not re-fire until after a compaction")
	}

	// A compaction resets the latch.
	if err := s.requestForceCompact(""); err != nil {
		t.Fatal(err)
	}
	s.applyPendingForceCompact(context.Background())
	forcePressureAbove(t, s, s.contextMgr.WarnThreshold)
	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire again after a compaction reset the latch")
	}
}

// TestNudge_ResetsOnSessionCompact verifies Session.Compact (the idle/explicit
// path, distinct from applyPendingForceCompact) also clears the latch so the
// nudge can fire again afterward.
func TestNudge_ResetsOnSessionCompact(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	forcePressureAbove(t, s, s.contextMgr.WarnThreshold)
	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire on first crossing")
	}
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	forcePressureAbove(t, s, s.contextMgr.WarnThreshold)
	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire again after Session.Compact reset the latch")
	}
}

// TestNudge_ResetsOnAutomaticCompaction verifies that the nudge latch resets via
// the shared compactionEmitFunc path — the same emit site used by ALL compaction
// paths (auto ManageContext, content-filter recovery, and both force paths).
// Driving the shared emit site directly is the correct test: it proves the reset
// is wired to the mechanism that every path flows through, not just the force paths.
func TestNudge_ResetsOnAutomaticCompaction(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	// Arm the latch as if a nudge already fired.
	s.mu.Lock()
	s.nudgedSinceCompact = true
	s.mu.Unlock()

	// Invoke compactionEmitFunc and fire EventContextCompaction through it.
	// This is the shared emit site that all compaction paths (auto, content-filter,
	// and force) route through; the latch reset must live here.
	hist := makeSteeringSeed(2)
	_, emitFn, flush, _ := s.compactionEmitFunc(context.Background(), &hist)
	emitFn(events.EventContextCompaction, events.ContextCompactionData{Layer: "test"})
	flush()

	s.mu.Lock()
	stuck := s.nudgedSinceCompact
	s.mu.Unlock()
	if stuck {
		t.Fatal("nudge latch must reset when EventContextCompaction flows through compactionEmitFunc (auto-compaction path)")
	}
}

// TestNudge_SilentBelowThreshold verifies no nudge fires when pressure is below
// WarnThreshold and the latch stays clear.
func TestNudge_SilentBelowThreshold(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	if s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge must not fire below WarnThreshold")
	}
	s.mu.Lock()
	latched := s.nudgedSinceCompact
	s.mu.Unlock()
	if latched {
		t.Fatal("latch must stay clear when pressure is below threshold")
	}
}

// TestPinnedNote_MetaRoundTrip verifies that setPinnedNote is captured by Meta()
// and survives a JSON marshal/unmarshal round-trip (the wire format used by
// SaveSessionMeta/LoadSessionMeta).
func TestPinnedNote_MetaRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: resume me")
	meta := s.Meta()
	if meta.PinnedNote != "REMEMBER: resume me" {
		t.Fatalf("meta.PinnedNote = %q", meta.PinnedNote)
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var back schema.SessionMeta
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.PinnedNote != "REMEMBER: resume me" {
		t.Fatalf("json round-trip lost the note: %q", back.PinnedNote)
	}
}

// TestPinnedNote_SurvivesResume verifies that PinnedNote is restored when a
// session is reconstructed via RestoreSessionFromMeta. It uses the same
// RestoreSessionFromMeta helper that the real resume path uses, with a
// SessionMeta carrying PinnedNote set directly (mirroring how
// LoadSessionMeta would return it after SaveSessionMeta wrote it).
func TestPinnedNote_SurvivesResume(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	stateDir := t.TempDir()

	meta := schema.SessionMeta{
		ID:         "resume-pinned-note",
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		PinnedNote: "REMEMBER: resume me",
	}

	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := restored.PinnedNote(); got != "REMEMBER: resume me" {
		t.Fatalf("PinnedNote after resume = %q, want %q", got, "REMEMBER: resume me")
	}
}

func TestMaybeElicitNoteBeforeCompaction_FiresWhenEnabledAndHighPressure(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	// on by default (no flag needed)
	s.elicitNoteFn = func(ctx context.Context, h []schema.Turn) (string, error) { return "STUB elicited note", nil }
	seedSessionHistory(t, s, 10)
	forcePressureAbove(t, s, s.contextMgr.CheckpointThreshold)

	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	if got := s.PinnedNote(); got != "STUB elicited note" {
		t.Fatalf("expected elicited note pinned, got %q", got)
	}
}

func TestMaybeElicitNoteBeforeCompaction_NoopWhenLowPressure(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.elicitNoteFn = func(ctx context.Context, h []schema.Turn) (string, error) { return "SHOULD NOT FIRE", nil }
	seedSessionHistory(t, s, 10)

	// low pressure (below CheckpointThreshold) — no compaction imminent
	s.contextMgr.RecordInputTokens(1, len(currentHistory(t, s))) // ~0 pressure
	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	if s.PinnedNote() != "" {
		t.Fatal("low pressure: no compaction imminent, should not elicit")
	}
}

func TestMaybeElicitNoteBeforeCompaction_AttentionResolutionDoesNotCreateFoldableHistory(t *testing.T) {
	s := newTestSession(t)
	called := false
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		called = true
		return "should not be elicited", nil
	}
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("must stay")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent answer")),
	}
	for i := range 6 {
		marker := delegateAttentionResolutionTurn(fmt.Sprintf("private-%d", i), delegateAttentionConsumed)
		history = append(history, marker)
	}
	history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User("latest")))
	s.contextMgr.RecordInputTokens(s.contextMgr.EstimateUsage(nil, 0).Window, len(history))
	s.maybeElicitNoteBeforeCompaction(context.Background(), history, 0)
	if called {
		t.Fatal("private resolution markers created a lossy foldable prefix")
	}
}

// TestMaybeElicitNoteBeforeCompaction_SkipsWhenNoteAlreadySet proves the
// don't-overwrite rule: with a note already set (e.g. the agent's compact-tool
// note), high pressure must NOT trigger elicitation — the agent's note wins, and
// the side LLM call is skipped (the per-compaction latch).
func TestMaybeElicitNoteBeforeCompaction_SkipsWhenNoteAlreadySet(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	called := false
	s.elicitNoteFn = func(ctx context.Context, h []schema.Turn) (string, error) {
		called = true
		return "ELICITED — SHOULD NOT REPLACE", nil
	}
	s.setPinnedNote("AGENT'S OWN NOTE")
	seedSessionHistory(t, s, 10)
	forcePressureAbove(t, s, s.contextMgr.CheckpointThreshold)

	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	if called {
		t.Fatal("elicitor must not be called when a note is already set")
	}
	if got := s.PinnedNote(); got != "AGENT'S OWN NOTE" {
		t.Fatalf("agent note must be preserved, got %q", got)
	}
}

// muteNoteElicitation replaces the note elicitor with a no-op so tests that script
// exact model steps are not perturbed by the default-on elicitation call.
func muteNoteElicitation(s *Session) {
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
}
