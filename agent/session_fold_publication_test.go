package agent

// Tests pinning the fold publication transaction: every piece of coupled
// state -- orphaned-tool-result repair, the turnHistoryBaseline update, the
// pinned note claim, compaction transcript entries, and
// EventContextCompaction -- must commit inside, or in strict order with, the
// locked history-publication section, never through an unlocked window of its
// own.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
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

	// Seed fresh post-A history past PreserveRecentTurns before fold B runs,
	// so B's fold is genuinely competing regardless of how much of A's
	// result the summarizer preserved: B must fold real content and produce
	// its own summary, not short-circuit over a too-short history.
	s.mu.Lock()
	for i := range 8 {
		s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("fresh post-A turn %d", i))))
	}
	s.mu.Unlock()

	if err := s.Compact(context.Background()); err != nil { // fold B: folds A's result + the fresh turns, publishes second
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
	aMarker, bMarker := -1, -1
	for i, e := range data.Entries {
		if e.Turn.Kind != schema.TurnSummary {
			continue
		}
		if strings.Contains(e.Turn.Message.Text(), "fold A summary") {
			aMarker = i
		}
		if strings.Contains(e.Turn.Message.Text(), "fold B summary") {
			bMarker = i
		}
	}
	if aMarker < 0 || bMarker < 0 {
		t.Fatalf("both folds must produce summary markers (A entry index %d, B entry index %d) -- B's history was seeded past PreserveRecentTurns precisely so its fold is genuine", aMarker, bMarker)
	}
	if aMarker > bMarker {
		t.Fatalf("fold A's summary entry (index %d) landed after fold B's (index %d) despite A publishing first -- compaction markers out of publish order", aMarker, bMarker)
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

// TestFoldPublication_TurnRecordedDuringFoldSurvivesRestart pins merge-back
// durability: a turn recorded (history append + transcript entry) WHILE the
// fold runs lands its entry before the later compaction marker, and
// publishFoldedHistory merges it into live history past the fold result --
// since ResumeHistory anchors on the LAST marker, the publication transaction
// must leave merged-back turns durably represented after the fold's marker or
// they vanish on restart.
func TestFoldPublication_TurnRecordedDuringFoldSurvivesRestart(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "merge-back-restart-cheap", func(llm.Request) llm.Response {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}))
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual fold

	compactErr := make(chan error, 1)
	go func() {
		compactErr <- s.Compact(context.Background()) // blocks inside Layer 2, past its unlocked snapshot
	}()
	<-entered // the fold is mid-flight; nothing is locked

	const concurrentText = "turn recorded while the fold was running"
	turn := schema.NewTurn(schema.TurnUserInput, llm.User(concurrentText))
	s.recordTurn(turn, turn) // completes fully: history append + transcript entry, both before the fold publishes

	close(proceed)
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if indexOfTurnText(currentHistory(t, s), concurrentText) < 0 {
		t.Fatal("test setup: the merge-back did not carry the concurrently recorded turn into live history")
	}
	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	resumed := ResumeHistory(data.Entries)
	if indexOfTurnText(resumed, concurrentText) < 0 {
		t.Fatal("a turn recorded during the fold survives in live history but is missing from the resumed history -- merged-back turns must be durably represented after the compaction marker")
	}
	count := 0
	for _, rt := range resumed {
		if rt.Message.Text() == concurrentText {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the merged-back turn appears %d times in the resumed history, want exactly 1", count)
	}
}

// assertBaselineTracksMarkedTurn asserts turnHistoryBaseline points at the
// marked in-flight turn's ACTUAL position in the current history — the
// ground-truth-by-content convention this branch's baseline tests share.
func assertBaselineTracksMarkedTurn(t *testing.T, s *Session, markerText string) {
	t.Helper()
	want := indexOfTurnText(currentHistory(t, s), markerText)
	if want < 0 {
		t.Fatalf("marked in-flight turn %q missing from history — test setup invalid", markerText)
	}
	s.mu.Lock()
	got := s.turnHistoryBaseline
	s.mu.Unlock()
	if got != want {
		t.Fatalf("turnHistoryBaseline = %d, want %d (the marked in-flight turn's actual position) — the mid-history mutation shifted in-flight indexes without moving the N4 boundary", got, want)
	}
}

// TestHistoryRepair_InsertionBeforeBaselineShiftsBaseline pins the insertion
// half of boundary tracking: orphaned-tool-result repair splices a synthetic
// result turn at the orphaned call's position, and an insertion at or before
// the N4 boundary shifts every in-flight turn right by one -- the baseline
// must move with them, atomically with the mutation, in the same locked
// section (bumping historyRevision alone only protects concurrent folds).
func TestHistoryRepair_InsertionBeforeBaselineShiftsBaseline(t *testing.T) {
	t.Parallel()
	const marker = "in-flight marker turn"
	orphanTurn := func() schema.Turn {
		return schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "running a tool"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{}`), Type: "function"}},
		}})
	}

	t.Run("insertion strictly before the boundary", func(t *testing.T) {
		t.Parallel()
		s := newTestSession(t)
		s.mu.Lock()
		s.history = []schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.User("start")),
			orphanTurn(),
			schema.NewTurn(schema.TurnUserInput, llm.User(marker)),
			schema.NewTurn(schema.TurnUserInput, llm.User("in-flight tail")),
		}
		s.turnHistoryBaseline = 2 // the marked turn opens the in-flight region
		s.mu.Unlock()

		if repairs := s.repairOrphanedToolResults(context.Background(), "baseline shift test"); repairs != 1 {
			t.Fatalf("repairs = %d, want 1 — test setup invalid", repairs)
		}
		assertBaselineTracksMarkedTurn(t, s, marker)
	})

	t.Run("insertion exactly at the boundary", func(t *testing.T) {
		t.Parallel()
		// The orphaned call is the LAST pre-boundary turn, so its synthetic
		// result lands exactly at the boundary index. It completes
		// pre-boundary content, so the boundary must still move past it.
		s := newTestSession(t)
		s.mu.Lock()
		s.history = []schema.Turn{
			orphanTurn(),
			schema.NewTurn(schema.TurnUserInput, llm.User(marker)),
		}
		s.turnHistoryBaseline = 1
		s.mu.Unlock()

		if repairs := s.repairOrphanedToolResults(context.Background(), "baseline shift test"); repairs != 1 {
			t.Fatalf("repairs = %d, want 1 — test setup invalid", repairs)
		}
		assertBaselineTracksMarkedTurn(t, s, marker)
	})
}

// TestAttentionRemoval_DeletionBeforeBaselineShiftsBaseline pins the deletion
// half of boundary tracking: removing an unverified delegate-attention turn
// that sits before the N4 boundary shifts every in-flight turn left by
// one; the revision bump protects concurrent folds, but the baseline stayed
// put. The decrement must happen atomically with the deletion, in the same
// locked section — and only for deletions strictly before the boundary
// (deleting the first in-flight turn leaves the boundary correct).
func TestAttentionRemoval_DeletionBeforeBaselineShiftsBaseline(t *testing.T) {
	t.Parallel()
	const marker = "in-flight marker turn"
	attentionTurn := func() schema.Turn {
		turn := schema.NewTurn(schema.TurnSteering, llm.User("unverified delegate attention"))
		turn.AttentionID = "att-r6"
		return turn
	}

	t.Run("deletion strictly before the boundary", func(t *testing.T) {
		t.Parallel()
		s := newTestSession(t)
		unverified := attentionTurn()
		s.mu.Lock()
		s.history = []schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.User("start")),
			unverified,
			schema.NewTurn(schema.TurnUserInput, llm.User(marker)),
		}
		s.turnHistoryBaseline = 2
		s.mu.Unlock()

		s.removeUnverifiedDelegateAttentionTurn(unverified)
		assertBaselineTracksMarkedTurn(t, s, marker)
	})

	t.Run("deletion at the boundary leaves it alone", func(t *testing.T) {
		t.Parallel()
		s := newTestSession(t)
		unverified := attentionTurn()
		s.mu.Lock()
		s.history = []schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.User("start")),
			schema.NewTurn(schema.TurnUserInput, llm.User("pre-boundary tail")),
			unverified, // the first in-flight turn
		}
		s.turnHistoryBaseline = 2
		s.mu.Unlock()

		s.removeUnverifiedDelegateAttentionTurn(unverified)
		s.mu.Lock()
		got := s.turnHistoryBaseline
		s.mu.Unlock()
		if got != 2 {
			t.Fatalf("turnHistoryBaseline = %d after deleting the first in-flight turn, want 2 (unchanged: the boundary still opens the in-flight region)", got)
		}
	})
}

// toolResultContents collects the string contents of every tool-result part
// across turns, for content-level assertions on projected transcripts.
func toolResultContents(turns []schema.Turn) []string {
	var contents []string
	for _, t := range turns {
		for _, part := range t.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
				if str, ok := part.ToolResult.Content.(string); ok {
					contents = append(contents, str)
				}
			}
		}
	}
	return contents
}

// TestFoldPublication_MergedTailRewriteUsesPersistedForm pins the merged-tail
// rewrite's projection contract: tool results deliberately diverge —
// projectToolResultsForTranscript persists a bounded re-read placeholder in
// place of private API-log evidence, and durable delegate tool results
// carry DelegateDeliveryCommits only on the persisted form. Rewriting the
// live form would leak private evidence into the durable transcript and drop
// the delivery metadata, so the rewrite reuses the exact persisted
// counterpart the pair originally wrote.
func TestFoldPublication_MergedTailRewriteUsesPersistedForm(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "persisted-form-cheap", func(llm.Request) llm.Response {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}))
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual fold

	compactErr := make(chan error, 1)
	go func() {
		compactErr <- s.Compact(context.Background()) // blocks inside Layer 2, past its unlocked snapshot
	}()
	<-entered // the fold is mid-flight; nothing is locked

	const secret = "SECRET-API-LOG-EVIDENCE-r7"
	const placeholder = "PLACEHOLDER-RE-READ-HANDLE-r7"
	live := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind:       llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{ToolCallID: "c-r7", Name: "read_session_transcript", Content: secret},
	}}})
	persisted := live
	persisted.Message = llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind:       llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{ToolCallID: "c-r7", Name: "read_session_transcript", Content: placeholder},
	}}}
	persisted.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "c-r7", DeliveryID: "d-r7"}}
	s.recordTurn(live, persisted) // the divergent pair, recorded while the fold runs

	close(proceed)
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// The model-facing live history keeps the evidence.
	foundLiveSecret := false
	for _, c := range toolResultContents(currentHistory(t, s)) {
		if c == secret {
			foundLiveSecret = true
		}
	}
	if !foundLiveSecret {
		t.Fatal("test setup: the live history no longer carries the private evidence")
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	var entryTurns []schema.Turn
	for _, e := range data.Entries {
		entryTurns = append(entryTurns, e.Turn)
	}
	for _, c := range toolResultContents(entryTurns) {
		if c == secret {
			t.Fatal("private API-log evidence leaked into the durable transcript -- the merged-tail rewrite must persist the projected (placeholder) form, never the live form")
		}
	}

	resumed := ResumeHistory(data.Entries)
	var resumedMatches []schema.Turn
	for _, rt := range resumed {
		for _, c := range toolResultContents([]schema.Turn{rt}) {
			if c == placeholder {
				resumedMatches = append(resumedMatches, rt)
			}
		}
	}
	if len(resumedMatches) != 1 {
		t.Fatalf("projected tool result appears %d times in the resumed history, want exactly 1", len(resumedMatches))
	}
	if len(resumedMatches[0].DelegateDeliveryCommits) != 1 || resumedMatches[0].DelegateDeliveryCommits[0].DeliveryID != "d-r7" {
		t.Fatalf("the rewritten copy lost the persisted form's DelegateDeliveryCommits: %#v", resumedMatches[0].DelegateDeliveryCommits)
	}
}

// TestFoldPublication_AttentionTurnRemovedMidTransactionNotResurrected pins
// the rewrite against attention-turn resurrection:
// removeUnverifiedDelegateAttentionTurn (which takes only s.mu) can delete a
// merged-back attention turn between publish and the merged-tail rewrite, and
// a rewrite that persisted it after the marker would resurrect, on resume, a
// turn whose durability verification had FAILED. Attention turns
// enter live history without a session-transcript pair (their durability is
// owned by the attention transcript machinery and their restart path is the
// attention re-fold), so the rewrite must never manufacture
// session-transcript entries for them at all.
func TestFoldPublication_AttentionTurnRemovedMidTransactionNotResurrected(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "attn-resurrect-cheap", func(llm.Request) llm.Response {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}))
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual fold

	inCommit := make(chan struct{})
	proceedCommit := make(chan struct{})
	var commits atomic.Int32
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			if commits.Add(1) == 1 {
				close(inCommit)
				<-proceedCommit
			}
		}
	})

	compactErr := make(chan error, 1)
	go func() {
		compactErr <- s.Compact(context.Background())
	}()
	<-entered // mid-fold, past the snapshot

	const attnText = "unverified delegate attention r7"
	unverified := schema.NewTurn(schema.TurnSteering, llm.User(attnText))
	unverified.AttentionID = "att-r7"
	s.mu.Lock()
	s.history = append(s.history, unverified) // models retainDelegateAttentionTurn: history only, no session-transcript pair
	s.mu.Unlock()

	close(proceed)
	<-inCommit // published; the merged tail has not been written

	s.removeUnverifiedDelegateAttentionTurn(unverified) // durability verification failed: the turn must vanish

	close(proceedCommit)
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if indexOfTurnText(currentHistory(t, s), attnText) >= 0 {
		t.Fatal("test setup: the removed attention turn is still in live history")
	}
	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	for _, e := range data.Entries {
		if e.Turn.Message.Text() == attnText {
			t.Fatal("the removed (durability-unverified) attention turn was written to the session transcript by the merged-tail rewrite -- it resurrects on resume")
		}
	}
}

// TestFoldPublication_StaleFoldFlushDoesNotOverrideNewerFoldNaming pins
// deferred-effect ordering: fold A publishes and parks before its deferred
// flush; fold B publishes AND flushes (launching the compaction namer for B's
// newer summary); when A's stale flush runs it must not launch the namer for
// A's older summary -- compaction naming is last-write-wins, so deferred
// last-write-wins effects are superseded by publication order.
func TestFoldPublication_StaleFoldFlushDoesNotOverrideNewerFoldNaming(t *testing.T) {
	t.Parallel()
	var summarizeCalls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "stale-namer-cheap", func(llm.Request) llm.Response {
		if summarizeCalls.Add(1) == 1 {
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nfold A summary\n[END SUMMARY]")}
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nfold B summary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}))
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): both folds actually fold

	var nameMu sync.Mutex
	var namedTexts []string
	s.nameSessionFromTextFunc = func(_ context.Context, _, text string) error {
		nameMu.Lock()
		namedTexts = append(namedTexts, text)
		nameMu.Unlock()
		return nil
	}

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
	<-parked // A has published; its flush has not run

	if err := s.Compact(context.Background()); err != nil { // fold B: publishes AND flushes in that window
		t.Fatalf("Compact (B): %v", err)
	}

	close(release)
	if err := <-errA; err != nil {
		t.Fatalf("Compact (A): %v", err)
	}
	s.Close() // joins the namer goroutines

	// Substring matching: the summarizer wraps the scripted response in its
	// own envelope, so the summary-turn text embeds the marker rather than
	// equaling it. B's own checkpoint digest does not embed A's summary
	// text (verified empirically -- it digests user turns), so "fold A
	// summary" appearing in ANY named text means A's stale launch ran.
	nameMu.Lock()
	defer nameMu.Unlock()
	sawB := false
	for _, text := range namedTexts {
		if strings.Contains(text, "fold A summary") {
			t.Fatalf("fold A's stale flush launched the compaction namer for A's older summary after fold B had already flushed -- last-write-wins naming lets A overwrite B (named texts: %q)", namedTexts)
		}
		if strings.Contains(text, "fold B summary") {
			sawB = true
		}
	}
	if !sawB {
		t.Fatalf("fold B's summary never reached the namer; named texts: %q", namedTexts)
	}
}

// syncSnapshotFS models a crash for the fold publication's durability
// contract: only bytes an fsync has covered survive. Every successful Sync
// snapshots the transcript's contents, and the test restores from that
// snapshot instead of the live file. A write carrying a SUMMARY entry syncs
// immediately, standing in for the writer's timed sync landing on the
// compaction marker while the entries written right after it are still
// unsynced.
type syncSnapshotFS struct {
	afero.Fs
	mu     sync.Mutex
	synced []byte
}

func (fs *syncSnapshotFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &syncSnapshotFile{File: file, fs: fs, name: name}, nil
}

func (fs *syncSnapshotFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &syncSnapshotFile{File: file, fs: fs, name: name}, nil
}

// snapshot returns the bytes the last fsync covered — what a restart would
// find on disk.
func (fs *syncSnapshotFS) snapshot() []byte {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]byte(nil), fs.synced...)
}

type syncSnapshotFile struct {
	afero.File
	fs   *syncSnapshotFS
	name string
}

func (file *syncSnapshotFile) Write(p []byte) (int, error) {
	n, err := file.File.Write(p)
	if err == nil && bytes.Contains(p, []byte(`"SUMMARY"`)) {
		err = file.Sync()
	}
	return n, err
}

func (file *syncSnapshotFile) Sync() error {
	if err := file.File.Sync(); err != nil {
		return err
	}
	data, err := afero.ReadFile(file.fs.Fs, file.name)
	if err != nil {
		return err
	}
	file.fs.mu.Lock()
	file.fs.synced = data
	file.fs.mu.Unlock()
	return nil
}

// TestFoldPublication_DurablyRecordedTurnSurvivesRestartBeforeRewriteSync
// pins the durability of the merged-tail rewrite: a turn appended DURABLY
// while a fold runs has its only durable entry before the compaction marker,
// which ResumeHistory discards, so the post-marker rewrite is the entry a
// restart depends on — and it must be durable before the publication counts
// as persisted. A crash after the marker's fsync but before a later sync
// covers the rewrite must not lose a turn the session already promised was
// durable.
func TestFoldPublication_DurablyRecordedTurnSurvivesRestartBeforeRewriteSync(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "durable-rewrite-restart-cheap", func(llm.Request) llm.Response {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}))
	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatalf("close default transcript: %v", err)
	}
	crashFS := &syncSnapshotFS{Fs: afero.NewOsFs()}
	writer, err := transcript.NewWriterWithFS(crashFS, transcriptPath(s.stateDir, s.id), transcript.Header{SessionID: s.id})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	writer.SyncInterval = time.Hour // plain appends never sync on their own here; only durable writes and the marker do
	s.attachTranscript(writer)
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual fold

	compactErr := make(chan error, 1)
	go func() {
		compactErr <- s.Compact(context.Background()) // blocks inside Layer 2, past its unlocked snapshot
	}()
	<-entered // the fold is mid-flight; nothing is locked

	const durableText = "turn recorded durably while the fold was running"
	msg := llm.User(durableText)
	if err := s.appendTurnWithDurableTranscriptMessage(schema.TurnUserInput, msg, msg); err != nil {
		t.Fatalf("durable append: %v", err)
	}

	close(proceed)
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if indexOfTurnText(currentHistory(t, s), durableText) < 0 {
		t.Fatal("test setup: the merge-back did not carry the durably recorded turn into live history")
	}

	// Crash now: restore from the bytes the last fsync covered.
	restoredPath := filepath.Join(t.TempDir(), "restored.jsonl")
	if err := os.WriteFile(restoredPath, crashFS.snapshot(), 0o600); err != nil {
		t.Fatalf("write restored transcript: %v", err)
	}
	data, err := readTranscriptFull(restoredPath)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	markerDurable := false
	for _, e := range data.Entries {
		if e.Turn.Kind == schema.TurnSummary {
			markerDurable = true
		}
	}
	if !markerDurable {
		t.Fatal("test setup: the compaction marker did not reach the durable transcript")
	}
	if indexOfTurnText(ResumeHistory(data.Entries), durableText) < 0 {
		t.Fatal("a turn appended durably during the fold is missing after a restart from the fsynced transcript: the merged-tail rewrite after the compaction marker was not durable, and the pre-marker durable entry is the one ResumeHistory discards")
	}
}
