package agent

// Tests that reach the compaction replay tail through a real model round
// rather than the compaction seam directly. session_fold_publication_test.go
// pins the write ordering by calling Compact's ForceCompact path; these pin
// that the production per-request path (session_model_call.go's ManageContext
// fold, whose compaction turns fill pendingCompactionTurns only when a layer
// actually folded) reaches publishFoldTransaction with the same inputs and
// produces the same transcript run: the compaction marker, its publication
// tag, and — only for a fold that landed a marker — the replay copies of the
// turns recorded while the fold was in flight, which ResumeHistory anchors on.

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// countTurnText returns how many turns in turns have exactly want as their
// message text.
func countTurnText(turns []schema.Turn, want string) int {
	n := 0
	for _, t := range turns {
		if t.Message.Text() == want {
			n++
		}
	}
	return n
}

// turnTexts returns each turn's message text, in order, for an equality check
// between a resumed history and the live session's history.
func turnTexts(turns []schema.Turn) []string {
	out := make([]string, 0, len(turns))
	for _, t := range turns {
		out = append(out, t.Message.Text())
	}
	return out
}

// forceRealRoundSummaryFold arms a real model request to run its ManageContext
// summarize layer and park on the cheap-model call, so a test can record a
// turn while the fold is in flight. The deterministic checkpoint layer is
// disabled, and no pre-fold turns are kept, so the fold's own marker + replay
// copies are the whole shape the transcript is expected to have. pressureAt
// must clear summarizeThreshold but stay under the model's max_input budget:
// a fold that fails to compact leaves the history (and its token estimate) at
// the forced pressure, and the request must still dispatch.
func forceRealRoundSummaryFold(t *testing.T, s *Session, summarizeThreshold, pressureAt float64) {
	t.Helper()
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual fold
	s.contextMgr.CheckpointThreshold = 2.0
	s.contextMgr.SummarizeThreshold = summarizeThreshold
	forcePressureAbove(t, s, pressureAt)
	s.contextMgr.PreserveRecentTurns = 0
}

// TestCompactionReplay_RealRoundWritesReplayTailAndResumes drives the fold
// through a real model request (prepareModelRequestWithError) rather than the
// ForceCompact seam: forced pressure makes the round's ManageContext summarize
// layer fold through its cheap model. A turn recorded while that fold is in
// flight is the pair the publication carries past the marker as a replay copy —
// the marked copy is the only record ResumeHistory can reach, because the
// original sits before the marker it anchors on. A second request follows, so
// the assertions hold on the folded history the live session actually
// continues from.
func TestCompactionReplay_RealRoundWritesReplayTailAndResumes(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "real-round-replay-cheap", func(llm.Request) llm.Response {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: t.TempDir()}))
	forceRealRoundSummaryFold(t, s, 0.5, 0.85)

	roundErr := make(chan error, 1)
	go func() {
		var rt events.RoundTimings
		_, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 1, &rt)
		roundErr <- err
	}()
	<-entered // the fold is mid-flight; nothing is locked

	// A real turn recorded through the production pair path while the fold
	// runs: its entry lands before the compaction marker, so the replay copy
	// the publication writes after the marker is what a restart resumes.
	const duringFold = "recorded while the real round's fold was in flight"
	turn := schema.NewTurn(schema.TurnUserInput, llm.User(duringFold))
	s.recordTurn(turn, turn)

	close(proceed)
	if err := <-roundErr; err != nil {
		t.Fatalf("prepareModelRequestWithError (fold round): %v", err)
	}

	// The next request continues from the folded history; nothing here should
	// fold again, so the transcript run the first round wrote is what it is.
	var rt2 events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 2, &rt2); err != nil {
		t.Fatalf("prepareModelRequestWithError (next round): %v", err)
	}

	live := currentHistory(t, s)
	if got := countTurnText(live, duringFold); got != 1 {
		t.Fatalf("the merged-back turn appears %d times in live history, want exactly 1", got)
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}

	// The marker: the fold landed a checkpoint/summary anchor ResumeHistory
	// reads, and it carries the fold's typed publication tag.
	markerIndex := -1
	for i, e := range data.Entries {
		if e.Turn.Kind != schema.TurnCheckpoint && e.Turn.Kind != schema.TurnSummary {
			continue
		}
		markerIndex = i
		state := e.Turn.SkillState
		if state == nil || state.Compaction == nil || state.Compaction.Operation.PublicationID == "" {
			t.Fatalf("compaction marker at %d carries no publication tag: %#v", i, state)
		}
	}
	if markerIndex < 0 {
		t.Fatal("the real-round fold wrote no compaction marker to the transcript")
	}

	// The copy: the pair recorded during the fold is re-appended after the
	// marker, tagged as a copy with its original entry ordinal.
	copyIndex := -1
	for i, e := range data.Entries {
		if e.Turn.Message.Text() == duringFold && e.Turn.OriginalOrdinal != nil {
			copyIndex = i
		}
	}
	if copyIndex < 0 {
		t.Fatal("the pair recorded during the real-round fold has no replay copy in the transcript: the merged-tail rewrite did not reach the production path")
	}
	if copyIndex <= markerIndex {
		t.Fatalf("the replay copy at %d sits at or before the marker at %d; ResumeHistory's last-marker anchor would discard it", copyIndex, markerIndex)
	}

	// Resume: the last-marker anchor keeps the copy, not the pre-marker
	// original, so the fold's turns survive and rebuild the live history.
	resumed := ResumeHistory(data.Entries)
	if got := countTurnText(resumed, duringFold); got != 1 {
		t.Fatalf("resumed history holds the during-fold turn %d times, want exactly 1 (the post-marker copy)", got)
	}
	for _, turn := range resumed {
		if turn.Message.Text() == duringFold && turn.OriginalOrdinal == nil {
			t.Fatalf("resumed history kept the un-tagged turn for %q; it should resume the post-marker copy", duringFold)
		}
	}
	if got, want := turnTexts(resumed), turnTexts(live); !slices.Equal(got, want) {
		t.Fatalf("ResumeHistory rebuilt a different history than the live session had:\n resumed=%v\n live=%v", got, want)
	}
}

