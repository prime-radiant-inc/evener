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
	cm.Meta = CompactionMeta{SessionID: "SESSION_ID_SENTINEL", AvailableTranscriptTools: []string{"read_transcript"}, TranscriptKept: true}
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

// A delegate's parent steers it during a turn. The steer is the instruction
// the delegate works from, so it survives a summary that drops it, with its
// attribution, while daemon-authored steering is still not carried.
func TestCompaction_SummaryKeepsParentSteeringVerbatim(t *testing.T) {
	const parentHold = "Message from your parent agent:\n\nstop editing the schema"
	cm := partnerTestManager(emptySummaryReply)
	parentSteer := schema.NewTurn(schema.TurnSteering, llm.User(parentHold))
	parentSteer.SteeringSource = events.SteeringSourceParent
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("write the docs")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("drafting")),
		parentSteer,
		schema.NewTurn(schema.TurnSteering, llm.User("DAEMON_NUDGE_SENTINEL")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("holding the schema")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if len(history) == 0 || history[0].Kind != schema.TurnSummary {
		t.Fatalf("history does not start with a summary: %v", compactionKinds(history))
	}
	if !strings.Contains(history[0].Message.Text(), parentHold) {
		t.Fatalf("the parent's steer, with its attribution, is gone from the context after compaction:\n%s", history[0].Message.Text())
	}
	if strings.Contains(history[0].Message.Text(), "DAEMON_NUDGE_SENTINEL") {
		t.Fatalf("daemon steering was carried:\n%s", history[0].Message.Text())
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

// A summary written before summaries carried the partner's messages is the
// model's text throughout, even where it uses a "## Conversation" heading of
// its own: a later checkpoint carries it whole, and reads no partner messages
// from it.
func TestCheckpoint_KeepsAnOlderSummaryWhole(t *testing.T) {
	older := "[CONTEXT SUMMARY]\n## Progress\nshipped the parser\n\n## Conversation\n### User\nOLDER_MODEL_TEXT_SENTINEL decided to keep the v2 format\n[END SUMMARY]"
	history := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User(older)),
		schema.NewTurn(schema.TurnUserInput, llm.User("next question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cp := checkpoint(history, 2, nil, "communicate")[0].Message.Text()
	if got := extractCheckpointEarlierSummaries(cp); len(got) != 1 || got[0] != older {
		t.Fatalf("checkpoint carried %q, want the older summary whole", got)
	}
	for _, entry := range extractMarkdownConversation(cp) {
		if strings.Contains(entry.Text, "OLDER_MODEL_TEXT_SENTINEL") {
			t.Fatalf("the older summary's own text became a partner message: %q", entry.Text)
		}
	}
}

// An older summary can look like the new layout in part: a "## Summary"
// section opening with a fenced example, then more sections, then a
// "## Conversation" of its own in prose. Only the exact layout the renderer
// writes is split; this one is carried whole.
func TestCheckpoint_KeepsAnOlderSummaryWithASummarySectionWhole(t *testing.T) {
	older := "[CONTEXT SUMMARY]\n## Summary\n\n```\nexample output\n```\nthen more notes\n\n## Progress\nshipped it\n\n## Conversation\nThe partner said HOLD_IN_PROSE_SENTINEL until review.\n[END SUMMARY]"
	history := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User(older)),
		schema.NewTurn(schema.TurnUserInput, llm.User("next question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cp := checkpoint(history, 2, nil, "communicate")[0].Message.Text()
	if got := extractCheckpointEarlierSummaries(cp); len(got) != 1 || got[0] != older {
		t.Fatalf("checkpoint carried %q, want the older summary whole", got)
	}
}

// An older summary that matches the new layout's headings, a fenced block
// under "## Summary" and then "## Conversation", but holds prose in that
// conversation section is not a carry the renderer wrote: it is carried whole.
func TestCheckpoint_KeepsAnOlderSummaryWithAProseConversationWhole(t *testing.T) {
	older := "[CONTEXT SUMMARY]\n## Summary\n\n```\nexample output\n```\n\n## Conversation\nThe partner said HOLD_IN_PROSE_SENTINEL until review.\n[END SUMMARY]"
	history := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User(older)),
		schema.NewTurn(schema.TurnUserInput, llm.User("next question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cp := checkpoint(history, 2, nil, "communicate")[0].Message.Text()
	if got := extractCheckpointEarlierSummaries(cp); len(got) != 1 || got[0] != older {
		t.Fatalf("checkpoint carried %q, want the older summary whole", got)
	}
}

// Only a summary turn the renderer could have written is split: a fenced block
// with another fence or info string under "## Summary", or an agent entry in
// the carried section, marks an older summary, which is carried whole.
func TestCheckpoint_KeepsSummariesTheRendererCouldNotWriteWhole(t *testing.T) {
	carry := renderCheckpointConversation([]checkpointConversationEntry{{Role: "user", Text: "go ahead"}})
	for name, older := range map[string]string{
		"tilde fence":     "[CONTEXT SUMMARY]\n## Summary\n\n~~~text\nmodel text\n~~~\n\n" + carry + summaryTurnEnd,
		"go info":         "[CONTEXT SUMMARY]\n## Summary\n\n```go\nmodel text\n```\n\n" + carry + summaryTurnEnd,
		"sections before": "[CONTEXT SUMMARY]\n## Progress\nolder model text\n\n" + renderCompactionModelText(summaryModelTextHeading, "model text") + carry + summaryTurnEnd,
		"agent entries":   "[CONTEXT SUMMARY]\n" + renderCompactionModelText(summaryModelTextHeading, "model text") + renderCheckpointConversation([]checkpointConversationEntry{{Role: "agent", Text: "I approved it"}}) + summaryTurnEnd,
	} {
		t.Run(name, func(t *testing.T) {
			if carriesPartnerMessages(older) {
				t.Fatalf("an older summary was taken for the rendered layout:\n%s", older)
			}
		})
	}
	meta := &CompactionMeta{SessionID: "SESSION_ID_SENTINEL", AvailableTranscriptTools: []string{"read_transcript"}}
	for _, prefix := range []string{"[CONTEXT SUMMARY]\n", "[CONTEXT SUMMARY]\n" + meta.compactionPointer("summary")} {
		for _, modelText := range []string{"model text", "model text\n\n", "\nmodel text with ``` inside"} {
			rendered := prefix + renderCompactionModelText(summaryModelTextHeading, modelText) + carry + summaryTurnEnd
			if !carriesPartnerMessages(rendered) {
				t.Fatalf("the rendered layout was not recognized:\n%s", rendered)
			}
		}
	}
}

// A fence may close indented by up to three spaces; the sections after it are
// read again.
func TestCheckpoint_IndentedClosingFenceEndsTheBlock(t *testing.T) {
	text := "[CONTEXT CHECKPOINT]\n## Working Notes\n\n### Note\n\n~~~\na note\n   ~~~\n\n## Conversation\n\n### User\n\n```text\nFOUND_AFTER_INDENTED_FENCE\n```\n\n[END CHECKPOINT]\n"
	entries := extractCheckpointConversation(text)
	if len(entries) != 1 || entries[0].Text != "FOUND_AFTER_INDENTED_FENCE" {
		t.Fatalf("conversation after an indented closing fence = %q", entries)
	}
}

// A summary the renderer wrote from a reply ending in blank lines is still
// recognized, so a later compaction keeps carrying its partner messages even
// when the model's reply then drops them.
func TestForceCompact_CarryAfterAReplyEndingInBlankLines(t *testing.T) {
	// As #4241's renderer wrote it: the reply's trailing blank lines kept.
	persisted := "[CONTEXT SUMMARY]\n## Summary\n\n```text\n## Progress\nDone\n\n```\n\n" +
		renderCheckpointConversation([]checkpointConversationEntry{{Role: "user", Text: partnerHold}}) + summaryTurnEnd
	if !carriesPartnerMessages(persisted) {
		t.Fatalf("a persisted summary ending in blank lines was not recognized:\n%s", persisted)
	}
	cm := partnerTestManager(emptySummaryReply + "\n\n")
	history := partnerHoldHistory()
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if !carriesPartnerMessages(history[0].Message.Text()) {
		t.Fatalf("a summary the renderer wrote was not recognized:\n%s", history[0].Message.Text())
	}
	history = append(history, moreTurns()...)
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	requirePartnerHoldInContext(t, history, schema.TurnSummary)
}

// The human partner's messages are never dropped (#4281): over budget the
// checkpoint sheds the agent's own text first, keeps a short later stop whole,
// and trims only the middle of a very long pasted log, saying where its full
// text is.
func TestFormatCheckpoint_KeepsEveryPartnerMessage(t *testing.T) {
	log := "LOG_HEAD " + strings.Repeat("log line\n", 6000) + " LOG_TAIL"
	stop := "stop, don't push until I review STOP_SENTINEL"
	data := checkpointData{
		fileWrites:      map[string]checkpointWriteStatus{},
		activatedSkills: map[string]bool{},
		toolCounts:      map[string]int{},
		conversation: []checkpointConversationEntry{
			{Role: "user", Text: "please look at this log"},
			{Role: "user", Text: log},
			{Role: "agent", Text: "reading it " + strings.Repeat("analysis ", 2000)},
			{Role: "agent", Text: "found the cause " + strings.Repeat("detail ", 2000)},
			{Role: "user", Text: stop},
		},
		workingNotes: []string{"a note " + strings.Repeat("n", 3000)},
	}
	cp := formatCheckpoint(data, persistentMeta, 20_000)
	var partner []string
	for _, entry := range extractCheckpointConversation(cp) {
		if entry.Role != "user" {
			t.Fatalf("the agent's own text survived while the partner's messages did not fit: %.60q", entry.Text)
		}
		partner = append(partner, entry.Text)
	}
	if len(partner) != 3 || partner[0] != "please look at this log" || partner[2] != stop {
		t.Fatalf("partner messages = %d, want all three with the short ones whole", len(partner))
	}
	if !isMiddleTrimOf(partner[1], log) {
		t.Fatalf("the pasted log was not middle-trimmed with the note:\n%.300s", partner[1])
	}
}

// When even the partner's short messages exceed the budget, every one is kept
// whole: the cap yields, never a message.
func TestFormatCheckpoint_KeepsShortPartnerMessagesOverBudget(t *testing.T) {
	data := checkpointData{
		fileWrites:      map[string]checkpointWriteStatus{},
		activatedSkills: map[string]bool{},
		toolCounts:      map[string]int{},
	}
	for i := range 40 {
		data.conversation = append(data.conversation, checkpointConversationEntry{Role: "user", Text: fmt.Sprintf("instruction %d %s", i, strings.Repeat("x", 300))})
	}
	entries := extractCheckpointConversation(formatCheckpoint(data, nil, 4000))
	if len(entries) != 40 {
		t.Fatalf("kept %d of 40 short partner messages", len(entries))
	}
	for i, entry := range entries {
		if entry.Text != data.conversation[i].Text {
			t.Fatalf("short message %d was changed: %.60q", i, entry.Text)
		}
	}
}

// The summary's verbatim carry follows the same rule end to end: a later
// short stop survives whole beside a middle-trimmed pasted log.
func TestForceCompact_CarryKeepsAStopBesideALongLog(t *testing.T) {
	cm := partnerTestManager(emptySummaryReply)
	log := "LOG_HEAD " + strings.Repeat("log line\n", 9000) + " LOG_TAIL"
	stop := "stop, don't push until I review STOP_SENTINEL"
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(log)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("reading it")),
		schema.NewTurn(schema.TurnUserInput, llm.User(stop)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("holding")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	entries := extractMarkdownConversation(history[0].Message.Text())
	if len(entries) != 2 || entries[1].Text != stop || !isMiddleTrimOf(entries[0].Text, log) {
		t.Fatalf("summary carry = %d entries; want the trimmed log and the whole stop", len(entries))
	}
}

// persistentMeta is a persistent session's compaction metadata: its transcript
// holds every message in full, so a trimmed message can point there.
var persistentMeta = &CompactionMeta{SessionID: "SESSION_ID_SENTINEL", AvailableTranscriptTools: []string{"read_transcript"}, TranscriptKept: true}

// A partner message only just over the trim threshold is kept whole: trimming
// it would add the note without saving anything.
func TestFormatCheckpoint_KeepsAMessageTrimmingWouldNotShorten(t *testing.T) {
	near := strings.Repeat("n", 2*partnerMessageEdgeChars+50)
	data := checkpointData{
		fileWrites:      map[string]checkpointWriteStatus{},
		activatedSkills: map[string]bool{},
		toolCounts:      map[string]int{},
		conversation:    []checkpointConversationEntry{{Role: "user", Text: near}},
	}
	entries := extractCheckpointConversation(formatCheckpoint(data, persistentMeta, 1000))
	if len(entries) != 1 || entries[0].Text != near {
		t.Fatalf("a message trimming would not shorten was changed: %d chars", len(entries[0].Text))
	}
}

// Without a transcript there is nowhere to recover a trimmed middle from, so a
// non-persistent session keeps every partner message whole.
func TestFormatCheckpoint_KeepsMessagesWholeWithoutATranscript(t *testing.T) {
	log := "LOG_HEAD " + strings.Repeat("log line\n", 6000) + " LOG_TAIL"
	data := checkpointData{
		fileWrites:      map[string]checkpointWriteStatus{},
		activatedSkills: map[string]bool{},
		toolCounts:      map[string]int{},
		conversation:    []checkpointConversationEntry{{Role: "user", Text: log}},
	}
	for name, meta := range map[string]*CompactionMeta{
		"non-persistent":               nil,
		"transcript could not be made": {SessionID: "SESSION_ID_SENTINEL"},
	} {
		entries := extractCheckpointConversation(formatCheckpoint(data, meta, 20_000))
		if len(entries) != 1 || entries[0].Text != log {
			t.Fatalf("%s: a message was trimmed with no transcript to recover it from", name)
		}
	}
}

// Past the carry ceiling (a quarter of the model's context window) the oldest
// partner messages are left to the model's summary instead of quoted, so the
// compacted context stays well under the model's limit. The newest messages
// stay verbatim, a reminder points at the transcript for the rest, and a hold
// from an older message survives as the summary's quote (#4309).
func TestForceCompact_PartnerMessagesPastTheCeilingAreSummarized(t *testing.T) {
	const window = 20_000 // tokens; the ceiling is a quarter of it, 20_000 chars
	reply := "## Permissions and Holds\n\"" + partnerHold + "\" (message 0)\n\n## Progress\nDone"
	client := llm.NewClient()
	client.Register(alwaysReplying(func(llm.Request) string { return reply }))
	cm := NewManager(testProfile("openai", "test", window), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta

	history := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(partnerHold))}
	for i := range 40 {
		history = append(history,
			schema.NewTurn(schema.TurnAssistant, llm.Assistant("ok")),
			schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("instruction %02d %s", i, strings.Repeat("x", 1500)))))
	}
	newest := history[len(history)-1].Message.Text()
	history = append(history,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")))

	cm.ForceCompact(context.Background(), &history, "", noopEmit)

	summary := history[0].Message.Text()
	if history[0].Kind != schema.TurnSummary {
		t.Fatalf("history does not start with a summary")
	}
	if tokens := cm.estimateTokensFor(cm.currentProfile(), history); tokens > window/2 {
		t.Fatalf("compacted history is %d tokens, want well under the %d-token window", tokens, window)
	}
	entries := extractMarkdownConversation(summary)
	if len(entries) == 0 || entries[len(entries)-1].Text != newest {
		t.Fatalf("the newest partner message is not carried verbatim")
	}
	if len(entries) >= 41 {
		t.Fatalf("all %d partner messages were carried verbatim past the ceiling", len(entries))
	}
	if !strings.Contains(summary, partnerCarryReminder(41-len(entries), persistentMeta)) {
		t.Fatalf("the summary does not remind the model where the summarized messages are:\n%.600s", summary)
	}
	if !strings.Contains(summary, partnerHold) {
		t.Fatalf("the older hold is gone: neither carried nor quoted by the summary")
	}
	// A later fold still reads this turn's carry back.
	if !carriesPartnerMessages(summary) {
		t.Fatalf("a summary with the reminder is not recognized as the rendered layout")
	}
}

// partnerFloodHistory is a conversation of n partner messages of size chars
// each, with hold as message holdAt, followed by two preserved recent turns.
func partnerFloodHistory(n, size, holdAt int) []schema.Turn {
	var history []schema.Turn
	for i := range n {
		text := fmt.Sprintf("instruction %03d %s", i, strings.Repeat("x", size))
		if i == holdAt {
			text = partnerHold
		}
		history = append(history,
			schema.NewTurn(schema.TurnUserInput, llm.User(text)),
			schema.NewTurn(schema.TurnAssistant, llm.Assistant("ok")))
	}
	return append(history,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")))
}

// Every partner message the carry leaves out reaches the summarizer, even one
// that sits past the part of the checkpoint the summarizer reads and before
// the newest messages the carry keeps.
func TestForceCompact_MessagesLeftOutOfTheCarryReachTheSummarizer(t *testing.T) {
	var prompt string
	client := llm.NewClient()
	client.Register(alwaysReplying(func(req llm.Request) string {
		prompt += req.Messages[0].Text()
		return emptySummaryReply
	}))
	cm := NewManager(testProfile("openai", "test", 20_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	// 70 messages of 1.5k: the summarizer's view of the checkpoint ends near
	// message 38 and the carry starts near 57, so message 48 sits in between.
	history := partnerFloodHistory(70, 1500, 48)
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if strings.Contains(history[0].Message.Text(), "\n"+partnerHold+"\n") {
		t.Fatalf("fixture: the hold is in the carry, so this does not test the gap")
	}
	if !strings.Contains(prompt, partnerHold) {
		t.Fatalf("a partner message left out of the carry never reached the summarizer (prompt is %d chars)", len(prompt))
	}
}

// The newest message is held to the ceiling too: on a small-window model a
// single oversized newest message is middle-trimmed to fit, with its pointer.
func TestForceCompact_OversizedNewestMessageFitsTheCeiling(t *testing.T) {
	answer := func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(emptySummaryReply)} }
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{answer}}
	client := llm.NewClient()
	client.Register(adapter)
	const window = 8192
	cm := NewManager(testProfile("openai", "test", window), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	log := "LOG_HEAD " + strings.Repeat("log line\n", 4500) + " LOG_TAIL"
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(log)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("reading")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	carry := markdownSection(history[0].Message.Text(), checkpointConversationHeading)
	if len(carry) > partnerCarryCeiling(cm.currentProfile()) {
		t.Fatalf("the carry is %d chars, over the %d-char ceiling", len(carry), partnerCarryCeiling(cm.currentProfile()))
	}
	entries := extractMarkdownConversation(history[0].Message.Text())
	if len(entries) != 1 || !isMiddleTrimOf(entries[0].Text, log) {
		t.Fatalf("the oversized newest message was not middle-trimmed to the ceiling")
	}
}

// checkpoint-pred's prediction never sees the partner's messages whole, so
// its predictive checkpoint keeps every one rather than leaving any to it.
func TestCheckpointPred_PredictionKeepsEveryPartnerMessage(t *testing.T) {
	answer := func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("Predicted checkpoint.")} }
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{answer}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 20_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	history := partnerFloodHistory(40, 1500, 3)
	runCheckpointPred(t, cm, &history, 2)
	if got := len(extractCheckpointConversation(history[0].Message.Text())); got != 40 {
		t.Fatalf("the predictive checkpoint kept %d of 40 partner messages", got)
	}
}

// However many partner messages the carry leaves out, every one reaches a
// summarizer request: those that overflow one request are summarized in
// bounded batches first, and the hold among them survives into the summary.
func TestForceCompact_EveryLeftOutMessageIsSummarized(t *testing.T) {
	var requests []string
	respond := func(req llm.Request) llm.Response {
		text := req.Messages[0].Text()
		requests = append(requests, text)
		reply := "## Progress\nsummary"
		if strings.Contains(text, partnerHold) {
			reply = "## Permissions and Holds\n\"" + partnerHold + "\"\n\n## Progress\nsummary"
		}
		return llm.Response{Message: llm.Assistant(reply)}
	}
	adapter := &stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) { return respond(req), nil }}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	history := partnerFloodHistory(200, 1500, 80)
	var messages []string
	for _, turn := range history {
		if turn.Kind == schema.TurnUserInput {
			messages = append(messages, turn.Message.Text())
		}
	}
	cm.ForceCompact(context.Background(), &history, "", noopEmit)

	summary := history[0].Message.Text()
	carried := extractMarkdownConversation(summary)
	all := strings.Join(requests, "\n")
	for i, message := range messages[:len(messages)-len(carried)] {
		if !strings.Contains(all, message) {
			t.Fatalf("left-out partner message %d never reached a summarizer request (%d requests)", i, len(requests))
		}
	}
	if !strings.Contains(summary, partnerHold) {
		t.Fatalf("the hold among the left-out messages did not survive into the summary")
	}
}

