package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The human partner's hold, given early enough that every compaction folds it.
const partnerHold = "don't publish until I say so"

// A summary that keeps nothing the conversation said.
const emptySummaryReply = "## Progress\nDone"

// partnerHoldHistory is a conversation whose first message is a hold, followed
// by enough turns that the two preserved recent turns never include it.
func partnerHoldHistory() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(partnerHold)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("understood, holding the release")),
		schema.NewTurn(schema.TurnUserInput, llm.User("keep working on the docs")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("drafting")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
}

// moreTurns is what the conversation adds before a second compaction.
func moreTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("next section")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("writing it")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent3")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent4")),
	}
}

// partnerTestManager returns a manager whose every model call answers reply,
// preserving two recent turns, with a persistent session's transcript meta.
func partnerTestManager(reply string) *Manager {
	answer := func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(reply)} }
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{answer, answer, answer, answer}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = CompactionMeta{SessionID: "SESSION_ID_SENTINEL", AvailableTranscriptTools: []string{"read_transcript"}}
	return cm
}

// requirePartnerHoldInContext fails unless the hold is still in front of the
// model after a compaction that folded it, and the history now starts with a
// compaction turn of kind.
func requirePartnerHoldInContext(t *testing.T, history []schema.Turn, kind schema.TurnKind) {
	t.Helper()
	if len(history) == 0 || history[0].Kind != kind {
		t.Fatalf("history does not start with a %s turn: %v", kind, compactionKinds(history))
	}
	for _, turn := range history[1:] {
		if strings.Contains(turn.Message.Text(), partnerHold) {
			t.Fatalf("the hold is in a preserved recent turn, so this compaction did not fold it")
		}
	}
	if !strings.Contains(history[0].Message.Text(), partnerHold) {
		t.Fatalf("the hold is gone from the model's context after compaction:\n%s", history[0].Message.Text())
	}
}

func compactionKinds(history []schema.Turn) []schema.TurnKind {
	kinds := make([]schema.TurnKind, 0, len(history))
	for _, turn := range history {
		kinds = append(kinds, turn.Kind)
	}
	return kinds
}

// runCheckpointPred runs the checkpoint-pred strategy with every layer up to
// the predictive checkpoint forced, summarizing at summarizeThreshold.
func runCheckpointPred(t *testing.T, cm *Manager, history *[]schema.Turn, summarizeThreshold float64) {
	t.Helper()
	cm.ObservationMaskThreshold = 0.0001
	cm.ThinkingClearThreshold = 0.0001
	cm.CheckpointThreshold = 0.0001
	cm.SummarizeThreshold = summarizeThreshold
	if err := NewCheckpointPredStrategy(cm).ManageContext(context.Background(), history, 0, noopEmit); err != nil {
		t.Fatalf("ManageContext: %v", err)
	}
}

// A summary that drops everything still leaves the human partner's own
// messages in front of the model, on every path where the summary replaces
// the checkpoint, and through a second compaction (#4173).
func TestCompaction_SummaryKeepsPartnerMessagesVerbatim(t *testing.T) {
	for _, tc := range []struct {
		name    string
		compact func(cm *Manager, history *[]schema.Turn)
	}{
		{"auto", func(cm *Manager, history *[]schema.Turn) {
			cm.CheckpointThreshold = 0.0001
			cm.SummarizeThreshold = 0.0001
			cm.MaybeCompact(context.Background(), history, 0, noopEmit)
		}},
		{"forced", func(cm *Manager, history *[]schema.Turn) {
			cm.ForceCompact(context.Background(), history, "", noopEmit)
		}},
		{"steered", func(cm *Manager, history *[]schema.Turn) {
			cm.ForceCompact(context.Background(), history, "Keep only the docs plan.", noopEmit)
		}},
		{"checkpoint-pred summarize", func(cm *Manager, history *[]schema.Turn) {
			runCheckpointPred(t, cm, history, 0.0001)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := partnerTestManager(emptySummaryReply)
			history := partnerHoldHistory()
			tc.compact(cm, &history)
			requirePartnerHoldInContext(t, history, schema.TurnSummary)
			if !strings.Contains(history[0].Message.Text(), "SESSION_ID_SENTINEL") {
				t.Fatalf("the summary does not point at the session's transcript:\n%s", history[0].Message.Text())
			}

			history = append(history, moreTurns()...)
			tc.compact(cm, &history)
			requirePartnerHoldInContext(t, history, schema.TurnSummary)
		})
	}
}

// checkpoint-pred's own predictive checkpoint, written by a model, still
// carries the human partner's messages verbatim.
func TestCheckpointPred_PredictiveCheckpointKeepsPartnerMessagesVerbatim(t *testing.T) {
	cm := partnerTestManager("Predicted checkpoint.")
	history := partnerHoldHistory()
	runCheckpointPred(t, cm, &history, 2) // never summarize: the predictive checkpoint stands
	requirePartnerHoldInContext(t, history, schema.TurnCheckpoint)
	if !strings.Contains(history[0].Message.Text(), "SESSION_ID_SENTINEL") {
		t.Fatalf("the predictive checkpoint does not point at the session's transcript:\n%s", history[0].Message.Text())
	}
}

// A section heading inside fenced content is content, not a section: a pasted
// "## Earlier Summaries" in a user message is not an earlier summary, and an
// earlier summary that mentions "## Working Notes" does not hide the
// checkpoint's real working notes.
func TestCheckpoint_SectionHeadingsInsideFencesAreContent(t *testing.T) {
	recent := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	pasted := "here is the old checkpoint:\n## Earlier Summaries\n\n### Summary\n\nPASTED_NOT_A_SUMMARY"
	history := append([]schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(pasted)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("noted")),
	}, recent...)
	cp := checkpoint(history, 2, nil, "communicate")[0].Message.Text()
	if got := extractCheckpointEarlierSummaries(cp); len(got) != 0 {
		t.Fatalf("a pasted heading in a user message was read as earlier summaries: %q", got)
	}

	note := "REAL_WORKING_NOTE " + strings.Repeat("analysis ", 10)
	summary := "[CONTEXT SUMMARY]\n## Progress\nmentions\n## Working Notes\n\n### Note\n\nFAKE_NOTE\n[END SUMMARY]"
	history = append([]schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User(summary)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant(note)),
	}, recent...)
	cp = checkpoint(history, 2, nil, "communicate")[0].Message.Text()
	notes := extractCheckpointWorkingNotes(cp)
	if len(notes) != 1 || !strings.Contains(notes[0], "REAL_WORKING_NOTE") {
		t.Fatalf("working notes = %q, want only the real note", notes)
	}
}
