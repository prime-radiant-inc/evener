package agent

// Tests pinning the fold publication transaction: every piece of coupled
// state -- orphaned-tool-result repair, the turnHistoryBaseline update, the
// pinned note claim, compaction transcript entries, and
// EventContextCompaction -- must commit inside, or in strict order with, the
// locked history-publication section, never through an unlocked window of its
// own.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// seedOrphanedToolCallHistory replaces s.history with a short history whose
// final assistant turn carries a tool call with no recorded result, so the
// next prepareModelRequestWithError must run orphaned-tool-result repair.
func seedOrphanedToolCallHistory(t *testing.T, s *Session) {
	t.Helper()
	orphan := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "running a tool"},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{}`), Type: "function"}},
	}}
	s.mu.Lock()
	s.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("do it")),
		schema.NewTurn(schema.TurnAssistant, orphan),
		schema.NewTurn(schema.TurnUserInput, llm.User("never mind")),
	}
	s.mu.Unlock()
}

// TestPrepareModelRequest_OrphanRepairPreservesConcurrentAppend pins the
// append half of the repair contract: a snapshot/repair/replace split across
// two lock acquisitions would drop a turn appended between them, so the
// capture, repair, and publish must be one atomic critical section.
func TestPrepareModelRequest_OrphanRepairPreservesConcurrentAppend(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	seedOrphanedToolCallHistory(t, s)

	entered := make(chan struct{})
	proceed := make(chan struct{})
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeHistoryRepairPublish = func() {
			close(entered)
			<-proceed
		}
	})

	var rt events.RoundTimings
	prepErr := make(chan error, 1)
	go func() {
		_, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &rt)
		prepErr <- err
	}()

	<-entered // the repair is committed to publishing, in its racy window

	const concurrentText = "concurrent append during orphan repair"
	s.mu.Lock()
	s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(concurrentText)))
	s.mu.Unlock()

	close(proceed)

	if err := <-prepErr; err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	if indexOfTurnText(currentHistory(t, s), concurrentText) < 0 {
		t.Fatal("turn appended to s.history during the orphan-repair window was dropped by the repair's publish")
	}
}

// TestPrepareModelRequest_OrphanRepairDoesNotClobberConcurrentPublish pins
// the fold half of the repair contract: a fold publishing between the
// repair's snapshot and its replace must not be overwritten by a stale
// repaired copy (resurrecting folded-away history), so the repair re-reads
// and repairs the CURRENT history inside one critical section.
func TestPrepareModelRequest_OrphanRepairDoesNotClobberConcurrentPublish(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	seedOrphanedToolCallHistory(t, s)

	entered := make(chan struct{})
	proceed := make(chan struct{})
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeHistoryRepairPublish = func() {
			close(entered)
			<-proceed
		}
	})

	var rt events.RoundTimings
	prepErr := make(chan error, 1)
	go func() {
		_, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &rt)
		prepErr <- err
	}()

	<-entered // the repair is committed to publishing, in its racy window

	// A competing fold publishes in exactly that window, through the same
	// publishFoldedHistory seam every real publisher shares.
	const competingMarker = "competing fold's published summary"
	competingResult := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\n"+competingMarker+"\n[END SUMMARY]")),
	}
	s.mu.Lock()
	snapLen := len(s.history)
	snapRevision := s.historyRevision
	_, publishedCompeting := s.publishFoldedHistory(snapLen, snapRevision, competingResult)
	s.mu.Unlock()
	if !publishedCompeting {
		t.Fatal("test setup: the simulated competing publish itself unexpectedly conflicted")
	}

	close(proceed)

	if err := <-prepErr; err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	if indexOfTurnText(currentHistory(t, s), "[CONTEXT SUMMARY]\n"+competingMarker+"\n[END SUMMARY]") < 0 {
		t.Fatal("the competing fold's published content is gone -- the stale orphan repair clobbered it instead of repairing the current history")
	}
}

// countContextCompactionEvents counts the EventContextCompaction payloads in
// a collectEvents capture.
func countContextCompactionEvents(evs *[]events.SessionEvent, mu *sync.Mutex) int {
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, ev := range *evs {
		if _, ok := ev.Data.(events.ContextCompactionData); ok {
			n++
		}
	}
	return n
}

// TestPrepareModelRequest_LosingFoldAttemptEmitsNoCompactionEvent pins the
// staging of EventContextCompaction: a fold attempt that LOSES the publish
// race must not have told any event consumer (or the metrics built on them)
// that a compaction happened, so compaction events are buffered with the rest
// of the staged side effects and emitted only once their fold wins
// publication.
//
// The losing attempt is produced deterministically: the elicit-note seam runs
// on the fold path between the attempt's atomic snapshot and its publish, so
// a competing publish issued from inside it is guaranteed to conflict that
// attempt. The retry then folds the competitor's tiny result -- pressure was
// reset by the first attempt's own layers, so the retry folds nothing and
// emits nothing, leaving zero legitimate compaction events.
func TestPrepareModelRequest_LosingFoldAttemptEmitsNoCompactionEvent(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	evs, evMu, doneCh := collectEvents(s)
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold
	var competingOnce sync.Once
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		competingOnce.Do(func() {
			competingResult := []schema.Turn{
				schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\ncompeting\n[END SUMMARY]")),
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.publishFoldedHistory(len(s.history), s.historyRevision, competingResult); !ok {
				t.Error("test setup: the simulated competing publish itself unexpectedly conflicted")
			}
		})
		return "", nil
	}
	forcePressureAbove(t, s, 0.85) // > CheckpointThreshold(0.80): the fold and the elicit seam both fire

	var rt events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 1, &rt); err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	s.Close()
	<-doneCh
	if n := countContextCompactionEvents(evs, evMu); n != 0 {
		t.Fatalf("got %d EventContextCompaction event(s); the only fold attempt that compacted anything lost the publish race, so none of its events may be emitted", n)
	}
}

// TestPrepareModelRequest_WinningFoldEmitsCompactionEvent is the suppression
// guard for the buffering above: a fold that WINS publication must still emit
// its EventContextCompaction (exactly once for a checkpoint-only fold).
func TestPrepareModelRequest_WinningFoldEmitsCompactionEvent(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	evs, evMu, doneCh := collectEvents(s)
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	forcePressureAbove(t, s, 0.85) // > CheckpointThreshold(0.80), < SummarizeThreshold: exactly one layer emits

	var rt events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 1, &rt); err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	s.Close()
	<-doneCh
	if n := countContextCompactionEvents(evs, evMu); n != 1 {
		t.Fatalf("got %d EventContextCompaction event(s) for one published checkpoint fold, want exactly 1", n)
	}
}

// newScriptedSummaryCompactSession builds a session whose cheap-model route
// answers summarize calls through responder -- the established fixture for
// driving real ForceCompact folds (Layer 2 included) deterministically.
func newScriptedSummaryCompactSession(t *testing.T, provider string, responder func(llm.Request) llm.Response, opts ...sessionOpt) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(&agenttest.ScriptedAdapter{Provider: provider, Responder: responder})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), provider+"/model")
	return newSession(t, append([]sessionOpt{withClient(client), withProfile(profile), withoutGitSnapshot()}, opts...)...)
}

// TestFoldPublication_ClaimedNoteNotReinjectedByConcurrentFold pins the
// double-inject half of the note claim: the pinned note must be claimed
// atomically at publication, not at flush -- a note left visible between a
// fold's publish and its deferred flush would be read and re-injected by a
// second fold starting in that window.
func TestFoldPublication_ClaimedNoteNotReinjectedByConcurrentFold(t *testing.T) {
	t.Parallel()
	s := newScriptedSummaryCompactSession(t, "note-reinject-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	})
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual fold, note handoff lands in the preserved tail

	s.setPinnedNote("REMEMBER: the API signature")

	parked := make(chan struct{})
	release := make(chan struct{})
	var flushes atomic.Int32
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldSideEffectsFlush = func() {
			if flushes.Add(1) == 1 {
				close(parked)
				<-release
			}
		}
	})

	errA := make(chan error, 1)
	go func() {
		errA <- s.Compact(context.Background()) // fold A: publishes, then parks before its deferred flush
	}()

	<-parked // fold A has published; its deferred side effects have not run

	if err := s.Compact(context.Background()); err != nil { // fold B: full fold in exactly that window
		t.Fatalf("Compact (B): %v", err)
	}

	close(release)
	if err := <-errA; err != nil {
		t.Fatalf("Compact (A): %v", err)
	}

	if n := countSteering(currentHistory(t, s), noteHandoffPrefix); n != 1 {
		t.Fatalf("note handoff appears %d time(s) in history; a note already handed off by a published fold must not be re-injected by a concurrent fold", n)
	}
	if got := s.PinnedNote(); got != "" {
		t.Fatalf("pinned note not consumed after both folds: %q", got)
	}
}

// TestFoldPublication_NoteRepinnedMidFoldSurvivesPublication pins the erasure
// half of the note claim: the claim clears the note only if it is still the
// exact note (generation) the fold captured, so a NEWER note pinned while the
// fold is in flight is never erased unhanded.
func TestFoldPublication_NoteRepinnedMidFoldSurvivesPublication(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "note-repin-cheap", func(llm.Request) llm.Response {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	})
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual fold

	const oldNote = "N1: keep the flag names"
	const newNote = "N2: the newer note"
	s.setPinnedNote(oldNote)

	errA := make(chan error, 1)
	go func() {
		errA <- s.Compact(context.Background()) // captures N1 into its handoff, then blocks inside Layer 2
	}()

	<-entered // the hook has read N1; the fold is mid-flight

	s.setPinnedNote(newNote) // a newer note lands while the fold is still in flight

	close(proceed)
	if err := <-errA; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if got := s.PinnedNote(); got != newNote {
		t.Fatalf("pinned note = %q after the fold published; the fold captured %q and must not erase the newer note", got, oldNote)
	}
	if n := countSteering(currentHistory(t, s), oldNote); n != 1 {
		t.Fatalf("captured note %q handed off %d time(s), want exactly 1", oldNote, n)
	}
}

// TestPrepareModelRequest_BaselineAppliedAtomicallyWithPublication pins
// publishFoldedHistory's contract in the inline path ("callers must hold s.mu
// across this call and, on success, whatever baseline correction follows"):
// the baseline init/correction must share the publish's critical section, or
// a competing fold's atomic publish+shrink pair landing between them is
// overwritten -- at round 0 a late absolute SET would stamp the stale
// pre-competitor length over the competitor's corrected boundary, leaving the
// N4 boundary past the end of the live history.
//
// The inline path parks at the post-publication seam; the competitor's
// publish+shrink pair (the exact atomic pair foldWithForceCompact performs)
// lands there; on release the inline path must NOT touch the baseline again
// -- it was already applied inside its own publication transaction, and the
// competitor's shrink then translated it.
func TestPrepareModelRequest_BaselineAppliedAtomicallyWithPublication(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	forcePressureAbove(t, s, 0.85) // > CheckpointThreshold(0.80): the round-0 fold actually folds

	parked := make(chan struct{})
	release := make(chan struct{})
	var flushes atomic.Int32
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldSideEffectsFlush = func() {
			if flushes.Add(1) == 1 {
				close(parked)
				<-release
			}
		}
	})

	var rt events.RoundTimings
	prepErr := make(chan error, 1)
	go func() {
		_, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &rt)
		prepErr <- err
	}()

	<-parked // the inline fold has published; its deferred side effects have not run

	// A competing fold's atomic publish+shrink pair lands in that window --
	// the same pair foldWithForceCompact commits under one s.mu hold.
	competingResult := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\ncompetitor folded everything\n[END SUMMARY]")),
	}
	s.mu.Lock()
	snapLen := len(s.history)
	snapRevision := s.historyRevision
	_, publishedCompeting := s.publishFoldedHistory(snapLen, snapRevision, competingResult)
	if publishedCompeting {
		s.shrinkTurnHistoryBaseline(snapLen, len(competingResult), 0)
	}
	s.mu.Unlock()
	if !publishedCompeting {
		t.Fatal("test setup: the simulated competing publish itself unexpectedly conflicted")
	}

	close(release)
	if err := <-prepErr; err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	live := currentHistory(t, s)
	s.mu.Lock()
	gotBaseline := s.turnHistoryBaseline
	s.mu.Unlock()
	// Round 0's baseline points at the first turn of the in-flight turn --
	// nothing has been appended for it yet, so after the competitor's fold
	// (and its shrink translating the boundary) it must sit exactly at the
	// end of the live history, never past it.
	if gotBaseline != len(live) {
		t.Fatalf("turnHistoryBaseline = %d with live history of %d turns; the inline path's late baseline write clobbered the competing fold's atomically-corrected boundary", gotBaseline, len(live))
	}
}

// TestFoldPublication_CompetingFoldsCommitTranscriptEntriesInPublishOrder
// pins the competing-folds half of the transcript transaction: compaction
// markers must land in publish order. If fold B could publish and commit its
// entries between fold A's publish and A's entries, A's marker would land
// after B's and ResumeHistory (anchored on the LAST marker) would resume A's
// stale summary -- so publication and transcript commit form one transaction.
func TestFoldPublication_CompetingFoldsCommitTranscriptEntriesInPublishOrder(t *testing.T) {
	t.Parallel()
	var summarizeCalls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "fold-order-cheap", func(llm.Request) llm.Response {
		if summarizeCalls.Add(1) == 1 {
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nfold A summary\n[END SUMMARY]")}
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nfold B summary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}))
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): both folds actually fold

	parked := make(chan struct{})
	release := make(chan struct{})
	var flushes atomic.Int32
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldSideEffectsFlush = func() {
			if flushes.Add(1) == 1 {
				close(parked)
				<-release
			}
		}
	})

	errA := make(chan error, 1)
	go func() {
		errA <- s.Compact(context.Background()) // fold A: publishes first
	}()
	<-parked // fold A has published

	if err := s.Compact(context.Background()); err != nil { // fold B: folds A's result, publishes second
		t.Fatalf("Compact (B): %v", err)
	}

	close(release)
	if err := <-errA; err != nil {
		t.Fatalf("Compact (A): %v", err)
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	resumed := ResumeHistory(data.Entries)
	if len(resumed) == 0 {
		t.Fatal("resumed history is empty")
	}
	if got := resumed[0].Message.Text(); !strings.Contains(got, "fold B summary") {
		t.Fatalf("resume anchors on %q; fold B published last, so its summary must be the resume anchor -- fold A's transcript entries were committed out of publish order", got)
	}
}

// TestFoldPublication_ConcurrentAppendTranscriptEntryLandsAfterCompactionMarker
// pins the append half of the transcript transaction: a turn recorded
// (history append + transcript write, recordTurn's exact sequence) after the
// fold published must sequence its entry AFTER the compaction marker, since
// ResumeHistory anchors on the last marker and would otherwise discard it on
// restart.
func TestFoldPublication_ConcurrentAppendTranscriptEntryLandsAfterCompactionMarker(t *testing.T) {
	t.Parallel()
	s := newScriptedSummaryCompactSession(t, "append-order-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}))
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual fold

	inCommit := make(chan struct{})
	proceed := make(chan struct{})
	var commits atomic.Int32
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			if commits.Add(1) == 1 {
				close(inCommit)
				<-proceed
			}
		}
	})

	compactErr := make(chan error, 1)
	go func() {
		compactErr <- s.Compact(context.Background())
	}()
	<-inCommit // published; the fold's own transcript entries are not yet committed

	const concurrentText = "turn recorded after the fold published"
	turn := schema.NewTurn(schema.TurnUserInput, llm.User(concurrentText))
	// The recording goroutine's transcript write queues behind the
	// publication transaction's attentionMu hold, so the seam is released
	// before joining it — the lock, not goroutine scheduling, is what forces
	// its entry after the fold's markers. (The pre-fix red form of this test
	// completed the recording synchronously inside the window; the fix makes
	// that interleave impossible, which is the point.)
	recorded := make(chan struct{})
	go func() {
		s.recordTurn(turn, turn)
		close(recorded)
	}()

	close(proceed)
	<-recorded
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if indexOfTurnText(currentHistory(t, s), concurrentText) < 0 {
		t.Fatal("test setup: the concurrently recorded turn is not in live history")
	}
	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	if indexOfTurnText(ResumeHistory(data.Entries), concurrentText) < 0 {
		t.Fatal("a turn recorded after the fold published is missing from the resumed history -- its transcript entry was sequenced before the compaction marker")
	}
}