// TestCompactionReplay_RealRoundMarkerLessFoldWritesNoReplayTail is the other
// half of the same rule, reached through a real model request: the per-request
// fold publishes whenever a strategy is configured, even when no layer folded
// anything. A fold that landed no marker moved no anchor, so the pairs recorded
// while it ran are already on the surviving side of ResumeHistory and a replay
// copy is a pure duplicate. This drives a real round whose summarize layer
// fails (so no marker lands) with a turn recorded mid-fold and asserts the
// transcript holds the turn exactly once, unchanged.
func TestCompactionReplay_RealRoundMarkerLessFoldWritesNoReplayTail(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	cheap := &agenttest.ScriptedAdapter{
		Provider: "real-round-nomarker-cheap",
		FaultResponder: func(llm.Request) error {
			if calls.Add(1) == 1 {
				close(entered)
				<-proceed
				return errors.New("summarizer unavailable")
			}
			return nil
		},
		Responder: func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nsummary\n[END SUMMARY]")}
		},
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(cheap)
	stateDir := t.TempDir()
	s := newSession(t,
		withClient(client),
		withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "real-round-nomarker-cheap/model")),
		withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: stateDir}),
		withoutGitSnapshot(),
	)
	forceRealRoundSummaryFold(t, s, 0.25, 0.3)

	roundErr := make(chan error, 1)
	go func() {
		var rt events.RoundTimings
		_, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 1, &rt)
		roundErr <- err
	}()
	<-entered // the fold is mid-flight; nothing is locked

	const duringFold = "recorded while the real round's marker-less fold was in flight"
	turn := schema.NewTurn(schema.TurnUserInput, llm.User(duringFold))
	s.recordTurn(turn, turn)

	close(proceed)
	if err := <-roundErr; err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	markers, copies, originals := 0, 0, 0
	for _, e := range data.Entries {
		switch {
		case e.Turn.Kind == schema.TurnCheckpoint || e.Turn.Kind == schema.TurnSummary:
			markers++
		case e.Turn.OriginalOrdinal != nil:
			copies++
		case e.Turn.Message.Text() == duringFold:
			originals++
		}
	}
	if markers != 0 {
		t.Fatalf("test setup: the fold landed %d replacement markers, so this is not the marker-less path", markers)
	}
	if originals != 1 {
		t.Fatalf("the durable pair reached the transcript %d times, want once", originals)
	}
	if copies != 0 {
		t.Fatalf("a marker-less real-round fold wrote %d replay copies; nothing discards the originals, so each is a duplicate", copies)
	}
}
