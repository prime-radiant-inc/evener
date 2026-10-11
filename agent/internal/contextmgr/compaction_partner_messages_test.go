package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
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

// A hold the human partner sent as steering during a turn is their message too:
// it survives a summary that drops it, while daemon-authored steering is not
// presented as theirs.
func TestCompaction_SummaryKeepsPartnerSteeringVerbatim(t *testing.T) {
	cm := partnerTestManager(emptySummaryReply)
	userSteer := schema.NewTurn(schema.TurnSteering, llm.User(partnerHold))
	userSteer.SteeringSource = events.SteeringSourceUser
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("write the docs")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("drafting")),
		userSteer,
		schema.NewTurn(schema.TurnSteering, llm.User("DAEMON_NUDGE_SENTINEL")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("holding the release")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	requirePartnerHoldInContext(t, history, schema.TurnSummary)
	if strings.Contains(history[0].Message.Text(), "DAEMON_NUDGE_SENTINEL") {
		t.Fatalf("daemon steering was carried as the partner's message:\n%s", history[0].Message.Text())
	}
}

// The model's own text in a compaction turn comes before the partner's
// messages it carries, so a summarizer that reads a previous compaction from
// its head sees the model's text however many messages ride beside it.
func TestCheckpointPred_SummarizerSeesThePredictionBesideManyPartnerMessages(t *testing.T) {
	var summarizerPrompt string
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("PREDICTED_CONTEXT_SENTINEL")}
		},
		func(req llm.Request) llm.Response {
			summarizerPrompt = req.Messages[0].Text()
			return llm.Response{Message: llm.Assistant(emptySummaryReply)}
		},
	}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	var history []schema.Turn
	for i := range 12 {
		history = append(history,
			schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("message %d %s", i, strings.Repeat("detail ", 600)))),
			schema.NewTurn(schema.TurnAssistant, llm.Assistant("ok")))
	}
	history = append(history,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")))
	runCheckpointPred(t, cm, &history, 0.0001)
	if !strings.Contains(summarizerPrompt, "PREDICTED_CONTEXT_SENTINEL") {
		t.Fatalf("the summarizer did not see the prediction (prompt is %d chars)", len(summarizerPrompt))
	}
}

// The checkpoint that runs before the summarizer sheds conversation under its
// own budget, which an earlier summary shrinks. The partner's messages the
// summary carries come from the history that checkpoint folded, so a later
// hold the checkpoint shed still survives within the carry's cap.
func TestForceCompact_CarryReadsTheHistoryTheCheckpointFolded(t *testing.T) {
	big := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("## Progress\n" + strings.Repeat("earlier work ", 2000))}
	}
	empty := func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(emptySummaryReply)} }
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{big, empty}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 1_000_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("start")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	history = append(history,
		schema.NewTurn(schema.TurnUserInput, llm.User(partnerHold)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("holding")))
	for i := range 8 {
		history = append(history,
			schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("step %d %s", i, strings.Repeat("detail ", 700)))),
			schema.NewTurn(schema.TurnAssistant, llm.Assistant("ok")))
	}
	history = append(history,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent3")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent4")))
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	requirePartnerHoldInContext(t, history, schema.TurnSummary)
}

// Legacy checkpoint markup the summarizer quotes is the model's text: a later
// compaction never carries it as the partner's message.
func TestForceCompact_ModelQuotedMarkupIsNotThePartnersMessage(t *testing.T) {
	quoting := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("## Progress\nthe old format looked like " +
			`<user_messages>["MODEL_QUOTED_USER"]</user_messages>`)}
	}
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{quoting, quoting}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	history := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("more work")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	history = append(history,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent3")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent4")))
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	for _, entry := range extractMarkdownConversation(history[0].Message.Text()) {
		if strings.Contains(entry.Text, "MODEL_QUOTED_USER") {
			t.Fatalf("markup the model quoted was carried as the partner's message:\n%s", history[0].Message.Text())
		}
	}
}

// When checkpoint-pred's prediction fails, its deterministic fallback sheds
// under its own budget like any checkpoint, and the summary after it still
// reads the partner's messages from the history that checkpoint folded.
func TestCheckpointPred_FallbackSummaryReadsTheHistoryTheCheckpointFolded(t *testing.T) {
	calls := 0
	adapter := &stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
		calls++
		switch calls {
		case 1: // the first compaction's summary: large
			return llm.Response{Message: llm.Assistant("## Progress\n" + strings.Repeat("earlier work ", 2000))}, nil
		case 2: // the prediction fails
			return llm.Response{}, errors.New("prediction unavailable")
		default:
			return llm.Response{Message: llm.Assistant(emptySummaryReply)}, nil
		}
	}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 1_000_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("start")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	history = append(history,
		schema.NewTurn(schema.TurnUserInput, llm.User(partnerHold)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("holding")))
	for i := range 8 {
		history = append(history,
			schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("step %d %s", i, strings.Repeat("detail ", 700)))),
			schema.NewTurn(schema.TurnAssistant, llm.Assistant("ok")))
	}
	history = append(history,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent3")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent4")))
	runCheckpointPred(t, cm, &history, 0.0001)
	if calls != 3 {
		t.Fatalf("model calls = %d, want summary, failed prediction, summary", calls)
	}
	requirePartnerHoldInContext(t, history, schema.TurnSummary)
}