// alwaysReplying is a model that answers every request with reply's result.
func alwaysReplying(reply func(llm.Request) string) *stubSummarizeAdapter {
	return &stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant(reply(req))}, nil
	}}
}

// A hold deep inside a long left-out message still reaches a summarizer: long
// messages are fed in full, in bounded chunks, never cut short.
func TestForceCompact_LongLeftOutMessagesReachTheSummarizerWhole(t *testing.T) {
	var all strings.Builder
	client := llm.NewClient()
	client.Register(alwaysReplying(func(req llm.Request) string {
		all.WriteString(req.Messages[0].Text())
		return "## Progress\nsummary"
	}))
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = CompactionMeta{} // no transcript, so nothing is middle-trimmed
	history := partnerFloodHistory(50, 6000, -1)
	deep := strings.Repeat("y", 5500) + " " + partnerHold + " " + strings.Repeat("z", 500)
	history[40] = schema.NewTurn(schema.TurnUserInput, llm.User(deep)) // message 20
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if strings.Contains(history[0].Message.Text(), deep) {
		t.Fatalf("fixture: the message is carried, so this does not test the left-out path")
	}
	if !strings.Contains(all.String(), partnerHold) {
		t.Fatalf("a hold past byte 5000 of a left-out message never reached a summarizer")
	}
}

