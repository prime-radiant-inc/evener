package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestCheckpoint_UsesTurnCheckpointKind(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug in login.go")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"login.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "1 | package main\n", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "edit_file", `{"file_path":"login.go","old_string":"old","new_string":"new"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "edit_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 2, nil, "communicate")

	if len(result) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(result))
	}
	if result[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("checkpoint turn kind = %q, want %q", result[0].Kind, schema.TurnCheckpoint)
	}
	// The text content should still have [CONTEXT CHECKPOINT] header.
	text := result[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT CHECKPOINT]") {
		t.Fatalf("checkpoint missing header: %q", text)
	}
}

func TestSummarizeWithLLM_UsesTurnSummaryKind(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("## Progress\nSummary: fixed auth bug")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I'll fix it")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	result, err := cm.summarizeWithLLM(context.Background(), history, 2)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	if len(result) < 1 {
		t.Fatalf("expected at least 1 turn, got %d", len(result))
	}
	if result[0].Kind != schema.TurnSummary {
		t.Fatalf("summary turn kind = %q, want %q", result[0].Kind, schema.TurnSummary)
	}
	// The text content should still have [CONTEXT SUMMARY] header.
	text := result[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT SUMMARY]") {
		t.Fatalf("summary missing header: %q", text)
	}
}

func TestSummarizeWithLLM_RoutesToCheapProvider(t *testing.T) {
	// A cross-provider cheap model summarizes on the cheap provider; the active
	// model remains the fallback on its own (main) provider.
	mainAdapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				t.Errorf("summarizer routed to active provider %q, want cheap provider anthropic", req.Provider)
				return llm.Response{Message: llm.Assistant("wrong")}
			},
		},
	}
	cheapAdapter := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.Provider != "anthropic" || req.Model != "claude-haiku-4-5-20251001" {
					t.Fatalf("cheap call = (%q, %q), want (anthropic, claude-haiku-4-5-20251001)", req.Provider, req.Model)
				}
				return llm.Response{Message: llm.Assistant("## Progress\nSummary: routed to cheap")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(mainAdapter)
	client.Register(cheapAdapter)

	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "anthropic/claude-haiku-4-5-20251001")
	cm := NewManager(profile, client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I'll fix it")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	if _, err := cm.summarizeWithLLM(context.Background(), history, 2); err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
}

func TestMaybeCompact_CallsOnCompactionTurn(t *testing.T) {
	// Use a tiny context window to force checkpoint (L3).
	profile := testProfile("openai", "test", 500)
	cm := NewManager(profile, nil, cheapmodel.New(nil))
	cm.PreserveRecentTurns = 2

	// Use assistant text (not tool results) so observation masking can't reduce pressure.
	// Need >80% of 500 = 400 tokens.
	history := []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")}}
	for cm.estimateTokens(history) < 425 {
		history = append(history,
			schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("analysis ", 50))},
		)
	}
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var callbackTurns []schema.Turn
	cm.OnCompactionTurn = func(turn schema.Turn) {
		callbackTurns = append(callbackTurns, turn)
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	if len(callbackTurns) == 0 {
		t.Fatal("expected OnCompactionTurn callback to be called")
	}
	if callbackTurns[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("callback turn kind = %q, want %q", callbackTurns[0].Kind, schema.TurnCheckpoint)
	}
	if !strings.Contains(callbackTurns[0].Message.Text(), "[CONTEXT CHECKPOINT]") {
		t.Fatalf("callback turn missing checkpoint text: %q", callbackTurns[0].Message.Text())
	}
}

// TestSummarizeWithLLM_ResummarizingKeepsThePreviousSummary pins that a
// re-compaction shows the summarizer the previous summary well past its
// opening. That summary is the only record of the conversation it folded, so a
// permission or hold it quotes after its timeline must still reach the
// summarizer, or the next summary drops it (#4173). The summary still cannot
// crowd out the conversation folded after it.
func TestSummarizeWithLLM_ResummarizingKeepsThePreviousSummary(t *testing.T) {
	for _, tc := range []struct {
		name, previous string
		want           []string
	}{
		// A quote past the old 1000-character cut still reaches the summarizer.
		{"quote past the opening", "[CONTEXT SUMMARY]\n## Conversation Timeline\n" + strings.Repeat("timeline ", 400) +
			"\n## Key Decisions\nPREVIOUS_SUMMARY_QUOTE_SENTINEL\n[END SUMMARY]", []string{"PREVIOUS_SUMMARY_QUOTE_SENTINEL"}},
		// An oversized previous summary keeps its head, where the summary
		// prompts put permissions and holds, and still leaves room for the
		// conversation folded after it.
		{"oversized summary", "[CONTEXT SUMMARY]\n## " + summarySections[0] + "\nPREVIOUS_SUMMARY_QUOTE_SENTINEL\n## Conversation Timeline\n" +
			strings.Repeat("timeline ", 20_000) + "\n[END SUMMARY]", []string{"PREVIOUS_SUMMARY_QUOTE_SENTINEL", "FOLDED_USER_SENTINEL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := resummarizePrompt(t, tc.previous)
			for _, want := range tc.want {
				if !strings.Contains(prompt, want) {
					t.Fatalf("the summarizer did not see %s (prompt is %d chars)", want, len(prompt))
				}
			}
		})
	}
}

// resummarizePrompt compacts a history that starts with previous and returns
// the prompt the summarizer received.
func resummarizePrompt(t *testing.T, previous string) string {
	t.Helper()
	var prompt string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				prompt = req.Messages[0].Text()
				return llm.Response{Message: llm.Assistant("## Progress\nre-summarized")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))
	history := []schema.Turn{
		{Kind: schema.TurnSummary, Message: llm.User(previous)},
		{Kind: schema.TurnUserInput, Message: llm.User("FOLDED_USER_SENTINEL")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("working")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}
	if _, err := cm.summarizeWithLLM(context.Background(), history, 2); err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	return prompt
}

// TestForceCompact_SecondCompactionSeesTheFirstSummary pins the production
// order: the deterministic checkpoint folds history before the summarizer
// runs, so a previous LLM summary reaches the next summarizer only through the
// checkpoint. Its permissions and holds must survive that fold, or the second
// summary drops a hold or permission the first one quoted (#4173).
func TestForceCompact_SecondCompactionSeesTheFirstSummary(t *testing.T) {
	var prompts []string
	summarize := func(req llm.Request) llm.Response {
		prompts = append(prompts, req.Messages[0].Text())
		return llm.Response{Message: llm.Assistant("## " + summarySections[0] + "\nFIRST_SUMMARY_QUOTE_SENTINEL\n\n## Progress\nfirst summary")}
	}
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{summarize, summarize}}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(testProfile("openai", "test", 100_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working on it")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	noop := func(events.EventKind, events.EventData) {}
	if !cm.ForceCompact(context.Background(), &history, "", noop) {
		t.Fatal("first compaction did not summarize")
	}
	history = append(history,
		schema.NewTurn(schema.TurnUserInput, llm.User("second question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("more work")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent3")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent4")),
	)
	if !cm.ForceCompact(context.Background(), &history, "", noop) {
		t.Fatal("second compaction did not summarize")
	}
	if len(prompts) != 2 {
		t.Fatalf("summarizer ran %d times, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "FIRST_SUMMARY_QUOTE_SENTINEL") {
		t.Fatalf("the second summarizer did not see the first summary's quote:\n%s", prompts[1])
	}
}

// TestCheckpoint_CarriesEarlierSummaries pins that the deterministic
// checkpoint keeps an LLM summary it folds: whole and first when it fits,
// through a checkpoint of that checkpoint, and trimmed from its tail when it
// is oversized, so its leading permissions and holds survive.
func TestCheckpoint_CarriesEarlierSummaries(t *testing.T) {
	recent := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	fold := func(first schema.Turn) string {
		history := append([]schema.Turn{first, schema.NewTurn(schema.TurnUserInput, llm.User("next question"))}, recent...)
		result := checkpoint(history, 2, nil, "communicate")
		if result[0].Kind != schema.TurnCheckpoint {
			t.Fatalf("first turn kind = %q, want a checkpoint", result[0].Kind)
		}
		return result[0].Message.Text()
	}
	summary := "[CONTEXT SUMMARY]\n## " + summarySections[0] + "\nSUMMARY_QUOTE_SENTINEL\n\n## Progress\nSUMMARY_TAIL_SENTINEL\n[END SUMMARY]"

	once := fold(schema.NewTurn(schema.TurnSummary, llm.User(summary)))
	if got := extractCheckpointEarlierSummaries(once); len(got) != 1 || got[0] != summary {
		t.Fatalf("checkpoint carried %q, want the whole summary", got)
	}
	if !strings.HasPrefix(once, "[CONTEXT CHECKPOINT]\n## Earlier Summaries") {
		t.Fatalf("earlier summaries are not first in the checkpoint:\n%s", once)
	}
	// A legacy compaction stored as user input carries the same way.
	if got := extractCheckpointEarlierSummaries(fold(schema.NewTurn(schema.TurnUserInput, llm.User(summary)))); len(got) != 1 || got[0] != summary {
		t.Fatalf("checkpoint carried %q from a legacy summary, want the whole summary", got)
	}
	if got := extractCheckpointEarlierSummaries(fold(schema.NewTurn(schema.TurnUserInput, llm.User(once)))); len(got) != 1 || got[0] != summary {
		t.Fatalf("checkpoint carried %q from a legacy checkpoint, want the whole summary", got)
	}
	twice := fold(schema.NewTurn(schema.TurnCheckpoint, llm.User(once)))
	if got := extractCheckpointEarlierSummaries(twice); len(got) != 1 || got[0] != summary {
		t.Fatalf("checkpoint of a checkpoint carried %q, want the whole summary", got)
	}

	oversized := strings.Replace(summary, "## Progress\n", "## Progress\n"+strings.Repeat("progress ", 10_000), 1)
	trimmed := fold(schema.NewTurn(schema.TurnSummary, llm.User(oversized)))
	got := extractCheckpointEarlierSummaries(trimmed)
	if len(got) != 1 || !strings.Contains(got[0], "SUMMARY_QUOTE_SENTINEL") || strings.Contains(got[0], "SUMMARY_TAIL_SENTINEL") {
		t.Fatalf("oversized summary was not trimmed from its tail: %d summaries", len(got))
	}
	if len(trimmed) > 60_000 {
		t.Fatalf("checkpoint is %d chars, over its 60k cap", len(trimmed))
	}
}