// When batch summaries will not shrink to fit, condensing fails rather than
// cutting them short, so the checkpoint, which keeps every partner message,
// stands instead of a summary that lost one.
func TestForceCompact_NonShrinkingBatchSummariesKeepTheCheckpoint(t *testing.T) {
	client := llm.NewClient()
	client.Register(alwaysReplying(func(req llm.Request) string {
		text := req.Messages[0].Text()
		if batch, ok := strings.CutPrefix(text, partnerBatchPrompt); ok {
			return batch // never shorter
		}
		return emptySummaryReply
	}))
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	history := partnerFloodHistory(200, 1500, 80)
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if history[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("history starts with a %s, want the checkpoint to stand", history[0].Kind)
	}
	if !strings.Contains(history[0].Message.Text(), partnerHold) {
		t.Fatalf("the standing checkpoint lost the hold")
	}
}

// Batch summaries that first fit after the last condensing round are used, not
// rejected.
func TestForceCompact_SummariesFittingOnTheLastRoundAreUsed(t *testing.T) {
	client := llm.NewClient()
	client.Register(alwaysReplying(func(req llm.Request) string {
		text := req.Messages[0].Text()
		if batch, ok := strings.CutPrefix(text, partnerBatchPrompt); ok {
			return batch[:len(batch)/3] // each round shrinks the text to a third
		}
		return emptySummaryReply
	}))
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	// About 1.3M characters left out: three rounds leave over 40k, the fourth fits.
	history := partnerFloodHistory(900, 1500, -1)
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if history[0].Kind != schema.TurnSummary {
		t.Fatalf("history starts with a %s, want the summary built from the last round", history[0].Kind)
	}
}

// The summary request stays within the summarizer's history budget when the
// left-out partner messages come first: the previous compaction gets what is
// left.
func TestForceCompact_SummaryRequestStaysInBudgetWithLeftOutMessages(t *testing.T) {
	var main string
	client := llm.NewClient()
	client.Register(alwaysReplying(func(req llm.Request) string {
		text := req.Messages[0].Text()
		if strings.HasPrefix(text, partnerBatchPrompt) {
			return "summary of a batch"
		}
		main = text
		return emptySummaryReply
	}))
	cm := NewManager(testProfile("openai", "test", 20_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	// About 38k characters left out (just under the 40k unbatched limit) and a
	// checkpoint near its 60k cap.
	history := partnerFloodHistory(38, 1500, -1)
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	historyText := len(main) - len(buildSummaryPrompt("", ""))
	if historyText > 80_000+5_000 {
		t.Fatalf("the summary request carries %d characters of history, over the summarizer's 80k budget", historyText)
	}
}

// collectRequests is a model that records every request's text and answers
// with a plain summary.
func collectRequests(all *strings.Builder) *stubSummarizeAdapter {
	return alwaysReplying(func(req llm.Request) string {
		all.WriteString(req.Messages[0].Text())
		all.WriteString("\n")
		return "## Progress\nsummary"
	})
}

// A left-out message reaches the summarizer as written, even when its middle
// would be trimmed for the carry: the hold in the middle of an older long
// message still reaches a summarizer request.
func TestForceCompact_LeftOutMessagesReachTheSummarizerUntrimmed(t *testing.T) {
	var all strings.Builder
	client := llm.NewClient()
	client.Register(collectRequests(&all))
	cm := NewManager(testProfile("openai", "test", 20_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	history := partnerFloodHistory(30, 6000, -1)
	history[10] = schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("a", 3000)+" "+partnerHold+" "+strings.Repeat("b", 3000)))
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if !strings.Contains(all.String(), partnerHold) {
		t.Fatalf("a hold in the middle of a left-out long message never reached a summarizer")
	}
}

// Without a transcript an oversized newest message cannot be trimmed, so it is
// summarized like the rest rather than carried whole past the ceiling.
func TestForceCompact_OversizedNewestMessageWithoutTranscriptIsSummarized(t *testing.T) {
	var all strings.Builder
	client := llm.NewClient()
	client.Register(collectRequests(&all))
	cm := NewManager(testProfile("openai", "test", 8192), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = CompactionMeta{}
	log := "LOG_HEAD " + strings.Repeat("log line\n", 4500) + " LOG_TAIL"
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(log)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("reading")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if carry := markdownSection(history[0].Message.Text(), checkpointConversationHeading); len(carry) > partnerCarryCeiling(cm.currentProfile()) {
		t.Fatalf("the carry is %d chars, over the ceiling", len(carry))
	}
	if !strings.Contains(all.String(), "LOG_TAIL") {
		t.Fatalf("the oversized newest message never reached a summarizer")
	}
}

// A long left-out message split into parts keeps a hold that crosses a part
// boundary whole in some summarizer request.
func TestForceCompact_HoldAcrossAPartBoundaryReachesTheSummarizer(t *testing.T) {
	var all strings.Builder
	client := llm.NewClient()
	client.Register(collectRequests(&all))
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = CompactionMeta{}
	history := partnerFloodHistory(30, 30_000, -1)
	// A hold spanning byte 20,000 (and, with smaller parts, other cut points).
	long := []byte(strings.Repeat("c", 60_000))
	for _, at := range []int{13_310, 19_980, 26_640, 39_980} {
		copy(long[at:], " "+partnerHold+" ")
	}
	history[4] = schema.NewTurn(schema.TurnUserInput, llm.User(string(long)))
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if got := strings.Count(all.String(), partnerHold); got < 4 {
		t.Fatalf("only %d of 4 holds crossing part boundaries reached a summarizer whole", got)
	}
}

// A batch summary the model cut off at its output limit may have dropped a
// hold, so it fails the summary and the checkpoint, which keeps every partner
// message, stands.
func TestForceCompact_TruncatedBatchSummaryKeepsTheCheckpoint(t *testing.T) {
	adapter := &stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) {
		if strings.HasPrefix(req.Messages[0].Text(), partnerBatchPrompt) {
			return llm.Response{Message: llm.Assistant("partial summary"), Finish: llm.FinishReason{Reason: llm.FinishReasonLength}}, nil
		}
		return llm.Response{Message: llm.Assistant(emptySummaryReply)}, nil
	}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	history := partnerFloodHistory(200, 1500, 80)
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if history[0].Kind != schema.TurnCheckpoint || !strings.Contains(history[0].Message.Text(), partnerHold) {
		t.Fatalf("history starts with a %s; want the checkpoint, with the hold, to stand", history[0].Kind)
	}
}

// A long batch summary is split into parts like a long message, so no later
// condensing request runs past the batch limit.
func TestForceCompact_OversizedBatchSummariesStayInBatchBounds(t *testing.T) {
	largest, first := 0, true
	client := llm.NewClient()
	client.Register(alwaysReplying(func(req llm.Request) string {
		batch, ok := strings.CutPrefix(req.Messages[0].Text(), partnerBatchPrompt)
		if !ok {
			return emptySummaryReply
		}
		largest = max(largest, len(batch))
		if first {
			first = false
			return strings.Repeat("verbose summary line.\n", 6000) // about 130k characters
		}
		return "short summary"
	}))
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	history := partnerFloodHistory(200, 1500, -1)
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if largest > 40_000+1_000 {
		t.Fatalf("a condensing request carried %d characters, over the 40k batch limit", largest)
	}
}

// A final summary the model cut off at its output limit may have dropped what
// the left-out messages said, so it is rejected and the checkpoint stands.
func TestForceCompact_TruncatedFinalSummaryKeepsTheCheckpoint(t *testing.T) {
	adapter := &stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) {
		if strings.HasPrefix(req.Messages[0].Text(), partnerBatchPrompt) {
			return llm.Response{Message: llm.Assistant("summary of a batch")}, nil
		}
		return llm.Response{Message: llm.Assistant(emptySummaryReply), Finish: llm.FinishReason{Reason: llm.FinishReasonLength}}, nil
	}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	history := partnerFloodHistory(200, 1500, 80)
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if history[0].Kind != schema.TurnCheckpoint || !strings.Contains(history[0].Message.Text(), partnerHold) {
		t.Fatalf("history starts with a %s; want the checkpoint, with the hold, to stand", history[0].Kind)
	}
}

// The carry is chosen from the messages as written: a long newest message
// that fits under the ceiling is carried whole, not trimmed to its edges.
func TestForceCompact_CarryKeepsAFittingLongMessageWhole(t *testing.T) {
	client := llm.NewClient()
	client.Register(alwaysReplying(func(llm.Request) string { return emptySummaryReply }))
	cm := NewManager(testProfile("openai", "test", 20_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = *persistentMeta
	history := partnerFloodHistory(30, 6000, -1)
	newest := history[len(history)-4].Message.Text() // the last partner message
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	entries := extractMarkdownConversation(history[0].Message.Text())
	if len(entries) == 0 || entries[len(entries)-1].Text != newest {
		t.Fatalf("the newest message, which fits the ceiling, was not carried whole")
	}
}

// A final summary cut off at the output limit is rejected even when this fold
// left nothing out of the carry: an earlier summary it replaces may hold the
// only trace of messages an earlier fold left out.
func TestForceCompact_TruncatedFinalSummaryIsRejectedOnEveryFold(t *testing.T) {
	adapter := &stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant(emptySummaryReply), Finish: llm.FinishReason{Reason: llm.FinishReasonLength}}, nil
	}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	history := partnerHoldHistory()
	cm.ForceCompact(context.Background(), &history, "", noopEmit)
	if history[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("history starts with a %s, want the checkpoint to stand", history[0].Kind)
	}
}
